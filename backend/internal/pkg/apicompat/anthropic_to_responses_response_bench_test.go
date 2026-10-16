package apicompat

import (
	"fmt"
	"strings"
	"testing"
)

// BenchmarkAnthropicResponsesLongDeltas measures the complete converter without
// SSE serialization, so allocation growth from retaining deltas remains visible.
func BenchmarkAnthropicResponsesLongDeltas(b *testing.B) {
	for _, kind := range []string{"text", "thinking", "arguments", "signature"} {
		for _, count := range []int{1024, 8192} {
			b.Run(fmt.Sprintf("%s/%d", kind, count), func(b *testing.B) {
				benchmarkAnthropicResponsesLongDeltas(b, kind, count, false)
			})
		}
	}
}

func BenchmarkAnthropicResponsesDeltaOnlyLongDeltas(b *testing.B) {
	for _, kind := range []string{"text", "thinking", "arguments", "signature"} {
		for _, count := range []int{1024, 8192} {
			b.Run(fmt.Sprintf("%s/%d", kind, count), func(b *testing.B) {
				benchmarkAnthropicResponsesLongDeltas(b, kind, count, true)
			})
		}
	}
}

func benchmarkAnthropicResponsesLongDeltas(b *testing.B, kind string, count int, deltaOnly bool) {
	fragment := strings.Repeat("x", 32)
	block := AnthropicContentBlock{Type: "text"}
	delta := AnthropicDelta{Type: "text_delta", Text: fragment}
	switch kind {
	case "thinking":
		block.Type = "thinking"
		delta = AnthropicDelta{Type: "thinking_delta", Thinking: fragment}
	case "arguments":
		block = AnthropicContentBlock{Type: "tool_use", ID: "toolu_bench", Name: "lookup"}
		delta = AnthropicDelta{Type: "input_json_delta", PartialJSON: fragment}
	case "signature":
		block.Type = "thinking"
		delta = AnthropicDelta{Type: "signature_delta", Signature: fragment}
	}
	start := AnthropicStreamEvent{Type: "message_start", Message: &AnthropicResponse{ID: "msg_bench", Model: "claude-opus-5-5"}}
	blockStart := AnthropicStreamEvent{Type: "content_block_start", ContentBlock: &block}
	chunk := AnthropicStreamEvent{Type: "content_block_delta", Delta: &delta}
	blockStop := AnthropicStreamEvent{Type: "content_block_stop"}
	stop := AnthropicStreamEvent{Type: "message_stop"}

	b.ReportAllocs()
	b.SetBytes(int64(len(fragment) * count))
	b.ResetTimer()
	for b.Loop() {
		state := NewAnthropicEventToResponsesState()
		state.DeltaOnly = deltaOnly
		AnthropicEventToResponsesEvents(&start, state)
		AnthropicEventToResponsesEvents(&blockStart, state)
		for range count {
			AnthropicEventToResponsesEvents(&chunk, state)
		}
		AnthropicEventToResponsesEvents(&blockStop, state)
		terminal := AnthropicEventToResponsesEvents(&stop, state)
		if len(terminal) == 0 || terminal[len(terminal)-1].Response == nil {
			b.Fatal("missing terminal response")
		}
	}
}
