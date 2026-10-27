package codebuddy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// DeepSeek V4 on CodeBuddy. The official client's catalog gives these models a
// high/max effort scale (minimal/low/medium have no tier of their own) and a
// 1M window. Picking the 1M budget in the official client only moves its own
// compaction point; the upstream takes the full window either way, so nothing
// about the window is sent on the wire.
const (
	DeepSeekContextWindow int64 = 1_000_000
	// DeepSeekDefaultEffort applies when the client names no level, so a
	// plain request runs in max mode.
	DeepSeekDefaultEffort = "max"
)

// DeepSeekReasoningLevels are the effort levels worth advertising for DeepSeek.
var DeepSeekReasoningLevels = []string{"high", "max"}

// IsDeepSeekModel reports a DeepSeek model id (deepseek-v4-pro, -flash, ...).
func IsDeepSeekModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "deepseek-")
}

// DeepSeekReasoningEffort maps a requested effort onto DeepSeek's high/max
// scale. send=false omits the field, which is how the official client asks for
// thinking off. Lower levels clamp up to high, as the official client does for
// levels the model does not declare; an empty or unknown level means max.
func DeepSeekReasoningEffort(requested string) (effort string, send bool) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "none", "off", "disabled":
		return "", false
	case "minimal", "low", "medium", "high":
		return "high", true
	default:
		return DeepSeekDefaultEffort, true
	}
}

// applyDeepSeekCompat fits a body to DeepSeek on CodeBuddy: the effort lands on
// the high/max scale, and response_format (Responses text.format), which the
// upstream rejects, travels as a system instruction instead. messages[0] must
// be the system message.
func applyDeepSeekCompat(root map[string]json.RawMessage, messages []map[string]json.RawMessage) error {
	var requested string
	_ = json.Unmarshal(root["reasoning_effort"], &requested)
	if effort, send := DeepSeekReasoningEffort(requested); send {
		root["reasoning_effort"] = mustRaw(effort)
	} else {
		delete(root, "reasoning_effort")
	}

	raw, ok := root["response_format"]
	if !ok {
		return nil
	}
	delete(root, "response_format")
	instruction := responseFormatInstruction(raw)
	if instruction == "" {
		return nil
	}
	return appendSystemText(messages[0], instruction)
}

// responseFormatInstruction words a JSON response_format as a prompt
// instruction. Plain text and unknown formats need none. The schema may sit
// under json_schema (Chat shape) or at the top level (Responses text.format
// shape, which some clients send to Chat as is).
func responseFormatInstruction(raw json.RawMessage) string {
	var format struct {
		Type       string          `json:"type"`
		Schema     json.RawMessage `json:"schema"`
		JSONSchema *struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &format) != nil {
		return ""
	}
	const objectOnly = "Your final answer must be a single valid JSON object, with no Markdown code fences and no text before or after it."
	switch strings.TrimSpace(format.Type) {
	case "json_object":
		return objectOnly
	case "json_schema":
		source := format.Schema
		if format.JSONSchema != nil {
			source = format.JSONSchema.Schema
		}
		var schema bytes.Buffer
		if json.Compact(&schema, source) != nil || schema.String() == "null" {
			return objectOnly
		}
		return "Your final answer must be a single valid JSON value that conforms to the JSON schema below, with no Markdown code fences and no text before or after it.\n\nJSON schema:\n" + schema.String()
	default:
		return ""
	}
}

// appendSystemText adds text to the end of a system message, keeping the
// content's string or content-part shape.
func appendSystemText(message map[string]json.RawMessage, text string) error {
	raw := bytes.TrimSpace(message["content"])
	var current string
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		message["content"] = mustRaw(text)
		return nil
	}
	if json.Unmarshal(raw, &current) == nil {
		if current != "" {
			text = current + "\n\n" + text
		}
		message["content"] = mustRaw(text)
		return nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return requestErrorf("system message content must be a string or an array of content parts")
	}
	part, err := marshalNoEscape(map[string]string{"type": "text", "text": text})
	if err != nil {
		return err
	}
	encoded, err := marshalNoEscape(append(parts, part))
	if err != nil {
		return err
	}
	message["content"] = encoded
	return nil
}
