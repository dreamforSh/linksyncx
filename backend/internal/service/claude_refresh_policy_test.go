//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func claudeRefreshPolicyAccount(now time.Time, atIn, rtIn time.Duration, lastUsedAgo time.Duration, extra map[string]any) *Account {
	creds := map[string]any{
		"access_token":  "at",
		"refresh_token": "rt",
		"expires_at":    strconv.FormatInt(now.Add(atIn).Unix(), 10),
	}
	if rtIn != 0 {
		creds["refresh_token_expires_at"] = strconv.FormatInt(now.Add(rtIn).Unix(), 10)
	}
	for k, v := range extra {
		creds[k] = v
	}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Credentials: creds}
	if lastUsedAgo > 0 {
		used := now.Add(-lastUsedAgo)
		account.LastUsedAt = &used
	}
	return account
}

// 后台刷新只处理三类账号：活跃账号临近过期、refresh token 最后 3 天未确认期限固定的保活；
// 闲置账号与已确认期限固定的账号不刷新。
func TestClaudeBackgroundRefreshKind(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	window := 30 * time.Minute
	day := 24 * time.Hour
	rtSoon := 2 * day
	fixedAt := map[string]any{claudeRefreshTokenFixedExpiryKey: strconv.FormatInt(now.Add(rtSoon).Unix(), 10)}
	staleFixed := map[string]any{claudeRefreshTokenFixedExpiryKey: strconv.FormatInt(now.Add(rtSoon-time.Hour).Unix(), 10)}

	cases := []struct {
		name    string
		account *Account
		want    claudeBackgroundRefreshKind
	}{
		{"active and expiring", claudeRefreshPolicyAccount(now, 10*time.Minute, 20*day, time.Hour, nil), claudeBackgroundRefreshActive},
		{"active but not expiring", claudeRefreshPolicyAccount(now, 2*time.Hour, 20*day, time.Hour, nil), claudeBackgroundRefreshNone},
		{"idle and expiring", claudeRefreshPolicyAccount(now, 10*time.Minute, 20*day, 25*time.Hour, nil), claudeBackgroundRefreshNone},
		{"never used", claudeRefreshPolicyAccount(now, 10*time.Minute, 20*day, 0, nil), claudeBackgroundRefreshNone},
		{"legacy account without refresh token expiry", claudeRefreshPolicyAccount(now, 10*time.Minute, 0, time.Hour, nil), claudeBackgroundRefreshActive},
		{"idle, refresh token in its last 3 days", claudeRefreshPolicyAccount(now, 5*time.Hour, rtSoon, 0, nil), claudeBackgroundRefreshKeepAlive},
		{"idle, refresh token expiry known fixed", claudeRefreshPolicyAccount(now, 5*time.Hour, rtSoon, 0, fixedAt), claudeBackgroundRefreshNone},
		{"active, expiry fixed, access token expiring", claudeRefreshPolicyAccount(now, 10*time.Minute, rtSoon, time.Hour, fixedAt), claudeBackgroundRefreshActive},
		{"marker for another expiry is ignored", claudeRefreshPolicyAccount(now, 5*time.Hour, rtSoon, 0, staleFixed), claudeBackgroundRefreshKeepAlive},
		{"refresh token already expired", claudeRefreshPolicyAccount(now, -time.Hour, -time.Hour, 0, nil), claudeBackgroundRefreshNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claudeBackgroundRefreshKindFor(tc.account, window, now))
		})
	}

	noRT := claudeRefreshPolicyAccount(now, 10*time.Minute, rtSoon, time.Hour, nil)
	noRT.Credentials["refresh_token"] = " "
	require.Equal(t, claudeBackgroundRefreshNone, claudeBackgroundRefreshKindFor(noRT, window, now), "no refresh token, nothing to refresh")
}

// 刷新前已进入最后 3 天：期限没变晚就记下期限固定；变晚则不记。
func TestMarkClaudeRefreshTokenExtension(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	day := 24 * time.Hour
	before := now.Add(2 * day).Unix()

	account := claudeRefreshPolicyAccount(now, time.Hour, 2*day, 0, nil)
	unchanged := MergeCredentials(account.Credentials, map[string]any{"access_token": "new"})
	markClaudeRefreshTokenExtension(account, unchanged, now)
	require.Equal(t, strconv.FormatInt(before, 10), unchanged[claudeRefreshTokenFixedExpiryKey])
	require.Equal(t, claudeBackgroundRefreshNone, claudeBackgroundRefreshKindFor(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: unchanged}, 30*time.Minute, now))

	extended := MergeCredentials(account.Credentials, map[string]any{"refresh_token_expires_at": strconv.FormatInt(now.Add(30*day).Unix(), 10)})
	markClaudeRefreshTokenExtension(account, extended, now)
	require.NotContains(t, extended, claudeRefreshTokenFixedExpiryKey)

	far := claudeRefreshPolicyAccount(now, time.Hour, 10*day, 0, nil)
	creds := MergeCredentials(far.Credentials, map[string]any{"access_token": "new"})
	markClaudeRefreshTokenExtension(far, creds, now)
	require.NotContains(t, creds, claudeRefreshTokenFixedExpiryKey, "outside the last 3 days nothing is recorded")
}

func TestClaudeReauthNotice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	day := 24 * time.Hour
	rtIn := 2*day + 12*time.Hour
	fixedAt := map[string]any{claudeRefreshTokenFixedExpiryKey: strconv.FormatInt(now.Add(rtIn).Unix(), 10)}

	notice := ClaudeReauthNoticeFor(claudeRefreshPolicyAccount(now, time.Hour, rtIn, 0, fixedAt), now)
	require.NotNil(t, notice)
	require.Equal(t, 3, notice.DaysLeft)
	require.False(t, notice.Expired)
	require.Equal(t, now.Add(rtIn).Unix(), notice.ExpiresAt.Unix())

	require.Nil(t, ClaudeReauthNoticeFor(claudeRefreshPolicyAccount(now, time.Hour, rtIn, 0, nil), now), "keep-alive still pending")
	require.Nil(t, ClaudeReauthNoticeFor(claudeRefreshPolicyAccount(now, time.Hour, 10*day, 0, nil), now))
	require.Nil(t, ClaudeReauthNoticeFor(claudeRefreshPolicyAccount(now, time.Hour, 0, 0, nil), now), "no recorded expiry")

	expired := ClaudeReauthNoticeFor(claudeRefreshPolicyAccount(now, -time.Hour, -time.Hour, 0, nil), now)
	require.NotNil(t, expired)
	require.True(t, expired.Expired)
	require.Zero(t, expired.DaysLeft)

	apiKey := claudeRefreshPolicyAccount(now, time.Hour, -time.Hour, 0, nil)
	apiKey.Type = AccountTypeAPIKey
	require.Nil(t, ClaudeReauthNoticeFor(apiKey, now))
}

// 保活刷新要穿过 OAuthRefreshAPI 锁内的二次检查：access token 还远未过期，
// 只有包装后的执行器按后台判定放行。其它平台的刷新器不包装。
func TestBackgroundRegistrationKeepAlivePassesRecheck(t *testing.T) {
	now := time.Now()
	account := claudeRefreshPolicyAccount(now, 5*time.Hour, 2*24*time.Hour, 0, nil)
	repo := &refreshAPIAccountRepo{account: account}
	stub := &refreshAPIExecutorStub{needsRefresh: false, credentials: map[string]any{"access_token": "new"}}

	api := NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true})
	result, err := api.RefreshIfNeeded(context.Background(), account, stub, 30*time.Minute)
	require.NoError(t, err)
	require.False(t, result.Refreshed, "the plain executor skips a keep-alive")

	registration := backgroundRegistration(tokenRefreshRegistration{platform: PlatformAnthropic, refresher: &ClaudeTokenRefresher{}, executor: stub})
	require.True(t, needsBackgroundRefresh(registration.refresher, account, 30*time.Minute, now))
	result, err = api.RefreshIfNeeded(context.Background(), account, registration.executor, 30*time.Minute)
	require.NoError(t, err)
	require.True(t, result.Refreshed)
	require.Equal(t, 1, stub.refreshCalls)

	plain := tokenRefreshRegistration{platform: PlatformOpenAI, refresher: stub, executor: stub}
	require.Same(t, stub, backgroundRegistration(plain).executor.(*refreshAPIExecutorStub))
	require.False(t, needsBackgroundRefresh(stub, account, 30*time.Minute, now), "non-deciders keep NeedsRefresh")
}

// 带 refresh token 的 setup-token 账号在请求路径上按需刷新（后台不再刷闲置账号）。
func TestClaudeTokenProviderRefreshesSetupTokenWithRefreshToken(t *testing.T) {
	now := time.Now()
	account := claudeRefreshPolicyAccount(now, time.Minute, 20*24*time.Hour, 0, nil)
	account.Type = AccountTypeSetupToken
	require.True(t, ClaudeSetupTokenRefreshable(account))
	repo := &refreshAPIAccountRepo{account: account}
	stub := &refreshAPIExecutorStub{needsRefresh: true, credentials: map[string]any{
		"access_token":  "refreshed",
		"refresh_token": "rt2",
		"expires_at":    strconv.FormatInt(now.Add(8*time.Hour).Unix(), 10),
	}}
	provider := NewClaudeTokenProvider(repo, nil, nil)
	provider.SetRefreshAPI(NewOAuthRefreshAPI(repo, &refreshAPICacheStub{lockResult: true}), stub)

	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "refreshed", token)
	require.Equal(t, 1, stub.refreshCalls)

	gateway := &GatewayService{claudeTokenProvider: provider}
	legacy := claudeRefreshPolicyAccount(now, time.Minute, 0, 0, nil)
	legacy.Type = AccountTypeSetupToken
	legacy.Credentials["refresh_token"] = ""
	require.False(t, ClaudeSetupTokenRefreshable(legacy))
	token, kind, err := gateway.getOAuthToken(context.Background(), legacy)
	require.NoError(t, err)
	require.Equal(t, "oauth", kind)
	require.Equal(t, "at", token, "setup-token without refresh token reads the stored token")
	require.Equal(t, 1, stub.refreshCalls)
}

type usageTokenProviderStub struct {
	token string
	err   error
	calls int
}

func (p *usageTokenProviderStub) GetAccessToken(context.Context, *Account) (string, error) {
	p.calls++
	return p.token, p.err
}

// 主动用量查询经 token provider 取 token（闲置账号的 token 可能已过期），失败时回退凭据。
func TestClaudeUsageAccessTokenUsesProvider(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "stored"}}
	svc := &AccountUsageService{}
	require.Equal(t, "stored", svc.claudeUsageAccessToken(context.Background(), account))

	provider := &usageTokenProviderStub{token: "fresh"}
	svc.claudeTokenProvider = provider
	require.Equal(t, "fresh", svc.claudeUsageAccessToken(context.Background(), account))

	provider.token, provider.err = "", context.DeadlineExceeded
	require.Equal(t, "stored", svc.claudeUsageAccessToken(context.Background(), account))

	gemini := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "g"}}
	require.Equal(t, "g", svc.claudeUsageAccessToken(context.Background(), gemini))
	require.Equal(t, 2, provider.calls)
}
