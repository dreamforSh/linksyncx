//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

type crsQuotaTokenFunc func(context.Context, *Account) (string, error)

func (f crsQuotaTokenFunc) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	return f(ctx, account)
}

func TestCRSQuotaRejectsInvalidProxyBeforeTokenProvider(t *testing.T) {
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
			svc := &ClaudeQuotaService{usage: &AccountUsageService{}, tokenProvider: crsQuotaTokenFunc(
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

func TestCRSOAuthEntryPointsRejectInvalidProxyBeforeClient(t *testing.T) {
	id := int64(9)
	for _, fixture := range []struct {
		name  string
		proxy *Proxy
		err   error
	}{
		{"missing", nil, nil},
		{"lookup failure", nil, errors.New("fixture lookup failed")},
		{"mismatched", &Proxy{ID: id + 1, Protocol: "http", Host: "proxy.invalid", Port: 8080}, nil},
		{"invalid", &Proxy{ID: id, Protocol: "unsupported", Host: "proxy.invalid", Port: 8080}, nil},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			repo := &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) {
				return fixture.proxy, fixture.err
			}}
			// A call to the deliberately absent client would panic before assertions.
			svc := NewOAuthService(repo, nil)
			t.Cleanup(svc.Stop)
			_, err := svc.GenerateAuthURL(t.Context(), &id)
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			_, err = svc.CookieAuth(t.Context(), &CookieAuthInput{SessionKey: "fixture", ProxyID: &id})
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			svc.sessionStore.Set("fixture", &oauth.OAuthSession{CreatedAt: time.Now()})
			_, err = svc.ExchangeCode(t.Context(), &ExchangeCodeInput{SessionID: "fixture", Code: "fixture", ProxyID: &id})
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			_, retained := svc.sessionStore.Get("fixture")
			require.True(t, retained, "failed proxy resolution must not consume the authorization session")
			_, err = svc.RefreshAccountToken(t.Context(), &Account{
				ProxyID: &id, Credentials: map[string]any{"refresh_token": "fixture"},
			})
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
		})
	}
}
