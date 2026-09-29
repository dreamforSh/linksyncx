package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 不自洽的 TLS profile 在建连接池之前就被拒绝，也不会留下缓存条目。
func TestTLSTransportRejectsInvalidProfileBeforeCaching(t *testing.T) {
	upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	valid := &tlsfingerprint.Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	entry, err := upstream.getClientEntryWithTLS("", 1, 1, valid, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.NotNil(t, entry)
	upstream.mu.RLock()
	cached := len(upstream.clients)
	upstream.mu.RUnlock()
	require.Equal(t, 1, cached)

	invalid := &tlsfingerprint.Profile{Curves: []uint16{29}}
	_, err = upstream.getClientEntryWithTLS("", 2, 1, invalid, service.HTTPUpstreamProfileDefault, false, false)
	require.ErrorContains(t, err, "key share")
	upstream.mu.RLock()
	cached = len(upstream.clients)
	upstream.mu.RUnlock()
	require.Equal(t, 1, cached, "an invalid profile must not create a transport")
}
