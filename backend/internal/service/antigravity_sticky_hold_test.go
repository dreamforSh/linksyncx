//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// antigravityStickyHoldFixture：分组 10 里两个 Antigravity OAuth 账号，对话 "conv" 绑定在账号 1。
type antigravityStickyHoldFixture struct {
	svc         *GatewayService
	ctx         context.Context
	groupID     int64
	concurrency *mockConcurrencyCache
}

func newAntigravityStickyHoldFixture(t *testing.T, settings map[string]string, mutate func(*Account)) *antigravityStickyHoldFixture {
	t.Helper()
	groupID := int64(10)
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{groupID}},
			{ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{groupID}},
		},
		accountsByID: map[int64]*Account{},
	}
	if mutate != nil {
		mutate(&accountRepo.accounts[0])
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	group := &Group{ID: groupID, Platform: PlatformAntigravity, Status: StatusActive, Hydrated: true}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	if settings == nil {
		settings = map[string]string{}
	}
	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
		cache:              &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"conv": 1}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
		settingService:     NewSettingService(&settingRepoStub{values: settings}, &config.Config{}),
	}
	ctx := WithClaudeStickyHold(context.WithValue(context.Background(), ctxkey.Group, group))
	return &antigravityStickyHoldFixture{svc: svc, ctx: ctx, groupID: groupID}
}

func (f *antigravityStickyHoldFixture) selectFor(t *testing.T, model string) (*AccountSelectionResult, error) {
	t.Helper()
	return f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "conv", model, nil, "", 0)
}

func antigravityModelRateLimitExtra(model string, resetAt time.Time) map[string]any {
	return map[string]any{
		modelRateLimitsKey: map[string]any{
			model: map[string]any{
				"rate_limited_at":     resetAt.Add(-time.Minute).UTC().Format(time.RFC3339),
				"rate_limit_reset_at": resetAt.UTC().Format(time.RFC3339),
			},
		},
	}
}

// 过载冷却只影响新对话：已绑定对话继续打原账号（与 Claude 行为一致）。
func TestAntigravityStickyHoldKeepsOverloadedBoundAccount(t *testing.T) {
	f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
		until := time.Now().Add(10 * time.Minute)
		a.OverloadUntil = &until
	})
	selection, err := f.selectFor(t, "claude-sonnet-4-5")
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)
}

// 上游 429 写下的模型限流：剩余不超过 MaxWait 时原地等待（不换号），超过才换号。
func TestAntigravityStickyHoldModelRateLimit(t *testing.T) {
	t.Run("within max wait holds", func(t *testing.T) {
		f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
			a.Extra = antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(5*time.Minute))
		})
		_, err := f.selectFor(t, "claude-sonnet-4-5")
		holdErr, ok := AsClaudeStickyHoldError(err)
		require.True(t, ok, "got %v", err)
		require.Equal(t, int64(1), holdErr.AccountID)
		require.Equal(t, "model_rate_limited", holdErr.Reason)
		require.InDelta(t, 300, holdErr.RetryAfter.Seconds(), 5)
	})

	t.Run("beyond max wait switches", func(t *testing.T) {
		f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
			a.Extra = antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(30*time.Minute))
		})
		selection, err := f.selectFor(t, "claude-sonnet-4-5")
		require.NoError(t, err)
		require.Equal(t, int64(2), selection.Account.ID)
	})

	t.Run("other models unaffected", func(t *testing.T) {
		f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
			a.Extra = antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(5*time.Minute))
		})
		selection, err := f.selectFor(t, "claude-opus-4-8")
		require.NoError(t, err)
		require.Equal(t, int64(1), selection.Account.ID)
	})
}

// 账号级限流（429 无法解析出模型时的兜底冷却）：剩余不超过 MaxWait 不换号，超过换号。
func TestAntigravityStickyHoldAccountRateLimit(t *testing.T) {
	t.Run("within max wait holds", func(t *testing.T) {
		f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
			until := time.Now().Add(2 * time.Minute)
			a.RateLimitResetAt = &until
		})
		_, err := f.selectFor(t, "claude-sonnet-4-5")
		holdErr, ok := AsClaudeStickyHoldError(err)
		require.True(t, ok, "got %v", err)
		require.Equal(t, "rate_limited", holdErr.Reason)
	})

	t.Run("beyond max wait switches", func(t *testing.T) {
		f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
			until := time.Now().Add(time.Hour)
			a.RateLimitResetAt = &until
		})
		selection, err := f.selectFor(t, "claude-sonnet-4-5")
		require.NoError(t, err)
		require.Equal(t, int64(2), selection.Account.ID)
	})
}

// API Key / 中转账号不参与粘性保持：照原逻辑换号。
func TestAntigravityStickyHoldSkipsAPIKeyAccounts(t *testing.T) {
	f := newAntigravityStickyHoldFixture(t, nil, func(a *Account) {
		a.Type = AccountTypeAPIKey
		until := time.Now().Add(10 * time.Minute)
		a.OverloadUntil = &until
	})
	selection, err := f.selectFor(t, "claude-sonnet-4-5")
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
}

// 关闭开关即恢复原有换号行为。
func TestAntigravityStickyHoldDisabledRestoresSwitching(t *testing.T) {
	f := newAntigravityStickyHoldFixture(t, map[string]string{SettingKeyClaudeStickyHoldEnabled: "false"}, func(a *Account) {
		until := time.Now().Add(10 * time.Minute)
		a.OverloadUntil = &until
	})
	selection, err := f.selectFor(t, "claude-sonnet-4-5")
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
}

func TestAntigravityStickyHoldSoftSwitchGate(t *testing.T) {
	svc := &AntigravityGatewayService{}
	settings := NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{})
	account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	holdCtx := WithClaudeStickyHold(context.Background())

	params := antigravityRetryLoopParams{
		ctx:             holdCtx,
		account:         account,
		settingService:  settings,
		isStickySession: true,
	}
	soft, ok := svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Second)
	require.True(t, ok)
	require.True(t, soft.SoftHold)
	require.True(t, soft.IsStickySession)
	require.Equal(t, 30*time.Second, soft.RetryAfter)
	require.Equal(t, "claude-sonnet-4-5", soft.RateLimitedModel)

	// 超过 MaxWait → 硬切换
	_, ok = svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Minute)
	require.False(t, ok)

	// 非粘性会话 → 硬切换
	params.isStickySession = false
	_, ok = svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Second)
	require.False(t, ok)
	params.isStickySession = true

	// 未声明可处理 hold 的入口 → 硬切换
	params.ctx = context.Background()
	_, ok = svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Second)
	require.False(t, ok)
	params.ctx = holdCtx

	// API key 账号 → 硬切换
	params.account = &Account{ID: 3, Platform: PlatformAntigravity, Type: AccountTypeAPIKey}
	_, ok = svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Second)
	require.False(t, ok)
	params.account = account

	// 开关关闭 → 硬切换
	params.settingService = NewSettingService(&settingRepoStub{values: map[string]string{SettingKeyClaudeStickyHoldEnabled: "false"}}, &config.Config{})
	_, ok = svc.stickyHoldSoftSwitch(params, "claude-sonnet-4-5", 30*time.Second)
	require.False(t, ok)
}

// 粘性保持生效时，长延迟限流降级为软保持：记录模型限流但不清除粘性绑定。
func TestHandleSmartRetryStickyHoldSoftensLongDelay(t *testing.T) {
	repo := &stubAntigravityAccountRepo{}
	cache := &stubSmartRetryCache{}
	account := &Account{ID: 1, Name: "acc-1", Type: AccountTypeOAuth, Platform: PlatformAntigravity}

	respBody := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "15s"}
			]
		}
	}`)
	newResp := func() *http.Response {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(respBody))}
	}

	params := antigravityRetryLoopParams{
		ctx:             WithClaudeStickyHold(context.Background()),
		prefix:          "[test]",
		account:         account,
		accessToken:     "token",
		action:          "generateContent",
		body:            []byte(`{"input":"test"}`),
		accountRepo:     repo,
		settingService:  NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{}),
		isStickySession: true,
		groupID:         10,
		sessionHash:     "conv",
		handleError: func(ctx context.Context, prefix string, account *Account, statusCode int, headers http.Header, body []byte, requestedModel string, groupID int64, sessionHash string, isStickySession bool) *handleModelRateLimitResult {
			return nil
		},
	}

	svc := &AntigravityGatewayService{cache: cache}
	result := svc.handleSmartRetry(params, newResp(), respBody, "https://ag-1.test", 0, []string{"https://ag-1.test"})

	require.NotNil(t, result)
	require.Equal(t, smartRetryActionBreakWithResp, result.action)
	require.NotNil(t, result.switchError)
	require.True(t, result.switchError.SoftHold)
	require.Equal(t, 15*time.Second, result.switchError.RetryAfter)
	require.Len(t, repo.modelRateLimitCalls, 1, "模型限流仍要记录，调度层才能看到冷却")
	require.Empty(t, cache.deleteCalls, "粘性保持下不得清除粘性绑定")

	// 转换给上层：429 + retry-after + StickyHoldSoft
	failover := result.switchError.toFailoverError()
	require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
	require.True(t, failover.StickyHoldSoft)
	require.Equal(t, "15", failover.ResponseHeaders.Get("retry-after"))
	require.True(t, failover.ForceCacheBilling)

	// 超过 MaxWait：维持现行硬切换并清除粘性
	repo2 := &stubAntigravityAccountRepo{}
	cache2 := &stubSmartRetryCache{}
	longBody := []byte(`{
		"error": {
			"status": "RESOURCE_EXHAUSTED",
			"details": [
				{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "metadata": {"model": "claude-sonnet-4-5"}, "reason": "RATE_LIMIT_EXCEEDED"},
				{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "900s"}
			]
		}
	}`)
	params.accountRepo = repo2
	params2 := params
	params2.accountRepo = repo2
	svc2 := &AntigravityGatewayService{cache: cache2}
	longResp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(longBody))}
	result = svc2.handleSmartRetry(params2, longResp, longBody, "https://ag-1.test", 0, []string{"https://ag-1.test"})

	require.NotNil(t, result.switchError)
	require.False(t, result.switchError.SoftHold)
	require.Len(t, cache2.deleteCalls, 1, "超过 MaxWait 仍按硬切换处理并清除粘性")
	require.Equal(t, "conv", cache2.deleteCalls[0].sessionHash)

	failover = result.switchError.toFailoverError()
	require.Equal(t, http.StatusServiceUnavailable, failover.StatusCode)
	require.False(t, failover.StickyHoldSoft)
}

// 预检查：账号已在限流中，粘性保持生效且剩余不超过 MaxWait → 软保持信号而不是硬切换。
func TestAntigravityRetryLoopPreCheckSoftHold(t *testing.T) {
	account := &Account{
		ID:       1,
		Name:     "acc-1",
		Type:     AccountTypeOAuth,
		Platform: PlatformAntigravity,
		Extra:    antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(3*time.Minute)),
	}
	svc := &AntigravityGatewayService{}
	params := antigravityRetryLoopParams{
		ctx:             WithClaudeStickyHold(context.Background()),
		prefix:          "[test]",
		account:         account,
		action:          "streamGenerateContent",
		body:            []byte(`{"input":"test"}`),
		settingService:  NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{}),
		requestedModel:  "claude-sonnet-4-5",
		isStickySession: true,
	}

	_, err := svc.antigravityRetryLoop(params)
	switchErr, ok := IsAntigravityAccountSwitchError(err)
	require.True(t, ok, "got %v", err)
	require.True(t, switchErr.SoftHold)
	require.InDelta(t, 180, switchErr.RetryAfter.Seconds(), 5)

	// 剩余时间超过 MaxWait → 现行硬切换
	account.Extra = antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(time.Hour))
	_, err = svc.antigravityRetryLoop(params)
	switchErr, ok = IsAntigravityAccountSwitchError(err)
	require.True(t, ok, "got %v", err)
	require.False(t, switchErr.SoftHold)

	// 未声明可处理 hold 的入口 → 硬切换
	params.ctx = context.Background()
	account.Extra = antigravityModelRateLimitExtra("claude-sonnet-4-5", time.Now().Add(3*time.Minute))
	_, err = svc.antigravityRetryLoop(params)
	switchErr, ok = IsAntigravityAccountSwitchError(err)
	require.True(t, ok, "got %v", err)
	require.False(t, switchErr.SoftHold)
}

func TestIsAntigravityStickySoftFailure(t *testing.T) {
	rateLimitedBody := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"30s"}]}}`)
	quotaBody := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","message":"quota_exhausted: daily limit"}}`)

	cases := []struct {
		name string
		err  *UpstreamFailoverError
		want bool
	}{
		{"nil", nil, false},
		{"soft hold switch", &UpstreamFailoverError{StatusCode: 429, StickyHoldSoft: true}, true},
		{"rate limited 429", &UpstreamFailoverError{StatusCode: 429, ResponseBody: rateLimitedBody}, true},
		{"unknown 429", &UpstreamFailoverError{StatusCode: 429, ResponseBody: []byte(`{"error":{"message":"slow down"}}`)}, true},
		{"quota exhausted 429", &UpstreamFailoverError{StatusCode: 429, ResponseBody: quotaBody}, false},
		{"server error", &UpstreamFailoverError{StatusCode: 503}, true},
		{"overload", &UpstreamFailoverError{StatusCode: 529}, true},
		{"unauthorized", &UpstreamFailoverError{StatusCode: 401}, false},
		{"credential failure", &UpstreamFailoverError{StatusCode: 502, Stage: GatewayFailureStageAccountAuth}, false},
		{"account unavailable", &UpstreamFailoverError{StatusCode: 503, AccountUnavailable: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsAntigravityStickySoftFailure(tc.err))
		})
	}
}

// StickyUpstreamRetryAfter：Antigravity 优先 retry-after 头，其次响应体 RetryInfo。
func TestStickyUpstreamRetryAfterAntigravity(t *testing.T) {
	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}

	headers := http.Header{}
	headers.Set("retry-after", "12")
	require.Equal(t, 12*time.Second, StickyUpstreamRetryAfter(account, &UpstreamFailoverError{ResponseHeaders: headers}))

	body := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"model":"claude-sonnet-4-5"}},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"45s"}]}}`)
	require.Equal(t, 45*time.Second, StickyUpstreamRetryAfter(account, &UpstreamFailoverError{StatusCode: 429, ResponseBody: body}))

	require.Equal(t, claudeStickyHoldUpstreamRetryAfter, StickyUpstreamRetryAfter(account, &UpstreamFailoverError{}))

	claude := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	require.Equal(t, 12*time.Second, StickyUpstreamRetryAfter(claude, &UpstreamFailoverError{ResponseHeaders: headers}))
}

// 换号水位线对 Antigravity 账号同样生效：剥离旧账号签发的 thinking 签名。
func TestPrepareClaudeSessionBodyAppliesToAntigravity(t *testing.T) {
	cache := newMemoryIdentityCache()
	svc := &GatewayService{
		identityService: NewIdentityService(cache),
		settingService:  NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{}),
	}
	ctx := WithClaudeStickyHold(context.Background())
	antigravityAccount := &Account{ID: 2, Platform: PlatformAntigravity, Type: AccountTypeOAuth}

	out, changed := svc.PrepareClaudeSessionBody(ctx, antigravityAccount, "conv", 1, []byte(migrationBody))
	require.True(t, changed)
	require.NotContains(t, string(out), "sig-old")

	// API Key 账号不参与
	apiKeyAccount := &Account{ID: 3, Platform: PlatformAntigravity, Type: AccountTypeAPIKey}
	_, changed = svc.PrepareClaudeSessionBody(ctx, apiKeyAccount, "conv2", 1, []byte(migrationBody))
	require.False(t, changed)
}
