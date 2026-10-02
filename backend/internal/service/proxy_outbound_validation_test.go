//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProxyOutboundValidationPreservesBothBindingAndURLChecks(t *testing.T) {
	id := int64(7)
	for name, proxy := range map[string]*Proxy{
		"missing":      nil,
		"mismatched":   {ID: 8, Protocol: "http", Host: "proxy.example", Port: 8080},
		"unsupported":  {ID: id, Protocol: "unsupported", Host: "proxy.example", Port: 8080},
		"host missing": {ID: id, Protocol: "http", Port: 8080},
	} {
		t.Run(name, func(t *testing.T) {
			account := &Account{ProxyID: &id, Proxy: proxy}
			repo := &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) { return proxy, nil }}
			for _, resolve := range []func() (string, error){
				account.ProxyURLForOutbound,
				func() (string, error) { return accountProxyURL(account) },
				func() (string, error) { return resolveProxyURLByID(t.Context(), repo, &id) },
				func() (string, error) { return accountProxyURLWithRepo(t.Context(), repo, account) },
			} {
				raw, err := resolve()
				require.ErrorIs(t, err, ErrAccountProxyUnavailable)
				require.Empty(t, raw)
			}
		})
	}
}
