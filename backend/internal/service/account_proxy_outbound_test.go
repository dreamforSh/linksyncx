//go:build unit

package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ProxyURLForOutbound 是所有出站路径（转发/测试/探测/配额/计费/OAuth）解析代理的唯一入口，
// 语义必须 fail-closed：分配了代理但不可用时返回错误，绝不返回空串让调用方直连泄漏出口 IP。
func TestAccountProxyURLForOutbound(t *testing.T) {
	proxyID := int64(7)
	loaded := &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example.com", Port: 8080}

	t.Run("no proxy assigned returns empty without error", func(t *testing.T) {
		url, err := (&Account{}).ProxyURLForOutbound()
		require.NoError(t, err)
		require.Empty(t, url)
	})

	t.Run("nil account returns empty without error", func(t *testing.T) {
		var account *Account
		url, err := account.ProxyURLForOutbound()
		require.NoError(t, err)
		require.Empty(t, url)
	})

	t.Run("assigned and loaded returns the proxy url", func(t *testing.T) {
		url, err := (&Account{ProxyID: &proxyID, Proxy: loaded}).ProxyURLForOutbound()
		require.NoError(t, err)
		require.Equal(t, "http://proxy.example.com:8080", url)
	})

	t.Run("assigned but relation missing fails closed", func(t *testing.T) {
		url, err := (&Account{ProxyID: &proxyID}).ProxyURLForOutbound()
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
		require.Empty(t, url, "must not leak a direct-connection empty string")
	})

	t.Run("assigned but relation id mismatched fails closed", func(t *testing.T) {
		mismatched := &Proxy{ID: proxyID + 1, Protocol: "http", Host: "stale.example.com", Port: 3128}
		url, err := (&Account{ProxyID: &proxyID, Proxy: mismatched}).ProxyURLForOutbound()
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
		require.Empty(t, url)
	})
}

// 回归护栏：一旦有人把 helper 改回 fail-open（分配了代理却返回 ("", nil)），本用例立即失败。
func TestAccountProxyURLForOutboundNeverSilentlyGoesDirect(t *testing.T) {
	proxyID := int64(42)
	url, err := (&Account{ProxyID: &proxyID}).ProxyURLForOutbound()
	require.Empty(t, url)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAccountProxyUnavailable))
}
