//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 非 Anthropic 数据面（OpenAI / Grok / Gemini / Antigravity）遇到代理绑定失效时必须
// fail-closed 且换号：ProxyURLForOutbound 报出的 ErrAccountProxyUnavailable 若原样返回，
// handler 只认 *UpstreamFailoverError，会把请求判死而不是换号（v1beta handler 还会以为响应已写过，
// 客户端拿到空响应）。URL 无效的情况在代理校验前移之前由传输层报错、经各平台传输错误辅助函数
// 换号；关系缺失则一直是终态——两者都要覆盖。

const brokenBindingProxyID = int64(9)

type brokenProxyBinding struct {
	name string
	bind func(*Account)
}

func brokenProxyBindings() []brokenProxyBinding {
	return []brokenProxyBinding{
		{name: "invalid url", bind: func(a *Account) {
			id := brokenBindingProxyID
			a.ProxyID = &id
			a.Proxy = &Proxy{ID: id, Name: "typo-proxy", Protocol: "http", Host: "bad host", Port: 8080}
		}},
		{name: "missing relation", bind: func(a *Account) {
			id := brokenBindingProxyID
			a.ProxyID = &id
			a.Proxy = nil
		}},
	}
}

// countingProxyBindingUpstream 只计数：绑定失效时上游一次都不能被调用（也就不可能直连）。
type countingProxyBindingUpstream struct{ calls int }

func (u *countingProxyBindingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	u.calls++
	return nil, errors.New("upstream must not be reached with a broken proxy binding")
}

func (u *countingProxyBindingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func newProxyBindingTestContext(method, path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

// requireProxyBindingFailover：502 failover、AccountUnavailable（粘性会话也换号）、上游零调用、
// 不写任何响应（响应归 handler：换号或耗尽后渲染），并留下带代理归因的 ops request_error 事件。
func requireProxyBindingFailover(t *testing.T, err error, account *Account, c *gin.Context, rec *httptest.ResponseRecorder, upstream *countingProxyBindingUpstream) {
	t.Helper()
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr, "broken proxy binding must fail over, got %v", err)
	require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	require.True(t, failoverErr.AccountUnavailable, "a broken binding makes the account itself unavailable")
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.NotEmpty(t, failoverErr.ResponseBody, "failover body feeds passthrough rules once failover is exhausted")
	require.Zero(t, upstream.calls, "no outbound request may be made")
	require.Zero(t, rec.Body.Len(), "nothing may be written before the handler fails over")
	require.False(t, c.Writer.Written())

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok, "the broken binding must show up in ops error logs")
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	require.Equal(t, "request_error", last.Kind)
	require.Contains(t, last.Message, ErrAccountProxyUnavailable.Error())
	if account.Proxy != nil {
		// URL 无效时代理行还在：事件指向具体代理，管理员能定位是哪条绑定写错了。
		require.NotNil(t, last.ProxyID)
		require.Equal(t, brokenBindingProxyID, *last.ProxyID)
		require.NotContains(t, last.Message, "bad host", "the event must not echo proxy configuration")
	}
}

func proxyBindingOpenAIService(upstream HTTPUpstream) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		accountRepo:  &openaiTransportAccountRepoStub{},
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
	}
}

func TestOpenAIDataPlaneBrokenProxyBindingFailsOver(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	chatBody := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}]}`)
	responsesBody := []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`)
	embeddingsBody := []byte(`{"model":"text-embedding-3-small","input":"hello"}`)

	entries := []struct {
		name    string
		path    string
		body    []byte
		account func() *Account
		call    func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
	}{
		{
			name: "raw chat completions",
			path: "/v1/chat/completions",
			body: chatBody,
			account: func() *Account {
				return &Account{ID: 81, Name: "oc", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://opencode.example/v1"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.forwardAsRawChatCompletions(context.Background(), c, a, body, "")
				return err
			},
		},
		{
			name: "chat completions public entry (oauth)",
			path: "/v1/chat/completions",
			body: chatBody,
			account: func() *Account {
				return &Account{ID: 82, Name: "codex", Platform: PlatformOpenAI, Type: AccountTypeOAuth,
					Credentials: map[string]any{"access_token": "at"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.ForwardAsChatCompletions(context.Background(), c, a, body, "", "gpt-5.1")
				return err
			},
		},
		{
			name: "responses (apikey)",
			path: "/v1/responses",
			body: responsesBody,
			account: func() *Account {
				return &Account{ID: 83, Name: "apikey", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.example/v1"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.Forward(context.Background(), c, a, body)
				return err
			},
		},
		{
			name: "responses passthrough",
			path: "/v1/responses",
			body: responsesBody,
			account: func() *Account {
				return &Account{ID: 84, Name: "passthrough", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.example/v1"},
					Extra:       map[string]any{"openai_passthrough": true}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.Forward(context.Background(), c, a, body)
				return err
			},
		},
		{
			name: "embeddings",
			path: "/v1/embeddings",
			body: embeddingsBody,
			account: func() *Account {
				return &Account{ID: 85, Name: "embed", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.example/v1"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.ForwardEmbeddings(context.Background(), c, a, body, "")
				return err
			},
		},
		{
			name: "grok responses",
			path: "/v1/responses",
			body: []byte(`{"model":"grok-4","stream":false,"input":"hello"}`),
			account: func() *Account {
				return &Account{ID: 86, Name: "grok", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
					Credentials: map[string]any{"api_key": "xai-key", "base_url": "https://xai.test/v1"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.Forward(context.Background(), c, a, body)
				return err
			},
		},
		{
			name: "grok media",
			path: "/v1/images/generations",
			body: []byte(`{"model":"grok-imagine","prompt":"draw a cat"}`),
			account: func() *Account {
				return &Account{ID: 87, Name: "grok-media", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
					Credentials: map[string]any{"api_key": "xai-key", "base_url": "https://xai.test/v1"}}
			},
			call: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
				_, err := s.ForwardGrokMedia(context.Background(), c, a, GrokMediaEndpointImagesGenerations, "", body, "application/json")
				return err
			},
		},
	}

	for _, entry := range entries {
		for _, binding := range brokenProxyBindings() {
			t.Run(entry.name+"/"+binding.name, func(t *testing.T) {
				upstream := &countingProxyBindingUpstream{}
				svc := proxyBindingOpenAIService(upstream)
				account := entry.account()
				binding.bind(account)
				c, rec := newProxyBindingTestContext(http.MethodPost, entry.path, entry.body)

				err := entry.call(svc, c, account, entry.body)

				requireProxyBindingFailover(t, err, account, c, rec, upstream)
				require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "the request never left: do not block scheduling")
			})
		}
	}
}

func TestGeminiDataPlaneBrokenProxyBindingFailsOver(t *testing.T) {
	newAccount := func() *Account {
		return &Account{ID: 91, Name: "gemini", Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "g-key"}}
	}
	claudeBody := []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
	nativeBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)

	for _, binding := range brokenProxyBindings() {
		t.Run("claude compat/"+binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream}
			account := newAccount()
			binding.bind(account)
			c, rec := newProxyBindingTestContext(http.MethodPost, "/v1/messages", claudeBody)

			_, err := svc.Forward(context.Background(), c, account, claudeBody)

			requireProxyBindingFailover(t, err, account, c, rec, upstream)
		})
		t.Run("native generateContent/"+binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream}
			account := newAccount()
			binding.bind(account)
			c, rec := newProxyBindingTestContext(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", nativeBody)

			_, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "generateContent", false, nativeBody)

			requireProxyBindingFailover(t, err, account, c, rec, upstream)
		})
		// countTokens 与传输层失败同口径：本地估算兜底、不换号，但仍绝不出站。
		t.Run("native countTokens estimates locally/"+binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &GeminiMessagesCompatService{httpUpstream: upstream}
			account := newAccount()
			binding.bind(account)
			c, rec := newProxyBindingTestContext(http.MethodPost, "/v1beta/models/gemini-2.5-flash:countTokens", nativeBody)

			result, err := svc.ForwardNative(context.Background(), c, account, "gemini-2.5-flash", "countTokens", false, nativeBody)

			require.NoError(t, err)
			require.NotNil(t, result)
			require.Zero(t, upstream.calls)
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "totalTokens")
		})
	}
}

func TestAntigravityDataPlaneBrokenProxyBindingFailsOver(t *testing.T) {
	for _, binding := range brokenProxyBindings() {
		t.Run("upstream passthrough/"+binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &AntigravityGatewayService{httpUpstream: upstream}
			account := &Account{ID: 95, Name: "ag-upstream", Platform: PlatformAntigravity, Type: AccountTypeUpstream, Concurrency: 1,
				Credentials: map[string]any{"api_key": "k", "base_url": "https://ag.example"}}
			binding.bind(account)
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`)
			c, rec := newProxyBindingTestContext(http.MethodPost, "/v1/messages", body)

			_, err := svc.Forward(context.Background(), c, account, body, false)

			requireProxyBindingFailover(t, err, account, c, rec, upstream)
		})
		t.Run("gemini/"+binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &AntigravityGatewayService{
				settingService: NewSettingService(&antigravitySettingRepoStub{}, &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}),
				tokenProvider:  &AntigravityTokenProvider{},
				httpUpstream:   upstream,
			}
			account := &Account{ID: 96, Name: "ag-oauth", Platform: PlatformAntigravity, Type: AccountTypeOAuth, Status: StatusActive, Concurrency: 1,
				Credentials: map[string]any{
					"access_token": "token",
					antigravityProjectIDFallbackCredentialKey: "configured-project",
					"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
				}}
			binding.bind(account)
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			c, rec := newProxyBindingTestContext(http.MethodPost, "/antigravity/v1beta/models/gemini-2.5-flash:streamGenerateContent", body)

			_, err := svc.ForwardGemini(context.Background(), c, account, "gemini-2.5-flash", "streamGenerateContent", true, body, false)

			requireProxyBindingFailover(t, err, account, c, rec, upstream)
		})
	}
}

// /v1/web_search 的 Grok 原生搜索早已对绑定失效换号，这里锁住它与其它数据面一致标 AccountUnavailable。
func TestGrokNativeWebSearchBrokenProxyBindingIsAccountUnavailable(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	for _, binding := range brokenProxyBindings() {
		t.Run(binding.name, func(t *testing.T) {
			upstream := &countingProxyBindingUpstream{}
			svc := &GatewayService{httpUpstream: upstream}
			account := &Account{ID: 97, Name: "grok-search", Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
				Credentials: map[string]any{"api_key": "xai-key", "base_url": "https://xai.test/v1"}}
			binding.bind(account)

			_, err := svc.DoGrokNativeResponsesJSON(context.Background(), account, []byte(`{"model":"grok-4","input":"news"}`))

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.True(t, failoverErr.AccountUnavailable)
			require.Zero(t, upstream.calls)
		})
	}
}
