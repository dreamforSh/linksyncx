//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type quotaTokenProviderFunc func(context.Context, *Account) (string, error)

func (f quotaTokenProviderFunc) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	return f(ctx, account)
}

// 代理绑定失效的账号：先校验代理再取 token，避免为一个注定不能发的请求去刷新 OAuth token
// （刷新本身也是一次上游调用）。
func TestClaudeQuotaRejectsBrokenProxyBeforeTokenProvider(t *testing.T) {
	id := int64(9)
	for _, fixture := range []struct {
		name  string
		proxy *Proxy
	}{
		{"missing", nil},
		{"mismatched", &Proxy{ID: id + 1, Protocol: "socks5", Host: "proxy.invalid", Port: 1080}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := claudeQuotaTestAccount(nil)
			account.ProxyID, account.Proxy = &id, fixture.proxy
			calls := 0
			svc := &ClaudeQuotaService{usage: &AccountUsageService{}, tokenProvider: quotaTokenProviderFunc(
				func(context.Context, *Account) (string, error) {
					calls++
					return "must-not-refresh", nil
				})}
			opts, err := svc.fetchOptions(t.Context(), &account)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Nil(t, opts)
			require.Zero(t, calls)
		})
	}
}

// 配额刷新 / 额度重置 / 原始用量读取三条入口都在发出任何上游请求前拒绝失效的代理绑定。
func TestClaudeAccountAPIsRejectBrokenProxyBinding(t *testing.T) {
	account := claudeQuotaTestAccount(nil)
	account.Credentials = map[string]any{"access_token": "dummy"}
	proxyID := int64(9)
	account.ProxyID, account.Proxy = &proxyID, nil
	fetcher := &claudeQuotaFakeFetcher{body: `{}`}
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile}
	svc, _ := newClaudeQuotaTestService(account, fetcher, api)
	_, err := svc.RefreshQuota(t.Context(), account.ID)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, err = svc.ResetCredit(t.Context(), account.ID, "")
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	_, err = svc.usage.fetchOAuthUsageRaw(t.Context(), &account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Empty(t, fetcher.queries)
	require.Zero(t, api.profileCalls)
	require.Empty(t, api.claims)
}
