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
		{"invalid", &Proxy{ID: id, Protocol: "unsupported", Host: "proxy.invalid", Port: 1080}},
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

// 配额刷新 / 额度重置 / 原始用量读取三条入口都在发出任何上游请求前拒绝失效的代理绑定
// （关系缺失，或 URL 无效——后者交给传输层只会变成一次普通请求失败）。
func TestClaudeAccountAPIsRejectBrokenProxyBinding(t *testing.T) {
	proxyID := int64(9)
	for _, fixture := range []struct {
		name  string
		proxy *Proxy
	}{
		{"missing", nil},
		{"invalid", &Proxy{ID: proxyID, Protocol: "unsupported", Host: "proxy.invalid", Port: 1080}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := claudeQuotaTestAccount(nil)
			account.Credentials = map[string]any{"access_token": "dummy"}
			account.ProxyID, account.Proxy = &proxyID, fixture.proxy
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
		})
	}
}

// claudeUsageOptsRecorder 记录原始用量读取实际带出的选项（token 与代理）。
type claudeUsageOptsRecorder struct {
	opts []*ClaudeUsageFetchOptions
}

func (r *claudeUsageOptsRecorder) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*ClaudeUsageResponse, error) {
	return r.FetchUsageWithOptions(ctx, &ClaudeUsageFetchOptions{AccessToken: accessToken, ProxyURL: proxyURL})
}

func (r *claudeUsageOptsRecorder) FetchUsageWithOptions(_ context.Context, opts *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	r.opts = append(r.opts, opts)
	return &ClaudeUsageResponse{}, nil
}

// 主动用量读取（GetUsage → fetchOAuthUsageRaw）经 token provider 取 token，临近过期会刷新；
// 代理绑定失效时必须先拒绝，provider 一次都不能被调用（刷新本身也是一次上游请求）。
func TestClaudeUsageRawRejectsBrokenProxyBeforeTokenProvider(t *testing.T) {
	id := int64(9)
	for _, fixture := range []struct {
		name  string
		proxy *Proxy
	}{
		{"missing", nil},
		{"mismatched", &Proxy{ID: id + 1, Protocol: "socks5", Host: "proxy.invalid", Port: 1080}},
		{"invalid", &Proxy{ID: id, Protocol: "unsupported", Host: "proxy.invalid", Port: 1080}},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			account := claudeQuotaTestAccount(nil)
			account.Credentials = map[string]any{"access_token": "stored"}
			account.ProxyID, account.Proxy = &id, fixture.proxy
			calls := 0
			fetcher := &claudeUsageOptsRecorder{}
			usage := &AccountUsageService{usageFetcher: fetcher, claudeTokenProvider: quotaTokenProviderFunc(
				func(context.Context, *Account) (string, error) {
					calls++
					return "must-not-refresh", nil
				})}
			resp, err := usage.fetchOAuthUsageRaw(t.Context(), &account)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Nil(t, resp)
			require.Zero(t, calls)
			require.Empty(t, fetcher.opts)
		})
	}

	// 绑定正常时照常经 provider 取 token，并带上账号代理。
	t.Run("valid binding", func(t *testing.T) {
		account := claudeQuotaTestAccount(nil)
		account.Credentials = map[string]any{"access_token": "stored"}
		account.ProxyID, account.Proxy = &id, &Proxy{ID: id, Protocol: "socks5", Host: "127.0.0.1", Port: 7891}
		fetcher := &claudeUsageOptsRecorder{}
		usage := &AccountUsageService{usageFetcher: fetcher, claudeTokenProvider: quotaTokenProviderFunc(
			func(context.Context, *Account) (string, error) { return "fresh-token", nil })}
		_, err := usage.fetchOAuthUsageRaw(t.Context(), &account)
		require.NoError(t, err)
		require.Len(t, fetcher.opts, 1)
		require.Equal(t, "fresh-token", fetcher.opts[0].AccessToken)
		require.Equal(t, "socks5://127.0.0.1:7891", fetcher.opts[0].ProxyURL)
	})
}
