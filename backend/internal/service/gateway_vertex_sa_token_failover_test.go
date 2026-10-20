//go:build unit

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// vertexSATokenTestUpstream 只统计数据面调用次数：token 交换阶段失败时，请求绝不能走到数据面。
type vertexSATokenTestUpstream struct {
	HTTPUpstream
	calls int
}

func (u *vertexSATokenTestUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return nil, context.Canceled
}

func (u *vertexSATokenTestUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return nil, context.Canceled
}

var (
	vertexSATestKeyOnce sync.Once
	vertexSATestKeyPEM  string
)

// vertexSATestPrivateKeyPEM 生成一把真实 RSA 私钥，保证 JWT 能签出来、流程一直走到网络交换那一步。
func vertexSATestPrivateKeyPEM(t *testing.T) string {
	t.Helper()
	vertexSATestKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		vertexSATestKeyPEM = string(pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		}))
	})
	return vertexSATestKeyPEM
}

// deadLocalProxyPort 拿到一个刚释放、没有监听者的本地端口：语法合法但拨号必然失败的“死代理”。
func deadLocalProxyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func newVertexSAAccountForTokenTest(t *testing.T, privateKeyPEM string, proxy *Proxy) *Account {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "vertex-proj",
		"private_key_id": "kid",
		"private_key":    privateKeyPEM,
		"client_email":   "svc@vertex-proj.iam.gserviceaccount.com",
	})
	require.NoError(t, err)
	proxyID := int64(9101)
	account := &Account{
		ID:          9101,
		Name:        "vertex-sa-token-test",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeServiceAccount,
		Concurrency: 1,
		Status:      StatusActive,
		Schedulable: true,
		ProxyID:     &proxyID,
		Proxy:       proxy,
		Credentials: map[string]any{
			"service_account_json": string(raw),
			"location":             "us-east5",
		},
	}
	if proxy != nil {
		proxy.ID = proxyID
	}
	return account
}

func newVertexSAGatewayServiceForTokenTest() (*GatewayService, *vertexSATokenTestUpstream, *transportTempUnschedRepoStub) {
	upstream := &vertexSATokenTestUpstream{}
	repo := &transportTempUnschedRepoStub{}
	return &GatewayService{
		cfg:                 &config.Config{},
		httpUpstream:        upstream,
		accountRepo:         repo,
		claudeTokenProvider: NewClaudeTokenProvider(nil, nil, nil),
	}, upstream, repo
}

var vertexSATokenTestEntries = []struct {
	name string
	path string
	call func(t *testing.T, svc *GatewayService, c *gin.Context, account *Account) error
}{
	{
		name: "messages",
		path: "/v1/messages",
		call: func(t *testing.T, svc *GatewayService, c *gin.Context, account *Account) error {
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			_, err = svc.Forward(t.Context(), c, account, parsed)
			return err
		},
	},
	{
		name: "chat_completions",
		path: "/v1/chat/completions",
		call: func(t *testing.T, svc *GatewayService, c *gin.Context, account *Account) error {
			body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
			_, err := svc.ForwardAsChatCompletions(t.Context(), c, account, body, nil)
			return err
		},
	},
	{
		name: "responses",
		path: "/v1/responses",
		call: func(t *testing.T, svc *GatewayService, c *gin.Context, account *Account) error {
			body := []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`)
			_, err := svc.ForwardAsResponses(t.Context(), c, account, body, nil)
			return err
		},
	},
}

func newVertexSATokenTestGin(path string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c
}

// 代理语法合法但已失效（拨号失败）时，SA token 交换的传输层错误必须变成 *UpstreamFailoverError，
// 让 handler 换号；而不是以普通错误逃出 Forward、直接把请求判死。
func TestVertexServiceAccountDeadProxyTokenExchangeFailsOver(t *testing.T) {
	port := deadLocalProxyPort(t)
	for _, entry := range vertexSATokenTestEntries {
		t.Run(entry.name, func(t *testing.T) {
			svc, upstream, _ := newVertexSAGatewayServiceForTokenTest()
			account := newVertexSAAccountForTokenTest(t, vertexSATestPrivateKeyPEM(t), &Proxy{
				Name: "dead-proxy", Protocol: "http", Host: "127.0.0.1", Port: port,
			})
			c := newVertexSATokenTestGin(entry.path)

			err := entry.call(t, svc, c, account)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr, "dead proxy during SA token exchange must fail over, got %T: %v", err, err)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.Equal(t, string(gatewayTransportFailoverBody), string(failoverErr.ResponseBody))
			require.True(t, failoverErr.ShouldRetryNextAccount())
			require.Zero(t, upstream.calls, "token 都没拿到，数据面请求不能发出")
			require.False(t, c.Writer.Written(), "响应由 handler 负责（换号或最终错误），service 不能先写")

			raw, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok, "token 交换的传输失败也要记 Ops 上游错误")
			events := raw.([]*OpsUpstreamErrorEvent)
			require.Len(t, events, 1)
			require.Equal(t, "request_error", events[0].Kind)
			require.Equal(t, vertexDefaultTokenURL, events[0].UpstreamURL)
			require.NotNil(t, events[0].ProxyID)
			require.Equal(t, *account.ProxyID, *events[0].ProxyID)
		})
	}
}

// 分配了代理但关系缺失时，token 交换阶段同样 fail-closed 并 failover（标记账号不可用），
// 不依赖 Forward 里 ProxyURLForOutbound 检查与 GetAccessToken 的先后顺序。
func TestVertexServiceAccountMissingProxyBindingAtTokenStepFailsOver(t *testing.T) {
	for _, entry := range vertexSATokenTestEntries {
		t.Run(entry.name, func(t *testing.T) {
			svc, upstream, _ := newVertexSAGatewayServiceForTokenTest()
			account := newVertexSAAccountForTokenTest(t, vertexSATestPrivateKeyPEM(t), nil)
			c := newVertexSATokenTestGin(entry.path)

			err := entry.call(t, svc, c, account)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr, "got %T: %v", err, err)
			require.True(t, failoverErr.AccountUnavailable)
			require.Zero(t, upstream.calls)
		})
	}
}

// 坏的 SA 私钥是凭据问题，换代理/换时间都不会好：必须保持终止错误，不能伪装成传输失败去 failover，
// 也不能因此把账号按“代理故障”临时下线。
func TestVertexServiceAccountBadKeyStaysTerminal(t *testing.T) {
	port := deadLocalProxyPort(t)
	for _, entry := range vertexSATokenTestEntries {
		t.Run(entry.name, func(t *testing.T) {
			svc, upstream, repo := newVertexSAGatewayServiceForTokenTest()
			account := newVertexSAAccountForTokenTest(t, "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n", &Proxy{
				Name: "dead-proxy", Protocol: "http", Host: "127.0.0.1", Port: port,
			})
			c := newVertexSATokenTestGin(entry.path)

			err := entry.call(t, svc, c, account)

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.NotErrorAs(t, err, &failoverErr, "credential errors must stay terminal")
			require.Contains(t, err.Error(), "parse service account private key")
			require.Zero(t, upstream.calls)
			require.Zero(t, repo.calls, "凭据错误不能触发传输层临时下线")
			_, recorded := c.Get(OpsUpstreamErrorsKey)
			require.False(t, recorded, "凭据错误不是上游传输错误，不记 request_error 事件")
		})
	}
}

// token 端点经代理正常应答、但拒绝断言（invalid_grant）属于凭据错误：不能打传输层标记，
// handleAccessTokenTransportError 必须放行（返回 nil），让调用方维持终止语义。
func TestExchangeVertexServiceAccountTokenRejectedAssertionIsNotTransport(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
	}))
	defer proxy.Close()

	key := &vertexServiceAccountKey{
		ClientEmail: "svc@vertex-proj.iam.gserviceaccount.com",
		PrivateKey:  vertexSATestPrivateKeyPEM(t),
		TokenURI:    "http://oauth2.googleapis.com/token",
	}
	_, _, err := exchangeVertexServiceAccountToken(contextWithTestTimeout(t), key, proxy.URL)
	require.ErrorContains(t, err, "Invalid JWT Signature.")
	var exchangeErr *tokenExchangeTransportError
	require.NotErrorAs(t, err, &exchangeErr)

	repo := &transportTempUnschedRepoStub{}
	svc := &GatewayService{accountRepo: repo}
	c := newVertexSATokenTestGin("/v1/messages")
	require.NoError(t, svc.handleAccessTokenTransportError(t.Context(), c, &Account{ID: 1, Platform: PlatformAnthropic}, err))
	require.Zero(t, repo.calls)
	_, recorded := c.Get(OpsUpstreamErrorsKey)
	require.False(t, recorded)
}

// 死代理在交换层就要打上传输层标记，且保留原错误链（分类器靠 errors.Is/As 判断是否持久故障）。
func TestExchangeVertexServiceAccountTokenDeadProxyIsTransport(t *testing.T) {
	key := &vertexServiceAccountKey{
		ClientEmail: "svc@vertex-proj.iam.gserviceaccount.com",
		PrivateKey:  vertexSATestPrivateKeyPEM(t),
		TokenURI:    vertexDefaultTokenURL,
	}
	_, _, err := exchangeVertexServiceAccountToken(contextWithTestTimeout(t), key, fmt.Sprintf("http://127.0.0.1:%d", deadLocalProxyPort(t)))
	var exchangeErr *tokenExchangeTransportError
	require.ErrorAs(t, err, &exchangeErr)
	require.Equal(t, vertexDefaultTokenURL, exchangeErr.tokenURL)
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr, "wrapper must keep the dial error reachable for classification")
	require.Contains(t, err.Error(), "service account token request failed")
}

// 持久故障（代理拒连）经包装后仍被识别：账号临时下线并标记不可用，与数据面 DoWithTLS 失败一致。
// 用构造的 ECONNREFUSED 而不是真拨号，避免依赖平台的拒连错误形态（Windows 上是 WSAECONNREFUSED）。
func TestHandleAccessTokenTransportErrorPersistentProxyFaultEvictsAccount(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	svc := &GatewayService{accountRepo: repo}
	account := &Account{ID: 9102, Name: "vertex-sa", Platform: PlatformAnthropic, Type: AccountTypeServiceAccount}
	err := &tokenExchangeTransportError{
		tokenURL: vertexDefaultTokenURL,
		err:      fmt.Errorf("service account token request failed: %w", &net.OpError{Op: "proxyconnect", Net: "tcp", Err: syscall.ECONNREFUSED}),
	}

	got := svc.handleAccessTokenTransportError(t.Context(), newVertexSATokenTestGin("/v1/messages"), account, err)

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, got, &failoverErr)
	require.True(t, failoverErr.AccountUnavailable)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, account.ID, repo.lastID)
}

// 客户端已断开时与数据面一致：不换号、不下线账号，但也必须返回非 nil，调用方不能把它当凭据错误吞掉。
func TestHandleAccessTokenTransportErrorClientCanceledNoFailover(t *testing.T) {
	repo := &transportTempUnschedRepoStub{}
	svc := &GatewayService{accountRepo: repo}
	err := &tokenExchangeTransportError{
		tokenURL: vertexDefaultTokenURL,
		err:      fmt.Errorf("service account token request failed: %w", context.Canceled),
	}

	got := svc.handleAccessTokenTransportError(t.Context(), newVertexSATokenTestGin("/v1/messages"), &Account{ID: 9103, Platform: PlatformAnthropic}, err)

	require.ErrorIs(t, got, context.Canceled)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, got, &failoverErr)
	require.Zero(t, repo.calls)
}
