//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// 数据面代理绑定失效（关系缺失，或 URL 无效）时，三个 Anthropic 入口都必须在取凭证之前
// 返回 AccountUnavailable 的 UpstreamFailoverError——handler 只按这个类型切号，其它错误
// 都是终态。凭证路径也不能被触达：Vertex service account 冷缓存时取 token 本身就要经代理
// 出站，此前它先于代理检查执行，失效绑定的错误原样上抛，用户拿到终态 502 而不是切号。
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
		// API Key 透传走独立分支（forwardAnthropicAPIKeyPassthroughWithInput），规则相同。
		{"apikey passthrough", func() *Account {
			account := newAnthropicAPIKeyAccountForTest()
			account.ID = 603
			return account
		}},
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

					upstream := &anthropicHTTPUpstreamRecorder{}
					cache := &proxyBindingCountingTokenCache{}
					svc := newForwardPartialUsageServiceForTest(upstream)
					svc.claudeTokenProvider = NewClaudeTokenProvider(nil, cache, nil)

					account := acc.account()
					account.ProxyID = &proxyID
					account.Proxy = binding.proxy

					result, err := entry.call(svc, c, account)
					require.Nil(t, result)
					var failoverErr *UpstreamFailoverError
					require.ErrorAs(t, err, &failoverErr, "a broken binding must fail over, not end the request")
					require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
					require.True(t, failoverErr.AccountUnavailable, "the account must be switched, not retried by sticky hold")
					require.Nil(t, upstream.lastReq, "no upstream request may leave without the assigned proxy")
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
			{"apikey passthrough", newAnthropicAPIKeyAccountForTest()},
		} {
			t.Run(binding.name+"/"+acc.name, func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

				body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
				parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
				require.NoError(t, err)

				upstream := &anthropicHTTPUpstreamRecorder{}
				cache := &proxyBindingCountingTokenCache{}
				svc := newForwardPartialUsageServiceForTest(upstream)
				svc.claudeTokenProvider = NewClaudeTokenProvider(nil, cache, nil)

				account := acc.account
				account.ProxyID = &proxyID
				account.Proxy = binding.proxy

				err = svc.ForwardCountTokens(context.Background(), c, account, parsed)
				require.ErrorIs(t, err, ErrAccountProxyUnavailable)
				require.Equal(t, http.StatusBadGateway, rec.Code)
				require.Nil(t, upstream.lastReq, "no upstream request may leave without the assigned proxy")
				require.Zero(t, cache.calls, "credentials must not be resolved or refreshed for a broken binding")
			})
		}
	}
}
