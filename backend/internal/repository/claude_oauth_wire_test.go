package repository

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
)

// 控制面 token 刷新与数据面同一 persona：真实 CLI 2.1.283 的 fetch 只设
// Content-Type / anthropic-beta / User-Agent，Accept: */* 等由 Bun 补在默认头块。
// 直连与经 HTTP 代理两条路径的线级头序都要逐字节一致，gzip 响应能正常解码；
// 请求体字段序固定为 grant_type, refresh_token, client_id（结构体，非 map 字母序）。
func TestClaudeOAuthRefreshTokenEmitsBunWireShape(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)
	proxyAddr, tunnels := startWireConnectProxy(t, nil)

	wantBody := `{"grant_type":"refresh_token","refresh_token":"rt-test","client_id":"` + oauth.ClientID + `"}`
	want := strings.Join([]string{
		"POST /v1/oauth/token HTTP/1.1",
		"Content-Type: application/json",
		"User-Agent: " + claude.OAuthHelperUserAgent,
		"anthropic-beta: oauth-2025-04-20",
		"Connection: keep-alive",
		"Accept: */*",
		"Host: " + upstream.addr,
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: " + strconv.Itoa(len(wantBody)),
	}, "\r\n") + "\r\n\r\n"

	for _, proxyURL := range []string{"", "http://" + proxyAddr} {
		svc := &claudeOAuthService{
			tokenURL: "https://" + upstream.addr + "/v1/oauth/token",
			clientFactory: func(proxy string) (*req.Client, error) {
				return newControlPlaneReqClient(proxy, pool)
			},
		}
		before := len(upstream.capturedHeads())
		_, err := svc.RefreshToken(context.Background(), "rt-test", proxyURL)
		require.NoError(t, err, "proxy=%q", proxyURL)

		heads := upstream.capturedHeads()
		bodies := upstream.capturedBodies()
		require.Len(t, heads, before+1)
		require.Equal(t, want, heads[before], "proxy=%q", proxyURL)
		require.Equal(t, wantBody, string(bodies[before]), "refresh body field order must match the real client")
	}
	require.Equal(t, int64(1), tunnels.Load(), "proxied refresh must go through the CONNECT tunnel")
}

// 授权码交换走真实 CLI 的 axios 1.9.0 登录路径：UA 为 axios/1.9.0，Accept 与
// Accept-Encoding 为 axios 默认值，**不带 anthropic-beta**；请求体字段序为
// grant_type, code, redirect_uri, client_id, code_verifier, state。
func TestClaudeOAuthExchangeCodeEmitsAxiosLoginShape(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)

	svc := &claudeOAuthService{
		tokenURL: "https://" + upstream.addr + "/v1/oauth/token",
		clientFactory: func(proxy string) (*req.Client, error) {
			return newControlPlaneReqClient(proxy, pool)
		},
	}
	_, err := svc.ExchangeCodeForToken(context.Background(), "AUTHCODE#STATEVAL", "verifier-xyz", "", "", false)
	require.NoError(t, err)

	heads := upstream.capturedHeads()
	bodies := upstream.capturedBodies()
	require.Len(t, heads, 1)

	head := heads[0]
	require.Contains(t, head, "User-Agent: "+claude.OAuthLoginUserAgent)
	require.Contains(t, head, "Accept: "+claude.OAuthLoginAccept)
	require.Contains(t, head, "Accept-Encoding: "+claude.OAuthLoginAcceptEncoding)
	require.Contains(t, head, "Content-Type: application/json")
	require.NotContains(t, strings.ToLower(head), "anthropic-beta", "the axios login exchange must not send anthropic-beta")

	wantBody := `{"grant_type":"authorization_code","code":"AUTHCODE","redirect_uri":"` + oauth.RedirectURI +
		`","client_id":"` + oauth.ClientID + `","code_verifier":"verifier-xyz","state":"STATEVAL"}`
	require.Equal(t, wantBody, string(bodies[0]), "exchange body field order must match the real client")
}
