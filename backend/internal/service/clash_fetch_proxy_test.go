//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// 订阅配置了拉取代理但代理行已不存在：必须报错，不能退回直连去拉订阅（会暴露网关真实出口）。
func TestClashMissingFetchProxyNeverFallsBackToDirect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(clashSubscriptionYAML("test")))
	}))
	t.Cleanup(srv.Close)
	env := newClashTestEnv(t, nil)
	proxyID := int64(9)
	for name, repo := range map[string]ProxyRepository{
		"not found":      &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) { return nil, nil }},
		"lookup failure": &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) { return nil, errors.New("db down") }},
		"clash managed": &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) {
			return &Proxy{ID: proxyID, Protocol: "socks5", Host: "127.0.0.1", Port: 1080, Source: ProxySourceClash}, nil
		}},
	} {
		t.Run(name, func(t *testing.T) {
			env.svc.proxyRepo = repo
			parsed, _, err := env.svc.fetchAndParse(t.Context(), srv.URL, "clash.meta", &proxyID)
			require.ErrorContains(t, err, "fetch proxy")
			require.Nil(t, parsed)
			require.Zero(t, hits.Load())
		})
	}
	env.svc.proxyRepo = nil
	parsed, _, err := env.svc.fetchAndParse(t.Context(), srv.URL, "clash.meta", &proxyID)
	require.Error(t, err)
	require.Nil(t, parsed)
	require.Zero(t, hits.Load())
}

// 订阅 URL 及重定向目标里的凭据（userinfo、路径 token、query）不能出现在存储/日志的错误信息里。
func TestClashErrorsHidePathAndRedirectCredentials(t *testing.T) {
	const source = "https://user:password@sub.example.com/private-path-token?token=query-secret"
	const redirected = "https://redirect.example.com/redirect-path-token?auth=redirect-query-secret"
	require.Equal(t, "https://sub.example.com/***", maskClashURL(source))
	target, err := url.Parse(source)
	require.NoError(t, err)
	for _, failure := range []error{
		errors.New("cannot request " + source),
		&url.Error{Op: "Get", URL: source, Err: errors.New("cannot parse redirect Location " + redirected)},
		&url.Error{Op: "Get", URL: redirected, Err: &url.Error{Op: "parse", URL: redirected, Err: errors.New("invalid port")}},
	} {
		message := redactClashURLError(failure, target)
		for _, secret := range []string{"password", "private-path-token", "query-secret", "redirect-path-token", "redirect-query-secret"} {
			require.NotContains(t, message, secret)
		}
		require.Contains(t, message, "example.com", "host stays visible for diagnosis")
	}
}
