package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCRSTLSProfileChangesReplaceCachedTransport(t *testing.T) {
	upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	first := &tlsfingerprint.Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	a, err := upstream.getClientEntryWithTLS("", 1, 1, first, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	same, err := upstream.getClientEntryWithTLS("", 1, 1, first, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, a, same)
	changed := &tlsfingerprint.Profile{Curves: []uint16{29, 23}, KeyShareGroups: []uint16{29}}
	b, err := upstream.getClientEntryWithTLS("", 1, 1, changed, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.NotSame(t, a, b)
	invalid := &tlsfingerprint.Profile{Curves: []uint16{29}}
	_, err = upstream.getClientEntryWithTLS("", 1, 1, invalid, service.HTTPUpstreamProfileDefault, false, false)
	require.ErrorContains(t, err, "key share")
}

func TestCRSTLSTransportIsolatesAccountsEvenInProxyPoolMode(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationProxy}}
	upstream, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
	require.True(t, ok)
	a, err := upstream.getClientEntryWithTLS("", 1, 1, nil, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	b, err := upstream.getClientEntryWithTLS("", 2, 1, nil, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.NotSame(t, a, b)
}

func TestCRSOAuthHTTPSProxyDoesNotSendPlaintextCONNECT(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)
	proxyAddr, tunnels := startWireConnectProxy(t, &cert)
	proxyURL := "https://" + proxyAddr

	untrusted, err := newControlPlaneReqClient(proxyURL, nil)
	require.NoError(t, err)
	_, err = untrusted.R().SetContext(t.Context()).Get("https://" + upstream.addr)
	require.ErrorContains(t, err, "certificate")
	require.Zero(t, tunnels.Load(), "untrusted HTTPS proxy must receive no CONNECT")
	require.Empty(t, upstream.capturedHeads(), "proxy failure must not fall back to direct")

	trusted, err := newControlPlaneReqClient(proxyURL, pool)
	require.NoError(t, err)
	_, err = trusted.R().SetContext(t.Context()).Get("https://" + upstream.addr)
	require.NoError(t, err, "verified TLS-to-proxy replaces the old unsupported path")
	require.EqualValues(t, 1, tunnels.Load())
	require.Len(t, upstream.capturedHeads(), 1)
}
