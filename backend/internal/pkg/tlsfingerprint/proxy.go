package tlsfingerprint

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// DialOptions 控制指纹拨号各阶段的超时与证书校验；零值字段取默认值。
type DialOptions struct {
	// DialTimeout 限制到首跳（目标或代理）的 DNS 解析 + TCP 建连，默认 10s。
	DialTimeout time.Duration
	// HandshakeTimeout 分别限制代理协商（HTTPS 代理的 TLS、CONNECT、SOCKS5）与
	// 目标的 TLS 握手，默认 10s。
	HandshakeTimeout time.Duration
	// KeepAlive 为 TCP keepalive 探测间隔，默认 30s。
	KeepAlive time.Duration
	// RootCAs 校验目标与 HTTPS 代理证书的根证书池；nil 使用系统根证书。
	RootCAs *x509.CertPool
	// SessionCache 为 TLS 会话票据缓存；nil 时每个 ProxyDialer 独占一个 LRU。
	// 票据按 ServerName 复用，全局共享会让不同账号/出口的连接可被上游关联。
	SessionCache utls.ClientSessionCache
}

const (
	defaultDialTimeout      = 10 * time.Second
	defaultHandshakeTimeout = 10 * time.Second
	defaultDialKeepAlive    = 30 * time.Second
	defaultSessionCacheSize = 64
)

// aLongTimeAgo 用作立即到期的连接截止时间，以打断阻塞中的读写。
var aLongTimeAgo = time.Unix(1, 0)

// ProxyDialer 经直连或代理隧道建立到目标的连接，并以 Profile 指纹完成 TLS 握手。
//
// 支持 http / https（先与代理 TLS 握手再 CONNECT）/ socks5 / socks5h 代理；代理只
// 转发字节，uTLS ClientHello 原样到达目标。每个阶段都有独立超时：net/http 对自定义
// DialTLSContext 既不施加 TLSHandshakeTimeout，也把拨号 ctx 与请求取消解耦，这里
// 不设上限会让卡住的代理或握手永久占住 MaxConnsPerHost 名额。
type ProxyDialer struct {
	profile  *Profile
	proxyURL *url.URL
	opts     DialOptions
	baseDial func(ctx context.Context, network, addr string) (net.Conn, error)
	socks    proxy.ContextDialer
}

// NewProxyDialer 创建指纹拨号器；proxyURL 为 nil 表示直连。
func NewProxyDialer(profile *Profile, proxyURL *url.URL, opts DialOptions) (*ProxyDialer, error) {
	return newProxyDialer(profile, proxyURL, opts, nil)
}

func newProxyDialer(profile *Profile, proxyURL *url.URL, opts DialOptions, baseDial func(ctx context.Context, network, addr string) (net.Conn, error)) (*ProxyDialer, error) {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = defaultHandshakeTimeout
	}
	if opts.KeepAlive == 0 {
		opts.KeepAlive = defaultDialKeepAlive
	}
	if opts.SessionCache == nil {
		opts.SessionCache = utls.NewLRUClientSessionCache(defaultSessionCacheSize)
	}
	if baseDial == nil {
		baseDial = (&net.Dialer{Timeout: opts.DialTimeout, KeepAlive: opts.KeepAlive}).DialContext
	}
	d := &ProxyDialer{profile: profile, proxyURL: proxyURL, opts: opts, baseDial: baseDial}
	if proxyURL == nil {
		return d, nil
	}

	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
	case "socks5", "socks5h":
		// x/net/proxy 的 SOCKS5 总是把域名交给代理解析（socks5h 语义），本机不做 DNS。
		var auth *proxy.Auth
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
		}
		socksDialer, err := proxy.SOCKS5("tcp", proxyHostPort(proxyURL), auth, forwardDialer(baseDial))
		if err != nil {
			return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
		}
		contextDialer, ok := socksDialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("SOCKS5 dialer does not support context")
		}
		d.socks = contextDialer
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
	}
	return d, nil
}

// DialContext 经同一路由（直连或代理隧道）建立到 addr 的明文 TCP 连接。用作
// http.Transport.DialContext：明文请求（如重定向到 http://）同样走代理，不会直连泄漏出口 IP。
func (d *ProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialTunnel(ctx, network, addr)
}

// DialTLSContext 经同一路由建立到 addr 的连接，并以 Profile 指纹完成 TLS 握手。
func (d *ProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := d.dialTunnel(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return d.handshake(ctx, conn, addr)
}

func (d *ProxyDialer) dialTunnel(ctx context.Context, network, addr string) (net.Conn, error) {
	switch {
	case d.proxyURL == nil:
		return d.baseDial(ctx, network, addr)
	case d.socks != nil:
		slog.Debug("tls_fingerprint_socks5_connecting", "proxy", d.proxyURL.Host, "target", addr)
		// SOCKS5 的建连与协商在同一次调用里完成；socks 实现会把 ctx 截止时间落到连接上。
		sctx, cancel := context.WithTimeout(ctx, d.opts.DialTimeout+d.opts.HandshakeTimeout)
		defer cancel()
		conn, err := d.socks.DialContext(sctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("SOCKS5 connect via %s: %w", d.proxyURL.Host, err)
		}
		return conn, nil
	default:
		return d.dialHTTPConnect(ctx, network, addr)
	}
}

// dialHTTPConnect 建立 HTTP(S) 代理隧道：TCP 连代理 →（https 代理）与代理 TLS
// 握手 → CONNECT。
func (d *ProxyDialer) dialHTTPConnect(ctx context.Context, network, addr string) (net.Conn, error) {
	slog.Debug("tls_fingerprint_http_proxy_connecting", "proxy", d.proxyURL.Host, "target", addr)
	conn, err := d.baseDial(ctx, network, proxyHostPort(d.proxyURL))
	if err != nil {
		return nil, fmt.Errorf("connect to proxy %s: %w", d.proxyURL.Host, err)
	}

	hctx, cancel := context.WithTimeout(ctx, d.opts.HandshakeTimeout)
	defer cancel()
	if strings.EqualFold(d.proxyURL.Scheme, "https") {
		// 与代理之间是独立的一跳 TLS（标准库指纹只有代理可见），隧道内才是目标的 uTLS。
		proxyTLS := tls.Client(conn, &tls.Config{
			ServerName: d.proxyURL.Hostname(),
			RootCAs:    d.opts.RootCAs,
			NextProtos: []string{"http/1.1"},
			MinVersion: tls.VersionTLS12,
		})
		if err := proxyTLS.HandshakeContext(hctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("TLS handshake with proxy %s: %w", d.proxyURL.Host, err)
		}
		conn = proxyTLS
	}

	tunnel, err := d.connect(hctx, conn, addr)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	slog.Debug("tls_fingerprint_http_proxy_tunnel_established", "proxy", d.proxyURL.Host, "target", addr)
	return tunnel, nil
}

// connect 在 conn 上发出 CONNECT 并读取代理响应。请求形态对齐 Bun 的代理隧道
// （本机 claude.exe 2.1.283 二进制核实）：Host 与 Proxy-Connection: Keep-Alive，
// 有凭据时带 Proxy-Authorization；不带 net/http 的默认 User-Agent。
func (d *ProxyDialer) connect(ctx context.Context, conn net.Conn, addr string) (net.Conn, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(aLongTimeAgo) })

	request := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\nProxy-Connection: Keep-Alive\r\n"
	if user := d.proxyURL.User; user != nil {
		password, _ := user.Password()
		request += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(user.Username()+":"+password)) + "\r\n"
	}
	request += "\r\n"

	var (
		resp *http.Response
		br   *bufio.Reader
		err  error
	)
	if _, err = io.WriteString(conn, request); err == nil {
		br = bufio.NewReader(conn)
		resp, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	}
	if !stop() {
		// ctx 已结束，截止时间可能已被拨到过去，连接不可再用。
		return nil, fmt.Errorf("proxy CONNECT via %s: %w", d.proxyURL.Host, context.Cause(ctx))
	}
	if err != nil {
		return nil, fmt.Errorf("proxy CONNECT via %s: %w", d.proxyURL.Host, err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("proxy CONNECT via %s failed: %s", d.proxyURL.Host, resp.Status)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// handshake 在已建立的隧道上完成 uTLS 握手。失败时关闭 conn。
func (d *ProxyDialer) handshake(ctx context.Context, conn net.Conn, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	hctx, cancel := context.WithTimeout(ctx, d.opts.HandshakeTimeout)
	defer cancel()

	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName:                         host,
		RootCAs:                            d.opts.RootCAs,
		ClientSessionCache:                 d.opts.SessionCache,
		OmitEmptyPsk:                       true, // 未命中复用时 hello 与首次抓包逐字节一致
		PreferSkipResumptionOnNilExtension: true, // 2.1.280 hello 没有 PSK 扩展，避免 utls panic
	}, utls.HelloCustom)
	if err := tlsConn.ApplyPreset(buildClientHelloSpecFromProfile(d.profile)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("apply TLS preset: %w", err)
	}
	if err := tlsConn.HandshakeContext(hctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("TLS handshake failed: %w", err)
	}

	state := tlsConn.ConnectionState()
	// uTLS 连接不是 *tls.Conn，net/http 与 req 都只能在其上讲 HTTP/1.1。
	if proto := state.NegotiatedProtocol; proto != "" && proto != "http/1.1" {
		_ = tlsConn.Close()
		return nil, fmt.Errorf("TLS fingerprint: server negotiated ALPN %q, but the transport only speaks http/1.1; check the profile's ALPN list", proto)
	}
	slog.Debug("tls_fingerprint_handshake_success",
		"host", host,
		"version", state.Version,
		"cipher_suite", state.CipherSuite,
		"alpn", state.NegotiatedProtocol,
		"resumed", state.DidResume)
	return tlsConn, nil
}

// proxyHostPort 返回代理地址，端口缺省时按协议补全。
func proxyHostPort(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	switch strings.ToLower(u.Scheme) {
	case "https":
		port = "443"
	case "socks5", "socks5h":
		port = "1080"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// forwardDialer 把 baseDial 适配为 x/net/proxy 的前置拨号器（带建连超时与 ctx）。
type forwardDialer func(ctx context.Context, network, addr string) (net.Conn, error)

func (f forwardDialer) Dial(network, addr string) (net.Conn, error) {
	return f(context.Background(), network, addr)
}

func (f forwardDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

// bufferedConn 先交出 CONNECT 响应之后已被 bufio 读入的字节，再直接读连接。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.r.Buffered() > 0 {
		return c.r.Read(p)
	}
	return c.Conn.Read(p)
}
