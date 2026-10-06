//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

// sessionStateFromAuthURL 取出授权链接里的 state（即会话 state）。
func sessionStateFromAuthURL(t *testing.T, authURL string) string {
	t.Helper()
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state)
	return state
}

// 授权码交换必须发送本次会话的 state：token 端点对缺失 state 回 400
// "Invalid request format"。粘贴 "code#state"、整条回调 URL 或只粘 code 都要可用。
func TestOAuthServiceExchangeCodeSendsSessionState(t *testing.T) {
	var gotCode, gotState string
	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(_ context.Context, code, _, state, _ string, _ bool) (*oauth.TokenResponse, error) {
			gotCode, gotState = code, state
			return &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600}, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	for _, paste := range []func(state string) string{
		func(state string) string { return "  AUTHCODE#" + state + "\n" },
		func(state string) string { return oauth.RedirectURI + "?code=AUTHCODE&state=" + state },
		func(string) string { return "AUTHCODE" },
	} {
		result, err := svc.GenerateAuthURL(context.Background(), nil)
		require.NoError(t, err)
		state := sessionStateFromAuthURL(t, result.AuthURL)

		gotCode, gotState = "", ""
		_, err = svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: result.SessionID, Code: paste(state)})
		require.NoError(t, err)
		require.Equal(t, "AUTHCODE", gotCode, "the state suffix / URL wrapper must be stripped from the code")
		require.Equal(t, state, gotState, "the session state must always be sent")
	}
}

func TestOAuthServiceExchangeCodeRejectsCodeFromAnotherAuthorization(t *testing.T) {
	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(context.Context, string, string, string, string, bool) (*oauth.TokenResponse, error) {
			t.Fatal("a code from another authorization must not be exchanged")
			return nil, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)

	_, err = svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: result.SessionID, Code: "AUTHCODE#other-state"})
	require.Error(t, err)
	require.Equal(t, "CLAUDE_OAUTH_STATE_MISMATCH", infraerrors.Reason(err))
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))

	// 会话保留，管理员可以重新粘贴正确的授权码。
	_, ok := svc.sessionStore.Get(result.SessionID)
	require.True(t, ok)
}

func TestOAuthServiceExchangeCodeErrorsAreReadable(t *testing.T) {
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	defer svc.Stop()

	_, err := svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: "missing", Code: "AUTHCODE#s"})
	require.Equal(t, "CLAUDE_OAUTH_SESSION_NOT_FOUND", infraerrors.Reason(err))

	result, err := svc.GenerateAuthURL(context.Background(), nil)
	require.NoError(t, err)
	_, err = svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: result.SessionID, Code: "   "})
	require.Equal(t, "CLAUDE_OAUTH_CODE_REQUIRED", infraerrors.Reason(err))

	// 选了不存在的代理：报可读错误，同时仍可按 ErrAccountProxyUnavailable 判定。
	proxyID := int64(9)
	_, err = svc.ExchangeCode(context.Background(), &ExchangeCodeInput{SessionID: result.SessionID, Code: "AUTHCODE", ProxyID: &proxyID})
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Equal(t, "CLAUDE_OAUTH_PROXY_UNAVAILABLE", infraerrors.Reason(err))
}

// Cookie 授权把会话 state 传给交换步骤（第 2 步回调里不一定带 state）。
func TestOAuthServiceCookieAuthPassesGeneratedState(t *testing.T) {
	var authorizeState string
	client := &mockClaudeOAuthClient{
		getOrgUUIDFunc: func(context.Context, string, string) (string, error) { return "org-1", nil },
		getAuthCodeFunc: func(_ context.Context, _, _, scope, _, state, _ string) (string, error) {
			require.Equal(t, oauth.ScopeAPI, scope)
			authorizeState = state
			return "AUTHCODE", nil
		},
		exchangeCodeFunc: func(_ context.Context, code, _, state, _ string, isSetupToken bool) (*oauth.TokenResponse, error) {
			require.Equal(t, "AUTHCODE", code)
			require.NotEmpty(t, state)
			require.Equal(t, authorizeState, state)
			require.False(t, isSetupToken)
			return &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600}, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	info, err := svc.CookieAuth(context.Background(), &CookieAuthInput{SessionKey: "sk", Scope: "full"})
	require.NoError(t, err)
	require.Equal(t, "org-1", info.OrgUUID)
}

func TestClaudeRefreshScopeAttempts(t *testing.T) {
	const legacyGranted = "user:inference user:mcp_servers user:profile user:sessions:claude_code user:file_upload"
	tests := []struct {
		name        string
		accountType string
		granted     string
		want        []string
	}{
		{"subscription login asks for the CLI default set, falls back to granted", AccountTypeOAuth, legacyGranted,
			[]string{oauth.ScopeAPI, legacyGranted}},
		{"current default set needs no fallback", AccountTypeOAuth, oauth.ScopeAPI, []string{oauth.ScopeAPI}},
		{"granted projects scopes are kept", AccountTypeOAuth, legacyGranted + " user:projects:read",
			[]string{oauth.ScopeAPI + " user:projects:read", legacyGranted + " user:projects:read"}},
		{"unknown granted scope asks for the default set, then omits scope", AccountTypeOAuth, "",
			[]string{oauth.ScopeAPI, ""}},
		{"non-inference grant is refreshed as granted", AccountTypeOAuth, "org:create_api_key user:profile",
			[]string{"org:create_api_key user:profile"}},
		{"setup token keeps inference only", AccountTypeSetupToken, "user:inference", []string{"user:inference"}},
		{"setup token without recorded scope", AccountTypeSetupToken, "", []string{oauth.ScopeInference}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, claudeRefreshScopeAttempts(tt.accountType, tt.granted))
		})
	}
}

// 与真实 CLI 一样：请求的默认 scope 集被拒（invalid_scope）时退回已授予的 scope；
// 其他错误不重试，避免重复消耗 refresh_token。
func TestOAuthServiceRefreshAccountTokenFallsBackOnInvalidScope(t *testing.T) {
	const granted = "user:inference user:profile"
	var scopes []string
	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(_ context.Context, refreshToken, scope, _ string) (*oauth.TokenResponse, error) {
			require.Equal(t, "rt", refreshToken)
			scopes = append(scopes, scope)
			if scope != granted {
				return nil, errors.New(`token refresh failed: status 400, body: {"error": "invalid_scope"}`)
			}
			return &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600, Scope: granted}, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt", "scope": granted}}
	info, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "at", info.AccessToken)
	require.Equal(t, []string{oauth.RefreshScope(granted), granted}, scopes)

	scopes = nil
	client.refreshTokenFunc = func(_ context.Context, _, scope, _ string) (*oauth.TokenResponse, error) {
		scopes = append(scopes, scope)
		return nil, errors.New(`token refresh failed: status 400, body: {"error": "invalid_grant"}`)
	}
	_, err = svc.RefreshAccountToken(context.Background(), account)
	require.ErrorContains(t, err, "invalid_grant")
	require.Len(t, scopes, 1, "only invalid_scope triggers the fallback")
}

func TestOAuthServiceRefreshSetupTokenKeepsInferenceScope(t *testing.T) {
	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(_ context.Context, _, scope, _ string) (*oauth.TokenResponse, error) {
			require.Equal(t, oauth.ScopeInference, scope)
			return &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600}, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	_, err := svc.RefreshAccountToken(context.Background(), &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken,
		Credentials: map[string]any{"refresh_token": "rt", "scope": "user:inference"}})
	require.NoError(t, err)
}

// refresh token 期限与真实 CLI 一致：授权时有 refresh_token_expires_in 就用它，否则估 30 天；
// 刷新时只有上游返回才更新，否则留空由凭据合并沿用旧值。
func TestOAuthServiceRecordsRefreshTokenExpiry(t *testing.T) {
	ctx := context.Background()
	var exchangeResp *oauth.TokenResponse
	client := &mockClaudeOAuthClient{
		exchangeCodeFunc: func(context.Context, string, string, string, string, bool) (*oauth.TokenResponse, error) {
			return exchangeResp, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client)
	defer svc.Stop()

	exchange := func() *TokenInfo {
		t.Helper()
		result, err := svc.GenerateAuthURL(ctx, nil)
		require.NoError(t, err)
		info, err := svc.ExchangeCode(ctx, &ExchangeCodeInput{SessionID: result.SessionID, Code: "AUTHCODE"})
		require.NoError(t, err)
		return info
	}

	exchangeResp = &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600, RefreshToken: "rt"}
	info := exchange()
	require.InDelta(t, time.Now().Add(oauth.DefaultRefreshTokenLifetime).Unix(), info.RefreshTokenExpiresAt, 5)

	exchangeResp = &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 3600, RefreshToken: "rt", RefreshTokenExpiresIn: 86400}
	info = exchange()
	require.InDelta(t, time.Now().Add(24*time.Hour).Unix(), info.RefreshTokenExpiresAt, 5)

	exchangeResp = &oauth.TokenResponse{AccessToken: "at", ExpiresIn: 31536000}
	require.Zero(t, exchange().RefreshTokenExpiresAt, "no refresh token, no refresh-token expiry")

	var refreshResp *oauth.TokenResponse
	client.refreshTokenFunc = func(context.Context, string, string, string) (*oauth.TokenResponse, error) {
		return refreshResp, nil
	}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"refresh_token": "rt", "refresh_token_expires_at": "1700000000"}}

	refreshResp = &oauth.TokenResponse{AccessToken: "at2", ExpiresIn: 3600, RefreshToken: "rt2"}
	refreshed, err := svc.RefreshAccountToken(ctx, account)
	require.NoError(t, err)
	require.Zero(t, refreshed.RefreshTokenExpiresAt)
	merged := MergeCredentials(account.Credentials, BuildClaudeAccountCredentials(refreshed))
	require.Equal(t, "1700000000", merged["refresh_token_expires_at"], "an absent expiry keeps the stored value")

	refreshResp = &oauth.TokenResponse{AccessToken: "at3", ExpiresIn: 3600, RefreshToken: "rt3", RefreshTokenExpiresIn: 7200}
	refreshed, err = svc.RefreshAccountToken(ctx, account)
	require.NoError(t, err)
	merged = MergeCredentials(account.Credentials, BuildClaudeAccountCredentials(refreshed))
	require.Equal(t, strconv.FormatInt(refreshed.RefreshTokenExpiresAt, 10), merged["refresh_token_expires_at"])
	require.InDelta(t, time.Now().Add(2*time.Hour).Unix(), refreshed.RefreshTokenExpiresAt, 5)
}
