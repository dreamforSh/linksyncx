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
			upstreamModel:         "sonar-pro",
			promptTokens:          promptTokens,
			stream:                stream,
			created:               1700000000,
			id:                    "chatcmpl-test",
			IncludeSources:        true,
			IncludeSearchProgress: true,
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
	// 已有部分内容也不能把截断伪装成成功。
	upstream := "event: message\r\n" +
		"data: {\"blocks\":[{\"intended_usage\":\"ask_text\",\"markdown_block\":{\"answer\":\"partial\"}}]}\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, true, 2)
	require.Error(t, err)
	require.Contains(t, out, "partial")
	require.NotContains(t, out, "data: [DONE]")
	require.NotContains(t, out, `"finish_reason":"stop"`)
}

func TestTranslatePerplexitySSEToCC_EmptyStream(t *testing.T) {
	// 干净 EOF 且无内容：以错误关闭 pipe，交由下游截断/failover 处理。
	out, err := collectPerplexityTranslation(t, "event: ping\r\ndata: {}\r\n\r\n", true, 2)
	require.Error(t, err)
	require.Empty(t, strings.TrimSpace(out))
}

func TestTranslatePerplexitySSEToCC_StepsAsSafeProgress(t *testing.T) {
	step := func(uuid, status string) string {
		return `{"intended_usage":"pro_search_steps","plan_block":{"steps":[{"step_type":"SEARCH_WEB","uuid":"` + uuid +
			`","status":"` + status + `","search_web_content":{"query":"private query","browser_key":"secret"}}]}}`
	}
	upstream := "data: {\"blocks\":[" + step("s-1", "IN_PROGRESS") + "]}\n\n" +
		"data: {\"blocks\":[" + step("s-1", "IN_PROGRESS") + "]}\n\n" +
		"data: {\"blocks\":[" + step("s-1", "DONE") + "]}\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"answer":"Answer."}}]}` + "\n\n" +
		"event: end_of_stream\n\n"

	out, err := collectPerplexityTranslation(t, upstream, true, 5)
	require.NoError(t, err)
	require.NotContains(t, out, "tool_calls")
	require.NotContains(t, out, "private query")
	require.NotContains(t, out, "secret")
	texts, updates := 0, 0
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		chunk := gjson.Parse(strings.TrimPrefix(line, "data: "))
		if chunk.Get("choices.0.delta.reasoning_content").Exists() {
			texts++
		}
		// 末尾 usage 块（choices 为空）汇总全部进度，不算增量更新。
		if chunk.Get("choices.#").Int() > 0 && chunk.Get("perplexity.search_progress").Exists() {
			updates++
		}
	}
	require.Equal(t, 1, texts, "同一步骤的文字只下发一次，状态变化不重复堆叠")
	require.Equal(t, 2, updates, "重复状态去重，同 UUID 的完成状态仍应更新元数据")
	require.Contains(t, out, `"status":"DONE"`)
	require.Contains(t, out, `"Answer."`)
	require.Contains(t, out, `"finish_reason":"stop"`)
	require.Less(t, strings.Index(out, `"role":"assistant"`), strings.Index(out, `"reasoning_content"`))
}

func TestTranslatePerplexitySSEToCC_NonStreamSuppressesInternalSteps(t *testing.T) {
	upstream := "event: message\r\n" +
		"data: {\"blocks\":[{\"intended_usage\":\"pro_search_steps\",\"plan_block\":{\"steps\":[{\"step_type\":\"CODE\",\"uuid\":\"3333\",\"code_content\":{\"language\":\"go\",\"code\":\"secret\"}}]}}," +
		`{"intended_usage":"ask_text","markdown_block":{"answer":"done"}}]}` + "\r\n\r\n" +
		"event: end_of_stream\r\n\r\n"

	out, err := collectPerplexityTranslation(t, upstream, false, 5)
	require.NoError(t, err)
	root := gjson.Parse(strings.TrimSpace(out))
	require.False(t, root.Get("choices.0.message.tool_calls").Exists())
	require.False(t, root.Get("choices.0.message.reasoning_content").Exists())
	require.NotContains(t, out, "secret")
	require.Equal(t, "stop", root.Get("choices.0.finish_reason").String())
}

// jsonQuoteForTest 把字符串编码为 JSON 字符串字面量（含引号）。
func jsonQuoteForTest(s string) string {
	var b strings.Builder
	_ = b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			_, _ = b.WriteString(`\"`)
		case '\\':
			_, _ = b.WriteString(`\\`)
		default:
			_, _ = b.WriteRune(r)
		}
	}
	_ = b.WriteByte('"')
	return b.String()
}
