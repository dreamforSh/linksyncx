//go:build unit

package clashsub

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const clashYAML = `
port: 7890
proxies:
  - {name: "HK 01", type: ss, server: hk.example.com, port: 8388, cipher: aes-128-gcm, password: pw}
  - {name: "direct-node", type: direct}
  - {name: "US 01", type: vmess, server: us.example.com, port: 443, uuid: 11111111-1111-1111-1111-111111111111, alterId: 0, cipher: auto, interface-name: eth0, routing-mark: 1}
  - {name: "chained", type: trojan, server: jp.example.com, port: 443, password: pw, dialer-proxy: "HK 01"}
  - {name: "bad-port", type: trojan, server: jp.example.com, port: 70000, password: pw}
  - {type: trojan, server: sg.example.com, port: 443, password: pw}
proxy-groups:
  - {name: auto, type: select, proxies: ["HK 01"]}
rules:
  - MATCH,auto
`

func TestParseClashYAML(t *testing.T) {
	result, err := Parse([]byte(clashYAML), Options{})
	require.NoError(t, err)
	require.Equal(t, FormatClashYAML, result.Format)
	require.Len(t, result.Nodes, 3)

	require.Equal(t, "HK 01", result.Nodes[0].Name)
	require.Equal(t, "ss", result.Nodes[0].Type)
	require.Equal(t, 8388, result.Nodes[0].Port)

	us := result.Nodes[1]
	require.Equal(t, "vmess", us.Type)
	require.NotContains(t, us.Config, "interface-name")
	require.NotContains(t, us.Config, "routing-mark")

	require.Equal(t, "trojan-sg.example.com:443", result.Nodes[2].Name, "unnamed nodes get a generated name")

	reasons := map[string]string{}
	for _, skipped := range result.Skipped {
		reasons[skipped.Name] = skipped.Reason
	}
	require.Contains(t, reasons["direct-node"], "unsupported type")
	require.Contains(t, reasons["chained"], "dialer-proxy")
	require.Equal(t, "invalid port", reasons["bad-port"])
}

func TestParseBase64WrappedYAML(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(clashYAML))
	// Wrap lines the way some panels do.
	wrapped := encoded[:40] + "\n" + encoded[40:]
	result, err := Parse([]byte(wrapped), Options{})
	require.NoError(t, err)
	require.Equal(t, FormatBase64YAML, result.Format)
	require.Len(t, result.Nodes, 3)
}

func TestParseBareProxyList(t *testing.T) {
	result, err := Parse([]byte("- {name: a, type: ss, server: a.example.com, port: 1, cipher: aes-128-gcm, password: p}\n"), Options{})
	require.NoError(t, err)
	require.Equal(t, FormatClashYAML, result.Format)
	require.Len(t, result.Nodes, 1)
}

func shareLinks() string {
	ssUser := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:pw"))
	vmessJSON := `{"v":"2","ps":"VMESS","add":"vm.example.com","port":"443","id":"22222222-2222-2222-2222-222222222222","aid":"0","net":"ws","type":"none","host":"vm.example.com","path":"/ws","tls":"tls"}`
	return strings.Join([]string{
		"ss://" + ssUser + "@ss.example.com:8388#SS%20Node",
		"trojan://pw@tj.example.com:443?sni=tj.example.com#Trojan",
		"vless://33333333-3333-3333-3333-333333333333@vl.example.com:443?encryption=none&security=tls&type=ws&path=%2Fws&host=vl.example.com#VLESS",
		"hysteria2://auth@hy.example.com:443?sni=hy.example.com#HY2",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(vmessJSON)),
		"not-a-link",
	}, "\n")
}

func TestParseShareLinks(t *testing.T) {
	for name, payload := range map[string]string{
		"plain":  shareLinks(),
		"base64": base64.StdEncoding.EncodeToString([]byte(shareLinks())),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := Parse([]byte(payload), Options{})
			require.NoError(t, err)
			require.Equal(t, FormatURIList, result.Format)
			byName := map[string]Node{}
			for _, node := range result.Nodes {
				byName[node.Name] = node
			}
			require.Len(t, byName, 5)
			require.Equal(t, "ss", byName["SS Node"].Type)
			require.Equal(t, 8388, byName["SS Node"].Port)
			require.Equal(t, "trojan", byName["Trojan"].Type)
			require.Equal(t, "vless", byName["VLESS"].Type)
			require.Equal(t, "hysteria2", byName["HY2"].Type)
			require.Equal(t, "vmess", byName["VMESS"].Type)
			require.Equal(t, "vm.example.com", byName["VMESS"].Server)
		})
	}
}

func TestParseRejectsUnusablePayloads(t *testing.T) {
	cases := map[string]struct {
		body string
		want error
	}{
		"empty":     {"  \n", ErrEmpty},
		"html":      {"<!DOCTYPE html><html><body>login</body></html>", ErrHTMLPage},
		"providers": {"proxy-providers:\n  p1: {type: http, url: https://example.com}\n", ErrProvidersOnly},
		"garbage":   {"hello world", ErrNoProxies},
		"no-usable": {"proxies:\n  - {name: d, type: direct}\n", ErrNoProxies},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body), Options{})
			require.True(t, errors.Is(err, tc.want), "got %v", err)
		})
	}
}

func TestParseNodeLimit(t *testing.T) {
	_, err := Parse([]byte(clashYAML), Options{MaxNodes: 2})
	require.ErrorIs(t, err, ErrTooManyNodes)
}

func TestConfigHashIgnoresNameAndKeyOrder(t *testing.T) {
	a := map[string]any{"name": "a", "type": "ss", "server": "s", "port": 1, "password": "x"}
	b := map[string]any{"password": "x", "port": 1, "server": "s", "type": "ss", "name": "renamed"}
	c := map[string]any{"name": "a", "type": "ss", "server": "s", "port": 1, "password": "rotated"}
	require.Equal(t, ConfigHash(a), ConfigHash(b))
	require.NotEqual(t, ConfigHash(a), ConfigHash(c))
}

func TestParseUserInfo(t *testing.T) {
	info, ok := ParseUserInfo("upload=455727941; download=6174315083; total=1073741824000; expire=1924992000")
	require.True(t, ok)
	require.EqualValues(t, 455727941, info.Upload)
	require.EqualValues(t, 6174315083, info.Download)
	require.EqualValues(t, 1073741824000, info.Total)
	require.NotNil(t, info.Expire)
	require.Equal(t, time.Unix(1924992000, 0).UTC(), *info.Expire)

	info, ok = ParseUserInfo("upload=1; download=2; total=3; expire=0")
	require.True(t, ok)
	require.Nil(t, info.Expire)

	_, ok = ParseUserInfo("garbage")
	require.False(t, ok)
}

func TestClassifyServer(t *testing.T) {
	require.Equal(t, ScopePublic, ClassifyServer("hk.example.com"))
	require.Equal(t, ScopePublic, ClassifyServer("1.1.1.1"))
	require.Equal(t, ScopeLoopback, ClassifyServer("127.0.0.1"))
	require.Equal(t, ScopeLoopback, ClassifyServer("localhost"))
	require.Equal(t, ScopeLoopback, ClassifyServer("[::1]"))
	require.Equal(t, ScopeLoopback, ClassifyServer("0.0.0.0"))
	require.Equal(t, ScopePrivate, ClassifyServer("10.0.0.8"))
	require.Equal(t, ScopePrivate, ClassifyServer("100.64.1.1"))
	require.Equal(t, ScopeLinkLocal, ClassifyServer("169.254.169.254"))
}
