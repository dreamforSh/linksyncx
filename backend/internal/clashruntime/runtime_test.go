//go:build unit

package clashruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestExternalRuntimeAgainstFakeController(t *testing.T) {
	var applied atomic.Int32
	deleted := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"meta":true,"version":"v1.19.31"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/configs":
			require.Equal(t, "true", r.URL.Query().Get("force"))
			var body struct{ Payload string }
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			if strings.Contains(body.Payload, "bad") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"message":"proxy 3: missing server"}`)
				return
			}
			applied.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/proxies/__sub2api_cfg_x":
			_, _ = io.WriteString(w, `{"name":"__sub2api_cfg_x"}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/proxies/") && strings.HasSuffix(r.URL.Path, "/delay"):
			require.Equal(t, "https://probe.example/204", r.URL.Query().Get("url"))
			require.Equal(t, "3000", r.URL.Query().Get("timeout"))
			_, _ = io.WriteString(w, `{"delay":88}`)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/proxies/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"resource not found"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/connections":
			_, _ = io.WriteString(w, `{"connections":[{"id":"c1","metadata":{"inboundName":"l-7"}},{"id":"c2","metadata":{"inboundName":"l-8"}}]}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/connections/"):
			deleted <- strings.TrimPrefix(r.URL.Path, "/connections/")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rt := newExternalRuntime(srv.URL+"/", "s3cret")
	ctx := context.Background()
	require.NoError(t, rt.Start(ctx))
	require.True(t, rt.Ready())
	require.Equal(t, "v1.19.31", rt.Version())

	require.NoError(t, rt.Apply(ctx, []byte("mode: rule")))
	require.EqualValues(t, 1, applied.Load())
	err := rt.Apply(ctx, []byte("bad"))
	var cfgErr *service.ClashConfigError
	require.True(t, errors.As(err, &cfgErr))
	idx, ok := service.ParseClashProxyIndexError(cfgErr.Message)
	require.True(t, ok)
	require.Equal(t, 3, idx)

	ok, err = rt.HasProxy(ctx, "__sub2api_cfg_x")
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = rt.HasProxy(ctx, "__sub2api_cfg_other")
	require.NoError(t, err)
	require.False(t, ok)

	delay, err := rt.DelayTest(ctx, "n-1", "https://probe.example/204", 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, 88*time.Millisecond, delay)

	closed, err := rt.CloseInboundConnections(ctx, []string{"l-7"})
	require.NoError(t, err)
	require.Equal(t, 1, closed)
	require.Equal(t, "c1", <-deleted)

	bad := newExternalRuntime(srv.URL, "wrong")
	shortCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	require.Error(t, bad.Start(shortCtx))
	require.False(t, bad.Ready())
}

func TestNewSelectsRuntimeByMode(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClashPool.Mode = config.ClashPoolModeDisabled
	require.Nil(t, New(cfg))
	cfg.ClashPool.Mode = config.ClashPoolModeExternal
	cfg.ClashPool.External.ControllerURL = "http://mihomo:9090"
	require.Equal(t, config.ClashPoolModeExternal, New(cfg).Mode())
	cfg.ClashPool.Mode = config.ClashPoolModeEmbedded
	cfg.ClashPool.DataDir = t.TempDir()
	require.Equal(t, config.ClashPoolModeEmbedded, New(cfg).Mode())
}

// startConnectNode is an HTTP CONNECT proxy standing in for a subscription node.
func startConnectNode(t *testing.T) (string, int, *atomic.Int64) {
	t.Helper()
	hits := &atomic.Int64{}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		hits.Add(1)
		dst, err := net.DialTimeout("tcp", r.Host, 5*time.Second)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		src, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = dst.Close()
			return
		}
		_, _ = src.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() { _, _ = io.Copy(dst, src); _ = dst.Close() }()
		_, _ = io.Copy(src, dst)
		_ = src.Close()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, hits
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// TestEmbeddedRuntimeRendersWorkingExits drives the real in-process core with
// a configuration produced by service.RenderClashConfig.
func TestEmbeddedRuntimeRendersWorkingExits(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "via-node")
	}))
	defer upstream.Close()
	nodeHost, nodePort, hits := startConnectNode(t)

	rt := &embeddedRuntime{dataDir: t.TempDir()}
	ctx := context.Background()
	require.NoError(t, rt.Start(ctx))
	defer func() { require.NoError(t, rt.Stop(ctx)) }()
	require.True(t, rt.Ready())

	livePort, placeholderPort := freeTCPPort(t), freeTCPPort(t)
	rendered, err := service.RenderClashConfig([]service.ClashRenderNode{
		{NodeID: 1, ListenPort: livePort, Username: "sub2api-a", Password: "pa", Config: map[string]any{
			"name": "HK 01", "type": "http", "server": nodeHost, "port": nodePort,
		}},
		{NodeID: 2, ListenPort: placeholderPort, Username: "sub2api-b", Password: "pb"},
	}, service.ClashRenderOptions{})
	require.NoError(t, err)
	require.NoError(t, rt.Apply(ctx, rendered.Payload))

	ok, err := rt.HasProxy(ctx, rendered.MarkerName)
	require.NoError(t, err)
	require.True(t, ok, "marker group must be visible for drift detection")

	// The listener self-check recognises our listeners and credentials.
	require.NoError(t, service.ProbeClashListener(ctx, "127.0.0.1:"+strconv.Itoa(livePort), "sub2api-a", "pa"))
	require.Error(t, service.ProbeClashListener(ctx, "127.0.0.1:"+strconv.Itoa(livePort), "sub2api-a", "nope"))
	require.NoError(t, service.ProbeClashListener(ctx, "127.0.0.1:"+strconv.Itoa(placeholderPort), "sub2api-b", "pb"))

	client := func(port int, user, pass string) *http.Client {
		proxy := &url.URL{Scheme: "socks5", User: url.UserPassword(user, pass), Host: fmt.Sprintf("127.0.0.1:%d", port)}
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}}
	}
	resp, err := client(livePort, "sub2api-a", "pa").Get(upstream.URL)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, "via-node", string(body))
	require.EqualValues(t, 1, hits.Load())

	_, err = client(placeholderPort, "sub2api-b", "pb").Get(upstream.URL)
	require.Error(t, err, "placeholder listeners must fail closed")
	require.EqualValues(t, 1, hits.Load())

	delay, err := rt.DelayTest(ctx, service.ClashProxyName(1), upstream.URL, 5*time.Second)
	require.NoError(t, err)
	require.GreaterOrEqual(t, delay, time.Duration(0))

	// A node the core rejects is reported by proxy index.
	broken, err := service.RenderClashConfig([]service.ClashRenderNode{
		{NodeID: 1, ListenPort: livePort, Username: "sub2api-a", Password: "pa", Config: map[string]any{
			"name": "HK 01", "type": "http", "server": nodeHost, "port": nodePort,
		}},
		{NodeID: 3, ListenPort: freeTCPPort(t), Username: "c", Password: "c", Config: map[string]any{"name": "x", "type": "vmess", "server": "a.example.com", "port": 443}},
	}, service.ClashRenderOptions{})
	require.NoError(t, err)
	err = rt.Apply(ctx, broken.Payload)
	var cfgErr *service.ClashConfigError
	require.True(t, errors.As(err, &cfgErr), "got %v", err)
	idx, ok := service.ParseClashProxyIndexError(cfgErr.Message)
	require.True(t, ok, cfgErr.Message)
	require.EqualValues(t, 3, broken.ProxyNodeIDs[idx])

	// The previous configuration is still serving.
	resp, err = client(livePort, "sub2api-a", "pa").Get(upstream.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
}
