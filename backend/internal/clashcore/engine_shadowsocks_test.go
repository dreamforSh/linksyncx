//go:build unit

package clashcore

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// startObfsHTTPServer terminates simple-obfs "http" mode (the obfs-local plugin of ss
// share links) and relays the unwrapped stream to target.
func startObfsHTTPServer(t *testing.T, target string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				// The client wraps its first payload in a fake HTTP upgrade request.
				req, err := http.ReadRequest(reader)
				if err != nil {
					return
				}
				first, err := io.ReadAll(io.LimitReader(req.Body, req.ContentLength))
				if err != nil {
					return
				}
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = upstream.Close() }()
				if _, err := upstream.Write(first); err != nil {
					return
				}
				go func() { _, _ = io.Copy(upstream, reader) }()
				// The client expects the fake upgrade response in front of the first reply.
				buf := make([]byte, 32*1024)
				n, err := upstream.Read(buf)
				if n > 0 {
					header := "HTTP/1.1 101 Switching Protocols\r\nServer: nginx\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"
					if _, werr := conn.Write(append([]byte(header), buf[:n]...)); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
				_, _ = io.Copy(conn, upstream)
			}(conn)
		}
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return addr.Port
}

// TestEngineShadowsocksNodes routes through shadowsocks nodes imported from share links
// (parsed the way subscriptions are, so the port is a string), both plain and with the
// obfs-local http plugin, against a shadowsocks inbound of the same core.
func TestEngineShadowsocksNodes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()

	ssPort, plainPort, obfsPort := freePort(t), freePort(t), freePort(t)
	obfsServerPort := startObfsHTTPServer(t, fmt.Sprintf("127.0.0.1:%d", ssPort))

	userinfo := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:test-password"))
	links := fmt.Sprintf("ss://%s@127.0.0.1:%d#plain\n", userinfo, ssPort) +
		fmt.Sprintf("ss://%s@127.0.0.1:%d?plugin=obfs-local%%3Bobfs%%3Dhttp%%3Bobfs-host%%3Dcdn.example.com#obfs\n", userinfo, obfsServerPort)
	parsed, err := clashsub.Parse([]byte(links), clashsub.Options{})
	require.NoError(t, err)
	require.Len(t, parsed.Nodes, 2)
	require.Equal(t, "obfs", parsed.Nodes[1].Config["plugin"])

	proxies := make([]map[string]any, 0, len(parsed.Nodes))
	for i, node := range parsed.Nodes {
		proxy := make(map[string]any, len(node.Config))
		for key, value := range node.Config {
			proxy[key] = value
		}
		proxy["name"] = fmt.Sprintf("n-%d", i+1)
		proxies = append(proxies, proxy)
	}
	listener := func(name string, port int, proxy, user string) map[string]any {
		return map[string]any{
			"name": name, "type": "mixed", "listen": "127.0.0.1", "port": port, "proxy": proxy,
			"users": []map[string]any{{"username": user, "password": user + "-pw"}},
		}
	}
	payload, err := yaml.Marshal(map[string]any{
		"mode": "rule", "log-level": "silent", "allow-lan": false, "ipv6": true,
		"unified-delay": true, "tcp-concurrent": true, "find-process-mode": "off",
		"profile": map[string]any{"store-selected": false, "store-fake-ip": false},
		"proxies": proxies,
		"listeners": []map[string]any{
			{"name": "ss-in", "type": "shadowsocks", "listen": "127.0.0.1", "port": ssPort,
				"cipher": "aes-128-gcm", "password": "test-password", "proxy": "DIRECT"},
			listener("l-1", plainPort, "n-1", "u1"),
			listener("l-2", obfsPort, "n-2", "u2"),
		},
		"rules": []string{"MATCH,REJECT"},
	})
	require.NoError(t, err)

	engine, err := Open(Options{HomeDir: t.TempDir()})
	require.NoError(t, err)
	defer func() { require.NoError(t, engine.Close()) }()
	require.NoError(t, engine.Apply(payload))

	for _, tc := range []struct {
		name, proxy, user string
		port              int
	}{
		{"plain", "n-1", "u1", plainPort},
		{"obfs-http", "n-2", "u2", obfsPort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := engine.DelayTest(ctx, tc.proxy, upstream.URL)
			require.NoError(t, err, "latency test through the node")

			body, err := getBody(socksClient(tc.user, tc.user+"-pw", tc.port, false), upstream.URL)
			require.NoError(t, err, "traffic through the node's listener")
			require.Equal(t, "upstream-ok", body)
		})
	}
}
