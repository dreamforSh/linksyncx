//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type perplexityIngressUpstream struct {
	HTTPUpstream
	t        *testing.T
	bodies   [][]byte
	requests []*http.Request
	proxies  []string
	respond  func(int, *http.Request) *http.Response
}

func (u *perplexityIngressUpstream) DoWithTLS(req *http.Request, proxy string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.t.Helper()
	require.NotNil(u.t, profile)
	body, err := io.ReadAll(req.Body)
	require.NoError(u.t, err)
	u.bodies = append(u.bodies, body)
	u.requests = append(u.requests, req)
	u.proxies = append(u.proxies, proxy)
	return u.respond(len(u.requests)-1, req), nil
}

func perplexityIngressService(t *testing.T, respond func(int, *http.Request) *http.Response) (*OpenAIGatewayService, *perplexityIngressUpstream, *Account) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := &perplexityIngressUpstream{t: t, respond: respond}
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 4 * 1024 * 1024}}, httpUpstream: upstream}
	account := &Account{ID: 41, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"api_key": "fake-session", "csrf_token": "fake-csrf", "base_url": "https://perplexity.fixture.invalid",
	}}
	return svc, upstream, account
}

func perplexityIngressContext(t *testing.T, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)
	c.Set("api_key", &APIKey{ID: 91})
	return c, recorder
}

func perplexityIngressAnswer(answer, backend string, completed bool, sources bool) string {
	frame := map[string]any{
		"backend_uuid": backend,
		"blocks":       []any{map[string]any{"intended_usage": "ask_text", "markdown_block": map[string]any{"answer": answer}}},
	}
	if sources {
		frame["web_results"] = []any{map[string]any{"url": "https://example.org/reference", "name": "Reference"}}
	}
	data, _ := json.Marshal(frame)
	body := "event: message\ndata: " + string(data) + "\n\n"
	if completed {
		body += "event: end_of_stream\n\n"
	}
	return body
}

func TestPerplexityIngressOptionsAndStreamFailure(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, complete := range []bool{false, true} {
				name := protocol + map[bool]string{false: "/buffer", true: "/stream"}[stream] + map[bool]string{false: "/truncated", true: "/complete"}[complete]
				t.Run(name, func(t *testing.T) {
					svc, upstream, account := perplexityIngressService(t, func(_ int, _ *http.Request) *http.Response {
						return perplexityAttachmentResponse(http.StatusOK, perplexityIngressAnswer("array[0] remains unchanged", uuid.NewString(), complete, true))
					})
					bodyObject := map[string]any{"model": "sonar-pro", "stream": stream, "perplexity": map[string]any{
						"sources": []string{"scholar"}, "language": "zh-CN", "timezone": "Asia/Shanghai", "is_incognito": true,
					}}
					switch protocol {
					case "responses":
						bodyObject["input"] = "question"
					case "messages":
						bodyObject["max_tokens"] = 64
						bodyObject["messages"] = []any{map[string]any{"role": "user", "content": "question"}}
					default:
						bodyObject["messages"] = []any{map[string]any{"role": "user", "content": "question"}}
					}
					body, err := json.Marshal(bodyObject)
					require.NoError(t, err)
					c, recorder := perplexityIngressContext(t, body)
					var result *OpenAIForwardResult
					switch protocol {
					case "responses":
						result, err = svc.forwardPerplexityResponses(context.Background(), c, account, body, "sonar-pro", stream, time.Now())
					case "messages":
						result, err = svc.forwardPerplexityMessages(context.Background(), c, account, body, "")
					default:
						result, err = svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
					}
					require.Len(t, upstream.bodies, 1)
					request := gjson.ParseBytes(upstream.bodies[0])
					require.Equal(t, "scholar", request.Get("params.sources.0").String())
					require.Equal(t, "zh-CN", request.Get("params.language").String())
					require.Equal(t, "Asia/Shanghai", request.Get("params.timezone").String())
					require.True(t, request.Get("params.is_incognito").Bool())
					require.Equal(t, "true", recorder.Header().Get("X-Perplexity-Usage-Estimated"))
					wire := recorder.Body.String()
					if complete {
						require.NoError(t, err)
						require.NotNil(t, result)
						require.True(t, result.UsageEstimated)
						require.False(t, result.UsageIncomplete)
						require.Contains(t, wire, "array[0]")
						require.Contains(t, wire, "https://example.org/reference")
						require.NotContains(t, wire, "tool_calls")
						return
					}
					require.Error(t, err)
					require.NotContains(t, wire, `"finish_reason":"stop"`)
					require.NotContains(t, wire, `"type":"response.completed"`)
					require.NotContains(t, wire, "event: message_stop")
					require.NotContains(t, wire, "data: [DONE]")
					if stream {
						require.Contains(t, wire, "array[0]")
						if protocol == "responses" {
							require.Equal(t, 1, strings.Count(wire, "event: response.failed"))
						}
						if protocol == "messages" {
							require.Equal(t, 1, strings.Count(wire, "event: error"))
						}
					}
				})
			}
		}
	}
}

func TestPerplexityNativeContinuationAndRejectedHandleReplay(t *testing.T) {
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "rejected_handle"}[rejected], func(t *testing.T) {
			firstUUID := uuid.NewString()
			svc, upstream, account := perplexityIngressService(t, func(index int, _ *http.Request) *http.Response {
				if index == 0 {
					return perplexityAttachmentResponse(http.StatusOK, perplexityIngressAnswer("first answer", firstUUID, true, false))
				}
				if rejected && index == 1 {
					return perplexityAttachmentResponse(http.StatusBadRequest, `{"error":{"message":"invalid last_backend_uuid"}}`)
				}
				return perplexityAttachmentResponse(http.StatusOK, perplexityIngressAnswer("second answer", uuid.NewString(), true, false))
			})
			first := []byte(`{"model":"sonar-pro","messages":[{"role":"user","content":"first question"}]}`)
			c, _ := perplexityIngressContext(t, first)
			_, err := svc.forwardPerplexityChatCompletions(context.Background(), c, account, first, "", "")
			require.NoError(t, err)
			second := []byte(`{"model":"sonar-pro","messages":[{"role":"user","content":"first question"},{"role":"assistant","content":"first answer"},{"role":"user","content":"second question"}]}`)
			c, _ = perplexityIngressContext(t, second)
			result, err := svc.forwardPerplexityChatCompletions(context.Background(), c, account, second, "", "")
			require.NoError(t, err)
			require.True(t, result.UsageEstimated)
			require.Equal(t, firstUUID, gjson.GetBytes(upstream.bodies[1], "params.last_backend_uuid").String())
			require.Equal(t, "second question", gjson.GetBytes(upstream.bodies[1], "query_str").String())
			require.Equal(t, gjson.GetBytes(upstream.bodies[0], "params.frontend_context_uuid").String(), gjson.GetBytes(upstream.bodies[1], "params.frontend_context_uuid").String())
			if rejected {
				require.Len(t, upstream.bodies, 3)
				require.Equal(t, gjson.Null, gjson.GetBytes(upstream.bodies[2], "params.last_backend_uuid").Type)
				require.Contains(t, gjson.GetBytes(upstream.bodies[2], "query_str").String(), "first question")
				require.Contains(t, gjson.GetBytes(upstream.bodies[2], "query_str").String(), "first answer")
			} else {
				require.Len(t, upstream.bodies, 2)
			}
		})
	}
}

func TestPerplexityInvalidContinuationClassification(t *testing.T) {
	require.True(t, perplexityContinuationRejected(400, []byte(`{"error":{"message":"invalid backend_uuid"}}`)))
	require.True(t, perplexityContinuationRejected(404, []byte(`{"error":{"code":"backend_uuid_not_found"}}`)))
	for _, status := range []int{401, 403, 429, 500} {
		require.False(t, perplexityContinuationRejected(status, []byte(`{"error":{"message":"invalid backend_uuid"}}`)))
	}
	require.False(t, perplexityContinuationRejected(400, []byte(`{"query_str":"invalid backend_uuid","error":{"message":"invalid model"}}`)))
	require.False(t, perplexityContinuationRejected(400, []byte(`<html>invalid backend_uuid</html>`)))
}

func TestPerplexityBodyCloseCancelsAndClosesUpstream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	body := &perplexityCCBody{reader: reader, upstream: io.NopCloser(strings.NewReader("")), cancel: cancel}
	require.NoError(t, body.Close())
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.NoError(t, body.Close())
}
