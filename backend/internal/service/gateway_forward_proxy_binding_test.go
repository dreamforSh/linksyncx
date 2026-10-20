//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/websearch"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// proxyBindingCountingTokenCache 记录凭证路径是否被触达：Vertex service account 取 token
// 先查缓存，冷缓存时再用代理去 Google 换 token。
type proxyBindingCountingTokenCache struct {
	calls int
}

func (c *proxyBindingCountingTokenCache) GetAccessToken(context.Context, string) (string, error) {
	c.calls++
	return "", errors.New("cache miss")
}

func (c *proxyBindingCountingTokenCache) SetAccessToken(context.Context, string, string, time.Duration) error {
	c.calls++
	return nil
}

func (c *proxyBindingCountingTokenCache) DeleteAccessToken(context.Context, string) error {
	c.calls++
	return nil
}

func (c *proxyBindingCountingTokenCache) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	c.calls++
	return true, nil
}

func (c *proxyBindingCountingTokenCache) ReleaseRefreshLock(context.Context, string) error {
	c.calls++
	return nil
}

// 上游桩返回错误而不是 (nil, nil)：若回归导致请求漏到上游，Forward 走传输错误分支、
// 断言干净地失败在当前子测试，而不是解引用空响应把整个测试二进制 panic 掉、吞掉其它结果。
var errProxyBindingUpstreamMustNotBeCalled = errors.New("upstream must not be called for a broken proxy binding")

// API Key 取凭证只读本地字段、不出站，单看凭证路径的调用次数证明不了"先查代理"。
// 故意删掉 api_key：一旦顺序回退成先取凭证，就会先拿到一个普通错误（既不是 failover，
// 也不是 ErrAccountProxyUnavailable），这一行随之失败——API Key 行因此也能守住顺序。
func newAPIKeyAccountWithoutKeyForProxyBindingTest(id int64) *Account {
	account := newAnthropicAPIKeyAccountForTest()
	account.ID = id
	delete(account.Credentials, "api_key")
	return account
}

// 数据面代理绑定失效（关系缺失，或 URL 无效）时，三个 Anthropic 入口都必须在取凭证之前
// 返回 AccountUnavailable 的 UpstreamFailoverError——handler 只按这个类型切号，其它错误
// 都是终态。凭证路径也不能被触达：Vertex service account 冷缓存时取 token 本身就要经代理
// 出站，先查绑定保证失效绑定不会触发任何凭证解析、token 刷新或换 token。
func TestGatewayForwardBrokenProxyBindingFailsOverBeforeCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	proxyID := int64(9)
	bindings := []struct {
		name  string
		proxy *Proxy
	}{
		{"missing relation", nil},
		{"invalid url", &Proxy{ID: proxyID, Protocol: "unsupported", Host: "proxy.example.com", Port: 1080, Username: "user", Password: "s3cr3tpw"}},
	}
	accounts := []struct {
		name    string
		account func() *Account
	}{
		{"oauth", func() *Account {
			return &Account{
				ID: 601, Name: "anthropic-oauth-proxy-binding", Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Concurrency: 1, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"access_token": "oauth-token"},
			}
		}},
		// 账号开了 anthropic_passthrough：messages 入口走独立的透传分支
		// （forwardAnthropicAPIKeyPassthroughWithInput），chat completions / responses 没有透传分支、
		// 走普通路径；规则都一样。
		{"apikey", func() *Account { return newAPIKeyAccountWithoutKeyForProxyBindingTest(603) }},
		{"service account", func() *Account {
			return &Account{
				ID: 602, Name: "anthropic-vertex-proxy-binding", Platform: PlatformAnthropic, Type: AccountTypeServiceAccount,
				Concurrency: 1, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"service_account_json": map[string]any{
					"client_email": "sa@example-project.iam.gserviceaccount.com",
					"private_key":  "not-a-real-key",
					"project_id":   "example-project",
				}},
			}
		}},
	}
	entries := []struct {
		name string
		call func(svc *GatewayService, c *gin.Context, account *Account) (*ForwardResult, error)
	}{
		{"messages", func(svc *GatewayService, c *gin.Context, account *Account) (*ForwardResult, error) {
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			return svc.Forward(context.Background(), c, account, parsed)
		}},
		{"chat completions", func(svc *GatewayService, c *gin.Context, account *Account) (*ForwardResult, error) {
			body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
			return svc.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
		}},
		{"responses", func(svc *GatewayService, c *gin.Context, account *Account) (*ForwardResult, error) {
			body := []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`)
			return svc.ForwardAsResponses(context.Background(), c, account, body, nil)
		}},
	}

	for _, binding := range bindings {
		for _, acc := range accounts {
			for _, entry := range entries {
				t.Run(binding.name+"/"+acc.name+"/"+entry.name, func(t *testing.T) {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

					upstream := &anthropicHTTPUpstreamRecorder{err: errProxyBindingUpstreamMustNotBeCalled}
					cache := &proxyBindingCountingTokenCache{}
					svc := newForwardPartialUsageServiceForTest(upstream)
					svc.claudeTokenProvider = NewClaudeTokenProvider(nil, cache, nil)

					account := acc.account()
					account.ProxyID = &proxyID
					account.Proxy = binding.proxy

					result, err := entry.call(svc, c, account)
					require.Nil(t, upstream.lastReq, "no upstream request may leave without the assigned proxy")
					require.Nil(t, result)
					var failoverErr *UpstreamFailoverError
					require.ErrorAs(t, err, &failoverErr, "a broken binding must fail over, not end the request")
					require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
					require.True(t, failoverErr.AccountUnavailable, "the account must be switched, not retried by sticky hold")
					require.Zero(t, cache.calls, "credentials must not be resolved or refreshed for a broken binding")
					require.NotContains(t, rec.Body.String(), "s3cr3tpw")
				})
			}
		}
	}
}

// count_tokens 没有 failover，但同样先查代理绑定：绑定失效时直接 502，不去取 / 刷新凭证。
func TestGatewayCountTokensBrokenProxyBindingSkipsCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	proxyID := int64(9)
	for _, binding := range []struct {
		name  string
		proxy *Proxy
	}{
		{"missing relation", nil},
		{"invalid url", &Proxy{ID: proxyID, Protocol: "unsupported", Host: "proxy.example.com", Port: 1080}},
	} {
		for _, acc := range []struct {
			name    string
			account *Account
		}{
			{"oauth", &Account{
				ID: 611, Name: "anthropic-oauth-count-tokens", Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Concurrency: 1, Status: StatusActive, Schedulable: true,
				Credentials: map[string]any{"access_token": "oauth-token"},
			}},
			// 开了 anthropic_passthrough，走 forwardCountTokensAnthropicAPIKeyPassthrough。
			{"apikey passthrough", newAPIKeyAccountWithoutKeyForProxyBindingTest(612)},
		} {
			t.Run(binding.name+"/"+acc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

				body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)

				upstream := &anthropicHTTPUpstreamRecorder{err: errProxyBindingUpstreamMustNotBeCalled}
				cache := &proxyBindingCountingTokenCache{}
				svc := newForwardPartialUsageServiceForTest(upstream)
				svc.claudeTokenProvider = NewClaudeTokenProvider(nil, cache, nil)

				account := acc.account
				account.ProxyID = &proxyID
				account.Proxy = binding.proxy

				err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
				require.Nil(t, upstream.lastReq, "no upstream request may leave without the assigned proxy")
				require.ErrorIs(t, err, ErrAccountProxyUnavailable)
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Zero(t, cache.calls, "credentials must not be resolved or refreshed for a broken binding")
			})
		}
	}
}

// Web Search 模拟分支在 Forward 里先于上面的“先查代理绑定”执行：纯 web_search 请求根本走不到
// 通用转发路径，而搜索请求同样经账号代理出站，所以这个分支要自己先查绑定、按账号不可用换号。
// canary 是本地 HTTP 代理桩，同时充当 provider 自带的代理：账号代理若被当成“没有代理”放行，
// 搜索会改走 provider 代理、打到 canary。对照组把账号代理指向 canary，证明夹具确实走进了
// 模拟分支（通用转发路径只会调 httpUpstream，碰不到 canary）。
func TestGatewayWebSearchEmulationBrokenProxyBindingFailsOver(t *testing.T) {
	var canaryHits atomic.Int32
	canary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		canaryHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer canary.Close()
	canaryURL, err := url.Parse(canary.URL)
	require.NoError(t, err)
	canaryPort, err := strconv.Atoi(canaryURL.Port())
	require.NoError(t, err)

	SetWebSearchManager(websearch.NewManager([]websearch.ProviderConfig{{Type: "brave", APIKey: "k", ProxyURL: canary.URL}}, nil))
	defer SetWebSearchManager(nil)
	setGlobalWebSearchConfig(&WebSearchEmulationConfig{
		Enabled:   true,
		Providers: []WebSearchProviderConfig{{Type: "brave", APIKey: "k"}},
	})
	defer clearGlobalWebSearchConfig()

	forward := func(t *testing.T, bind func(*Account)) (*Account, *gin.Context, *httptest.ResponseRecorder, *countingProxyBindingUpstream, error) {
		t.Helper()
		body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"stream":true,"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"latest go release"}]}`)
		parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
		require.NoError(t, err)
		c, rec := newProxyBindingTestContext(http.MethodPost, "/v1/messages", body)
		upstream := &countingProxyBindingUpstream{}
		svc := &GatewayService{
			httpUpstream:     upstream,
			settingService:   newSettingServiceForWebSearchTest(true),
			rateLimitService: &RateLimitService{},
			deferredService:  &DeferredService{},
		}
		account := newAnthropicAPIKeyAccount(WebSearchModeEnabled)
		account.ID = 621
		account.Name = "anthropic-websearch-proxy-binding"
		bind(account)
		_, err = svc.Forward(context.Background(), c, account, parsed)
		return account, c, rec, upstream, err
	}

	t.Run("control: healthy binding searches through the account proxy", func(t *testing.T) {
		canaryHits.Store(0)
		_, _, _, upstream, err := forward(t, func(a *Account) {
			id := brokenBindingProxyID
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: id, Protocol: "http", Host: canaryURL.Hostname(), Port: canaryPort}
		})
		require.Error(t, err, "the canary refuses CONNECT, so the search itself fails")
		require.NotZero(t, canaryHits.Load(), "the fixture must reach web search emulation")
		require.Zero(t, upstream.calls)
	})
	for _, binding := range brokenProxyBindings() {
		t.Run(binding.name, func(t *testing.T) {
			canaryHits.Store(0)
			account, c, rec, upstream, err := forward(t, binding.bind)
			requireProxyBindingFailover(t, err, account, c, rec, upstream)
			require.Zero(t, canaryHits.Load(), "no search may run without the assigned proxy")
			// main 上这一行的 failover body 是搜索客户端的报错原文，会带出完整代理 URL（含账号密码）；
			// 换号耗尽后可能经错误透传规则回给客户端。
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.NotContains(t, string(failoverErr.ResponseBody), "bad host", "the failover body must not echo proxy configuration")
		})
	}
}
