package codebuddy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// DefaultSystemMessage is inserted when a request has no system message; the
// upstream expects the conversation to open with one.
const DefaultSystemMessage = "You are a helpful assistant."

// IntlAutoModel is what the international estate calls the public "auto".
const IntlAutoModel = "default-model"

// allowedBodyKeys is the Chat Completions surface the official clients send.
// Anything else (store, metadata, service_tier, ...) is dropped rather than
// handed to an upstream that never sees it from a real client.
var allowedBodyKeys = map[string]struct{}{
	"model": {}, "messages": {}, "tools": {}, "tool_choice": {}, "temperature": {},
	"max_tokens": {}, "max_completion_tokens": {}, "top_p": {}, "top_k": {}, "stream": {},
	"stream_options": {}, "stop": {}, "presence_penalty": {}, "frequency_penalty": {},
	"n": {}, "response_format": {}, "seed": {}, "user": {}, "reasoning_effort": {},
	"prompt_cache_key": {}, "verbosity": {}, "reasoning_summary": {}, "parallel_tool_calls": {},
}

// RequestError is a client-side request problem the gateway answers with 400.
type RequestError struct{ Message string }

func (e *RequestError) Error() string { return e.Message }

func requestErrorf(format string, args ...any) error {
	return &RequestError{Message: fmt.Sprintf(format, args...)}
}

// UpstreamModel maps the public model id to the profile's upstream id.
func UpstreamModel(model, profile string) string {
	if model == "auto" && IsInternational(profile) {
		return IntlAutoModel
	}
	return model
}

// PublicModel reverses UpstreamModel for response rewriting.
func PublicModel(model string) string {
	if model == IntlAutoModel {
		return "auto"
	}
	return model
}

// PrepareChatBody normalizes a Chat Completions body for the CodeBuddy
// upstream:
//   - only the official parameter surface is kept;
//   - developer messages become system and a system message leads;
//   - an object tool_choice naming one function becomes tools=[it] +
//     "required" (upstream accepts string tool_choice only);
//   - stream is forced on with usage, because the upstream is stream-only;
//   - "auto" becomes "default-model" internationally, where consecutive user
//     messages carrying images are merged into one;
//   - DeepSeek models get their effort scale and lose response_format (see
//     applyDeepSeekCompat).
func PrepareChatBody(body []byte, profile string) ([]byte, error) {
	prepared, err := PrepareChat(body, profile)
	if err != nil {
		return nil, err
	}
	return prepared.Body, nil
}

// PreparedChat is a Chat Completions body rewritten by PrepareChat.
type PreparedChat struct {
	Body []byte
	// Model is the upstream model id written into Body, as a JSON reader of
	// Body's "model" field returns it.
	Model string
}

// PrepareChat is PrepareChatBody that also reports the upstream model, so
// callers need not search the rewritten body for it again.
func PrepareChat(body []byte, profile string) (PreparedChat, error) {
	root, messages, scanned := splitChatBody(body)
	if !scanned {
		var err error
		if root, messages, err = decodeChatBody(body); err != nil {
			return PreparedChat{}, err
		}
	}
	for key := range root {
		if _, ok := allowedBodyKeys[key]; !ok {
			delete(root, key)
		}
	}
	var model string
	_ = json.Unmarshal(root["model"], &model)
	if model = strings.TrimSpace(model); model == "" {
		model = "auto"
	}

	for _, message := range messages {
		if rawRole(message) == "developer" {
			message["role"] = mustRaw("system")
		}
	}
	messages = leadWithSystem(messages)
	if IsInternational(profile) {
		merged, err := mergeIntlUserImages(messages)
		if err != nil {
			return PreparedChat{}, err
		}
		messages = merged
	}
	// Strip third-party coding-agent fingerprints from the system prompt so the
	// upstream channel risk control (code 11128) does not reject the request.
	desensitizeSystemMessages(messages)
	if IsDeepSeekModel(model) {
		if err := applyDeepSeekCompat(root, messages); err != nil {
			return PreparedChat{}, err
		}
	}

	if err := normalizeToolChoice(root); err != nil {
		return PreparedChat{}, err
	}
	if raw, ok := root["n"]; ok {
		var n float64
		if json.Unmarshal(raw, &n) == nil && n != 1 {
			return PreparedChat{}, requestErrorf("n must be 1")
		}
	}
	upstreamModel := UpstreamModel(model, profile)
	root["model"] = mustRaw(upstreamModel)
	root["stream"] = json.RawMessage("true")
	root["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	encoded, err := encodeChatBody(root, messages, len(body))
	if err != nil {
		return PreparedChat{}, err
	}
	return PreparedChat{Body: encoded, Model: upstreamModel}, nil
}

// decodeChatBody is the encoding/json split of a chat body: the top-level
// members, then the messages array, each a non-nil object.
func decodeChatBody(body []byte) (map[string]json.RawMessage, []map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, nil, requestErrorf("invalid chat completions body: %v", err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil || len(messages) == 0 {
		return nil, nil, requestErrorf("messages must be a non-empty array of objects")
	}
	for _, message := range messages {
		if message == nil {
			return nil, nil, requestErrorf("messages must be a non-empty array of objects")
		}
	}
	return root, messages, nil
}

// encodeChatBody encodes the prepared body: root with messages in place of its
// "messages" member.
func encodeChatBody(root map[string]json.RawMessage, messages []map[string]json.RawMessage, sizeHint int) ([]byte, error) {
	if encoded, ok := appendChatBody(root, messages, sizeHint); ok {
		return encoded, nil
	}
	encodedMessages, err := marshalNoEscape(messages)
	if err != nil {
		return nil, err
	}
	root["messages"] = encodedMessages
	return marshalNoEscape(root)
}

func leadWithSystem(messages []map[string]json.RawMessage) []map[string]json.RawMessage {
	if rawRole(messages[0]) == "system" {
		return messages
	}
	for i, message := range messages {
		if rawRole(message) == "system" {
			out := make([]map[string]json.RawMessage, 0, len(messages))
			out = append(out, message)
			out = append(out, messages[:i]...)
			return append(out, messages[i+1:]...)
		}
	}
	system := map[string]json.RawMessage{"role": mustRaw("system"), "content": mustRaw(DefaultSystemMessage)}
	return append([]map[string]json.RawMessage{system}, messages...)
}

func normalizeToolChoice(root map[string]json.RawMessage) error {
	raw, ok := root["tool_choice"]
	if !ok || len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var choice struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &choice); err != nil {
		return requestErrorf("invalid tool_choice")
	}
	name := choice.Name
	if choice.Function != nil {
		name = choice.Function.Name
	}
	var tools []json.RawMessage
	_ = json.Unmarshal(root["tools"], &tools)
	var matches []json.RawMessage
	for _, tool := range tools {
		var decl struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(tool, &decl) == nil && decl.Type == "function" && decl.Function.Name == name {
			matches = append(matches, tool)
		}
	}
	if choice.Type != "function" || strings.TrimSpace(name) == "" || len(matches) != 1 {
		return requestErrorf("tool_choice must name exactly one declared function")
	}
	encoded, err := marshalNoEscape(matches)
	if err != nil {
		return err
	}
	root["tools"] = encoded
	root["tool_choice"] = mustRaw("required")
	return nil
}

// mergeIntlUserImages merges runs of consecutive user messages when any of
// them carries an image: the international backend drops images otherwise.
// Runs never cross a system, assistant or tool boundary.
func mergeIntlUserImages(messages []map[string]json.RawMessage) ([]map[string]json.RawMessage, error) {
	out := make([]map[string]json.RawMessage, 0, len(messages))
	for i := 0; i < len(messages); {
		if rawRole(messages[i]) != "user" {
			out = append(out, messages[i])
			i++
			continue
		}
		end := i + 1
		for end < len(messages) && rawRole(messages[end]) == "user" {
			end++
		}
		run := messages[i:end]
		hasImage := false
		for _, message := range run {
			if contentHasImage(message["content"]) {
				hasImage = true
				break
			}
		}
		if len(run) < 2 || !hasImage {
			out = append(out, run...)
			i = end
			continue
		}
		attributes := otherFields(run[0])
		var parts []json.RawMessage
		for offset, message := range run {
			if !reflect.DeepEqual(otherFields(message), attributes) {
				return nil, requestErrorf("consecutive user messages with images cannot be merged losslessly; keep message attributes identical")
			}
			content, err := contentParts(message["content"])
			if err != nil {
				return nil, err
			}
			if offset > 0 {
				parts = append(parts, json.RawMessage(`{"type":"text","text":"\n\n"}`))
			}
			parts = append(parts, content...)
		}
		encoded, err := marshalNoEscape(parts)
		if err != nil {
			return nil, err
		}
		merged := make(map[string]json.RawMessage, len(run[0]))
		for key, value := range run[0] {
			merged[key] = value
		}
		merged["content"] = encoded
		out = append(out, merged)
		i = end
	}
	return out, nil
}

func contentHasImage(raw json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if part.Type == "image_url" {
			return true
		}
	}
	return false
}

func contentParts(raw json.RawMessage) ([]json.RawMessage, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		encoded, err := marshalNoEscape(map[string]string{"type": "text", "text": text})
		if err != nil {
			return nil, err
		}
		return []json.RawMessage{encoded}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, requestErrorf("user message content must be a string or an array of content parts")
	}
	for _, part := range parts {
		if len(bytes.TrimSpace(part)) == 0 || bytes.TrimSpace(part)[0] != '{' {
			return nil, requestErrorf("user message content must be a string or an array of content parts")
		}
	}
	return parts, nil
}

func otherFields(message map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for key, value := range message {
		if key == "role" || key == "content" {
			continue
		}
		out[key] = string(value)
	}
	return out
}

func rawRole(message map[string]json.RawMessage) string {
	var role string
	_ = json.Unmarshal(message["role"], &role)
	return role
}

func mustRaw(v string) json.RawMessage {
	encoded, _ := marshalNoEscape(v)
	return encoded
}

// marshalNoEscape matches JSON.stringify: no HTML escaping of <, > and &.
func marshalNoEscape(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
