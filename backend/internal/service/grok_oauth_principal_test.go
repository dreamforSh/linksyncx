//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"net/url"
	"strconv"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func grokTestJWT(payload string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

// Grok Build 登录后从 access token 读出 consent 页选定的主体并保存，刷新时回传；
// 团队授权的账号不回传主体就续期不到同一个团队 token。
func TestGrokOAuthServiceKeepsConsentPrincipalAcrossRefresh(t *testing.T) {
	teamToken := grokTestJWT(`{"sub":"u-1","principal_type":"Team","principal_id":"team-1"}`)
	client := &grokOAuthClientStub{
		exchangeResponse: &xai.TokenResponse{AccessToken: teamToken, RefreshToken: "rt-1", ExpiresIn: 3600},
		// 刷新回来的 token 不带主体声明：沿用登录时的主体。
		refreshResponse: &xai.TokenResponse{AccessToken: "opaque-access", RefreshToken: "rt-2", ExpiresIn: 3600},
	}
	svc := NewGrokOAuthService(nil, client)
	defer svc.Stop()

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	info, err := svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{SessionID: auth.SessionID, Code: "code", State: auth.State})
	require.NoError(t, err)
	require.Equal(t, "Team", info.PrincipalType)
	require.Equal(t, "team-1", info.PrincipalID)

	creds := svc.BuildAccountCredentials(info)
	require.Equal(t, "Team", creds["principal_type"])
	require.Equal(t, "team-1", creds["principal_id"])

	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: creds}
	refreshed, err := svc.RefreshAccountToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, xai.TokenPrincipal{Type: "Team", ID: "team-1"}, client.refreshPrincipal)
	require.Equal(t, "Team", refreshed.PrincipalType, "the consent principal survives a refresh without principal claims")
	require.Equal(t, "team-1", refreshed.PrincipalID)
}

func TestGrokOAuthServiceRefreshDerivesPrincipalForLegacyAccounts(t *testing.T) {
	client := &grokOAuthClientStub{refreshResponse: &xai.TokenResponse{AccessToken: "at", ExpiresIn: 3600}}
	svc := NewGrokOAuthService(nil, client)
	defer svc.Stop()

	// 旧账号凭据里没有 principal_*，但当前 access token 带主体声明。
	_, err := svc.RefreshAccountToken(context.Background(), &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "rt",
		"access_token":  grokTestJWT(`{"principalType":"User","principalId":"u-9"}`),
	}})
	require.NoError(t, err)
	require.Equal(t, xai.TokenPrincipal{Type: "User", ID: "u-9"}, client.refreshPrincipal)

	// 没有任何主体信息时不发 principal 字段（与旧行为一致）。
	_, err = svc.RefreshAccountToken(context.Background(), &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{
		"refresh_token": "rt", "access_token": "opaque",
	}})
	require.NoError(t, err)
	require.Equal(t, xai.TokenPrincipal{}, client.refreshPrincipal)
}

// 每次授权用随机回环端口（Grok Build 生产登录由系统分配端口），交换时回传同一个值。
func TestGrokOAuthServiceUsesPerSessionLoopbackRedirect(t *testing.T) {
	t.Setenv(xai.EnvRedirectURI, "")
	client := &grokOAuthClientStub{}
	svc := NewGrokOAuthService(nil, client)
	defer svc.Stop()

	auth, err := svc.GenerateAuthURL(context.Background(), nil, "")
	require.NoError(t, err)
	parsed, err := url.Parse(auth.AuthURL)
	require.NoError(t, err)
	redirect := parsed.Query().Get("redirect_uri")
	redirectURL, err := url.Parse(redirect)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", redirectURL.Hostname())
	port, err := strconv.Atoi(redirectURL.Port())
	require.NoError(t, err)
	require.GreaterOrEqual(t, port, 49152)
	require.Equal(t, "grok-build", parsed.Query().Get("referrer"))

	_, err = svc.ExchangeCode(context.Background(), &GrokExchangeCodeInput{SessionID: auth.SessionID, Code: "code", State: auth.State})
	require.NoError(t, err)
	require.Equal(t, redirect, client.exchangeRedirectURI)
}
