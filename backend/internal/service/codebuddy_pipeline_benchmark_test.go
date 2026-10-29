//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

// codeBuddyBenchUpstream answers every request with a short SSE stream and
// records how long the gateway took from the start of Forward to the upstream
// call: that span is pure gateway work sitting in front of the first token.
type codeBuddyBenchUpstream struct {
	sse     string
	started time.Time
	toDo    time.Duration
	calls   int
}

func (u *codeBuddyBenchUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.toDo += time.Since(u.started)
	u.calls++
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(u.sse)),
	}, nil
}

func (u *codeBuddyBenchUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func codeBuddyBenchSSE() string {
	return codeBuddySSE(
		`{"id":"c1","model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`{"id":"c1","model":"glm-5.2","choices":[{"index":0,"delta":{"content":"ok"}}]}`,
		`{"id":"c1","model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","model":"glm-5.2","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`,
	)
}

var codeBuddyBenchSizes = []struct {
	name  string
	bytes int
}{
	{name: "64KB", bytes: 64 << 10},
	{name: "512KB", bytes: 512 << 10},
	{name: "2MB", bytes: 2 << 20},
}

// BenchmarkCodeBuddyForward measures each inbound protocol end to end against
// a stub upstream and reports the gateway time spent before the upstream call
// (upstream-ms) next to the whole request.
func BenchmarkCodeBuddyForward(b *testing.B) {
	gin.SetMode(gin.TestMode)
	entries := []struct {
		name    string
		path    string
		build   func(int) []byte
		forward func(*OpenAIGatewayService, *gin.Context, *Account, []byte) error
	}{
		{name: "chat", path: "/v1/chat/completions", build: buildCodeBuddyBenchChatBody, forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
			_, err := s.ForwardAsChatCompletions(context.Background(), c, a, body, "", "")
			return err
		}},
		{name: "messages", path: "/v1/messages", build: buildCodeBuddyBenchMessagesBody, forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
			_, err := s.ForwardAsAnthropic(context.Background(), c, a, body, "", "")
			return err
		}},
		{name: "responses", path: "/v1/responses", build: buildCodeBuddyBenchResponsesBody, forward: func(s *OpenAIGatewayService, c *gin.Context, a *Account, body []byte) error {
			_, err := s.Forward(context.Background(), c, a, body)
			return err
		}},
	}
	for _, entry := range entries {
		for _, size := range codeBuddyBenchSizes {
			b.Run(entry.name+"/"+size.name, func(b *testing.B) {
				body := entry.build(size.bytes)
				upstream := &codeBuddyBenchUpstream{sse: codeBuddyBenchSSE()}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
				account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
				b.SetBytes(int64(len(body)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, entry.path, bytes.NewReader(nil))
					c.Request.Header.Set("Content-Type", "application/json")
					upstream.started = time.Now()
					if err := entry.forward(svc, c, account, body); err != nil {
						b.Fatalf("forward: %v", err)
					}
				}
				b.StopTimer()
				if upstream.calls != b.N {
					b.Fatalf("upstream calls = %d, want %d", upstream.calls, b.N)
				}
				b.ReportMetric(float64(upstream.toDo.Microseconds())/1000/float64(b.N), "upstream-ms")
			})
		}
	}
}

// BenchmarkCodeBuddyPrepareChatBody isolates the CodeBuddy body rewrite.
func BenchmarkCodeBuddyPrepareChatBody(b *testing.B) {
	for _, size := range codeBuddyBenchSizes {
		b.Run(size.name, func(b *testing.B) {
			body := buildCodeBuddyBenchChatBody(size.bytes)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				prepared, err := codebuddy.PrepareChatBody(body, codebuddy.ProfileCNCLI)
				if err != nil {
					b.Fatalf("prepare: %v", err)
				}
				benchmarkIntSink = len(prepared)
			}
		})
	}
}

const codeBuddyBenchToolSchema = `{"type":"object","properties":{"path":{"type":"string","description":"Path relative to the workspace root"},"content":{"type":"string"},"recursive":{"type":"boolean"}},"required":["path"]}`

// buildCodeBuddyBenchChatBody builds an agent-client Chat Completions turn:
// a long system prompt, a replayed tool loop, then tools and sampling options
// after the history, the order the OpenAI SDKs emit them in.
func buildCodeBuddyBenchChatBody(targetBytes int) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 64<<10)
	_, _ = builder.WriteString(`{"model":"glm-5.2","messages":[{"role":"system","content":"`)
	_, _ = builder.WriteString(strings.Repeat(`You are a coding agent working in the user's repository. Follow the \"conventions\" section, keep edits minimal and run the tests.\n`, 90))
	_, _ = builder.WriteString(`"}`)
	for i := 0; builder.Len() < targetBytes; i++ {
		id := strconv.Itoa(i)
		_, _ = builder.WriteString(`,{"role":"user","content":"` + strings.Repeat("Please fix the failing test in module "+id+" and explain the change. ", 6) + `"}`)
		_, _ = builder.WriteString(`,{"role":"assistant","content":null,"tool_calls":[{"id":"call_` + id + `","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"internal/module` + id + `/handler.go\"}"}}]}`)
		_, _ = builder.WriteString(`,{"role":"tool","tool_call_id":"call_` + id + `","content":"` + strings.Repeat(`func handle`+id+`(w http.ResponseWriter, r *http.Request) {\n\tif err := decode(r); err != nil {\n\t\thttp.Error(w, \"bad request\", 400)\n\t}\n}\n`, 14) + `"}`)
		_, _ = builder.WriteString(`,{"role":"assistant","content":"` + strings.Repeat("The handler now validates input before decoding. ", 5) + `"}`)
	}
	_, _ = builder.WriteString(`],"tools":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"type":"function","function":{"name":"tool_` + strconv.Itoa(i) + `","description":"` + strings.Repeat("Operate on workspace files. ", 8) + `","parameters":` + codeBuddyBenchToolSchema + `}}`)
	}
	_, _ = builder.WriteString(`],"tool_choice":"auto","temperature":0.2,"max_tokens":8192,"stream":true,"stream_options":{"include_usage":true}}`)
	return []byte(builder.String())
}

// buildCodeBuddyBenchMessagesBody builds a Claude Code turn: field order model,
// messages, system, tools, metadata, max_tokens, thinking, stream.
func buildCodeBuddyBenchMessagesBody(targetBytes int) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 64<<10)
	_, _ = builder.WriteString(`{"model":"glm-5.2","messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context.\n</system-reminder>"},{"type":"text","text":"fix the failing tests"}]}`)
	for i := 0; builder.Len() < targetBytes; i++ {
		id := strconv.Itoa(i)
		_, _ = builder.WriteString(`,{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("Let me look at module "+id+". ", 6) + `"},{"type":"tool_use","id":"toolu_` + id + `","name":"Read","input":{"file_path":"/repo/internal/module` + id + `/handler.go"}}]}`)
		_, _ = builder.WriteString(`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_` + id + `","content":"` + strings.Repeat(`     1\tfunc handle`+id+`(w http.ResponseWriter, r *http.Request) {\n     2\t\tif err := decode(r); err != nil {\n     3\t\t\thttp.Error(w, \"bad request\", 400)\n`, 14) + `"}]}`)
	}
	_, _ = builder.WriteString(`],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.37.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"`)
	_, _ = builder.WriteString(strings.Repeat(`You are an interactive CLI tool that helps users with software engineering tasks. Use the instructions below and the tools available to you.\n`, 70))
	_, _ = builder.WriteString(`","cache_control":{"type":"ephemeral"}}],"tools":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"name":"Tool` + strconv.Itoa(i) + `","description":"` + strings.Repeat("Operate on workspace files. ", 8) + `","input_schema":` + codeBuddyBenchToolSchema + `}`)
	}
	_, _ = builder.WriteString(`],"metadata":{"user_id":"user_0123456789abcdef_account__session_0f8e7d6c-1111-2222-3333-444455556666"},"max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":31999},"stream":true}`)
	return []byte(builder.String())
}

// buildCodeBuddyBenchResponsesBody builds a Codex turn with replayed reasoning
// and tool output.
func buildCodeBuddyBenchResponsesBody(targetBytes int) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 64<<10)
	_, _ = builder.WriteString(`{"model":"glm-5.2","instructions":"`)
	_, _ = builder.WriteString(strings.Repeat("You are Codex, a coding agent. Follow the repository conventions. ", 150))
	_, _ = builder.WriteString(`","input":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		id := strconv.Itoa(i)
		_, _ = builder.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("please fix the failing test in module "+id+". ", 8) + `"}]},`)
		_, _ = builder.WriteString(`{"type":"function_call","call_id":"call_` + id + `","name":"shell","arguments":"{\"command\":[\"go\",\"test\",\"./...\"]}"},`)
		_, _ = builder.WriteString(`{"type":"function_call_output","call_id":"call_` + id + `","output":"` + strings.Repeat(`ok  \tgithub.com/x/y\t0.12s\n`, 30) + `"},`)
		_, _ = builder.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat("Done with step "+id+". ", 10) + `"}]}`)
	}
	_, _ = builder.WriteString(`],"tools":[`)
	for i := 0; i < 20; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"type":"function","name":"tool_` + strconv.Itoa(i) + `","description":"` + strings.Repeat("Run a tool. ", 20) + `","parameters":` + codeBuddyBenchToolSchema + `}`)
	}
	_, _ = builder.WriteString(`],"tool_choice":"auto","parallel_tool_calls":true,"reasoning":{"effort":"medium","summary":"auto"},"store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"019a1f2e-1111-7222-8333-944455556666"}`)
	return []byte(builder.String())
}

// codeBuddyBenchStreamBody is an upstream stream of n content chunks shaped
// like CodeBuddy's: a role chunk, content deltas, a finish chunk, then usage.
func codeBuddyBenchStreamBody(n int) string {
	var b strings.Builder
	_, _ = b.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1761700000,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}` + "\n\n")
	for i := 0; i < n; i++ {
		_, _ = b.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1761700000,"model":"glm-5.2","choices":[{"index":0,"delta":{"content":"token` + strconv.Itoa(i) + ` "},"finish_reason":null}]}` + "\n\n")
	}
	_, _ = b.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1761700000,"model":"glm-5.2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n")
	_, _ = b.WriteString(`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1761700000,"model":"glm-5.2","choices":[],"usage":{"prompt_tokens":70000,"completion_tokens":1000,"total_tokens":71000}}` + "\n\n")
	_, _ = b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// BenchmarkCodeBuddyStreamRelay measures the per-event relay cost of the two
// CodeBuddy stream paths once the upstream answered (ns/op is per event).
func BenchmarkCodeBuddyStreamRelay(b *testing.B) {
	gin.SetMode(gin.TestMode)
	const events = 1000
	stream := codeBuddyBenchStreamBody(events)
	account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
	newResp := func() *http.Response {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(stream)),
		}
	}
	newCtx := func(path string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(nil))
		return c
	}
	b.Run("chat", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i += events {
			c := newCtx("/v1/chat/completions")
			if _, err := svc.streamRawChatCompletions(c, newResp(), account, "glm-5.2", "glm-5.2", "glm-5.2", nil, nil, time.Now(), 128<<10); err != nil {
				b.Fatalf("stream: %v", err)
			}
		}
	})
	b.Run("messages", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i += events {
			c := newCtx("/v1/messages")
			if _, err := svc.streamChatCompletionsAsAnthropic(c, newResp(), "glm-5.2", "glm-5.2", "glm-5.2", nil, nil, time.Now()); err != nil {
				b.Fatalf("stream: %v", err)
			}
		}
	})
}
