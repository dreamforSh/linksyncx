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

	// 无效 URL 交给传输层只会得到一次普通传输错误（不触发"账号不可用"切号），
	// 必须在 helper 里判为不可用；错误带代理 ID 便于排查，但不回显代理配置与凭据。
	for _, tc := range []struct {
		name  string
		proxy *Proxy
	}{
		{"unsupported scheme", &Proxy{ID: proxyID, Protocol: "ftp", Host: "proxy.example.com", Port: 21, Username: "user", Password: "s3cr3tpw"}},
		{"empty scheme", &Proxy{ID: proxyID, Protocol: "", Host: "proxy.example.com", Port: 8080, Username: "user", Password: "s3cr3tpw"}},
		{"missing host", &Proxy{ID: proxyID, Protocol: "http", Port: 8080, Username: "user", Password: "s3cr3tpw"}},
		{"unparseable host", &Proxy{ID: proxyID, Protocol: "http", Host: "bad host", Port: 8080, Username: "user", Password: "s3cr3tpw"}},
	} {
		t.Run("invalid url fails closed: "+tc.name, func(t *testing.T) {
			account := &Account{ProxyID: &proxyID, Proxy: tc.proxy}
			url, err := account.ProxyURLForOutbound()
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Empty(t, url, "must not leak a direct-connection empty string")
			require.Contains(t, err.Error(), "proxy 7 has an invalid url")
			require.NotContains(t, err.Error(), "s3cr3tpw")

			// 指纹用的出口标识与出站共用同一 fail-closed 规则。
			identity, err := account.ProxyEgressIdentityURL()
			require.ErrorIs(t, err, ErrAccountProxyUnavailable)
			require.Empty(t, identity)
		})
	}
}

// 校验只拦无效行：Proxy.URL() 为合法代理行生成的各种形态必须原样放行（不做 socks5→socks5h
// 等改写，线上行为不变），否则会误伤正在工作的账号。
func TestAccountProxyURLForOutboundAcceptsValidProxyRows(t *testing.T) {
	proxyID := int64(7)
	for _, tc := range []struct {
		name  string
		proxy Proxy
		want  string
	}{
		{"http", Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080}, "http://proxy.example.com:8080"},
		{"https", Proxy{Protocol: "https", Host: "proxy.example.com", Port: 8443}, "https://proxy.example.com:8443"},
		{"socks5", Proxy{Protocol: "socks5", Host: "127.0.0.1", Port: 1080}, "socks5://127.0.0.1:1080"},
		{"socks5h", Proxy{Protocol: "socks5h", Host: "proxy.example.com", Port: 1080}, "socks5h://proxy.example.com:1080"},
		{
			"special-character credentials",
			Proxy{Protocol: "http", Host: "proxy.example.com", Port: 8080, Username: "u@x:y/z", Password: "p@ss:w/rd%#?&="},
			"http://u%40x%3Ay%2Fz:p%40ss%3Aw%2Frd%25%23%3F&=@proxy.example.com:8080",
		},
		{
			"ipv6 with credentials",
			Proxy{Protocol: "socks5", Host: "2001:db8::1", Port: 1080, Username: "user", Password: "pw"},
			"socks5://user:pw@[2001:db8::1]:1080",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := tc.proxy
			proxy.ID = proxyID
			url, err := (&Account{ProxyID: &proxyID, Proxy: &proxy}).ProxyURLForOutbound()
			require.NoError(t, err)
			require.Equal(t, tc.want, url)
		})
	}

	// Clash 共享出口：确认运行中的核心接受账号标签用户名后，返回带标签的 URL，同样通过校验。
	t.Run("clash tagged username", func(t *testing.T) {
		t.Cleanup(func() { clashAccountRoutes.set(nil) })
		clash := &Proxy{ID: 77, Protocol: "socks5", Host: "127.0.0.1", Port: 7891, Username: "clash-u", Password: "pw", Source: ProxySourceClash}
		clashID := clash.ID
		clashAccountRoutes.set(map[int64]map[int64]struct{}{clashID: {42: {}}})
		account := &Account{ID: 42, ProxyID: &clashID, Proxy: clash}

		url, err := account.ProxyURLForOutbound()
		require.NoError(t, err)
		require.Equal(t, "socks5://clash-u~a42:pw@127.0.0.1:7891", url)
		identity, err := account.ProxyEgressIdentityURL()
		require.NoError(t, err)
		require.Equal(t, "socks5://clash-u:pw@127.0.0.1:7891", identity, "egress identity stays untagged")
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
