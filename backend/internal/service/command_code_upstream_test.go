//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// commandCodeTestAccount 只带 api_key：端点与分流规则全部来自平台默认值。
func commandCodeTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "cc",
		Platform:    PlatformCommandCode,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "user_test_key"},
	}
}

func commandCodeUpstreamTestResponse(contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

const (
	commandCodeTestChatJSON = `{"id":"chatcmpl_cc","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	commandCodeTestChatSSE  = `data: {"id":"chatcmpl_cc","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}

data: {"id":"chatcmpl_cc","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}

data: [DONE]

`
	commandCodeTestResponseJSON = `{"id":"resp_cc","object":"response","model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	commandCodeTestResponseSSE  = `event: response.completed
data: {"type":"response.completed","response":` + commandCodeTestResponseJSON + `}

`
)

// commandCodeRoutingUpstream 记录请求，并按目标端点与是否流式返回对应协议的上游响应
// （跨协议转换链会强制上游流式）。
type commandCodeRoutingUpstream struct{ httpUpstreamRecorder }

func (u *commandCodeRoutingUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	_, _ = u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
	stream := gjson.GetBytes(u.lastBody, "stream").Bool()
	switch path := req.URL.Path; {
	case strings.HasSuffix(path, "/messages") && stream:
		return nativeAnthropicStreamResponse(), nil
	case strings.HasSuffix(path, "/messages"):
		return nativeAnthropicBufferedResponse(), nil
	case strings.HasSuffix(path, "/responses") && stream:
		return commandCodeUpstreamTestResponse("text/event-stream", commandCodeTestResponseSSE), nil
	case strings.HasSuffix(path, "/responses"):
		return commandCodeUpstreamTestResponse("application/json", commandCodeTestResponseJSON), nil
	case stream:
		return commandCodeUpstreamTestResponse("text/event-stream", commandCodeTestChatSSE), nil
	default:
		return commandCodeUpstreamTestResponse("application/json", commandCodeTestChatJSON), nil
	}
}

func (u *commandCodeRoutingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

// 网关真实转发路径按模型分流，与入站协议正交：Claude 只走 Anthropic Messages，
// GPT 走 Responses（Chat Completions 入站同协议直通），其余模型走 Chat Completions。
// 显式 api_protocol 优先于内置规则。出站 UA 一律收敛为规范客户端身份。
func TestCommandCodeGatewayRoutesByModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		messagesURL  = "https://api.commandcode.ai/provider/v1/messages"
		chatURL      = "https://api.commandcode.ai/provider/v1/chat/completions"
		responsesURL = "https://api.commandcode.ai/provider/v1/responses"
	)
	type ingress struct {
		path    string
		body    string
		forward func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
	}
	ingresses := map[string]ingress{
		APIProtocolChatCompletions: {
			path: "/v1/chat/completions",
			body: `{"model":%q,"stream":false,"messages":[{"role":"user","content":"hi"}]}`,
			forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				return err
			},
		},
		APIProtocolResponses: {
			path: "/v1/responses",
			body: `{"model":%q,"stream":false,"input":"hi"}`,
			forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
				_, err := svc.Forward(context.Background(), c, account, body)
				return err
			},
		},
		APIProtocolAnthropic: {
			path: "/v1/messages",
			body: `{"model":%q,"max_tokens":32,"stream":false,"messages":[{"role":"user","content":"hi"}]}`,
			forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) error {
				_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
				return err
			},
		},
	}

	cases := []struct {
		inbound  string
		model    string
		protocol string // 显式 api_protocol；空为默认 adaptive
		wantURL  string
	}{
		{APIProtocolChatCompletions, "claude-sonnet-4-6", "", messagesURL},
		{APIProtocolChatCompletions, "deepseek/deepseek-v4-flash", "", chatURL},
		{APIProtocolChatCompletions, "gpt-5.5", "", chatURL},
		{APIProtocolResponses, "claude-sonnet-4-6", "", messagesURL},
		{APIProtocolResponses, "deepseek/deepseek-v4-flash", "", chatURL},
		{APIProtocolResponses, "gpt-5.5", "", responsesURL},
		{APIProtocolAnthropic, "claude-sonnet-4-6", "", messagesURL},
		{APIProtocolAnthropic, "deepseek/deepseek-v4-flash", "", chatURL},
		{APIProtocolAnthropic, "gpt-5.5", "", responsesURL},
		{APIProtocolAnthropic, "claude-sonnet-4-6", APIProtocolChatCompletions, chatURL},
	}
	for _, tc := range cases {
		name := tc.inbound + "/" + tc.model
		if tc.protocol != "" {
			name += "/pinned-" + tc.protocol
		}
		t.Run(name, func(t *testing.T) {
			account := commandCodeTestAccount(1)
			if tc.protocol != "" {
				account.Credentials["api_protocol"] = tc.protocol
			} else {
				require.Equal(t, APIProtocolAdaptive, account.GetAPIProtocol())
			}
			in := ingresses[tc.inbound]
			body := []byte(fmt.Sprintf(in.body, tc.model))
			upstream := &commandCodeRoutingUpstream{}
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
			c := adaptiveProtocolTestContext(in.path, body)

			err := in.forward(svc, c, account, body)

			require.NotNil(t, upstream.lastReq)
			require.Equal(t, tc.wantURL, upstream.lastReq.URL.String())
			require.NoError(t, err)
			require.Equal(t, tc.model, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Equal(t, CodexCanonicalUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
		})
	}
}

func TestCommandCodeNativeAnthropicTargetURL(t *testing.T) {
	svc := &OpenAIGatewayService{}
	targetURL, err := svc.nativeAnthropicTargetURL(commandCodeTestAccount(2))
	require.NoError(t, err)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/messages", targetURL)
}

func TestAccountTestService_CommandCodeClaudeUsesAnthropicMessages(t *testing.T) {
	account := commandCodeTestAccount(501)
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNAnthropicTestResponse())
	c, recorder := newTestContext()

	err := svc.TestAccountConnection(c, account.ID, "claude-sonnet-4-6", "hi", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/messages", upstream.requests[0].URL.String())
	require.Equal(t, "user_test_key", upstream.requests[0].Header.Get("x-api-key"))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

// 连接测试与转发共用端点拼接：按模型分流的平台，Anthropic 基址带 /v1 也合法。
func TestAccountTestService_CommandCodeAnthropicBaseWithVersion(t *testing.T) {
	account := commandCodeTestAccount(504)
	account.Credentials["api_base_urls"] = map[string]any{APIProtocolAnthropic: "https://api.commandcode.ai/provider/v1"}
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNAnthropicTestResponse())
	c, recorder := newTestContext()

	err := svc.TestAccountConnection(c, account.ID, "claude-sonnet-4-6", "hi", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/messages", upstream.requests[0].URL.String())
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)

	target, err := (&OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}).nativeAnthropicTargetURL(account)
	require.NoError(t, err)
	require.Equal(t, upstream.requests[0].URL.String(), target)
}

func TestAccountTestService_CommandCodeGPTUsesResponses(t *testing.T) {
	account := commandCodeTestAccount(502)
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNResponsesTestResponse())
	c, recorder := newTestContext()

	err := svc.TestAccountConnection(c, account.ID, "gpt-5.5", "hi", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/responses", upstream.requests[0].URL.String())
	require.Equal(t, "Bearer user_test_key", upstream.requests[0].Header.Get("Authorization"))
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

func TestAccountTestService_CommandCodeEmptyModelUsesProfileDefault(t *testing.T) {
	account := commandCodeTestAccount(503)
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse())
	c, recorder := newTestContext()

	err := svc.TestAccountConnection(c, account.ID, "", "hi", AccountTestModeDefault)

	require.NoError(t, err)
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/chat/completions", upstream.requests[0].URL.String())
	require.Equal(t, DefaultCommandCodeTestModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, recorder.Body.String(), `"type":"test_complete"`)
}

// 官方主机不做上游计费探测：中转站的计费接口在官方 API 上不存在，只会把 Key 发往官方主机。
func TestCommandCodeOfficialHostSkipsUpstreamBillingProbe(t *testing.T) {
	require.True(t, upstreamBillingProbeTargetIsOfficialAPI(DefaultCommandCodeBaseURL))
	require.True(t, upstreamBillingProbeTargetIsOfficialAPI(DefaultCommandCodeAnthropicBaseURL))
}

// Command Code 没有 count_tokens / responses/input_tokens 端点：两种入站都本地估算，
// 不发上游（否则请求绕过 UA 收敛，被 Cloudflare 拦截后还会计入账号 403）。
func TestCommandCodeCountTokensEstimatedLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	for _, tc := range []struct {
		path    string
		body    string
		forward func(*gin.Context, []byte) error
	}{
		{
			path: "/v1/messages/count_tokens",
			body: `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`,
			forward: func(c *gin.Context, body []byte) error {
				return svc.ForwardCountTokensAsAnthropic(context.Background(), c, commandCodeTestAccount(1), body, "")
			},
		},
		{
			path: "/v1/responses/input_tokens",
			body: `{"model":"gpt-5.5","input":"hello"}`,
			forward: func(c *gin.Context, body []byte) error {
				return svc.ForwardResponsesInputTokens(context.Background(), c, commandCodeTestAccount(1), body)
			},
		},
	} {
		c, recorder := newTestContext()
		c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))

		require.NoError(t, tc.forward(c, []byte(tc.body)), tc.path)
		require.Equal(t, http.StatusOK, recorder.Code, tc.path)
		require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int(), tc.path)
	}
	require.Nil(t, upstream.lastReq)
}

// 模型同步走 OpenAI 兼容 /v1/models：基址已带 /v1 不重复拼接，经账号代理出站，
// UA 与推理同样收敛为规范客户端身份。
func TestCommandCodeUpstreamModelSync(t *testing.T) {
	proxyID := int64(7)
	account := commandCodeTestAccount(1)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.test", Port: 3128}
	upstream := &httpUpstreamRecorder{resp: commandCodeUpstreamTestResponse("application/json",
		`{"object":"list","data":[{"id":"deepseek/deepseek-v4-flash","object":"model"},{"id":"gpt-5.5","object":"model"}]}`)}
	svc := &AccountTestService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	models, _, err := svc.fetchUpstreamModelList(context.Background(), account)

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"deepseek/deepseek-v4-flash", "gpt-5.5"}, models)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/models", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer user_test_key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, CodexCanonicalUserAgent(), upstream.lastReq.Header.Get("User-Agent"))
	require.Equal(t, "http://proxy.test:3128", upstream.lastProxyURL)
	require.Equal(t, DefaultCommandCodeBaseURL, upstreamModelRegistryBaseURL(account))

	// 代理已分配但不可用：fail-closed，不直连。
	account.Proxy = nil
	upstream.lastReq = nil
	_, _, err = svc.fetchUpstreamModelList(context.Background(), account)
	require.Error(t, err)
	require.Nil(t, upstream.lastReq)
}
