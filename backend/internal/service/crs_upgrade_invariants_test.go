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

func TestCRSClashMissingFetchProxyNeverFallsBackToDirect(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(clashSubscriptionYAML("test")))
	}))
	t.Cleanup(srv.Close)
	env := newClashTestEnv(t, nil)
	env.svc.proxyRepo = &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) { return nil, nil }}
	proxyID := int64(9)
	parsed, _, err := env.svc.fetchAndParse(t.Context(), srv.URL, "clash.meta", &proxyID)
	require.ErrorContains(t, err, "fetch proxy not found")
	require.Nil(t, parsed)
	require.Zero(t, hits.Load())
}

func TestCRSClashErrorsHidePathAndRedirectCredentials(t *testing.T) {
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
	}
}

func TestCRSClaudeAccountAPIsRejectBrokenProxyBinding(t *testing.T) {
	for _, proxy := range []*Proxy{nil, {ID: 9, Protocol: "unsupported", Host: "example.invalid", Port: 443}} {
		account := claudeQuotaTestAccount(nil)
		account.Credentials = map[string]any{"access_token": "dummy"}
		proxyID := int64(9)
		account.ProxyID, account.Proxy = &proxyID, proxy
		fetcher := &claudeQuotaFakeFetcher{body: `{}`}
		api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile}
		svc, _ := newClaudeQuotaTestService(account, fetcher, api)
		_, err := svc.RefreshQuota(t.Context(), account.ID)
		require.ErrorContains(t, err, "proxy")
		_, err = svc.ResetCredit(t.Context(), account.ID, "")
		require.ErrorContains(t, err, "proxy")
		_, err = svc.usage.fetchOAuthUsageRaw(t.Context(), &account)
		require.ErrorContains(t, err, "proxy")
		require.Empty(t, fetcher.queries)
		require.Zero(t, api.profileCalls)
		require.Empty(t, api.claims)
	}
}
