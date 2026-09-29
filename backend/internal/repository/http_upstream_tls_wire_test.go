package repository

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// newWireTestPKI 生成自签 CA 与签给 127.0.0.1 的证书（目标与 HTTPS 代理共用）。
func newWireTestPKI(t *testing.T) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "wire test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

func listenWireTest(t *testing.T) net.Listener {
	t.Helper()
	if !localListenerAvailable() {
		t.Skipf("local listeners are not permitted in this environment: %v", canListenErr)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// wireCaptureServer 是保留线级字节的 HTTPS 上游：记录每个请求头块原文，响应体
// 用 gzip 压缩（验证 Accept-Encoding 交给 httpwire 后解压链路仍然生效）。
type wireCaptureServer struct {
	addr     string
	accepted atomic.Int64
	mu       sync.Mutex
	heads    []string
	bodies   [][]byte
}

type wireTee struct {
	r   io.Reader
	buf bytes.Buffer
}

func (t *wireTee) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	_, _ = t.buf.Write(p[:n])
	return n, err
}

func startWireCaptureServer(t *testing.T, cert tls.Certificate) *wireCaptureServer {
	t.Helper()
	ln := listenWireTest(t)
	s := &wireCaptureServer{addr: ln.Addr().String()}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}})
	go func() {
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			go s.serve(conn)
		}
	}()
	return s
}

func (s *wireCaptureServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tee := &wireTee{r: conn}
	br := bufio.NewReader(tee)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return
		}
		raw := tee.buf.Bytes()
		head := string(raw[:bytes.Index(raw, []byte("\r\n\r\n"))+4])
		tee.buf.Reset()
		s.mu.Lock()
		s.heads = append(s.heads, head)
		s.bodies = append(s.bodies, body)
		s.mu.Unlock()

		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = fmt.Fprintf(zw, `{"received":%d}`, len(body))
		_ = zw.Close()
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", gz.Len())
		_, _ = conn.Write(gz.Bytes())
	}
}

func (s *wireCaptureServer) capturedHeads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.heads...)
}

func (s *wireCaptureServer) capturedBodies() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.bodies...)
}

// startWireConnectProxy 启动 HTTP(S) CONNECT 代理，返回代理地址与已建隧道数。
func startWireConnectProxy(t *testing.T, cert *tls.Certificate) (string, *atomic.Int64) {
	t.Helper()
	ln := listenWireTest(t)
	addr := ln.Addr().String()
	if cert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*cert}})
	}
	tunnels := &atomic.Int64{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				target, err := net.Dial("tcp", req.Host)
				if err != nil {
					_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer func() { _ = target.Close() }()
				tunnels.Add(1)
				_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() { _, _ = io.Copy(target, br); _ = target.Close() }()
				_, _ = io.Copy(conn, target)
			}()
		}
	}()
	return addr, tunnels
}

func newWireTestUpstream(t *testing.T, pool *x509.CertPool) *httpUpstreamService {
	t.Helper()
	svc, ok := NewHTTPUpstream(nil).(*httpUpstreamService)
	require.True(t, ok)
	svc.tlsRootCAs = pool
	return svc
}

// newClaudeCodeWireRequest 以网关 setHeaderRaw 的方式（线级大小写直写 map）
// 构造 /v1/messages 请求。
func newClaudeCodeWireRequest(t *testing.T, host string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://"+host+"/v1/messages?beta=true", bytes.NewReader(body))
	require.NoError(t, err)
	for key, value := range map[string]string{
		"Accept":                                    "application/json",
		"Authorization":                             "Bearer sk-ant-oat01-test",
		"Content-Type":                              "application/json",
		"User-Agent":                                "claude-cli/2.1.283 (external, cli)",
		"X-Claude-Code-Session-Id":                  "6f1c1f7e-3f4c-4c55-9f55-0a4b2d0c9a11",
		"X-Stainless-Arch":                          "x64",
		"X-Stainless-Lang":                          "js",
		"X-Stainless-OS":                            "Windows",
		"X-Stainless-Package-Version":               "0.112.1",
		"X-Stainless-Retry-Count":                   "0",
		"X-Stainless-Runtime":                       "node",
		"X-Stainless-Runtime-Version":               "v24.3.0",
		"X-Stainless-Timeout":                       "600",
		"anthropic-beta":                            "claude-code-20250219,oauth-2025-04-20",
		"anthropic-dangerous-direct-browser-access": "true",
		"anthropic-version":                         "2023-06-01",
		"x-app":                                     "cli",
		"x-client-request-id":                       "0d4c2a7e-9a8b-4c1d-8e7f-112233445566",
		"Accept-Encoding":                           "gzip, deflate, br, zstd",
	} {
		req.Header[key] = []string{value}
	}
	return req
}

func claudeCodeWireHead(host string, bodyLen int) string {
	return strings.Join([]string{
		"POST /v1/messages?beta=true HTTP/1.1",
		"Accept: application/json",
		"Authorization: Bearer sk-ant-oat01-test",
		"Content-Type: application/json",
		"User-Agent: claude-cli/2.1.283 (external, cli)",
		"X-Claude-Code-Session-Id: 6f1c1f7e-3f4c-4c55-9f55-0a4b2d0c9a11",
		"X-Stainless-Arch: x64",
		"X-Stainless-Lang: js",
		"X-Stainless-OS: Windows",
		"X-Stainless-Package-Version: 0.112.1",
		"X-Stainless-Retry-Count: 0",
		"X-Stainless-Runtime: node",
		"X-Stainless-Runtime-Version: v24.3.0",
		"X-Stainless-Timeout: 600",
		"anthropic-beta: claude-code-20250219,oauth-2025-04-20",
		"anthropic-dangerous-direct-browser-access: true",
		"anthropic-version: 2023-06-01",
		"x-app: cli",
		"x-client-request-id: 0d4c2a7e-9a8b-4c1d-8e7f-112233445566",
		"Connection: keep-alive",
		"Host: " + host,
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: " + strconv.Itoa(bodyLen),
	}, "\r\n") + "\r\n\r\n"
}

func doWireRequest(t *testing.T, svc *httpUpstreamService, proxyURL string, accountID int64, concurrency int, req *http.Request) string {
	t.Helper()
	resp, err := svc.DoWithTLS(req, proxyURL, accountID, concurrency, &tlsfingerprint.Profile{Name: "wire-test"})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, resp.Header.Get("Content-Encoding"), "gzip response must be decoded by decompressResponseBody")
	return string(payload)
}

// DoWithTLS 的线级字节与真实 Claude Code（Bun fetch）逐字节一致，且无论直连还是
// 经 http / https / socks5 代理，都走指纹隧道。
func TestHTTPUpstreamDoWithTLSEmitsBunWireShape(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)
	httpProxy, httpTunnels := startWireConnectProxy(t, nil)
	httpsProxy, httpsTunnels := startWireConnectProxy(t, &cert)
	socksProxy, socksCalls := startTestSOCKS5Proxy(t)

	cases := []struct {
		name    string
		proxy   string
		tunnels *atomic.Int64
	}{
		{name: "direct"},
		{name: "http proxy", proxy: "http://" + httpProxy, tunnels: httpTunnels},
		{name: "https proxy", proxy: "https://" + httpsProxy, tunnels: httpsTunnels},
		{name: "socks5 proxy", proxy: socksProxy, tunnels: socksCalls},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newWireTestUpstream(t, pool)
			body := bytes.Repeat([]byte("m"), 20000)
			before := len(upstream.capturedHeads())

			payload := doWireRequest(t, svc, tc.proxy, int64(100+i), 1, newClaudeCodeWireRequest(t, upstream.addr, body))
			require.JSONEq(t, `{"received":20000}`, payload)

			heads := upstream.capturedHeads()
			require.Len(t, heads, before+1)
			require.Equal(t, claudeCodeWireHead(upstream.addr, len(body)), heads[before])
			if tc.tunnels != nil {
				require.Equal(t, int64(1), tc.tunnels.Load(), "request must travel through the proxy tunnel")
			}
		})
	}
}

// 账号并发决定连接上限：并发请求在上限内排队复用 keep-alive 连接，每个请求的
// 线级头序都保持 Bun 形态。
func TestHTTPUpstreamDoWithTLSConcurrentRequestsReuseBoundedPool(t *testing.T) {
	pool, cert := newWireTestPKI(t)
	upstream := startWireCaptureServer(t, cert)
	svc := newWireTestUpstream(t, pool)

	const accountConcurrency = 3
	sizes := []int{0, 512, 4096, 9000, 70000}
	batches := make([][]*http.Request, 12)
	for g := range batches {
		for i := 0; i < 10; i++ {
			size := sizes[(g+i)%len(sizes)]
			batches[g] = append(batches[g], newClaudeCodeWireRequest(t, upstream.addr, bytes.Repeat([]byte("c"), size)))
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(batches))
	for _, batch := range batches {
		wg.Add(1)
		go func(batch []*http.Request) {
			defer wg.Done()
			for _, req := range batch {
				size := int(req.ContentLength)
				resp, err := svc.DoWithTLS(req, "", 7, accountConcurrency, &tlsfingerprint.Profile{Name: "wire-test"})
				if err != nil {
					errs <- err
					return
				}
				payload, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if err != nil || string(payload) != fmt.Sprintf(`{"received":%d}`, size) {
					errs <- fmt.Errorf("unexpected response %q (%v)", payload, err)
					return
				}
			}
		}(batch)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	heads := upstream.capturedHeads()
	require.Len(t, heads, 120)
	for _, head := range heads {
		cl := head[strings.LastIndex(head, "Content-Length: ")+len("Content-Length: ") : len(head)-4]
		n, err := strconv.Atoi(cl)
		require.NoError(t, err)
		require.Equal(t, claudeCodeWireHead(upstream.addr, n), head)
	}
	require.LessOrEqual(t, upstream.accepted.Load(), int64(accountConcurrency), "connections must stay within the account's pool limit")
}
