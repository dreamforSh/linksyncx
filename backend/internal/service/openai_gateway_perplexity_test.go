package service

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// collectPerplexityTranslation 运行翻译协程并收集 pipe 输出与关闭错误。
func collectPerplexityTranslation(t *testing.T, upstreamBody string, stream bool, promptTokens int) (string, error) {
	t.Helper()
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		translatePerplexitySSEToCC(context.Background(), io.NopCloser(strings.NewReader(upstreamBody)), pw, perplexityTranslateParams{
			upstreamModel: "sonar-pro",
			promptTokens:  promptTokens,
			stream:        stream,
			created:       1700000000,
			id:            "chatcmpl-test",
		})
	}()
	out, err := io.ReadAll(pr)
	<-done
	return string(out), err
}

func TestTranslatePerplexitySSEToCC_StreamBlocksShape(t *testing.T) {
	upstream := "event: message\r\n" +
		"data: {\"backend_uuid\":\"t-1\",\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"Hello\"}}]}\r\n\r\n" +
		"event: message\r\n" +
		"data: {\"backend_uuid\":\"t-1\",\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"Hello, world\"}}]}\r\n\r\n" +
		"event: end_of_stream\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, true, 7)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var dataPayloads []string
	sawDone := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		require.True(t, strings.HasPrefix(line, "data: "), "line should be SSE data: %q", line)
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		dataPayloads = append(dataPayloads, payload)
	}
	require.True(t, sawDone, "stream should end with [DONE]")
	require.Len(t, dataPayloads, 5, "role + 2 content deltas + finish + usage")

	// role chunk
	first := gjson.Parse(dataPayloads[0])
	require.Equal(t, "chat.completion.chunk", first.Get("object").String())
	require.Equal(t, "sonar-pro", first.Get("model").String())
	require.Equal(t, "assistant", first.Get("choices.0.delta.role").String())

	// 累积快照按后缀差分出 delta
	require.Equal(t, "Hello", gjson.Parse(dataPayloads[1]).Get("choices.0.delta.content").String())
	require.Equal(t, ", world", gjson.Parse(dataPayloads[2]).Get("choices.0.delta.content").String())

	// 终止 chunk
	finish := gjson.Parse(dataPayloads[3])
	require.Equal(t, "stop", finish.Get("choices.0.finish_reason").String())

	// usage chunk（choices 为空数组，extractCCStreamUsage 依赖此形态）
	usage := gjson.Parse(dataPayloads[4])
	require.Equal(t, int64(0), usage.Get("choices.#").Int())
	require.Equal(t, int64(7), usage.Get("usage.prompt_tokens").Int())
	require.Greater(t, usage.Get("usage.completion_tokens").Int(), int64(0))
	require.Equal(t,
		usage.Get("usage.prompt_tokens").Int()+usage.Get("usage.completion_tokens").Int(),
		usage.Get("usage.total_tokens").Int(),
	)
}

func TestTranslatePerplexitySSEToCC_StreamLegacyShape(t *testing.T) {
	inner := `{"steps":[{"step_type":"FINAL","content":{"answer":"{\"answer\":\"Legacy answer text.\",\"web_results\":[]}"}}]}`
	outer := `{"backend_uuid":"t-9","text":` + jsonQuoteForTest(inner) + `}`
	upstream := "event: message\r\ndata: " + outer + "\r\n\r\nevent: end_of_stream\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, true, 3)
	require.NoError(t, err)
	require.Contains(t, out, `"Legacy answer text."`)
	require.Contains(t, out, "data: [DONE]")
}

func TestTranslatePerplexitySSEToCC_NonStream(t *testing.T) {
	upstream := "event: message\r\n" +
		"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"Full answer.\"}}]}\r\n\r\n" +
		"event: end_of_stream\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, false, 5)
	require.NoError(t, err)
	require.NotContains(t, out, "data:")

	root := gjson.Parse(strings.TrimSpace(out))
	require.Equal(t, "chat.completion", root.Get("object").String())
	require.Equal(t, "Full answer.", root.Get("choices.0.message.content").String())
	require.Equal(t, "stop", root.Get("choices.0.finish_reason").String())
	require.Equal(t, int64(5), root.Get("usage.prompt_tokens").Int())
}

func TestTranslatePerplexitySSEToCC_EOFWithoutEndOfStream(t *testing.T) {
	// 没有 end_of_stream 但已有内容：仍应补 final + usage + [DONE]。
	upstream := "event: message\r\n" +
		"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"partial\"}}]}\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, true, 2)
	require.NoError(t, err)
	require.Contains(t, out, "partial")
	require.Contains(t, out, "data: [DONE]")
}

func TestTranslatePerplexitySSEToCC_EmptyStream(t *testing.T) {
	// 干净 EOF 且无内容：以错误关闭 pipe，交由下游截断/failover 处理。
	out, err := collectPerplexityTranslation(t, "event: ping\r\ndata: {}\r\n\r\n", true, 2)
	require.Error(t, err)
	require.Empty(t, strings.TrimSpace(out))
}

// jsonQuoteForTest 把字符串编码为 JSON 字符串字面量（含引号）。
func jsonQuoteForTest(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
