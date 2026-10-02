package tlsfingerprint

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

func TestCRSTLSRejectsKeyShareOutsideSupportedGroupsBeforeDial(t *testing.T) {
	calls := 0
	dialer := NewDialer(&Profile{Curves: []uint16{29}}, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("network must not run")
	})
	_, err := dialer.DialTLSContext(t.Context(), "tcp", "example.invalid:443")
	require.ErrorContains(t, err, "key share")
	require.Zero(t, calls)
	require.NoError(t, ValidateProfile(&Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29}}))
	require.NoError(t, ValidateProfile(nil))
	require.NoError(t, ValidateProfile(&Profile{}))
	require.ErrorContains(t, ValidateProfile(&Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29, 23}}), "key share 23")
	for _, raw := range []string{"http://proxy.invalid:8080", "https://proxy.invalid:443", "socks5h://proxy.invalid:1080"} {
		t.Run(raw, func(t *testing.T) {
			proxyURL, err := url.Parse(raw)
			require.NoError(t, err)
			dialer, err := newProxyDialer(&Profile{Curves: []uint16{29}}, proxyURL, DialOptions{}, func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("network must not run")
			})
			require.NoError(t, err)
			_, err = dialer.DialTLSContext(t.Context(), "tcp", "example.invalid:443")
			require.ErrorContains(t, err, "key share")
			require.Zero(t, calls)
		})
	}
}

func TestCRSTLSProfileCacheKeyTracksWireConfiguration(t *testing.T) {
	a := &Profile{Name: "before", Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	b := *a
	b.Name = "renamed"
	require.Equal(t, ProfileCacheKey(a), ProfileCacheKey(&b))
	b.KeyShareGroups = []uint16{23}
	require.NotEqual(t, ProfileCacheKey(a), ProfileCacheKey(&b))
	require.Equal(t, ProfileCacheKey(nil), ProfileCacheKey(&Profile{}))
}

func TestCRSTLSSessionTicketsAreScopedToDialer(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:1080")
	require.NoError(t, err)
	caches := []utls.ClientSessionCache{
		NewDialer(nil, nil).dialer.opts.SessionCache,
		NewDialer(nil, nil).dialer.opts.SessionCache,
		NewHTTPProxyDialer(nil, proxyURL).dialer.opts.SessionCache,
		NewSOCKS5ProxyDialer(nil, proxyURL).dialer.opts.SessionCache,
	}
	for i, cache := range caches {
		_, found := cache.Get("same.example")
		require.False(t, found, "ticket leaked from an earlier dialer")
		cache.Put("same.example", &utls.ClientSessionState{})
		_, found = cache.Get("same.example")
		require.True(t, found, "dialer %d must retain its own ticket", i)
	}
}
