//go:build unit

package antigravity

import (
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func TestStreamingProcessor_ResponseShapes(t *testing.T) {
	const body = `{"responseId":"msg_direct","candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":3}}`
	for _, wrapped := range []bool{false, true} {
		name := "direct"
		data := body
		if wrapped {
			name = "wrapped"
			data = `{"response":` + body + `}`
		}
		t.Run(name, func(t *testing.T) {
			p := NewStreamingProcessor("gemini-test")
			events := p.ProcessLineEvents("data: " + data)
			require.NoError(t, p.Err())
			require.Len(t, events, 3)
			assert.Equal(t, "message_start", events[0].Type)
			assert.Equal(t, "msg_direct", events[0].Message.ID)
			assert.Equal(t, "hello", events[2].Delta.Text)
			assert.Nil(t, events[0].Message.StopReason)
			for _, event := range events {
				assert.NotEqual(t, "message_delta", event.Type)
				assert.NotEqual(t, "message_stop", event.Type)
			}
			final, usage := p.FinishEvents()
			require.NoError(t, p.Err())
			require.Len(t, final, 3)
			assert.Equal(t, "content_block_stop", final[0].Type)
			assert.Equal(t, "end_turn", final[1].Delta.StopReason)
			assert.Equal(t, "message_stop", final[2].Type)
			assert.Equal(t, 12, usage.InputTokens)
			assert.Equal(t, 3, usage.OutputTokens)
		})
	}
}

func TestStreamingProcessor_RejectsInvalidAndErrorFrames(t *testing.T) {
	cases := map[string]string{
		"invalid json":             `{"response":`,
		"null":                     `null`,
		"array":                    `[]`,
		"empty object":             `{}`,
		"null response":            `{"response":null,"candidates":[{"finishReason":"STOP"}]}`,
		"invalid response":         `{"response":"invalid","candidates":[{"finishReason":"STOP"}]}`,
		"error":                    `{"error":{"code":429,"message":"quota exceeded"}}`,
		"wrapped error":            `{"response":{"error":{"code":500,"message":"failed"}}}`,
		"error with valid content": `{"error":{"code":500},"candidates":[{"content":{"parts":[{"text":"not success"}]},"finishReason":"STOP"}]}`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := NewStreamingProcessor("gemini-test")
			assert.Empty(t, p.ProcessLineEvents("data: "+data))
			require.Error(t, p.Err())
			firstErr := p.Err()
			assert.False(t, p.MessageStartSent())
			assert.Empty(t, p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"text":"ignored"}]},"finishReason":"STOP"}]}`))
			final, usage := p.FinishEvents()
			assert.Empty(t, final)
			assert.NotNil(t, usage)
			assert.Same(t, firstErr, p.Err())
		})
	}
}

func TestStreamingProcessor_ErrorFramesRetainUsageWithoutEvents(t *testing.T) {
	const usage = `"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}`
	for name, body := range map[string]string{
		"direct":          `{"error":{"code":500},` + usage + `}`,
		"outer":           `{"error":{"code":500},"response":{` + usage + `}}`,
		"wrapped":         `{"response":{"error":{"code":500},` + usage + `}}`,
		"error content":   `{"error":{"code":500},"candidates":[{"content":{"parts":[{"text":"not success"}]},"finishReason":"STOP"}],` + usage + `}`,
		"wrapped content": `{"response":{"error":{"code":500},"candidates":[{"content":{"parts":[{"text":"not success"}]},"finishReason":"STOP"}],` + usage + `}}`,
	} {
		t.Run(name, func(t *testing.T) {
			for _, byteAPI := range []bool{false, true} {
				p := NewStreamingProcessor("gemini-test")
				if byteAPI {
					assert.Empty(t, p.ProcessLine("data: "+body))
				} else {
					assert.Empty(t, p.ProcessLineEvents("data: "+body))
				}
				require.Error(t, p.Err())
				assert.False(t, p.MessageStartSent())
				assert.Equal(t, 8, p.Usage().InputTokens)
				assert.Equal(t, 3, p.Usage().OutputTokens)
				if byteAPI {
					final, usage := p.Finish()
					assert.Empty(t, final)
					assert.Equal(t, 8, usage.InputTokens)
					assert.Equal(t, 3, usage.OutputTokens)
				} else {
					final, usage := p.FinishEvents()
					assert.Empty(t, final)
					assert.Equal(t, 8, usage.InputTokens)
					assert.Equal(t, 3, usage.OutputTokens)
				}
			}
		})
	}
}

func TestStreamingProcessor_InvalidJSONDoesNotCreateUsage(t *testing.T) {
	for _, body := range []string{
		`{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3},"error":{"code":500}`,
		`{"response":{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}},"error":`,
		`{"error":{"code":500},"usageMetadata":{"promptTokenCount":"8","candidatesTokenCount":3}}`,
	} {
		for _, byteAPI := range []bool{false, true} {
			p := NewStreamingProcessor("gemini-test")
			if byteAPI {
				assert.Empty(t, p.ProcessLine("data: "+body))
			} else {
				assert.Empty(t, p.ProcessLineEvents("data: "+body))
			}
			require.Error(t, p.Err())
			assert.False(t, p.MessageStartSent())
			assert.Zero(t, p.Usage().InputTokens)
			assert.Zero(t, p.Usage().OutputTokens)
		}
	}
}

func TestStreamingProcessor_IncompleteEOF(t *testing.T) {
	for name, lines := range map[string][]string{
		"empty":           nil,
		"done only":       {`data: [DONE]`},
		"partial content": {`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, `data: [DONE]`},
		"usage only":      {`data: {"usageMetadata":{"promptTokenCount":8}}`},
	} {
		t.Run(name, func(t *testing.T) {
			p := NewStreamingProcessor("gemini-test")
			for _, line := range lines {
				p.ProcessLineEvents(line)
			}
			require.NoError(t, p.Err())
			final, _ := p.FinishEvents()
			assert.Empty(t, final)
			assert.ErrorIs(t, p.Err(), io.ErrUnexpectedEOF)
			assert.Empty(t, p.ProcessLine(`data: {"candidates":[{"finishReason":"STOP"}]}`))
			again, _ := p.Finish()
			assert.Empty(t, again)
		})
	}
}

func TestStreamingProcessor_ErrorAfterFinishReasonIsNotSuccess(t *testing.T) {
	for _, tail := range []string{`{"error":{"message":"failed"}}`, `{"response":`} {
		p := NewStreamingProcessor("gemini-test")
		out := p.ProcessLine(`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]},"finishReason":"STOP"}]}`)
		assert.Contains(t, string(out), "message_start")
		assert.NotContains(t, string(out), "message_stop")
		assert.Empty(t, p.ProcessLine("data: "+tail))
		final, _ := p.Finish()
		assert.Empty(t, final)
		require.Error(t, p.Err())
	}
}

func TestStreamingProcessor_TailUsageAndIdempotentFinish(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	initial := p.ProcessLineEvents(`data: {"responseId":"msg_usage","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}`)
	assert.Empty(t, initial)
	assert.False(t, p.MessageStartSent())
	before := p.Usage()
	first := p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"MAX_TOKENS"}]}`)
	require.NotEmpty(t, first)
	assert.Equal(t, "msg_usage", first[0].Message.ID)
	assert.Equal(t, 10, first[0].Message.Usage.InputTokens)
	assert.Empty(t, p.ProcessLineEvents(`data: {"response":{"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":7,"thoughtsTokenCount":2,"cachedContentTokenCount":10,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":4}]}}}`))
	assert.Equal(t, 10, before.InputTokens)
	assert.Equal(t, 1, before.OutputTokens)
	before.InputTokens = 999
	assert.Equal(t, 30, p.Usage().InputTokens)
	final, usage := p.FinishEvents()
	require.NoError(t, p.Err())
	require.Len(t, final, 3)
	assert.Equal(t, "max_tokens", final[1].Delta.StopReason)
	assert.Equal(t, 30, final[1].Usage.InputTokens)
	assert.Equal(t, 9, final[1].Usage.OutputTokens)
	assert.Equal(t, 10, final[1].Usage.CacheReadInputTokens)
	assert.Equal(t, 4, usage.ImageOutputTokens)
	assert.Equal(t, 10, first[0].Message.Usage.InputTokens)
	assert.Equal(t, 0, *first[1].Index)
	assert.Equal(t, 0, *final[0].Index)
	again, againUsage := p.FinishEvents()
	assert.Empty(t, again)
	assert.Equal(t, usage, againUsage)
	assert.Empty(t, p.ProcessLineEvents(`data: {"error":{"message":"too late"}}`))
	assert.NoError(t, p.Err())
}

func TestStreamingProcessor_EmptyFinishedStreamDoesNotStartMessage(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	assert.Empty(t, p.ProcessLineEvents(`data: {"response":{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8}}}`))
	assert.False(t, p.MessageStartSent())
	final, usage := p.FinishEvents()
	assert.Empty(t, final)
	assert.Equal(t, 8, usage.InputTokens)
	assert.NoError(t, p.Err())
}

func TestStreamingProcessor_SignatureOnlyBindsOpenThinking(t *testing.T) {
	for _, textField := range []string{"", `"text":"",`} {
		p := NewStreamingProcessor("gemini-test")
		events := p.ProcessLineEvents(`data: {"response":{"candidates":[{"content":{"parts":[{"text":"plan","thought":true}]}}]}}`)
		events = append(events, p.ProcessLineEvents(`data: {"response":{"candidates":[{"content":{"parts":[{`+textField+`"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}}`)...)
		final, _ := p.FinishEvents()
		events = append(events, final...)
		require.NoError(t, p.Err())
		var thinkingStarts, signatures int
		var thinkingIndex, signatureIndex int
		for _, event := range events {
			if event.ContentBlock != nil && event.ContentBlock.Type == "thinking" {
				thinkingStarts++
				thinkingIndex = *event.Index
			}
			if event.Delta != nil && event.Delta.Type == "signature_delta" {
				signatures++
				signatureIndex = *event.Index
				assert.Equal(t, "sig", event.Delta.Signature)
			}
		}
		assert.Equal(t, 1, thinkingStarts)
		assert.Equal(t, 1, signatures)
		assert.Equal(t, thinkingIndex, signatureIndex)
	}
}

func TestStreamingProcessor_SignatureOnlyWithoutThinking(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	events := p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}`)
	final, _ := p.FinishEvents()
	events = append(events, final...)
	require.NoError(t, p.Err())
	require.Len(t, events, 7)
	assert.Equal(t, "thinking", events[1].ContentBlock.Type)
	assert.Equal(t, "sig", events[3].Delta.Signature)
	assert.Equal(t, *events[1].Index, *events[3].Index)
}

func TestStreamingProcessor_ToolGroundingAndSignatureOrder(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	events := p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"thoughtSignature":"thinking-sig"},{"functionCall":{"id":"call_1","name":"lookup","args":{"q":"test"}},"thoughtSignature":"tool-sig"}]},"finishReason":"STOP","groundingMetadata":{"webSearchQueries":["query"],"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}]}`)
	final, _ := p.FinishEvents()
	events = append(events, final...)
	require.NoError(t, p.Err())
	var order []string
	var toolIndex int
	for _, event := range events {
		switch {
		case event.ContentBlock != nil:
			order = append(order, "start:"+event.ContentBlock.Type)
			if event.ContentBlock.Type == "tool_use" {
				assert.Equal(t, "call_1", event.ContentBlock.ID)
				assert.Equal(t, "lookup", event.ContentBlock.Name)
				assert.Equal(t, "tool-sig", event.ContentBlock.Signature)
				toolIndex = *event.Index
			}
		case event.Delta != nil && event.Delta.Type == "signature_delta":
			order = append(order, "signature:"+event.Delta.Signature)
		case event.Delta != nil && event.Delta.Type == "input_json_delta":
			assert.Equal(t, `{"q":"test"}`, event.Delta.PartialJSON)
			assert.Equal(t, toolIndex, *event.Index)
		case event.Delta != nil && event.Delta.Type == "text_delta":
			assert.Contains(t, event.Delta.Text, "Web search queries: query")
			assert.Contains(t, event.Delta.Text, "[Example](https://example.com)")
		case event.Type == "message_delta":
			assert.Equal(t, "tool_use", event.Delta.StopReason)
		}
	}
	assert.Equal(t, []string{"start:thinking", "signature:thinking-sig", "start:tool_use", "start:text"}, order)
	assert.Equal(t, "message_stop", events[len(events)-1].Type)
}

func TestStreamingProcessor_ByteWrapperAndUsageHook(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	var calls int
	p.SetUsageMapHook(func(usage map[string]any) {
		calls++
		usage["cache_creation_input_tokens"] = 5
		usage["extra_usage"] = "preserved"
	})
	out := p.ProcessLine(`data: {"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":1}]}}`)
	final, _ := p.Finish()
	out = append(out, final...)
	require.NoError(t, p.Err())
	assert.Equal(t, 2, calls)
	assert.Contains(t, string(out), `"thinking":""`)
	assert.Contains(t, string(out), `"stop_reason":null`)
	assert.Contains(t, string(out), `"stop_sequence":null`)
	assert.Equal(t, 2, strings.Count(string(out), `"extra_usage":"preserved"`))
	assert.Equal(t, 2, strings.Count(string(out), `"image_output_tokens":1`))
	assert.Equal(t, 2, strings.Count(string(out), `"cache_creation_input_tokens":5`))
	assert.Equal(t, 1, strings.Count(string(out), "event: message_delta\n"))
	assert.Equal(t, 1, strings.Count(string(out), "event: message_stop\n"))
	assert.Zero(t, p.Usage().CacheCreationInputTokens)
}

func TestStreamingProcessor_StructuredUsageHook(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	p.SetUsageMapHook(func(usage map[string]any) {
		usage["input_tokens"] = 15
		usage["cache_creation_input_tokens"] = 4
	})
	events := p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20}}`)
	require.NoError(t, p.Err())
	assert.Equal(t, 15, events[0].Message.Usage.InputTokens)
	assert.Equal(t, 4, events[0].Message.Usage.CacheCreationInputTokens)
	final, usage := p.FinishEvents()
	assert.Equal(t, 15, final[len(final)-2].Usage.InputTokens)
	assert.Equal(t, 20, usage.InputTokens)
}

func TestStreamingProcessor_UsageHookErrorsReachErr(t *testing.T) {
	p := NewStreamingProcessor("gemini-test")
	p.SetUsageMapHook(func(usage map[string]any) { usage["invalid"] = math.NaN() })
	assert.Empty(t, p.ProcessLineEvents(`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`))
	require.Error(t, p.Err())
	final, _ := p.FinishEvents()
	assert.Empty(t, final)
}

func TestStreamingProcessor_ByteAndStructuredAPIsMatch(t *testing.T) {
	const line = `data: {"responseId":"msg_compare","candidates":[{"content":{"parts":[{"text":"plan","thought":true},{"thoughtSignature":"sig"},{"text":"answer"},{"functionCall":{"id":"call_1","name":"lookup","args":{"value":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"cachedContentTokenCount":3,"candidatesTokenCount":5}}`
	typed := NewStreamingProcessor("gemini-test")
	want := typed.ProcessLineEvents(line)
	finalEvents, _ := typed.FinishEvents()
	want = append(want, finalEvents...)

	bytesProcessor := NewStreamingProcessor("gemini-test")
	out := bytesProcessor.ProcessLine(line)
	finalBytes, _ := bytesProcessor.Finish()
	out = append(out, finalBytes...)
	var got []apicompat.AnthropicStreamEvent
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event apicompat.AnthropicStreamEvent
		require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
		got = append(got, event)
	}
	assert.Equal(t, want, got)
	assert.NoError(t, typed.Err())
	assert.NoError(t, bytesProcessor.Err())
}
