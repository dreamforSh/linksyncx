package apicompat

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// This fixture exercises signed/initial thinking, redacted thinking, multiple
// text parts, escaped/unicode content, empty arguments, and an unclosed tool.
func anthropicResponsesAccumulationFixture(stopReason string) []AnthropicStreamEvent {
	return []AnthropicStreamEvent{
		{Type: "message_start", Message: &AnthropicResponse{ID: "msg_diff", Model: "claude-opus-5-5", Usage: AnthropicUsage{InputTokens: 10, CacheReadInputTokens: 5, CacheCreationInputTokens: 2}}},
		{Type: "ping"},
		{Type: "content_block_start"},
		{Type: "content_block_delta"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "thinking", Thinking: "seed ", Signature: "sig-"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: "考虑"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "thinking_delta", Thinking: " <>&"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "signature_delta", Signature: "one"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "signature_delta", Signature: "-two"}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text", Text: "ignored initial text"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "text_delta", Text: "hello"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "text_delta", Text: " 世界<>&"}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "text"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "text_delta"}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "redacted_thinking", Data: "opaque"}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_1", Name: "lookup", Input: json.RawMessage(`{"ignored":true}`)}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"path":`}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `"a<>&","n":`}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `2}`}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_2", Name: "empty"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta"}},
		{Type: "content_block_stop"},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_3", Name: "tail"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"tail":true}`}},
		{Type: "message_delta", Delta: &AnthropicDelta{StopReason: stopReason}, Usage: &AnthropicUsage{InputTokens: 13, CacheReadInputTokens: 11, CacheCreationInputTokens: 3, OutputTokens: 7}},
	}
}

func captureAnthropicResponsesFixture(t *testing.T, stopReason string, finalize bool) []string {
	t.Helper()
	state := NewAnthropicEventToResponsesState()
	state.Created = 123
	var events []ResponsesStreamEvent
	for _, event := range anthropicResponsesAccumulationFixture(stopReason) {
		events = append(events, AnthropicEventToResponsesEvents(&event, state)...)
	}
	if finalize {
		events = append(events, FinalizeAnthropicResponsesStream(state)...)
	} else {
		events = append(events, AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)...)
	}
	if len(FinalizeAnthropicResponsesStream(state)) != 0 || len(AnthropicEventToResponsesEvents(&AnthropicStreamEvent{Type: "message_stop"}, state)) != 0 {
		t.Fatal("termination is not idempotent")
	}

	// Only random item IDs are normalized. Every other wire field, including
	// sequence/index numbers, explicit empty values, usage and opaque signatures,
	// is compared against output captured before the accumulation refactor.
	ids := make(map[string]string)
	var wire []string
	for _, event := range events {
		if event.Type == "response.output_item.added" {
			ids[event.Item.ID] = fmt.Sprintf("item_%d", len(ids))
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		line := string(data)
		for id, replacement := range ids {
			line = strings.ReplaceAll(line, id, replacement)
		}
		wire = append(wire, line)
	}
	return wire
}

func TestAnthropicResponsesAccumulationWireDiff(t *testing.T) {
	// Per-event SHA-256 snapshots of the original converter's JSON wire output.
	// Hashes keep the fixture compact without hiding any field from comparison;
	// failures print the complete changed event for diagnosis.
	want := []string{
		"c691dbf0a3772426414c38d55aebe9a4b98ad14cee7e198a5bc8b927c0ff3f36",
		"5c267648fb650f924badee9052180541af6d8419af21eebfdef094db13e54189",
		"da9f720f5e6ded5c6c8e06ff33ad26de230f3c1d4e5bed03651d29607bbcc526",
		"5dfe9901cf2adefc08d917b05ceddfba303f8c2c018a77242fa40242cc88531f",
		"5612d529e322bb7dc37be2e0d84ba526d5730505c867b088e667994f77433ff0",
		"4002b8df15f1a1cc0c0baf9296d52998505232efa4670c43740490e4b1e3925e",
		"97cded5069cf55fbd6fd9af1735b394a0b78bfed995b1849982c630c5e14a3ac",
		"e520978d7fed5d410d795ae9d8ac44318601d5a2a406a0235a18bde5d21c4c46",
		"237e34105e1924342d499e55363fd9a944230b097deba8743facf5a856e490b5",
		"bcb0fca6fef684cb07c463eebdada9171c9ef45d6ba905186c3f124534937ac1",
		"35944aa6218088e253544351ca2528c0db29921faf81fb1c92962f889817575b",
		"b6a0cacaa146458069703b372af29d44a62e2453c3d089cb324273c64aa16656",
		"ad75259bc33ff5473d0e83d6aa0a34f4e73f795356fbc1a74fb4d7dca53ebb7f",
		"b8bfb81a6d5212a1696d0773d60eb8f1d2ada621ae137b746f9878feeebc4c14",
		"e9d5c53016e44b540eb3732ca42f9cf38451201201f29bf242b2ba9ade2c8824",
		"2eca9cb6f37abab5d6d1353e3c046791c4ec211d9d62ddc1724f7ac67bcbd953",
		"77764060d1cb444bb27d507783b588e08c59a172cf0c94a0f62949d854a061a7",
		"009f05a1a179ed22e71582c52845a11c260dac46dd565f52dd8e63d344ce56c3",
		"643580581fa2cd3af592894c1bab759762b9c2cd012e2e29d55467ddc5e1fddd",
		"7779047c2e12da1584b221bea69228e86e254c5463beee5b5f53effa45ea94ba",
		"64962f8752ab4b3ce31e640759256d453ad216cb76a98697dfe397bca22ed1f1",
		"4f1caca5378f5037a3b68c6fc63a5414b3e5e293d60b2c4f29288b02bb066e00",
		"d162e8e54c19e9f393392bf095e0ccd84833397cde979ae35630ae3320df4956",
		"af48c61d1129711fedc4c0a9a4154d522d733dba08235593af6dff4042bce6d1",
		"c509ec038240046c6e8bfba09f27e4dafb8a702d45c51037b11479bfc2fb3695",
		"6a0db2e5b5c8856af4adb63176d8e28bb84886c18434a6c6672a5a4904ea9cad",
		"9b44a80cf73bea3132253114f0d731cfac133ae667a30d53b18d24d27824d1b0",
		"b253fe13f97d26ad17b6258c07cf23b077453b21f7001c4f72a1b43144f97b83",
		"7f402d04237cd79d57c48c3a4adc6321b918286f2ec0562a11b6bb89fbdbdee7",
		"3962c06028beb256957f3976e5f7bc76de12b21082d48caa41c2ca4c43383b6c",
		"e17812b832244723be4b90c5eaeea63b20c8c245a08ed805deaa41c4f492b9e2",
		"fedb95dcbfe0ab121f8213a03e1d928385d50cff82cbd67f026a883ae46e1779",
	}
	for _, finalize := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalize=%t", finalize), func(t *testing.T) {
			got := captureAnthropicResponsesFixture(t, "end_turn", finalize)
			if len(got) != len(want) {
				t.Fatalf("event count = %d, want %d; events:\n%s", len(got), len(want), strings.Join(got, "\n"))
			}
			for i := range want {
				if hash := fmt.Sprintf("%x", sha256.Sum256([]byte(got[i]))); hash != want[i] {
					t.Errorf("event %d changed: SHA-256 = %s, want %s; event:\n%s", i, hash, want[i], got[i])
				}
			}
		})
	}
}

func TestAnthropicResponsesDeltaOnlyChatEventDiff(t *testing.T) {
	for _, stopReason := range []string{"end_turn", "max_tokens"} {
		for _, finalize := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/finalize=%t", stopReason, finalize), func(t *testing.T) {
				full := NewAnthropicEventToResponsesState()
				delta := NewAnthropicEventToResponsesState()
				delta.DeltaOnly = true
				fullChat := NewResponsesEventToChatState()
				deltaChat := NewResponsesEventToChatState()
				fullChat.Created, deltaChat.Created = 123, 123
				fullChat.IncludeUsage, deltaChat.IncludeUsage = true, true
				toChatWire := func(events []ResponsesStreamEvent, state *ResponsesEventToChatState) []string {
					var wire []string
					for _, event := range events {
						for _, chunk := range ResponsesEventToChatChunks(&event, state) {
							data, err := ChatChunkToSSE(chunk)
							if err != nil {
								t.Fatal(err)
							}
							wire = append(wire, data)
						}
					}
					return wire
				}
				compare := func(label string, fullEvents, deltaEvents []ResponsesStreamEvent) {
					t.Helper()
					want := toChatWire(fullEvents, fullChat)
					got := toChatWire(deltaEvents, deltaChat)
					if !slices.Equal(got, want) {
						t.Errorf("%s Chat event diff:\n got %v\nwant %v", label, got, want)
					}
					if len(fullEvents) != len(deltaEvents) {
						t.Fatalf("%s Responses event count = %d, want %d", label, len(deltaEvents), len(fullEvents))
					}
					for i := range fullEvents {
						if deltaEvents[i].Type != fullEvents[i].Type || deltaEvents[i].SequenceNumber != fullEvents[i].SequenceNumber {
							t.Errorf("%s event %d lifecycle changed: got %s/%d, want %s/%d", label, i, deltaEvents[i].Type, deltaEvents[i].SequenceNumber, fullEvents[i].Type, fullEvents[i].SequenceNumber)
						}
					}
				}
				for i, event := range anthropicResponsesAccumulationFixture(stopReason) {
					compare(fmt.Sprintf("input event %d (%s)", i, event.Type), AnthropicEventToResponsesEvents(&event, full), AnthropicEventToResponsesEvents(&event, delta))
				}
				if finalize {
					compare("finalize", FinalizeAnthropicResponsesStream(full), FinalizeAnthropicResponsesStream(delta))
				} else {
					event := AnthropicStreamEvent{Type: "message_stop"}
					compare("message_stop", AnthropicEventToResponsesEvents(&event, full), AnthropicEventToResponsesEvents(&event, delta))
				}
				if !slices.Equal(toChatWire(FinalizeAnthropicResponsesStream(full), fullChat), toChatWire(FinalizeAnthropicResponsesStream(delta), deltaChat)) {
					t.Error("repeated finalization differs")
				}
			})
		}
	}
}

func TestAnthropicResponsesDeltaOnlyDoesNotRetainText(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.DeltaOnly = true
	for i, event := range anthropicResponsesAccumulationFixture("end_turn") {
		AnthropicEventToResponsesEvents(&event, state)
		if state.TextAccum != "" || state.CurrentSummary != "" || state.CurrentThinking.Type != "" || state.CurrentThinking.Thinking != "" || state.CurrentThinking.Signature != "" || state.CurrentThinking.Data != "" {
			t.Fatalf("event %d retains text/thinking: text=%q summary=%q thinking=%+v", i, state.TextAccum, state.CurrentSummary, state.CurrentThinking)
		}
		if state.textBuilder.Len() != 0 || state.summaryBuilder.Len() != 0 || state.signatureBuilder.Len() != 0 || len(state.CurrentContent) != 0 || len(state.Outputs) != 0 {
			t.Fatalf("event %d retains text/thinking buffers or completed outputs", i)
		}
	}
	terminal := FinalizeAnthropicResponsesStream(state)
	if len(terminal) == 0 || terminal[len(terminal)-1].Response == nil || len(terminal[len(terminal)-1].Response.Output) != 0 {
		t.Fatalf("DeltaOnly terminal response must have empty output: %+v", terminal)
	}
}

func TestAnthropicResponsesDeltaOnlyArgumentsDoneRecovery(t *testing.T) {
	state := NewAnthropicEventToResponsesState()
	state.DeltaOnly = true
	chat := NewResponsesEventToChatState()
	var arguments strings.Builder
	for _, event := range []AnthropicStreamEvent{
		{Type: "message_start", Message: &AnthropicResponse{ID: "msg_recovery"}},
		{Type: "content_block_start", ContentBlock: &AnthropicContentBlock{Type: "tool_use", ID: "toolu_recovery", Name: "lookup"}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `{"a":`}},
		{Type: "content_block_delta", Delta: &AnthropicDelta{Type: "input_json_delta", PartialJSON: `1}`}},
		{Type: "content_block_stop"},
		{Type: "message_stop"},
	} {
		for _, response := range AnthropicEventToResponsesEvents(&event, state) {
			// Simulate a downstream consumer missing the last argument delta. The
			// full done payload must remain available to repair that missing tail.
			if response.Type == "response.function_call_arguments.delta" && response.Delta == `1}` {
				continue
			}
			for _, chunk := range ResponsesEventToChatChunks(&response, chat) {
				for _, choice := range chunk.Choices {
					for _, call := range choice.Delta.ToolCalls {
						_, _ = arguments.WriteString(call.Function.Arguments)
					}
				}
			}
		}
	}
	if got := arguments.String(); got != `{"a":1}` {
		t.Errorf("recovered tool arguments = %q, want %q", got, `{"a":1}`)
	}
}

func TestAnthropicResponsesAccumulationPublicSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta AnthropicDelta
		field func(*AnthropicEventToResponsesState) *string
	}{
		{"text", AnthropicDelta{Type: "text_delta", Text: "-delta"}, func(s *AnthropicEventToResponsesState) *string { return &s.TextAccum }},
		{"thinking", AnthropicDelta{Type: "thinking_delta", Thinking: "-delta"}, func(s *AnthropicEventToResponsesState) *string { return &s.CurrentSummary }},
		{"arguments", AnthropicDelta{Type: "input_json_delta", PartialJSON: "-delta"}, func(s *AnthropicEventToResponsesState) *string { return &s.CurrentArgs }},
		{"signature", AnthropicDelta{Type: "signature_delta", Signature: "-delta"}, func(s *AnthropicEventToResponsesState) *string { return &s.CurrentThinking.Signature }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := NewAnthropicEventToResponsesState()
			state.PreserveThinkingSignatures = true
			field := tc.field(state)
			*field = "seed"
			event := AnthropicStreamEvent{Type: "content_block_delta", Delta: &tc.delta}
			AnthropicEventToResponsesEvents(&event, state)
			if *field != "seed-delta" {
				t.Fatalf("public snapshot = %q, want seed-delta", *field)
			}
			snapshot := *field
			for range 1024 {
				AnthropicEventToResponsesEvents(&event, state)
			}
			if want := "seed" + strings.Repeat("-delta", 1025); *field != want {
				t.Fatalf("public snapshot length = %d, want %d", len(*field), len(want))
			}
			if snapshot != "seed-delta" {
				t.Errorf("previous snapshot mutated: %q", snapshot)
			}
			// Public fields remain writable, including replacements with the same
			// length as the current builder view.
			*field = strings.Repeat("r", len(*field))
			want := *field + "-delta"
			AnthropicEventToResponsesEvents(&event, state)
			if *field != want {
				t.Error("replacement of public snapshot was ignored")
			}
			*field = ""
			AnthropicEventToResponsesEvents(&event, state)
			if *field != "-delta" {
				t.Errorf("reset public snapshot = %q, want -delta", *field)
			}
		})
	}
}
