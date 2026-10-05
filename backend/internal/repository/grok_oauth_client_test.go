//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

// token 端点请求对齐 Grok Build（xai-grok-login oidc/protocol.rs）：表单字段序固定，
// UA 为 Grok Build 进程 UA，交换带 x-grok-client-version，刷新回传 principal；
// 任何头部/字段都不能出现网关自身的标识。
func TestGrokOAuthClientExchangeAndRefreshUseFormFields(t *testing.T) {
	version := xai.ResolveCLIVersion()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		body := string(raw)
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		require.Equal(t, xai.CLIUserAgent(version), r.Header.Get("User-Agent"))
		require.Equal(t, "*/*", r.Header.Get("Accept"))
		for name, values := range r.Header {
			require.NotContains(t, strings.ToLower(name+strings.Join(values, ",")), "sub2api", "no gateway identity on the wire")
		}

		switch {
		case strings.HasPrefix(body, "grant_type=authorization_code&"):
			require.Equal(t, "grant_type=authorization_code&code=auth-code&redirect_uri=http%3A%2F%2F127.0.0.1%3A56121%2Fcallback&client_id=client-id&code_verifier=verifier", body)
			require.Equal(t, version, r.Header.Get("x-grok-client-version"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "exchange-access",
				"refresh_token": "exchange-refresh",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"scope":         "openid api:access",
			})
		case strings.HasPrefix(body, "grant_type=refresh_token&"):
			require.Empty(t, r.Header.Get("x-grok-client-version"), "Grok Build refresh carries no client-version header")
			require.Equal(t, "grant_type=refresh_token&refresh_token=refresh-token&client_id=client-id&principal_type=Team&principal_id=team-1", body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "refresh-access",
				"refresh_token": "refresh-rotated",
				"token_type":    "Bearer",
				"expires_in":    7200,
			})
		default:
			http.Error(w, "unexpected grant_type", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	// Tests inject a loopback token endpoint; allowlist requires unsafe override.
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	t.Setenv(xai.EnvTokenURL, server.URL)

	client := NewGrokOAuthClient()
	exchanged, err := client.ExchangeCode(context.Background(), "auth-code", "verifier", "http://127.0.0.1:56121/callback", "", "client-id")
	require.NoError(t, err)
	require.Equal(t, "exchange-access", exchanged.AccessToken)
	require.Equal(t, "exchange-refresh", exchanged.RefreshToken)
	require.Equal(t, int64(3600), exchanged.ExpiresIn)
	require.Equal(t, "openid api:access", exchanged.Scope)

	refreshed, err := client.RefreshToken(context.Background(), "refresh-token", "", "client-id", xai.TokenPrincipal{Type: "Team", ID: "team-1"})
	require.NoError(t, err)
	require.Equal(t, "refresh-access", refreshed.AccessToken)
	require.Equal(t, "refresh-rotated", refreshed.RefreshToken)
	require.Equal(t, int64(7200), refreshed.ExpiresIn)
}

func TestGrokOAuthClientRefreshForbiddenClassifiesOnlyExplicitEntitlement(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantReason string
	}{
		{name: "explicit entitlement", body: `{"error":"access_denied"}`, wantReason: "GROK_OAUTH_ENTITLEMENT_DENIED"},
		{name: "generic forbidden", body: `{"error":"forbidden"}`, wantReason: "GROK_OAUTH_TOKEN_REFRESH_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
			t.Setenv(xai.EnvTokenURL, server.URL)

			client := NewGrokOAuthClient()
			_, err := client.RefreshToken(context.Background(), "refresh-token", "", "client-id", xai.TokenPrincipal{})
			require.Error(t, err)
			require.Contains(t, strings.ToUpper(err.Error()), tt.wantReason)
		})
	}
}

func TestGrokOAuthClientStatusErrorRedactsSensitiveResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","access_token":"access-secret","refresh_token":"refresh-secret","code_verifier":"verifier-secret"}`))
	}))
	defer server.Close()
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	t.Setenv(xai.EnvTokenURL, server.URL)

	client := NewGrokOAuthClient()
	_, err := client.RefreshToken(context.Background(), "refresh-secret", "", "client-id", xai.TokenPrincipal{})
	require.Error(t, err)

	errText := err.Error()
	require.Contains(t, errText, "status 400")
	require.Contains(t, errText, `\"refresh_token\":\"***\"`)
	require.NotContains(t, errText, "access-secret")
	require.NotContains(t, errText, "refresh-secret")
	require.NotContains(t, errText, "verifier-secret")
}

func TestGrokOAuthEntitlementDenialRequiresExplicitEvidence(t *testing.T) {
	t.Parallel()

	require.True(t, grokOAuthHasExplicitEntitlementDenial(`{"error":"access_denied"}`))
	require.True(t, grokOAuthHasExplicitEntitlementDenial(`{"code":"entitlement_denied"}`))
	require.True(t, grokOAuthHasExplicitEntitlementDenial(`{"message":"no active Grok subscription"}`))
	require.False(t, grokOAuthHasExplicitEntitlementDenial(`{"error":"forbidden","message":"request forbidden"}`))
	require.False(t, grokOAuthHasExplicitEntitlementDenial(`<html>403 Forbidden</html>`))
	require.False(t, grokOAuthHasExplicitEntitlementDenial(`{"error":"access_denied","message":"You have run out of credits"}`))
	require.False(t, grokOAuthHasExplicitEntitlementDenial(`{"code":"subscription_required","message":"included free usage exhausted"}`))
}

func TestNewGrokOAuthClient_UnvalidatedTokenURLFallsBackToDefault(t *testing.T) {
	// Without unsafe overrides, a random env TokenURL must not be used (fail-closed).
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "")
	t.Setenv(xai.EnvTokenURL, "https://evil.example/oauth/token")

	client := NewGrokOAuthClient().(*grokOAuthClient)
	require.Equal(t, xai.DefaultTokenURL, client.tokenURL)
}
