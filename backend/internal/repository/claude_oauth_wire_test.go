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

// 控制面 token 刷新走真实 CLI 2.1.287 的 axios 刷新路径（npe）：与授权码交换同一
// 头部形状（axios/1.9.0 UA + axios 默认 Accept / Accept-Encoding，不带 anthropic-beta），
// 请求体字段序固定为 grant_type, refresh_token, client_id, scope。直连与经 HTTP 代理
// 两条路径的线级头部要一致，代理请求必须走 CONNECT 隧道。
func TestClaudeOAuthRefreshTokenEmitsAxiosWireShape(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)
	proxyAddr, tunnels := startWireConnectProxy(t, nil)

	wantBody := `{"grant_type":"refresh_token","refresh_token":"rt-test","client_id":"` + oauth.ClientID +
		`","scope":"` + oauth.ScopeAPI + `"}`

	var firstHead string
	for _, proxyURL := range []string{"", "http://" + proxyAddr} {
		svc := &claudeOAuthService{
			tokenURL: "https://" + upstream.addr + "/v1/oauth/token",
			clientFactory: func(proxy string) (*req.Client, error) {
				return newControlPlaneReqClient(proxy, pool)
			},
		}
		before := len(upstream.capturedHeads())
		_, err := svc.RefreshToken(context.Background(), "rt-test", oauth.ScopeAPI, proxyURL)
		require.NoError(t, err, "proxy=%q", proxyURL)

		heads := upstream.capturedHeads()
		bodies := upstream.capturedBodies()
		require.Len(t, heads, before+1)
		head := heads[before]
		require.True(t, strings.HasPrefix(head, "POST /v1/oauth/token HTTP/1.1\r\n"), "proxy=%q head=%q", proxyURL, head)
		require.Contains(t, head, "User-Agent: "+claude.OAuthLoginUserAgent)
		require.Contains(t, head, "Accept: "+claude.OAuthLoginAccept)
		require.Contains(t, head, "Accept-Encoding: "+claude.OAuthLoginAcceptEncoding)
		require.Contains(t, head, "Content-Type: application/json")
		require.Contains(t, head, "Content-Length: "+strconv.Itoa(len(wantBody)))
		require.NotContains(t, strings.ToLower(head), "anthropic-beta", "the axios refresh must not send anthropic-beta")
		require.Equal(t, wantBody, string(bodies[before]), "refresh body field order must match the real client")
		if firstHead == "" {
			firstHead = head
		} else {
			require.Equal(t, firstHead, head, "direct and proxied refresh must look the same on the wire")
		}
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
