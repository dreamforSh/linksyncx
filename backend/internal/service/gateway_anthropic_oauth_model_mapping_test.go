//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本文件覆盖 Anthropic OAuth/SetupToken 账号的模型限制（白名单/映射）：映射必须真正改写发往
// 上游的模型，并先于 Claude Code 伪装、beta 头与 Opus 5.5 校验生效；计费/日志与回写给客户端的
// 模型名沿用 API Key 映射的约定（原始请求模型），UpstreamModel 记录实际上游模型。

func newAnthropicOAuthMappingAccount(accountType string, mapping map[string]any) *Account {
	credentials := map[string]any{"access_token": "oauth-token"}
	if mapping != nil {
		credentials["model_mapping"] = mapping
	}
	return &Account{
		ID:          611,
		Name:        "anthropic-oauth-mapping",
		Platform:    PlatformAnthropic,
		Type:        accountType,
		Concurrency: 1,
		Credentials: credentials,
		Status:      StatusActive,
		Schedulable: true,
	}
}

func newAnthropicOAuthMappingGatewayService(upstream *anthropicHTTPUpstreamRecorder) *GatewayService {
	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
}

func newAnthropicOAuthMappingMessageResponse(model string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Request-Id": []string{"rid-oauth-mapping"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"` + model +
			`","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`)),
	}
}

func newAnthropicOAuthMappingTestContext(path string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, rec
}

func parseAnthropicOAuthMappingRequest(t *testing.T, body string) *ParsedRequest {
	t.Helper()
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
	require.NoError(t, err)
	return parsed
}

func TestAccount_ResolveAnthropicOAuthMappedModel(t *testing.T) {
	tests := []struct {
		name        string
		account     *Account
		model       string
		wantModel   string
		wantMatched bool
	}{
		{
			name:        "未配置映射时不命中",
			account:     newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil),
			model:       "claude-sonnet-4-5",
			wantModel:   "claude-sonnet-4-5",
			wantMatched: false,
		},
		{
			name: "API Key 账号不走该解析",
			account: &Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"model_mapping": map[string]any{"claude-opus-4-6": "claude-sonnet-4-6"}},
			},
			model:       "claude-opus-4-6",
			wantModel:   "claude-opus-4-6",
			wantMatched: false,
		},
		{
			name:        "白名单原样放行",
			account:     newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-4-6": "claude-opus-4-6"}),
			model:       "claude-opus-4-6",
			wantModel:   "claude-opus-4-6",
			wantMatched: true,
		},
		{
			name:        "短 ID 白名单命中后标准化为长 ID",
			account:     newAnthropicOAuthMappingAccount(AccountTypeSetupToken, map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}),
			model:       "claude-sonnet-4-5",
			wantModel:   "claude-sonnet-4-5-20250929",
			wantMatched: true,
		},
		{
			name:        "长 ID 映射键按标准化后的请求命中",
			account:     newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-sonnet-4-5-20250929": "claude-opus-4-6"}),
			model:       "claude-sonnet-4-5",
			wantModel:   "claude-opus-4-6",
			wantMatched: true,
		},
		{
			name:        "通配符映射且目标为短 ID",
			account:     newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-*": "claude-haiku-4-5"}),
			model:       "claude-opus-4-8",
			wantModel:   "claude-haiku-4-5-20251001",
			wantMatched: true,
		},
		{
			name:        "未命中映射保持原模型",
			account:     newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-4-6": "claude-opus-4-6"}),
			model:       "claude-sonnet-4-6",
			wantModel:   "claude-sonnet-4-6",
			wantMatched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotMatched := tt.account.ResolveAnthropicOAuthMappedModel(tt.model)
			require.Equal(t, tt.wantModel, gotModel)
			require.Equal(t, tt.wantMatched, gotMatched)
		})
	}
}

func TestAnthropicOAuthModelMapping_RateLimitAndChannelKeysUseUpstreamModel(t *testing.T) {
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-sonnet-4-5-20250929": "claude-opus-4-6"})

	// 限流键与渠道 upstream 限制检查都必须落在实际转发的上游模型上，而不是原始请求模型。
	require.Equal(t, []string{"claude-opus-4-6"}, account.modelRateLimitKeysForRequest(context.Background(), "claude-sonnet-4-5"))
	require.Equal(t, "claude-opus-4-6", resolveAccountUpstreamModel(account, "claude-sonnet-4-5"))

	// 未配置映射时保持原有行为。
	plain := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
	require.Equal(t, []string{"claude-sonnet-4-5"}, plain.modelRateLimitKeysForRequest(context.Background(), "claude-sonnet-4-5"))
	require.Equal(t, "claude-sonnet-4-5", resolveAccountUpstreamModel(plain, "claude-sonnet-4-5"))
}

func TestGatewayService_Forward_AnthropicOAuthModelMappingAppliesBeforeMimicry(t *testing.T) {
	c, rec := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-opus-4-6","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	const upstreamModel = "claude-haiku-4-5-20251001"
	upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse(upstreamModel)}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	// 映射目标写成短 ID：转发前统一标准化为带日期的原生 ID。
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-4-6": "claude-haiku-4-5"})

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)

	require.Equal(t, upstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
	// 伪装 beta 组合按模型分支（haiku 的 claude-code beta 挪到末尾），必须按最终上游模型计算。
	haikuBetas := mergeAnthropicBetaDropping(claude.ClaudeCodeMimicryBetas(upstreamModel, false), "", defaultDroppedBetasSet)
	opusBetas := mergeAnthropicBetaDropping(claude.ClaudeCodeMimicryBetas("claude-opus-4-6", false), "", defaultDroppedBetasSet)
	require.NotEqual(t, opusBetas, haikuBetas)
	require.Equal(t, haikuBetas, getHeaderRaw(upstream.lastReq.Header, "anthropic-beta"))

	// 计费/日志使用原始请求模型，UpstreamModel 记录实际上游模型，响应回写原始模型名。
	require.Equal(t, "claude-opus-4-6", result.Model)
	require.Equal(t, upstreamModel, result.UpstreamModel)
	require.Equal(t, "claude-opus-4-6", gjson.GetBytes(rec.Body.Bytes(), "model").String())
}

func TestGatewayService_Forward_AnthropicOAuthModelMappingDrivesMimicryNormalization(t *testing.T) {
	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-6","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse("claude-opus-5-5")}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	account := newAnthropicOAuthMappingAccount(AccountTypeSetupToken, map[string]any{"claude-sonnet-4-6": "claude-opus-5-5"})

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
	// 伪装补齐 temperature 时按模型分支（Opus 5.5 不补）：必须看到映射后的上游模型。
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.Equal(t, "claude-sonnet-4-6", result.Model)
	require.Equal(t, "claude-opus-5-5", result.UpstreamModel)
}

func TestGatewayService_Forward_AnthropicOAuthModelMappingValidatesOpus55Target(t *testing.T) {
	c, rec := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"public-opus","max_tokens":64,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"public-opus": "claude-opus-5-5"})

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Error(t, err)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Nil(t, upstream.lastReq)
}

func TestGatewayService_Forward_AnthropicOAuthModelMappingMatchesNormalizedRequestID(t *testing.T) {
	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse("claude-opus-4-6")}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	// 映射键按带日期的长 ID 书写，短 ID 请求经标准化后同样命中（与调度阶段的判定一致）。
	account := newAnthropicOAuthMappingAccount(AccountTypeSetupToken, map[string]any{"claude-sonnet-4-5-20250929": "claude-opus-4-6"})

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "claude-sonnet-4-5", result.Model)
	require.Equal(t, "claude-opus-4-6", result.UpstreamModel)
}

func TestGatewayService_Forward_AnthropicOAuthModelMappingAppliesToClaudeCodeClients(t *testing.T) {
	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-opus-4-6","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse("claude-sonnet-4-6")}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-4-6": "claude-sonnet-4-6"})

	// 真实 Claude Code 客户端不走伪装改写，账号级映射同样必须生效。
	result, err := svc.Forward(SetClaudeCodeClient(context.Background(), true), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "claude-opus-4-6", result.Model)
	require.Equal(t, "claude-sonnet-4-6", result.UpstreamModel)
}

func TestGatewayService_Forward_AnthropicOAuthWithoutModelMappingKeepsNativeNormalization(t *testing.T) {
	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse("claude-sonnet-4-5-20250929")}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5-20250929", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "temperature").Int())
	require.Equal(t, "claude-sonnet-4-5", result.Model)
	require.Equal(t, "claude-sonnet-4-5-20250929", result.UpstreamModel)
}

func TestGatewayService_ForwardCountTokens_AnthropicOAuthModelMapping(t *testing.T) {
	c, rec := newAnthropicOAuthMappingTestContext("/v1/messages/count_tokens")
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-opus-4-6","messages":[{"role":"user","content":"hi"}]}`)

	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"input_tokens":7}`)),
	}}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"claude-opus-4-6": "claude-haiku-4-5"})

	err := svc.ForwardCountTokens(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, int64(7), gjson.GetBytes(rec.Body.Bytes(), "input_tokens").Int())
}

func TestGatewayService_ForwardCompat_AnthropicOAuthModelMapping(t *testing.T) {
	for _, chat := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		body := `{"model":"public-opus","input":"hello","reasoning":{"effort":"xhigh"}}`
		path := "/v1/responses"
		if chat {
			body = `{"model":"public-opus","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"xhigh"}`
			path = "/v1/chat/completions"
		}
		c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(namespaceToolAnthropicStream()))}}
		svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, map[string]any{"public-opus": "claude-opus-5-5"})

		var result *ForwardResult
		var err error
		if chat {
			result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, []byte(body), nil)
		} else {
			result, err = svc.ForwardAsResponses(context.Background(), c, account, []byte(body), nil)
		}
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotNil(t, upstream.lastReq)
		require.Equal(t, "claude-opus-5-5", gjson.GetBytes(upstream.lastBody, "model").String())
		// OAuth 伪装同样按映射后的上游模型处理（Opus 5.5 不补 temperature）。
		require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
		require.Equal(t, "public-opus", result.Model)
		require.Equal(t, "claude-opus-5-5", result.UpstreamModel)
	}
}
