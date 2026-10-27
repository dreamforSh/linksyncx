//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func codeBuddyTestToken(issuer string) string {
	payload, _ := json.Marshal(map[string]any{"iss": issuer, "sub": "u-1"})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func codeBuddyTestAccount(profile string) *Account {
	issuer := "https://copilot.tencent.com/auth/realms/copilot"
	domain := "www.codebuddy.cn"
	if profile == codebuddy.ProfileIntlWork {
		issuer = "https://www.workbuddy.ai/auth/realms/copilot"
		domain = "www.workbuddy.ai"
	}
	return &Account{
		ID:          77,
		Name:        "codebuddy",
		Platform:    PlatformCodeBuddy,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":  codeBuddyTestToken(issuer),
			"refresh_token": "rt",
			"expires_at":    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			"domain":        domain,
			"uid":           "u-1",
			"profile":       profile,
		},
	}
}

func codeBuddySSE(chunks ...string) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString("data: " + chunk + "\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func newCodeBuddyTestContext(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "my-client/1.0")
	return c, rec
}

func TestCodeBuddyChatCompletionsNonStreamAggregatesUpstreamSSE(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","stream":false,"store":true,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(codeBuddySSE(
			`{"id":"c1","model":"glm-5.2","choices":[{"index":0,"delta":{"content":"hel"}}]}`,
			`{"id":"c1","model":"glm-5.2","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			`{"id":"c1","model":"glm-5.2","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
		))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 7, result.Usage.InputTokens)
	require.Equal(t, 2, result.Usage.OutputTokens)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "hello", gjson.Get(rec.Body.String(), "choices.0.message.content").String())
	require.Equal(t, "chat.completion", gjson.Get(rec.Body.String(), "object").String())

	req := upstream.lastReq
	require.Equal(t, "https://copilot.tencent.com/v2/chat/completions", req.URL.String())
	require.Equal(t, codebuddy.CLIUserAgent, req.Header.Get("User-Agent"), "client UA must not leak upstream")
	require.Equal(t, "u-1", req.Header.Get("X-User-Id"))
	require.Equal(t, "www.codebuddy.cn", req.Header.Get("X-Domain"))
	require.NotEmpty(t, req.Header.Get("X-Conversation-ID"))
	require.NotEmpty(t, req.Header.Get("X-Request-ID"))
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool(), "upstream is stream-only")
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream_options.include_usage").Bool())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
}

func TestCodeBuddyConversationIDStableAcrossTurns(t *testing.T) {
	first := []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"q1"}]}`)
	second := []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"q1"},{"role":"assistant","content":"a"},{"role":"user","content":"q2"}]}`)
	var ids []string
	for _, body := range [][]byte{first, second} {
		c, _ := newCodeBuddyTestContext("/v1/chat/completions", body)
		upstream := &httpUpstreamRecorder{resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(codeBuddySSE(`{"id":"c","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`))),
		}}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		_, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
		require.NoError(t, err)
		ids = append(ids, upstream.lastReq.Header.Get("X-Conversation-ID"))
	}
	require.Equal(t, ids[0], ids[1])
}

func TestCodeBuddyMessagesNonStreamIntlMapsAutoModel(t *testing.T) {
	body := []byte(`{"model":"auto","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(codeBuddySSE(
			`{"id":"c1","model":"deepseek-v4","choices":[{"index":0,"delta":{"content":"hey"},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		))),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.ForwardAsAnthropic(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileIntlWork), body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "https://www.workbuddy.ai/v2/chat/completions", upstream.lastReq.URL.String())
	require.Equal(t, codebuddy.IntlAutoModel, gjson.GetBytes(upstream.lastBody, "model").String())
	require.Contains(t, upstream.lastReq.Header.Get("User-Agent"), "WorkBuddy AI/")
	require.Equal(t, "message", gjson.Get(rec.Body.String(), "type").String())
	require.Equal(t, "hey", gjson.Get(rec.Body.String(), "content.0.text").String())
}

func TestCodeBuddyRateLimitFailsOver(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c, _ := newCodeBuddyTestContext("/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":429,"msg":"limit, resets 2099-01-01 00:00:00 UTC+8"}`)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
	var failover *UpstreamFailoverError
	require.True(t, errors.As(err, &failover), "429 must fail over: %v", err)
	require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
}

func TestCodeBuddyContentFilterDoesNotFailOver(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(`data: {"error":{"code":"content_filter","message":"blocked"}}` + "\n\n")),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestCodeBuddyApplyRefreshPersistsAccountType verifies the refresh flow writes
// the subscription tier when the rotation carries one, and preserves the stored
// tier when it does not.
func TestCodeBuddyApplyRefreshPersistsAccountType(t *testing.T) {
	svc := &CodeBuddyOAuthService{}
	account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
	require.NotContains(t, account.Credentials, codebuddy.KeyAccountType, "test fixture should start without a tier")

	// Refresh carries the tier (echoed or backfilled) → persisted.
	creds := svc.ApplyRefresh(account, &codebuddy.RefreshResult{
		AccessToken: "new", RefreshToken: "rt2", AccountType: "ultimate", EnterpriseID: "ent",
	})
	require.Equal(t, "new", creds[codebuddy.KeyAccessToken])
	require.Equal(t, "ultimate", creds[codebuddy.KeyAccountType])
	require.Equal(t, "ent", creds[codebuddy.KeyEnterpriseID])

	// A later refresh without a tier must keep the stored one, not blank it.
	account.Credentials[codebuddy.KeyAccountType] = "ultimate"
	creds = svc.ApplyRefresh(account, &codebuddy.RefreshResult{AccessToken: "n2"})
	require.Equal(t, "ultimate", creds[codebuddy.KeyAccountType])
}

// TestCodeBuddyUpstream400SurfacesCodeAndMsg locks in that a plain upstream 400
// carrying only CodeBuddy's {code, msg} envelope reaches the client with the real
// reason, not the opaque "Upstream error: 400" the generic extractor produced.
func TestCodeBuddyUpstream400SurfacesCodeAndMsg(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":40002,"msg":"invalid tool schema"}`)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
	require.Error(t, err)
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover), "a plain 400 must not fail over: %v", err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid tool schema (code 40002)", gjson.Get(rec.Body.String(), "error.message").String())
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
}

// TestCodeBuddyMessagesUpstream400SurfacesMsg is the Claude Code /v1/messages
// counterpart: the same envelope must surface through the Anthropic error format.
func TestCodeBuddyMessagesUpstream400SurfacesMsg(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/messages", body)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"code":"40003","msg":"messages malformed"}`)),
	}}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	_, err := svc.ForwardAsAnthropic(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "error", gjson.Get(rec.Body.String(), "type").String())
	require.Equal(t, "messages malformed (code 40003)", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestCodeBuddyCreditPolicyBlocksPricedModel(t *testing.T) {
	body := []byte(`{"model":"kimi","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyTestContext("/v1/chat/completions", body)
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
	account.Credentials["credit_policy"] = "zero_only"
	account.Extra = map[string]any{codeBuddyCatalogExtraKey: CodeBuddyCatalogSnapshot{Models: []codebuddy.CatalogModel{
		{ID: "kimi", Credits: "x1"}, {ID: "glm-5.2", Credits: "x0"},
	}}}

	_, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Nil(t, upstream.lastReq, "denied requests never reach upstream")
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestCodeBuddyAvailableModels(t *testing.T) {
	account := codeBuddyTestAccount(codebuddy.ProfileIntlCLI)
	require.Equal(t, codebuddy.DefaultModelIDs, codeBuddyIDs(account.CodeBuddyAvailableModels()))

	account.Extra = map[string]any{codeBuddyCatalogExtraKey: map[string]any{"models": []any{
		map[string]any{"id": "default-model", "credits": "x0"},
		map[string]any{"id": "kimi", "credits": "x2"},
	}}}
	require.Equal(t, []string{"auto", "kimi"}, codeBuddyIDs(account.CodeBuddyAvailableModels()))
	account.Credentials["credit_policy"] = "zero_only"
	require.Equal(t, []string{"auto"}, codeBuddyIDs(account.CodeBuddyAvailableModels()))
}

func codeBuddyIDs(models []codebuddy.CatalogModel) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.ID)
	}
	return out
}

func codeBuddyDeepSeekUpstream() *httpUpstreamRecorder {
	return &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(codeBuddySSE(
			`{"id":"c1","model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"{\"a\":\"x\"}"},"finish_reason":"stop"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		))),
	}}
}

func TestCodeBuddyResponsesDeepSeekCarriesTextFormatAsInstruction(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","stream":false,"input":"hi","text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}}}`)
	c, rec := newCodeBuddyTestContext("/v1/responses", body)
	upstream := codeBuddyDeepSeekUpstream()
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

	result, err := svc.Forward(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, gjson.GetBytes(upstream.lastBody, "response_format").Exists(), "DeepSeek rejects response_format")
	require.Equal(t, "max", gjson.GetBytes(upstream.lastBody, "reasoning_effort").String(), "no effort named runs max")
	require.Equal(t, "system", gjson.GetBytes(upstream.lastBody, "messages.0.role").String())
	require.Contains(t, gjson.GetBytes(upstream.lastBody, "messages.0.content").String(), `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`)
	require.NotNil(t, result.ReasoningEffort)
	require.Equal(t, "max", *result.ReasoningEffort)
}

func TestCodeBuddyMessagesDeepSeekReasoningEffort(t *testing.T) {
	cases := []struct {
		name  string
		extra string
		want  string
	}{
		{name: "no effort named runs max", want: "max"},
		{name: "low clamps to high", extra: `"output_config":{"effort":"low"},`, want: "high"},
		{name: "max stays max", extra: `"output_config":{"effort":"max"},`, want: "max"},
		{name: "thinking disabled omits effort", extra: `"thinking":{"type":"disabled"},`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{` + tc.extra + `"model":"deepseek-v4-pro","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
			c, _ := newCodeBuddyTestContext("/v1/messages", body)
			upstream := codeBuddyDeepSeekUpstream()
			svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}

			result, err := svc.ForwardAsAnthropic(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")
			require.NoError(t, err)
			effort := gjson.GetBytes(upstream.lastBody, "reasoning_effort")
			if tc.want == "" {
				require.False(t, effort.Exists())
				require.Nil(t, result.ReasoningEffort)
				return
			}
			require.Equal(t, tc.want, effort.String())
			require.NotNil(t, result.ReasoningEffort)
			require.Equal(t, tc.want, *result.ReasoningEffort)
		})
	}
}

func TestCodeBuddyDeepSeekModelMetadata(t *testing.T) {
	account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
	metadata, ok := account.GetUpstreamModelMetadata("deepseek-v4-pro")
	require.True(t, ok)
	require.Equal(t, int64(1_000_000), metadata.ContextWindow)
	require.Equal(t, int64(1_000_000), metadata.MaxContextWindow)
	require.NotNil(t, metadata.Reasoning)
	require.True(t, *metadata.Reasoning)
	require.Equal(t, "max", metadata.DefaultReasoningLevel)
	require.Equal(t, []string{"high", "max"}, metadata.SupportedReasoningLevels)
	require.Equal(t, []string{"text"}, metadata.InputModalities)

	_, ok = account.GetUpstreamModelMetadata("glm-5.2")
	require.False(t, ok, "only DeepSeek gets synthesized metadata")

	account.Extra = map[string]any{codeBuddyCatalogExtraKey: CodeBuddyCatalogSnapshot{Models: []codebuddy.CatalogModel{{ID: "deepseek-v4-pro", SupportsImages: true}}}}
	metadata, ok = account.GetUpstreamModelMetadata("deepseek-v4-pro")
	require.True(t, ok)
	require.Equal(t, []string{"text", "image"}, metadata.InputModalities)

	descriptor := newConfiguredCodexModelDescriptor("deepseek-v4-pro")
	applyUpstreamModelMetadataToCodexDescriptor(&descriptor, codexModelMetadataOverride{UpstreamModelMetadata: metadata})
	require.NotNil(t, descriptor.DefaultReasoningLevel)
	require.Equal(t, "max", *descriptor.DefaultReasoningLevel)
	require.Len(t, descriptor.SupportedReasoningLevels, 2)
	require.Equal(t, int64(1_000_000), descriptor.ContextWindow)
}
