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
