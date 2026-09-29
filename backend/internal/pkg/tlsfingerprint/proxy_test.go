package tlsfingerprint

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// newTestPKI 生成自签 CA 与签给 127.0.0.1 的服务端证书。
func newTestPKI(t *testing.T) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "tlsfingerprint test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool, tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// serveLoop 为每个连接起一个 goroutine；测试结束时关闭所有连接。
func serveLoop(t *testing.T, ln net.Listener, handle func(net.Conn)) {
	t.Helper()
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			go handle(conn)
		}
	}()
}

// startTLSGreeter 启动 TLS 目标：握手后写出 "hello" 并关闭。
func startTLSGreeter(t *testing.T, cert tls.Certificate, cfg *tls.Config) string {
	t.Helper()
	ln := listenLocal(t)
	if cfg == nil {
		cfg = &tls.Config{NextProtos: []string{"http/1.1"}}
	}
	cfg.Certificates = []tls.Certificate{cert}
	serveLoop(t, tls.NewListener(ln, cfg), func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "hello")
	})
	return ln.Addr().String()
}

// startPlainGreeter 启动明文目标：连上即写出 "hello"。
func startPlainGreeter(t *testing.T) string {
	t.Helper()
	ln := listenLocal(t)
	serveLoop(t, ln, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "hello")
	})
	return ln.Addr().String()
}

type connectProxy struct {
	addr     string
	mu       sync.Mutex
	requests []string // 收到的 CONNECT 请求原文
}

func (p *connectProxy) lastRequest() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return ""
	}
	return p.requests[len(p.requests)-1]
}

// startConnectProxy 启动 HTTP CONNECT 代理；tlsCert 非空时为 HTTPS 代理；
// wantAuth 非空时校验 Proxy-Authorization，不符返回 407。
func startConnectProxy(t *testing.T, tlsCert *tls.Certificate, wantAuth string) *connectProxy {
	t.Helper()
	ln := listenLocal(t)
	p := &connectProxy{addr: ln.Addr().String()}
	if tlsCert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*tlsCert}})
	}
	serveLoop(t, ln, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		var raw strings.Builder
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = raw.WriteString(line)
			if line == "\r\n" {
				break
			}
		}
		p.mu.Lock()
		p.requests = append(p.requests, raw.String())
		p.mu.Unlock()

		req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw.String())))
		if err != nil || req.Method != http.MethodConnect {
			_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\n\r\n")
			return
		}
		if wantAuth != "" && req.Header.Get("Proxy-Authorization") != wantAuth {
			_, _ = io.WriteString(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
			return
		}
		target, err := net.Dial("tcp", req.Host)
		if err != nil {
			_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
			return
		}
		defer func() { _ = target.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(target, br); _ = target.Close() }()
		_, _ = io.Copy(conn, target)
	})
	return p
}

// startSOCKS5Proxy 启动带用户名/密码认证（RFC 1929）的 SOCKS5 代理。
func startSOCKS5Proxy(t *testing.T, user, pass string) string {
	t.Helper()
	ln := listenLocal(t)
	serveLoop(t, ln, func(conn net.Conn) {
		defer func() { _ = conn.Close() }()
		hdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, hdr); err != nil || hdr[0] != 5 {
			return
		}
		if _, err := io.ReadFull(conn, make([]byte, hdr[1])); err != nil {
			return
		}
		_, _ = conn.Write([]byte{5, 2}) // username/password
		authHdr := make([]byte, 2)
		if _, err := io.ReadFull(conn, authHdr); err != nil {
			return
		}
		gotUser := make([]byte, authHdr[1])
		if _, err := io.ReadFull(conn, gotUser); err != nil {
			return
		}
		passLen := make([]byte, 1)
		if _, err := io.ReadFull(conn, passLen); err != nil {
			return
		}
		gotPass := make([]byte, passLen[0])
		if _, err := io.ReadFull(conn, gotPass); err != nil {
			return
		}
		if string(gotUser) != user || string(gotPass) != pass {
			_, _ = conn.Write([]byte{1, 1})
			return
		}
		_, _ = conn.Write([]byte{1, 0})

		req := make([]byte, 4)
		if _, err := io.ReadFull(conn, req); err != nil || req[1] != 1 {
			return
		}
		var host string
		switch req[3] {
		case 1:
			ip := make([]byte, 4)
			if _, err := io.ReadFull(conn, ip); err != nil {
				return
			}
			host = net.IP(ip).String()
		case 3:
			n := make([]byte, 1)
			if _, err := io.ReadFull(conn, n); err != nil {
				return
			}
			name := make([]byte, n[0])
			if _, err := io.ReadFull(conn, name); err != nil {
				return
			}
			host = string(name)
		default:
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(conn, port); err != nil {
			return
		}
		target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))))
		if err != nil {
			_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
			return
		}
		defer func() { _ = target.Close() }()
		_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		go func() { _, _ = io.Copy(target, conn); _ = target.Close() }()
		_, _ = io.Copy(conn, target)
	})
	return ln.Addr().String()
}

// startBlackhole 接受连接但从不回应（卡住的代理 / 不推进握手的目标）。
func startBlackhole(t *testing.T) string {
	t.Helper()
	ln := listenLocal(t)
	serveLoop(t, ln, func(conn net.Conn) { _, _ = io.Copy(io.Discard, conn) })
	return ln.Addr().String()
}

func mustProxyURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func readGreeting(t *testing.T, conn net.Conn) {
	t.Helper()
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil && !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("read greeting: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("expected greeting through tunnel, got %q", got)
	}
}

func TestProxyDialerDirectTLS(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, nil)

	d, err := NewProxyDialer(&Profile{Name: "test"}, nil, DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialTLSContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	uconn, ok := conn.(*utls.UConn)
	if !ok {
		t.Fatalf("expected *utls.UConn, got %T", conn)
	}
	if proto := uconn.ConnectionState().NegotiatedProtocol; proto != "http/1.1" {
		t.Fatalf("negotiated ALPN %q, want http/1.1", proto)
	}
	readGreeting(t, conn)
}

// HTTP 代理：CONNECT 形态对齐 Bun（Host + Proxy-Connection + Basic 认证，无 UA）。
func TestProxyDialerHTTPConnectProxy(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, nil)
	proxy := startConnectProxy(t, nil, "Basic dXNlcjpwQHNz")

	d, err := NewProxyDialer(nil, mustProxyURL(t, "http://user:p%40ss@"+proxy.addr), DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialTLSContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	readGreeting(t, conn)

	want := "CONNECT " + target + " HTTP/1.1\r\n" +
		"Host: " + target + "\r\n" +
		"Proxy-Connection: Keep-Alive\r\n" +
		"Proxy-Authorization: Basic dXNlcjpwQHNz\r\n" +
		"\r\n"
	if got := proxy.lastRequest(); got != want {
		t.Fatalf("CONNECT request mismatch\n got: %q\nwant: %q", got, want)
	}
}

// HTTPS 代理：先与代理 TLS 握手再 CONNECT，隧道内仍是指纹 TLS（此前会回退到
// 标准库 TLS，指纹泄漏）。
func TestProxyDialerHTTPSProxyTunnelsFingerprintedTLS(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, nil)
	proxy := startConnectProxy(t, &cert, "")

	d, err := NewProxyDialer(nil, mustProxyURL(t, "https://"+proxy.addr), DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialTLSContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.(*utls.UConn); !ok {
		t.Fatalf("expected a uTLS connection inside the proxy tunnel, got %T", conn)
	}
	readGreeting(t, conn)
	if !strings.HasPrefix(proxy.lastRequest(), "CONNECT "+target+" ") {
		t.Fatalf("proxy did not see the CONNECT: %q", proxy.lastRequest())
	}
}

func TestProxyDialerSOCKS5ProxyWithAuth(t *testing.T) {
	pool, cert := newTestPKI(t)
	tlsTarget := startTLSGreeter(t, cert, nil)
	plainTarget := startPlainGreeter(t)
	proxyAddr := startSOCKS5Proxy(t, "user", "secret")

	d, err := NewProxyDialer(nil, mustProxyURL(t, "socks5h://user:secret@"+proxyAddr), DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialTLSContext(context.Background(), "tcp", tlsTarget)
	if err != nil {
		t.Fatal(err)
	}
	readGreeting(t, conn)

	plain, err := d.DialContext(context.Background(), "tcp", plainTarget)
	if err != nil {
		t.Fatal(err)
	}
	readGreeting(t, plain)

	bad, err := NewProxyDialer(nil, mustProxyURL(t, "socks5h://user:wrong@"+proxyAddr), DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.DialTLSContext(context.Background(), "tcp", tlsTarget); err == nil {
		t.Fatal("expected SOCKS5 authentication failure")
	}
}

// 明文拨号同样经过代理隧道，绝不直连。
func TestProxyDialerPlainDialUsesProxyTunnel(t *testing.T) {
	target := startPlainGreeter(t)
	proxy := startConnectProxy(t, nil, "")

	d, err := NewProxyDialer(nil, mustProxyURL(t, "http://"+proxy.addr), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.DialContext(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	readGreeting(t, conn)
	if !strings.HasPrefix(proxy.lastRequest(), "CONNECT "+target+" ") {
		t.Fatalf("plain dial bypassed the proxy: %q", proxy.lastRequest())
	}
}

func TestProxyDialerReportsRejectedConnect(t *testing.T) {
	proxy := startConnectProxy(t, nil, "Basic expected")
	d, err := NewProxyDialer(nil, mustProxyURL(t, "http://"+proxy.addr), DialOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DialTLSContext(context.Background(), "tcp", "127.0.0.1:443")
	if err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("expected 407 error, got %v", err)
	}
}

// net/http 的拨号 ctx 与请求取消解耦且没有截止时间（context.Background 模拟），
// 卡住的代理协商与 TLS 握手必须由 dialer 自己的超时兜底。
func TestProxyDialerBoundsStalledPeers(t *testing.T) {
	stalled := startBlackhole(t)
	opts := DialOptions{DialTimeout: time.Second, HandshakeTimeout: 200 * time.Millisecond}
	cases := map[string]struct {
		proxy  string
		target string
	}{
		"http proxy never answers CONNECT": {proxy: "http://" + stalled, target: "127.0.0.1:443"},
		"https proxy never handshakes":     {proxy: "https://" + stalled, target: "127.0.0.1:443"},
		"socks5 proxy never negotiates":    {proxy: "socks5h://" + stalled, target: "127.0.0.1:443"},
		"target never answers ClientHello": {target: stalled},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var proxyURL *url.URL
			if tc.proxy != "" {
				proxyURL = mustProxyURL(t, tc.proxy)
			}
			d, err := NewProxyDialer(nil, proxyURL, opts)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			conn, err := d.DialTLSContext(context.Background(), "tcp", tc.target)
			if err == nil {
				_ = conn.Close()
				t.Fatal("expected timeout error")
			}
			if elapsed := time.Since(start); elapsed > 3*time.Second {
				t.Fatalf("stalled peer held the dial for %v (err=%v)", elapsed, err)
			}
		})
	}
}

// 配置了 h2 的 profile 会让服务端协商 h2，而传输层只讲 HTTP/1.1：明确报错而不是
// 在连接上发出服务端无法解析的请求。
func TestProxyDialerRejectsNonHTTP1ALPN(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, &tls.Config{NextProtos: []string{"h2"}})

	d, err := NewProxyDialer(&Profile{Name: "h2", ALPNProtocols: []string{"h2", "http/1.1"}}, nil, DialOptions{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.DialTLSContext(context.Background(), "tcp", target)
	if err == nil || !strings.Contains(err.Error(), `ALPN "h2"`) {
		t.Fatalf("expected ALPN error, got %v", err)
	}
}

func TestNewProxyDialerRejectsUnknownScheme(t *testing.T) {
	if _, err := NewProxyDialer(nil, mustProxyURL(t, "ftp://127.0.0.1:21"), DialOptions{}); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}

// 会话票据缓存按 dialer 隔离：同一 dialer（同一账号/出口的连接池）内可复用，
// 另一个 dialer 不会拿到它的票据。TLS 1.2 走 session_ticket 扩展即可验证。
func TestProxyDialerSessionCacheIsPerDialer(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, &tls.Config{NextProtos: []string{"http/1.1"}, MaxVersion: tls.VersionTLS12}) //nolint:gosec // G402: 只有 TLS 1.2 走 session_ticket 恢复，测试票据隔离需要它

	dial := func(d *ProxyDialer) bool {
		t.Helper()
		conn, err := d.DialTLSContext(context.Background(), "tcp", target)
		if err != nil {
			t.Fatal(err)
		}
		uconn, ok := conn.(*utls.UConn)
		if !ok {
			t.Fatalf("expected *utls.UConn, got %T", conn)
		}
		resumed := uconn.ConnectionState().DidResume
		readGreeting(t, conn)
		return resumed
	}

	first, _ := NewProxyDialer(nil, nil, DialOptions{RootCAs: pool})
	second, _ := NewProxyDialer(nil, nil, DialOptions{RootCAs: pool})
	if dial(first) {
		t.Fatal("first connection cannot resume")
	}
	if !dial(first) {
		t.Fatal("same dialer should resume its own session")
	}
	if dial(second) {
		t.Fatal("another dialer must not reuse the first dialer's session ticket")
	}
}
