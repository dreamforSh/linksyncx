package repository

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ProxyProbeServiceSuite struct {
	suite.Suite
	ctx      context.Context
	proxySrv *httptest.Server
	prober   *proxyProbeService
}

func (s *ProxyProbeServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.prober = &proxyProbeService{
		allowPrivateHosts: true,
	}
}

func (s *ProxyProbeServiceSuite) TearDownTest() {
	if s.proxySrv != nil {
		s.proxySrv.Close()
		s.proxySrv = nil
	}
}

func (s *ProxyProbeServiceSuite) setupProxyServer(handler http.HandlerFunc) {
	s.proxySrv = newLocalTestServer(s.T(), handler)
}

// The fake proxy only tunnels the built-in ipify target to a loopback TLS server.
// Trust its test certificate without disabling certificate or hostname checks.
func (s *ProxyProbeServiceSuite) setupHTTPSProxyServer(handler http.HandlerFunc) *atomic.Int64 {
	t := s.T()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"api64.ipify.org"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:        true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	target := httptest.NewUnstartedServer(handler)
	target.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	target.StartTLS()
	t.Cleanup(target.Close)
	connects := &atomic.Int64{}
	s.setupProxyServer(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			handler(w, r)
			return
		}
		if r.Host != "api64.ipify.org:443" {
			t.Errorf("unexpected CONNECT target: %s", r.Host)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		upstream, err := net.DialTimeout("tcp", target.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Errorf("dial local TLS target: %v", err)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack CONNECT: %v", err)
			return
		}
		defer conn.Close()
		connects.Add(1)
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		go func() { _, _ = io.Copy(upstream, rw); _ = upstream.Close() }()
		_, _ = io.Copy(conn, upstream)
	})
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: s.proxySrv.URL, Timeout: defaultProxyProbeTimeout, AllowPrivateHosts: true,
	})
	require.NoError(t, err)
	proxyURL, err := url.Parse(s.proxySrv.URL)
	require.NoError(t, err)
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	transport.DisableKeepAlives = true
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
	previous := client.Transport
	client.Transport = transport
	t.Cleanup(func() { client.Transport = previous; transport.CloseIdleConnections() })
	return connects
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidProxyURL() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "://bad")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_UnsupportedProxyScheme() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "ftp://127.0.0.1:1")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPAPI() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查是否是 ip-api 请求
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","query":"1.2.3.4","city":"c","regionName":"r","country":"cc","countryCode":"CC"}`)
			return
		}
		// 其他请求返回错误
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "c", info.City)
	require.Equal(s.T(), "r", info.Region)
	require.Equal(s.T(), "cc", info.Country)
	require.Equal(s.T(), "CC", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPifyFallback() {
	connects := s.setupHTTPSProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ip-api 失败
		if r.Host == "ip-api.com" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// ipify 成功
		if r.Host == "api64.ipify.org" && r.TLS != nil && r.URL.RawQuery == "format=json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ip": "5.6.7.8"}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy should fallback to ipify")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "5.6.7.8", info.IP)
	require.EqualValues(s.T(), 1, connects.Load(), "HTTPS fallback must use CONNECT")
	require.Empty(s.T(), info.CountryCode, "ipify does not provide geographic data")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_AllFailed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidJSON() {
	connects := s.setupHTTPSProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "ip-api.com" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		// ipify 也返回无效响应
		if r.Host == "api64.ipify.org" && r.TLS != nil {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
	require.ErrorContains(s.T(), err, "failed to parse ipify response")
	require.EqualValues(s.T(), 1, connects.Load())
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_ProxyServerClosed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.proxySrv.Close()

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err, "expected error when proxy server is closed")
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Success() {
	body := []byte(`{"status":"success","query":"1.2.3.4","city":"Beijing","regionName":"Beijing","country":"China","countryCode":"CN"}`)
	info, latencyMs, err := s.prober.parseIPAPI(body, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(100), latencyMs)
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "Beijing", info.City)
	require.Equal(s.T(), "Beijing", info.Region)
	require.Equal(s.T(), "China", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Failure() {
	body := []byte(`{"status":"fail","message":"rate limited"}`)
	_, _, err := s.prober.parseIPAPI(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "rate limited")
}

func (s *ProxyProbeServiceSuite) TestParseIPify_Success() {
	body := []byte(`{"ip": "2001:db8::1"}`)
	info, latencyMs, err := s.prober.parseIPify(body, 50)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(50), latencyMs)
	require.Equal(s.T(), "2001:db8::1", info.IP)
}

func (s *ProxyProbeServiceSuite) TestParseIPify_NoIP() {
	body := []byte(`{"ip": ""}`)
	_, _, err := s.prober.parseIPify(body, 50)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "no IP found")
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_Success() {
	body := []byte("fl=abc\nh=chatgpt.com\nip=203.0.113.5\nts=1700000000\nloc=US\ntz=UTC\n")
	info, latencyMs, err := s.prober.parseChatGPTTrace(body, 320)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(320), latencyMs)
	require.Equal(s.T(), "203.0.113.5", info.IP)
	require.Equal(s.T(), "US", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_NoIP() {
	body := []byte("fl=abc\nh=chatgpt.com\nloc=US\n")
	_, _, err := s.prober.parseChatGPTTrace(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "chatgpt-trace: no ip= found")
}

func TestProxyProbeServiceSuite(t *testing.T) {
	suite.Run(t, new(ProxyProbeServiceSuite))
}
