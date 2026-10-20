package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
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

// 模板被编辑或账号改绑后，下一个请求就要按新的 ClientHello 建池：持续有流量的账号
// 永远到不了空闲回收，缓存键不随 profile 变化的话旧指纹会一直用到重启。
// 只改展示名不影响线级字节，不应重建连接池。
func TestTLSTransportRebuildsPoolWhenProfileWireConfigChanges(t *testing.T) {
	upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	first := &tlsfingerprint.Profile{Name: "node", Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	a, err := upstream.getClientEntryWithTLS("", 1, 1, first, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	same, err := upstream.getClientEntryWithTLS("", 1, 1, first, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, a, same)

	renamed := *first
	renamed.Name = "node (renamed)"
	same, err = upstream.getClientEntryWithTLS("", 1, 1, &renamed, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, a, same, "a display-name change must keep the pool")

	edited := &tlsfingerprint.Profile{Name: "node", Curves: []uint16{29, 23}, KeyShareGroups: []uint16{29}}
	b, err := upstream.getClientEntryWithTLS("", 1, 1, edited, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.NotSame(t, a, b, "an edited template must get a new pool")
	again, err := upstream.getClientEntryWithTLS("", 1, 1, edited, service.HTTPUpstreamProfileDefault, false, false)
	require.NoError(t, err)
	require.Same(t, b, again)
}

// proxy 隔离模式是运维的选择：同一代理下的账号共享连接池。但共享池不能顺带共享 ClientHello——
// 账号 B 绑定了别的模板却借用账号 A 的 transport，就等于静默忽略了 B 的配置。
// 缓存键只追加模板摘要，所以同代理、同模板的账号继续共用一个池，不同模板各用各的池；
// account_proxy 模式的按账号隔离保持不变。profile 必须非 nil：DoWithTLS 对 nil profile
// 走普通 Do，根本到不了这里。
func TestTLSTransportPoolIsolationHonoursEachAccountsProfile(t *testing.T) {
	const proxyURL = "http://127.0.0.1:9"
	nodeProfile := &tlsfingerprint.Profile{Name: "node", Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	bunProfile := &tlsfingerprint.Profile{Name: "bun", Curves: []uint16{29, 23}, KeyShareGroups: []uint16{29}}
	nodeCopy := *nodeProfile
	nodeCopy.Name = "node (copy)"

	t.Run("proxy isolation", func(t *testing.T) {
		cfg := &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: config.ConnectionPoolIsolationProxy}}
		upstream, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
		require.True(t, ok)
		acct1, err := upstream.getClientEntryWithTLS(proxyURL, 1, 1, nodeProfile, service.HTTPUpstreamProfileDefault, false, false)
		require.NoError(t, err)
		acct2, err := upstream.getClientEntryWithTLS(proxyURL, 2, 1, bunProfile, service.HTTPUpstreamProfileDefault, false, false)
		require.NoError(t, err)
		require.NotSame(t, acct1, acct2, "an account must not borrow another account's ClientHello")
		acct3, err := upstream.getClientEntryWithTLS(proxyURL, 3, 1, &nodeCopy, service.HTTPUpstreamProfileDefault, false, false)
		require.NoError(t, err)
		require.Same(t, acct1, acct3, "accounts on the same proxy with the same template keep sharing one pool")
	})

	t.Run("account_proxy isolation", func(t *testing.T) {
		upstream, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
		require.True(t, ok)
		acct1, err := upstream.getClientEntryWithTLS(proxyURL, 1, 1, nodeProfile, service.HTTPUpstreamProfileDefault, false, false)
		require.NoError(t, err)
		acct2, err := upstream.getClientEntryWithTLS(proxyURL, 2, 1, nodeProfile, service.HTTPUpstreamProfileDefault, false, false)
		require.NoError(t, err)
		require.NotSame(t, acct1, acct2, "per-account isolation is unchanged")
	})
}

// 用量 / 配额探测曾经传并发 0，这个测试界定它影响的范围：account / account_proxy 隔离下
// 池大小按账号并发定、并发是池配置键的一部分，同一账号换个并发就会拆池重建；proxy 隔离
// 不看并发，换并发仍命中同一个池。所以探测补传并发只改变前两种模式的行为。
func TestTLSTransportPoolSizeFollowsConcurrencyOnlyUnderAccountIsolation(t *testing.T) {
	profile := &tlsfingerprint.Profile{Name: "node", Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	cases := []struct {
		isolation   string
		rebuildsOn0 bool
	}{
		{config.ConnectionPoolIsolationAccountProxy, true},
		{config.ConnectionPoolIsolationAccount, true},
		{config.ConnectionPoolIsolationProxy, false},
	}
	for _, tc := range cases {
		t.Run(tc.isolation, func(t *testing.T) {
			cfg := &config.Config{Gateway: config.GatewayConfig{ConnectionPoolIsolation: tc.isolation}}
			upstream, ok := NewHTTPUpstream(cfg).(*httpUpstreamService)
			require.True(t, ok)
			forward, err := upstream.getClientEntryWithTLS("", 7, 3, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			sameConcurrency, err := upstream.getClientEntryWithTLS("", 7, 3, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			require.Same(t, forward, sameConcurrency, "the same concurrency always reuses the pool")

			zero, err := upstream.getClientEntryWithTLS("", 7, 0, profile, service.HTTPUpstreamProfileDefault, false, false)
			require.NoError(t, err)
			if tc.rebuildsOn0 {
				require.NotSame(t, forward, zero, "concurrency 0 changes the pool settings and rebuilds the pool")
			} else {
				require.Same(t, forward, zero, "proxy isolation ignores concurrency, so concurrency 0 reuses the pool")
			}
			upstream.mu.RLock()
			cached := len(upstream.clients)
			upstream.mu.RUnlock()
			require.Equal(t, 1, cached, "a rebuild replaces the entry under the same key instead of adding one")
		})
	}
}
