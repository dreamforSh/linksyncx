//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

// Anthropic OAuth / 可刷新 setup-token 的 401：清缓存 + 冷却，并在后台强制刷新一次。
// 成功即解除这次 401 的冷却；invalid_grant 才标错误；其余刷新失败只保持冷却。

const claude401RevokedBody = `{"type":"error","error":{"type":"authentication_error","message":"OAuth token has been revoked. Please obtain a new token."}}`

// claudeAuthStatefulRepo 单账号的内存仓库：刷新、停调、标错都落在同一份状态上。
type claudeAuthStatefulRepo struct {
	mockAccountRepoForGemini
	mu               sync.Mutex
	account          *Account
	setErrorMsgs     []string
	tempReasons      []string
	clearTempCalls   int
	credentialWrites int
}

func (r *claudeAuthStatefulRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, errors.New("account not found")
	}
	cp := *r.account
	cp.Credentials = shallowCopyMap(r.account.Credentials)
	return &cp, nil
}

func (r *claudeAuthStatefulRepo) SetError(_ context.Context, _ int64, errorMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorMsgs = append(r.setErrorMsgs, errorMsg)
	r.account.Status = StatusError
	r.account.ErrorMessage = errorMsg
	return nil
}

// SetTempUnschedulable 与仓库实现同语义：只延长，不缩短。
func (r *claudeAuthStatefulRepo) SetTempUnschedulable(_ context.Context, _ int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tempReasons = append(r.tempReasons, reason)
	if r.account.TempUnschedulableUntil == nil || r.account.TempUnschedulableUntil.Before(until) {
		u := until
		r.account.TempUnschedulableUntil = &u
		r.account.TempUnschedulableReason = reason
	}
	return nil
}

func (r *claudeAuthStatefulRepo) ClearTempUnschedulable(_ context.Context, _ int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clearTempCalls++
	r.account.TempUnschedulableUntil = nil
	r.account.TempUnschedulableReason = ""
	return nil
}

func (r *claudeAuthStatefulRepo) UpdateCredentials(_ context.Context, _ int64, credentials map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentialWrites++
	r.account.Credentials = shallowCopyMap(credentials)
	return nil
}

func (r *claudeAuthStatefulRepo) snapshot() *Account {
	account, _ := r.GetByID(context.Background(), r.account.ID)
	return account
}

// claudeAuthTokenCache 带 SETNX 语义的 token 缓存桩：刷新锁与强刷名额都靠它。
type claudeAuthTokenCache struct {
	mu      sync.Mutex
	tokens  map[string]string
	locks   map[string]bool
	lockErr error
	deletes []string
}

func newClaudeAuthTokenCache() *claudeAuthTokenCache {
	return &claudeAuthTokenCache{tokens: map[string]string{}, locks: map[string]bool{}}
}

func (c *claudeAuthTokenCache) GetAccessToken(_ context.Context, key string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokens[key], nil
}

func (c *claudeAuthTokenCache) SetAccessToken(_ context.Context, key, token string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokens[key] = token
	return nil
}

func (c *claudeAuthTokenCache) DeleteAccessToken(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deletes = append(c.deletes, key)
	delete(c.tokens, key)
	return nil
}

func (c *claudeAuthTokenCache) AcquireRefreshLock(_ context.Context, key string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lockErr != nil {
		return false, c.lockErr
	}
	if c.locks[key] {
		return false, nil
	}
	c.locks[key] = true
	return true, nil
}

func (c *claudeAuthTokenCache) ReleaseRefreshLock(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.locks, key)
	return nil
}

type claudeAuthTempUnschedCache struct {
	deletes []int64
}

func (c *claudeAuthTempUnschedCache) SetTempUnsched(context.Context, int64, *TempUnschedState) error {
	return nil
}

func (c *claudeAuthTempUnschedCache) GetTempUnsched(context.Context, int64) (*TempUnschedState, error) {
	return nil, nil
}

func (c *claudeAuthTempUnschedCache) DeleteTempUnsched(_ context.Context, id int64) error {
	c.deletes = append(c.deletes, id)
	return nil
}

type claudeAuthHarness struct {
	repo         *claudeAuthStatefulRepo
	cache        *claudeAuthTokenCache
	tempCache    *claudeAuthTempUnschedCache
	blocker      *runtimeBlockRecorder
	provider     *ClaudeTokenProvider
	svc          *RateLimitService
	refreshCalls atomic.Int32
	refreshedRTs []string
	proxyRepo    *mockProxyRepoForOAuth
}

// newClaudeAuthHarness 用真实的 ClaudeTokenProvider + OAuthRefreshAPI + ClaudeTokenRefresher + OAuthService，
// 只把 token 端点换成桩；后台强刷改为同步执行。
func newClaudeAuthHarness(t *testing.T, account *Account, refresh func(refreshToken string) (*oauth.TokenResponse, error)) *claudeAuthHarness {
	t.Helper()
	h := &claudeAuthHarness{
		repo:      &claudeAuthStatefulRepo{account: account},
		cache:     newClaudeAuthTokenCache(),
		tempCache: &claudeAuthTempUnschedCache{},
		blocker:   &runtimeBlockRecorder{},
		proxyRepo: &mockProxyRepoForOAuth{},
	}
	oauthClient := &mockClaudeOAuthClient{
		refreshTokenFunc: func(_ context.Context, refreshToken, _, _ string) (*oauth.TokenResponse, error) {
			h.refreshCalls.Add(1)
			h.refreshedRTs = append(h.refreshedRTs, refreshToken)
			return refresh(refreshToken)
		},
	}
	oauthSvc := NewOAuthService(h.proxyRepo, oauthClient)
	t.Cleanup(oauthSvc.Stop)
	h.provider = ProvideClaudeTokenProvider(h.repo, h.cache, oauthSvc, NewOAuthRefreshAPI(h.repo, h.cache))
	h.svc = NewRateLimitService(h.repo, nil, &config.Config{}, nil, h.tempCache)
	h.svc.SetTokenCacheInvalidator(NewCompositeTokenCacheInvalidator(h.cache))
	h.svc.SetClaudeUnauthorizedRefresher(h.provider)
	h.svc.SetAccountRuntimeBlocker(h.blocker)
	h.svc.claudeForcedRefreshRunner = func(fn func()) { fn() }
	return h
}

func (h *claudeAuthHarness) handle401(account *Account) bool {
	return h.svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(claude401RevokedBody))
}

func revokedClaudeAccount(id int64, accountType string) *Account {
	return &Account{
		ID:          id,
		Platform:    PlatformAnthropic,
		Type:        accountType,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  "revoked-at",
			"refresh_token": "rt-1",
			// 被撤销的 token 离过期还早：请求路径与后台刷新都不会按 expires_at 刷新它。
			"expires_at": strconv.FormatInt(time.Now().Add(6*time.Hour).Unix(), 10),
		},
	}
}

func freshClaudeTokens(string) (*oauth.TokenResponse, error) {
	return &oauth.TokenResponse{AccessToken: "fresh-at", TokenType: "Bearer", ExpiresIn: 28800, RefreshToken: "rt-2"}, nil
}

func TestClaude401_ForcedRefreshRecoversRevokedButUnexpiredToken(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			account := revokedClaudeAccount(801, accountType)
			h := newClaudeAuthHarness(t, account, freshClaudeTokens)
			requestSnapshot := h.repo.snapshot()

			require.True(t, h.handle401(requestSnapshot), "本次请求照常换号")

			require.Empty(t, h.repo.setErrorMsgs, "可刷新的账号不再首发 401 即永久禁用")
			require.Len(t, h.repo.tempReasons, 1)
			require.Equal(t, int32(1), h.refreshCalls.Load())
			require.Equal(t, []string{"rt-1"}, h.refreshedRTs)
			stored := h.repo.snapshot()
			require.Equal(t, "fresh-at", stored.GetCredential("access_token"))
			require.Equal(t, "rt-2", stored.GetCredential("refresh_token"))
			// 刷新成功即解除这次 401 的冷却，并同步清 Redis 停调缓存与调度阻断。
			require.Equal(t, 1, h.repo.clearTempCalls)
			require.Nil(t, stored.TempUnschedulableUntil)
			require.Equal(t, []int64{account.ID}, h.tempCache.deletes)
			require.Equal(t, []int64{account.ID}, h.blocker.clearedIDs)
			require.Contains(t, h.cache.deletes, ClaudeTokenCacheKey(account))

			// 循环被打破：下一个请求即使拿着旧的调度快照，也取到新 token。
			token, err := h.provider.GetAccessToken(context.Background(), requestSnapshot)
			require.NoError(t, err)
			require.Equal(t, "fresh-at", token)
		})
	}
}

func TestClaude401_InvalidGrantMarksErrorOnlyAfterRefreshFails(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			h := newClaudeAuthHarness(t, revokedClaudeAccount(802, accountType), func(string) (*oauth.TokenResponse, error) {
				return nil, errors.New(`token refresh failed: status 400, body: {"error":"invalid_grant","error_description":"Refresh token not found or invalid"}`)
			})

			h.handle401(h.repo.snapshot())

			require.Equal(t, int32(1), h.refreshCalls.Load())
			require.Len(t, h.repo.setErrorMsgs, 1)
			require.Contains(t, h.repo.setErrorMsgs[0], "Token refresh failed after 401 (invalid_grant)")
			require.Zero(t, h.repo.clearTempCalls)
		})
	}
}

func TestClaude401_TransientRefreshFailureKeepsCooldownOnly(t *testing.T) {
	for name, refreshErr := range map[string]error{
		"token_endpoint_503":  errors.New("token refresh failed: status 503, body: upstream connect error"),
		"token_endpoint_edge": errors.New("token refresh failed: status 403, body: error code: 1010"),
		"network":             errors.New("request failed: dial tcp: i/o timeout"),
	} {
		t.Run(name, func(t *testing.T) {
			h := newClaudeAuthHarness(t, revokedClaudeAccount(803, AccountTypeOAuth), func(string) (*oauth.TokenResponse, error) {
				return nil, refreshErr
			})

			h.handle401(h.repo.snapshot())

			require.Equal(t, int32(1), h.refreshCalls.Load())
			require.Empty(t, h.repo.setErrorMsgs)
			require.Zero(t, h.repo.clearTempCalls)
			require.NotNil(t, h.repo.snapshot().TempUnschedulableUntil, "保持 401 冷却")
		})
	}
}

// proxy-never-direct：账号分配了代理但解析不了时，强刷 fail-closed——不发刷新请求、不标错。
func TestClaude401_ForcedRefreshFailsClosedWhenProxyUnavailable(t *testing.T) {
	account := revokedClaudeAccount(804, AccountTypeOAuth)
	proxyID := int64(42)
	account.ProxyID = &proxyID
	h := newClaudeAuthHarness(t, account, freshClaudeTokens)

	h.handle401(h.repo.snapshot())

	require.Zero(t, h.refreshCalls.Load(), "代理不可用时绝不直连刷新")
	require.Empty(t, h.repo.setErrorMsgs)
	require.Zero(t, h.repo.clearTempCalls)
	require.Equal(t, "revoked-at", h.repo.snapshot().GetCredential("access_token"))
}

// 30 分钟内同一账号最多强刷一次：401 若与 token 无关，刷新后马上又 401，不能每个请求都轮换一次。
func TestClaude401_ForcedRefreshThrottledPerAccount(t *testing.T) {
	h := newClaudeAuthHarness(t, revokedClaudeAccount(805, AccountTypeOAuth), func(rt string) (*oauth.TokenResponse, error) {
		return &oauth.TokenResponse{AccessToken: "at-after-" + rt, TokenType: "Bearer", ExpiresIn: 28800, RefreshToken: rt + "-next"}, nil
	})

	h.handle401(h.repo.snapshot())
	require.Equal(t, int32(1), h.refreshCalls.Load())
	require.Equal(t, 1, h.repo.clearTempCalls)

	// 刷新出的新 token 也被 401：名额已用，只冷却。
	h.handle401(h.repo.snapshot())
	require.Equal(t, int32(1), h.refreshCalls.Load())
	require.Equal(t, 1, h.repo.clearTempCalls)
	require.Len(t, h.repo.tempReasons, 2)
	require.NotNil(t, h.repo.snapshot().TempUnschedulableUntil)
	require.Empty(t, h.repo.setErrorMsgs)
}

// 凭据已被别的请求 / 后台刷新换新时不重复刷新，也不占用名额。
func TestClaude401_SkipsRefreshWhenCredentialAlreadyRotated(t *testing.T) {
	account := revokedClaudeAccount(806, AccountTypeOAuth)
	h := newClaudeAuthHarness(t, account, freshClaudeTokens)
	staleSnapshot := h.repo.snapshot()
	h.repo.account.Credentials["access_token"] = "rotated-at"

	h.handle401(staleSnapshot)
	require.Zero(t, h.refreshCalls.Load())
	require.Zero(t, h.repo.clearTempCalls)

	// 换新后的 token 再被 401，仍可强刷。
	h.handle401(h.repo.snapshot())
	require.Equal(t, int32(1), h.refreshCalls.Load())
	require.Equal(t, "fresh-at", h.repo.snapshot().GetCredential("access_token"))
}

// Redis 不可用时退回进程内限频。
func TestClaude401_ForcedRefreshSlotFallsBackToInProcessLimiter(t *testing.T) {
	h := newClaudeAuthHarness(t, revokedClaudeAccount(807, AccountTypeOAuth), func(rt string) (*oauth.TokenResponse, error) {
		return &oauth.TokenResponse{AccessToken: "at-after-" + rt, TokenType: "Bearer", ExpiresIn: 28800, RefreshToken: rt + "-next"}, nil
	})
	h.cache.lockErr = errors.New("redis down")

	h.handle401(h.repo.snapshot())
	h.handle401(h.repo.snapshot())

	require.Equal(t, int32(1), h.refreshCalls.Load())
}

// 冷却期间被别的原因（429、边缘 403、管理员规则）改写成更长的停调时，刷新成功也不清它。
func TestClaude401_DoesNotClearForeignCooldown(t *testing.T) {
	h := newClaudeAuthHarness(t, revokedClaudeAccount(808, AccountTypeOAuth), freshClaudeTokens)
	h.svc.claudeForcedRefreshRunner = func(fn func()) {
		_ = h.repo.SetTempUnschedulable(context.Background(), 808, time.Now().Add(2*time.Hour), "rate limited (429)")
		fn()
	}

	h.handle401(h.repo.snapshot())

	require.Equal(t, int32(1), h.refreshCalls.Load())
	require.Zero(t, h.repo.clearTempCalls)
	require.Equal(t, "rate limited (429)", h.repo.snapshot().TempUnschedulableReason)
}

// 管理员在刷新前停用了账号：不刷新、不清冷却。
func TestClaude401_SkipsRefreshForInactiveAccount(t *testing.T) {
	h := newClaudeAuthHarness(t, revokedClaudeAccount(809, AccountTypeOAuth), freshClaudeTokens)
	h.svc.claudeForcedRefreshRunner = func(fn func()) {
		h.repo.account.Status = StatusDisabled
		fn()
	}

	h.handle401(h.repo.snapshot())

	require.Zero(t, h.refreshCalls.Load())
	require.Zero(t, h.repo.clearTempCalls)
}

type claudeUnauthorizedRefresherStub struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (s *claudeUnauthorizedRefresherStub) ForceRefreshAfterUnauthorized(context.Context, *Account) (bool, error) {
	s.calls.Add(1)
	if s.started != nil {
		close(s.started)
		<-s.release
	}
	return false, nil
}

// 强刷在后台执行：失败的请求不等刷新，立即返回去换号。
func TestClaude401_ForcedRefreshDoesNotBlockFailover(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	refresher := &claudeUnauthorizedRefresherStub{started: make(chan struct{}), release: make(chan struct{})}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetClaudeUnauthorizedRefresher(refresher)

	require.True(t, svc.HandleUpstreamError(context.Background(), revokedClaudeAccount(810, AccountTypeOAuth),
		http.StatusUnauthorized, http.Header{}, []byte(claude401RevokedBody)))
	require.Equal(t, 1, repo.tempCalls)

	select {
	case <-refresher.started:
	case <-time.After(5 * time.Second):
		t.Fatal("后台强刷没有启动")
	}
	close(refresher.release)
}

// 作用域守卫：强刷与 setup-token 新语义只对 Anthropic 生效；没有 refresh token 的账号照旧标错。
func TestClaude401_ScopeGuard(t *testing.T) {
	cases := []struct {
		name          string
		account       *Account
		wantSetError  bool
		wantRefreshed bool
	}{
		{
			name:         "anthropic_setup_token_without_refresh_token",
			account:      &Account{ID: 811, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "at"}},
			wantSetError: true,
		},
		{
			name:         "anthropic_oauth_without_refresh_token",
			account:      &Account{ID: 812, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "at"}},
			wantSetError: true,
		},
		{
			name:         "antigravity_setup_token_with_refresh_token",
			account:      &Account{ID: 813, Platform: PlatformAntigravity, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "at", "refresh_token": "rt"}},
			wantSetError: true,
		},
		{
			name:    "openai_oauth",
			account: &Account{ID: 814, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "at", "refresh_token": "rt"}},
		},
		{
			name:          "anthropic_oauth_with_refresh_token",
			account:       revokedClaudeAccount(815, AccountTypeOAuth),
			wantRefreshed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			refresher := &claudeUnauthorizedRefresherStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetClaudeUnauthorizedRefresher(refresher)
			svc.claudeForcedRefreshRunner = func(fn func()) { fn() }

			svc.HandleUpstreamError(context.Background(), tc.account, http.StatusUnauthorized, http.Header{}, []byte(`{"error":{"message":"unauthorized"}}`))

			require.Equal(t, tc.wantSetError, repo.setErrorCalls == 1, "setErrorCalls=%d", repo.setErrorCalls)
			if tc.wantRefreshed {
				require.Equal(t, int32(1), refresher.calls.Load())
			} else {
				require.Zero(t, refresher.calls.Load())
			}
		})
	}
}

func TestClaudeTokenProvider_ForceRefreshAfterUnauthorizedNotApplicable(t *testing.T) {
	executor := &refreshAPIExecutorStub{credentials: map[string]any{"access_token": "new"}}
	repo := &refreshAPIAccountRepo{}
	provider := NewClaudeTokenProvider(repo, nil, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, nil), executor)

	for name, account := range map[string]*Account{
		"nil":                nil,
		"openai":             {ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "a", "refresh_token": "r"}},
		"apikey":             {ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "k"}},
		"no_refresh_token":   {ID: 3, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "a"}},
		"empty_access_token": {ID: 4, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"refresh_token": "r"}},
	} {
		t.Run(name, func(t *testing.T) {
			refreshed, err := provider.ForceRefreshAfterUnauthorized(context.Background(), account)
			require.NoError(t, err)
			require.False(t, refreshed)
		})
	}
	require.Zero(t, executor.refreshCalls)
}
