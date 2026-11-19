package tlsfingerprint

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

var errALPNFallbackHTTP1 = errors.New("tlsfingerprint: peer did not negotiate h2, fallback to http/1.1")

// HTTP2TransportConfig 控制支持 HTTP/2 的 uTLS 自适应 RoundTripper 连接池参数。
type HTTP2TransportConfig struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	ResponseHeaderTimeout time.Duration
	ReadIdleTimeout       time.Duration
	PingTimeout           time.Duration
}

//nolint:staticcheck // uTLS *utls.UConn requires x/net/http2.Transport for custom DialTLSContext with h2 ALPN.
type http2AdaptiveTransport struct {
	h2Transport *http2.Transport
	h1Transport *http.Transport
	h1Hosts     sync.Map // map[string]struct{}，记录未协商出 h2 的目标 host
}

// NewHTTP2CapableTransport 构建支持 uTLS 指纹 + HTTP/2 多路复用的自适应 RoundTripper：
//   - 对 HTTPS 目标优先使用携带 h2 ALPN 的 uTLS ClientHello 建立 HTTP/2 连接（并开启 PING 健康探测）；
//   - 对 HTTP/2 请求同样强制执行 ResponseHeaderTimeout（收到响应头即停表，不影响长时 SSE 流读取）；
//   - 若目标服务器在握手中未选择 "h2"（仅支持 HTTP/1.1），自动记忆该主机并平滑回退到共享同一
//     代理隧道与 TLS SessionCache 的 HTTP/1.1 Transport；
//   - 明文 http:// 请求经同一代理隧道发出，绝不绕过代理直连。
func NewHTTP2CapableTransport(profile *Profile, proxyURL *url.URL, dialOpts DialOptions, cfg HTTP2TransportConfig) (http.RoundTripper, error) {
	if err := ValidateProfile(profile); err != nil {
		return nil, err
	}
	// 同一 RoundTripper（同一账号/隔离槽位）内 H2 与 H1 回退共享同一个专属 SessionCache，
	// 不同 RoundTripper 之间完全隔离。
	if dialOpts.SessionCache == nil {
		dialOpts.SessionCache = utls.NewLRUClientSessionCache(defaultSessionCacheSize)
	}

	h2Opts := dialOpts
	h2Opts.AllowHTTP2 = true
	h2Dialer, err := NewProxyDialer(profile, proxyURL, h2Opts)
	if err != nil {
		return nil, err
	}

	h1Opts := dialOpts
	h1Opts.AllowHTTP2 = false
	h1Dialer, err := NewProxyDialer(WithoutHTTP2ALPN(profile), proxyURL, h1Opts)
	if err != nil {
		return nil, err
	}

	// Let x/net/http2 retain a minimal t1 reference solely so ResponseHeaderTimeout
	// is armed after request upload and disarmed upon receiving response headers.
	// Connection pooling for H2 is owned by h2Tr itself once ConnPool is cleared below.
	//
	//nolint:staticcheck // uTLS *utls.UConn requires x/net/http2.Transport for custom DialTLSContext with h2 ALPN.
	h2Tr, err := http2.ConfigureTransports(&http.Transport{
		Protocols:             new(http.Protocols),
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
	})
	if err != nil {
		return nil, err
	}
	// RoundTrip on this transport owns its connection pool and uTLS dialer.
	h2Tr.ConnPool = nil                                                                                      //nolint:staticcheck // See ConfigureTransports note above.
	h2Tr.IdleConnTimeout = cfg.IdleConnTimeout                                                               //nolint:staticcheck // See ConfigureTransports note above.
	h2Tr.ReadIdleTimeout = cfg.ReadIdleTimeout                                                               //nolint:staticcheck // See ConfigureTransports note above.
	h2Tr.PingTimeout = cfg.PingTimeout                                                                       //nolint:staticcheck // See ConfigureTransports note above.
	h2Tr.DialTLSContext = func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) { //nolint:staticcheck // See ConfigureTransports note above.
		conn, err := h2Dialer.DialTLSContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if uconn, ok := conn.(*utls.UConn); ok && uconn.ConnectionState().NegotiatedProtocol != "h2" {
			_ = conn.Close()
			return nil, errALPNFallbackHTTP1
		}
		return conn, nil
	}

	h1Tr := &http.Transport{
		DialContext:           h1Dialer.DialContext,
		DialTLSContext:        h1Dialer.DialTLSContext,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     false,
	}

	return &http2AdaptiveTransport{
		h2Transport: h2Tr,
		h1Transport: h1Tr,
	}, nil
}

func (t *http2AdaptiveTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.URL != nil && strings.EqualFold(req.URL.Scheme, "http") {
		return t.h1Transport.RoundTrip(req)
	}
	host := canonicalHTTPSHostKey(req)
	if host != "" {
		if _, preferH1 := t.h1Hosts.Load(host); preferH1 {
			return t.h1Transport.RoundTrip(req)
		}
	}

	//nolint:staticcheck // uTLS *utls.UConn requires x/net/http2.Transport for custom DialTLSContext with h2 ALPN.
	resp, err := t.h2Transport.RoundTrip(req)
	if err == nil || !errors.Is(err, errALPNFallbackHTTP1) {
		return resp, err
	}

	if host != "" {
		t.h1Hosts.Store(host, struct{}{})
	}
	retryReq := req
	if req != nil && req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, err
		}
		freshBody, gErr := req.GetBody()
		if gErr != nil {
			return nil, gErr
		}
		retryReq = req.Clone(req.Context())
		retryReq.Body = freshBody
	}
	return t.h1Transport.RoundTrip(retryReq)
}

func canonicalHTTPSHostKey(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(req.URL.Host))
	if host == "" {
		return ""
	}
	if h, port, err := net.SplitHostPort(host); err == nil && port == "443" {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}

func (t *http2AdaptiveTransport) CloseIdleConnections() {
	if t.h2Transport != nil {
		//nolint:staticcheck // uTLS *utls.UConn requires x/net/http2.Transport for custom DialTLSContext with h2 ALPN.
		t.h2Transport.CloseIdleConnections()
	}
	if t.h1Transport != nil {
		t.h1Transport.CloseIdleConnections()
	}
}
