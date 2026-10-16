package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func translatePerplexityFixture(t *testing.T, body string, p perplexityTranslateParams) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		translatePerplexitySSEToCC(ctx, io.NopCloser(strings.NewReader(body)), pw, p)
	}()
	out, err := io.ReadAll(pr)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("translator 未在期限内退出")
	}
	return string(out), err
}

func perplexityAnswerFixture(text string) string {
	payload, _ := json.Marshal(map[string]any{"blocks": []any{map[string]any{
		"intended_usage": "ask_text", "markdown_block": map[string]any{"answer": text},
	}}})
	return "data: " + string(payload) + "\n\n"
}

func perplexityCCContent(t *testing.T, out string) string {
	t.Helper()
	var content strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "data: {") {
			payload := strings.TrimPrefix(line, "data: ")
			require.True(t, gjson.Valid(payload))
			_, _ = content.WriteString(gjson.Get(payload, "choices.0.delta.content").String())
		}
	}
	return content.String()
}

func TestPerplexityTranslationFailureNeverCompletes(t *testing.T) {
	prefix := perplexityAnswerFixture("partial")
	for _, tc := range []struct {
		name, body string
		want       error
		partial    bool
	}{
		{"bad JSON before content", "data: {broken\n\n", perplexity.ErrMalformedEvent, false},
		{"non-object before content", "data: null\n\n", perplexity.ErrMalformedEvent, false},
		{"empty message", "data:\n\n", perplexity.ErrMalformedEvent, false},
		{"empty EOS", "event: end_of_stream\ndata:\n\n", perplexity.ErrEmptyAnswer, false},
		{"JSON error before content", "data: {\"error\":\"browser-secret\"}\n\n", perplexity.ErrUpstreamEvent, false},
		{"SSE error before content", "event: error\ndata: browser-secret\n\n", perplexity.ErrUpstreamEvent, false},
		{"truncated after content", prefix, perplexity.ErrIncompleteStream, true},
		{"JSON error after content", prefix + "data: {\"error\":\"browser-secret\"}\n\n", perplexity.ErrUpstreamEvent, true},
		{"SSE error after content", prefix + "event: error\ndata: browser-secret\n\n", perplexity.ErrUpstreamEvent, true},
		{"malformed after content", prefix + "data: {broken\n\n", perplexity.ErrMalformedEvent, true},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "/stream", false: "/buffer"}[stream], func(t *testing.T) {
				called := false
				out, err := translatePerplexityFixture(t, tc.body, perplexityTranslateParams{
					stream: stream, IncludeSources: true, onComplete: func(perplexityCompletion) { called = true },
				})
				require.ErrorIs(t, err, tc.want)
				require.False(t, called)
				require.NotContains(t, out, "[DONE]")
				require.NotContains(t, out, `"finish_reason":"stop"`)
				require.NotContains(t, out, "browser-secret")
				if tc.partial && stream {
					require.Equal(t, "partial", perplexityCCContent(t, out))
				} else {
					require.Empty(t, out, "首语义前失败不能泄漏 role 或半成品 JSON")
				}
			})
		}
	}
}

func TestPerplexityTranslationSnapshotRewrite(t *testing.T) {
	body := perplexityAnswerFixture("Hello world") + perplexityAnswerFixture("Hello there") + "event: end_of_stream\n\n"
	out, err := translatePerplexityFixture(t, body, perplexityTranslateParams{stream: true})
	require.ErrorIs(t, err, perplexity.ErrAnswerRewritten)
	require.Equal(t, "Hello world", perplexityCCContent(t, out))
	require.NotContains(t, out, "there")
	require.NotContains(t, out, "[DONE]")
	out, err = translatePerplexityFixture(t, body, perplexityTranslateParams{})
	require.NoError(t, err)
	require.Equal(t, "Hello there", gjson.Get(out, "choices.0.message.content").String())
	out, err = translatePerplexityFixture(t, perplexityAnswerFixture("stale")+perplexityAnswerFixture("")+"event: end_of_stream\n\n", perplexityTranslateParams{})
	require.ErrorIs(t, err, perplexity.ErrEmptyAnswer)
	require.Empty(t, out)
}

func TestPerplexityTranslationCompletionAndLocalUsage(t *testing.T) {
	const raw = "array[0] [1]"
	body := perplexityAnswerFixture(raw) + `data: {"backend_uuid":"thread-final","web_results":[{"url":"https://example.com/a","title":"A [title]"},{"url":"https://example.com/a#same","title":"Updated [title]"},{"url":"file:///secret","title":"bad"}]}` + "\n\nevent: end_of_stream\n\n"
	for _, stream := range []bool{false, true} {
		for _, includeSources := range []bool{false, true} {
			var completion perplexityCompletion
			calls := 0
			out, err := translatePerplexityFixture(t, body, perplexityTranslateParams{
				stream: stream, promptTokens: 7, IncludeSources: includeSources,
				onComplete: func(c perplexityCompletion) { completion = c; calls++ },
			})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, "thread-final", completion.BackendUUID)
			require.Len(t, completion.Sources, 1)
			want := raw
			if includeSources {
				want += perplexity.SourcesFooter(completion.Sources)
			}
			require.Equal(t, want, completion.Content)
			usagePayload := out
			if stream {
				require.Equal(t, want, perplexityCCContent(t, out))
				for _, line := range strings.Split(out, "\n") {
					if strings.HasPrefix(line, "data: {") && gjson.Get(strings.TrimPrefix(line, "data: "), "usage").Exists() {
						usagePayload = strings.TrimPrefix(line, "data: ")
					}
				}
			} else {
				require.Equal(t, want, gjson.Get(out, "choices.0.message.content").String())
			}
			require.Equal(t, int64(perplexity.EstimateTokens(raw)), gjson.Get(usagePayload, "usage.completion_tokens").Int(), "来源 footer 不能伪装成模型生成 token")
			require.True(t, gjson.Get(usagePayload, "perplexity.usage_estimated").Bool())
			require.Equal(t, "local_estimate", gjson.Get(usagePayload, "perplexity.usage_source").String())
			require.Equal(t, includeSources, gjson.Get(usagePayload, "citations").Exists())
		}
	}
}

func TestPerplexityTranslationThreeProtocolFixtures(t *testing.T) {
	body := `data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":["array[0]"]}},{"intended_usage":"ask_text_0_markdown","markdown_block":{"chunks":["mirror"]}},{"intended_usage":"pro_search_steps","plan_block":{"steps":[{"uuid":"s","step_type":"CODE","code_content":{"code":"browser-secret"}},{"uuid":"search","step_type":"SEARCH_WEB","status":"DONE"}]}}]}` + "\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"DONE","chunks":["array[0] result [1]"]}}],"web_results":[{"url":"https://go.dev","title":"Go"}]}` + "\n\nevent: end_of_stream\ndata:\n\n"
	want := "array[0] result [1]" + perplexity.SourcesFooter([]perplexity.Source{{URL: "https://go.dev", Title: "Go"}})
	for _, stream := range []bool{false, true} {
		out, err := translatePerplexityFixture(t, body, perplexityTranslateParams{
			stream: stream, IncludeSources: true, IncludeSearchProgress: true,
		})
		require.NoError(t, err)
		require.NotContains(t, out, "tool_calls")
		require.NotContains(t, out, "browser-secret")
		if !stream {
			var cc apicompat.ChatCompletionsResponse
			require.NoError(t, json.Unmarshal([]byte(out), &cc))
			require.Equal(t, want, gjson.Get(out, "choices.0.message.content").String())
			responses := apicompat.ChatCompletionsResponseToResponses(&cc, "sonar", nil, nil, false, nil)
			var responsesText strings.Builder
			for _, item := range responses.Output {
				for _, part := range item.Content {
					if part.Type == "output_text" {
						_, _ = responsesText.WriteString(part.Text)
					}
				}
			}
			require.Equal(t, want, responsesText.String(), "Responses 正文含统一来源")
			messages := apicompat.ChatCompletionsResponseToAnthropic(&cc, "sonar")
			var messagesText strings.Builder
			for _, block := range messages.Content {
				if block.Type == "text" {
					_, _ = messagesText.WriteString(block.Text)
				}
				require.NotEqual(t, "tool_use", block.Type)
			}
			require.Equal(t, want, messagesText.String(), "Messages 正文含统一来源")
			continue
		}
		require.Equal(t, want, perplexityCCContent(t, out))
		rs := apicompat.NewChatCompletionsToResponsesStreamState("sonar")
		ms := apicompat.NewChatCompletionsToAnthropicStreamState("sonar")
		var responseText, messageText strings.Builder
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "data: {") {
				continue
			}
			var chunk apicompat.ChatCompletionsChunk
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk))
			for _, event := range apicompat.ChatCompletionsChunkToResponsesEvents(&chunk, rs) {
				if event.Type == "response.output_text.delta" {
					_, _ = responseText.WriteString(event.Delta)
				}
				if event.Item != nil {
					require.NotEqual(t, "function_call", event.Item.Type)
				}
			}
			for _, event := range apicompat.ChatCompletionsChunkToAnthropicEvents(&chunk, ms) {
				if event.Delta != nil && event.Delta.Type == "text_delta" {
					_, _ = messageText.WriteString(event.Delta.Text)
				}
				if event.ContentBlock != nil {
					require.NotEqual(t, "tool_use", event.ContentBlock.Type)
				}
			}
		}
		require.Equal(t, want, responseText.String(), "Responses 流式正文含统一来源")
		require.Equal(t, want, messageText.String(), "Messages 流式正文含统一来源")
	}
}

type perplexityReadNotification struct {
	io.ReadCloser
	started chan struct{}
	once    sync.Once
}

func (r *perplexityReadNotification) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	return r.ReadCloser.Read(p)
}

func TestPerplexityTranslationCancellationUnblocksReadsAndWrites(t *testing.T) {
	for _, blockRead := range []bool{true, false} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pr, pw := io.Pipe()
		var upstream io.ReadCloser
		started := make(chan struct{})
		if blockRead {
			r, w := io.Pipe()
			upstream = &perplexityReadNotification{ReadCloser: r, started: started}
			defer func() { _ = w.Close() }()
		} else {
			upstream = io.NopCloser(strings.NewReader(perplexityAnswerFixture("content")))
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			translatePerplexitySSEToCC(ctx, upstream, pw, perplexityTranslateParams{stream: true})
		}()
		if blockRead {
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("translator 未开始读取上游")
			}
		} else {
			// Consume one byte of the role frame: its remaining pipe write is
			// necessarily blocked without a reader when cancellation occurs.
			_, err := pr.Read(make([]byte, 1))
			require.NoError(t, err)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("取消后 translator 仍阻塞于读取或 pipe 写入")
		}
		out, err := io.ReadAll(pr)
		require.ErrorIs(t, err, context.Canceled)
		require.NotContains(t, string(out), "[DONE]")
		_ = pr.Close()
	}
}

func TestPerplexityTranslationDownstreamCloseUnblocksWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		translatePerplexitySSEToCC(ctx, io.NopCloser(strings.NewReader(perplexityAnswerFixture("content"))), pw, perplexityTranslateParams{stream: true})
	}()
	_ = pr.CloseWithError(errors.New("client disconnected"))
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("下游关闭后 translator 仍阻塞于 pipe 写入")
	}
}

// BenchmarkPerplexityTranslationSmallDeltas tracks allocations as many tiny
// chunks grow the answer; old text must not be concatenated or rescanned per chunk.
func BenchmarkPerplexityTranslationSmallDeltas(b *testing.B) {
	const frame = `data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":["x"]}}]}` + "\n\n"
	for _, chunks := range []int{128, 1024, 8192} {
		b.Run(strconv.Itoa(chunks), func(b *testing.B) {
			body := strings.Repeat(frame, chunks) + perplexityAnswerFixture(strings.Repeat("x", chunks)) + "event: end_of_stream\n\n"
			p := perplexityTranslateParams{stream: true, id: "benchmark", upstreamModel: "sonar"}
			b.SetBytes(int64(chunks))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := writePerplexityTranslation(context.Background(), strings.NewReader(body), io.Discard, p); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
