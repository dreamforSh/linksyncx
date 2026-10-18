package codebuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// StreamError is an error frame the upstream sent inside a 200 SSE stream.
type StreamError struct {
	Body []byte // {"error": {...}} as received
}

func (e *StreamError) Error() string {
	return "codebuddy upstream stream error: " + strings.TrimSpace(string(e.Body))
}

type aggregatedToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

// AggregateChatStream folds an upstream Chat Completions SSE stream into one
// non-streaming chat.completion object. The CodeBuddy upstream only streams,
// so this is how non-stream clients are served.
//
// A chunk carrying an "error" key yields a *StreamError. A stream that ends
// without [DONE] is accepted once any chunk arrived.
func AggregateChatStream(r io.Reader, maxLineSize int) ([]byte, error) {
	if maxLineSize <= 0 {
		maxLineSize = 16 << 20
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	var (
		id, model, finishReason, systemFingerprint string
		created                                    int64
		content, reasoning, refusal                strings.Builder
		usage                                      json.RawMessage
		toolCalls                                  = map[int]*aggregatedToolCall{}
		sawChunk                                   bool
	)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			ID                string          `json:"id"`
			Model             string          `json:"model"`
			Created           int64           `json:"created"`
			SystemFingerprint string          `json:"system_fingerprint"`
			Error             json.RawMessage `json:"error"`
			Usage             json.RawMessage `json:"usage"`
			Choices           []struct {
				Delta struct {
					Content          *string `json:"content"`
					ReasoningContent *string `json:"reasoning_content"`
					Refusal          *string `json:"refusal"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			body, _ := marshalNoEscape(map[string]json.RawMessage{"error": chunk.Error})
			return nil, &StreamError{Body: body}
		}
		sawChunk = true
		id = firstNonEmpty(id, chunk.ID)
		model = firstNonEmpty(model, chunk.Model)
		systemFingerprint = firstNonEmpty(systemFingerprint, chunk.SystemFingerprint)
		if created == 0 {
			created = chunk.Created
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = append(json.RawMessage(nil), chunk.Usage...)
		}
		for _, choice := range chunk.Choices {
			delta := choice.Delta
			if delta.Content != nil {
				_, _ = content.WriteString(*delta.Content)
			}
			if delta.ReasoningContent != nil {
				_, _ = reasoning.WriteString(*delta.ReasoningContent)
			}
			if delta.Refusal != nil {
				_, _ = refusal.WriteString(*delta.Refusal)
			}
			for _, call := range delta.ToolCalls {
				agg := toolCalls[call.Index]
				if agg == nil {
					agg = &aggregatedToolCall{}
					toolCalls[call.Index] = agg
				}
				agg.ID += call.ID
				if call.Type != "" {
					agg.Type = call.Type
				}
				agg.Name += call.Function.Name
				_, _ = agg.Arguments.WriteString(call.Function.Arguments)
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read upstream stream: %w", err)
	}
	if !sawChunk {
		return nil, fmt.Errorf("upstream stream carried no chunks")
	}

	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if refusal.Len() > 0 {
		message["refusal"] = refusal.String()
	}
	if len(toolCalls) > 0 {
		indexes := make([]int, 0, len(toolCalls))
		for index := range toolCalls {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		calls := make([]map[string]any, 0, len(indexes))
		for _, index := range indexes {
			call := toolCalls[index]
			calls = append(calls, map[string]any{
				"id":   call.ID,
				"type": firstNonEmpty(call.Type, "function"),
				"function": map[string]any{
					"name":      call.Name,
					"arguments": call.Arguments.String(),
				},
			})
		}
		message["tool_calls"] = calls
		if content.Len() == 0 {
			message["content"] = nil
		}
		if finishReason == "" {
			finishReason = "tool_calls"
		}
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	if created == 0 {
		created = time.Now().Unix()
	}
	out := map[string]any{
		"id":      firstNonEmpty(id, "chatcmpl-codebuddy"),
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
	}
	if systemFingerprint != "" {
		out["system_fingerprint"] = systemFingerprint
	}
	if len(usage) > 0 {
		out["usage"] = usage
	}
	encoded, err := marshalNoEscape(out)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(encoded), nil
}
