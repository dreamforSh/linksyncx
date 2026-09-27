//go:build unit

package clashcore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startConnectProxy starts an HTTP CONNECT proxy standing in for a remote node.
func startConnectProxy(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "connect only", http.StatusMethodNotAllowed)
			return
		}
		hits.Add(1)
		dst, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = dst.Close()
			return
		}
		src, buf, err := hj.Hijack()
		if err != nil {
			_ = dst.Close()
			return
		}
		_, _ = src.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			if buf.Reader.Buffered() > 0 {
				_, _ = io.CopyN(dst, buf, int64(buf.Reader.Buffered()))
			}
			_, _ = io.Copy(dst, src)
			_ = dst.Close()
		}()
		_, _ = io.Copy(src, dst)
		_ = src.Close()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), hits
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func socksClient(user, pass string, port int, keepAlive bool) *http.Client {
	proxyURL := &url.URL{Scheme: "socks5", User: url.UserPassword(user, pass), Host: fmt.Sprintf("127.0.0.1:%d", port)}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: !keepAlive},
	}
}

func getBody(client *http.Client, target string) (string, error) {
	resp, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	return string(body), err
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *logSink) log(level, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, level+": "+msg)
}

func (s *logSink) contains(substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.lines {
		if strings.Contains(line, substr) {
			return true
		}
	}
	return false
}

const testConfigTemplate = `mode: rule
log-level: silent
allow-lan: false
ipv6: false
geo-auto-update: false
find-process-mode: "off"
profile: {store-selected: false, store-fake-ip: false}
proxies:
  - {name: n-1, type: http, server: %s, port: %s}
%s
proxy-groups:
  - {name: __sub2api_cfg_test, type: select, proxies: [REJECT]}
listeners:
  - {name: l-1, type: mixed, listen: 127.0.0.1, port: %d, proxy: n-1, users: [{username: u1, password: p1}]}
  - {name: l-2, type: mixed, listen: 127.0.0.1, port: %d, proxy: REJECT, users: [{username: u2, password: p2}]}
%s
rules:
  - MATCH,REJECT
`

func TestEngineInProcessRouting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "upstream-ok")
	}))
	defer upstream.Close()

	nodeAddr, hits := startConnectProxy(t)
	nodeHost, nodePort, err := net.SplitHostPort(nodeAddr)
	require.NoError(t, err)
	p1, p2 := freePort(t), freePort(t)

	sink := &logSink{}
	engine, err := Open(Options{HomeDir: t.TempDir(), Log: sink.log})
	require.NoError(t, err)
	defer func() { require.NoError(t, engine.Close()) }()

	_, err = Open(Options{})
	require.ErrorIs(t, err, ErrEngineOpen)

	base := fmt.Sprintf(testConfigTemplate, nodeHost, nodePort, "", p1, p2, "")
	require.NoError(t, engine.Apply([]byte(base)))
	require.True(t, engine.HasProxy("__sub2api_cfg_test"))
	require.True(t, engine.HasProxy("n-1"))

	body, err := getBody(socksClient("u1", "p1", p1, false), upstream.URL)
	require.NoError(t, err)
	require.Equal(t, "upstream-ok", body)
	require.GreaterOrEqual(t, hits.Load(), int64(1), "traffic must traverse the node")

	_, err = getBody(socksClient("u1", "wrong", p1, false), upstream.URL)
	require.Error(t, err, "wrong listener credentials must be rejected")

	before := hits.Load()
	_, err = getBody(socksClient("u2", "p2", p2, false), upstream.URL)
	require.Error(t, err, "REJECT placeholder must fail closed")
	require.Equal(t, before, hits.Load())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	delay, err := engine.DelayTest(ctx, "n-1", upstream.URL)
	require.NoError(t, err)
	require.GreaterOrEqual(t, delay, time.Duration(0))
	_, err = engine.DelayTest(ctx, "n-missing", upstream.URL)
	require.ErrorIs(t, err, ErrProxyNotFound)

	// A bad node is reported by index and leaves the running config untouched.
	bad := fmt.Sprintf(testConfigTemplate, nodeHost, nodePort, "  - {name: n-2, type: http, port: 1}", p1, p2, "")
	err = engine.Apply([]byte(bad))
	var cfgErr *ConfigError
	require.True(t, errors.As(err, &cfgErr), "want ConfigError, got %v", err)
	require.Contains(t, err.Error(), "proxy 1")
	body, err = getBody(socksClient("u1", "p1", p1, false), upstream.URL)
	require.NoError(t, err)
	require.Equal(t, "upstream-ok", body)

	// Hot reload keeps an established keep-alive connection on an unchanged listener.
	keepAlive := socksClient("u1", "p1", p1, true)
	_, err = getBody(keepAlive, upstream.URL)
	require.NoError(t, err)
	p3 := freePort(t)
	extended := fmt.Sprintf(testConfigTemplate, nodeHost, nodePort, "", p1, p2,
		fmt.Sprintf("  - {name: l-3, type: mixed, listen: 127.0.0.1, port: %d, proxy: n-1, users: [{username: u3, password: p3}]}", p3))
	require.NoError(t, engine.Apply([]byte(extended)))
	reused := false
	req, err := http.NewRequest(http.MethodGet, upstream.URL, nil)
	require.NoError(t, err)
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
	}))
	resp, err := keepAlive.Do(req)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	require.True(t, reused, "connection through unchanged listener must survive reload")
	body, err = getBody(socksClient("u3", "p3", p3, false), upstream.URL)
	require.NoError(t, err)
	require.Equal(t, "upstream-ok", body)

	// Occupied port: mihomo only logs the bind failure, Apply still succeeds.
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = blocker.Close() }()
	blockedPort := blocker.Addr().(*net.TCPAddr).Port
	conflict := fmt.Sprintf(testConfigTemplate, nodeHost, nodePort, "", p1, p2,
		fmt.Sprintf("  - {name: l-9, type: mixed, listen: 127.0.0.1, port: %d, proxy: n-1, users: [{username: u9, password: p9}]}", blockedPort))
	require.NoError(t, engine.Apply([]byte(conflict)))
	require.Eventually(t, func() bool { return sink.contains("l-9") }, 3*time.Second, 20*time.Millisecond,
		"listener bind failure must surface through the log bridge")

	// Per-listener byte counters, then closing inbound connections by name.
	longConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p1))
	require.NoError(t, err)
	defer func() { _ = longConn.Close() }()
	target := strings.TrimPrefix(upstream.URL, "http://")
	require.NoError(t, socks5Connect(longConn, "u1", "p1", target))
	_, err = fmt.Fprintf(longConn, "GET / HTTP/1.1\r\nHost: %s\r\n\r\n", target)
	require.NoError(t, err)
	_ = longConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	tunneled, err := http.ReadResponse(bufio.NewReader(longConn), nil)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, tunneled.Body)
	_ = tunneled.Body.Close()
	_ = longConn.SetReadDeadline(time.Time{})
	require.Eventually(t, func() bool {
		for _, c := range engine.Connections() {
			if c.Inbound == "l-1" && c.ID != "" && c.Upload > 0 && c.Download > 0 && !c.Start.IsZero() {
				return true
			}
		}
		return false
	}, 3*time.Second, 20*time.Millisecond, "connections report their listener and byte counters")
	for _, c := range engine.Connections() {
		require.NotEqual(t, "l-2", c.Inbound, "REJECT placeholders carry no connections")
	}
	require.Eventually(t, func() bool { return engine.CloseInboundConnections([]string{"l-1"}) > 0 }, 3*time.Second, 20*time.Millisecond)
}

// socks5Connect performs a SOCKS5 username/password handshake and CONNECT.
func socks5Connect(conn net.Conn, user, pass, target string) error {
	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	r := bufio.NewReader(conn)
	if _, err := conn.Write([]byte{5, 1, 2}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(r, reply); err != nil {
		return err
	}
	if reply[1] != 2 {
		return fmt.Errorf("unexpected method %d", reply[1])
	}
	auth := append([]byte{1, byte(len(user))}, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(r, reply); err != nil {
		return err
	}
	if reply[1] != 0 {
		return fmt.Errorf("auth failed")
	}
	ip := net.ParseIP(host).To4()
	req := []byte{5, 1, 0, 1}
	req = append(req, ip...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return err
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(r, head); err != nil {
		return err
	}
	if head[1] != 0 {
		return fmt.Errorf("connect failed: %d", head[1])
	}
	return nil
}
