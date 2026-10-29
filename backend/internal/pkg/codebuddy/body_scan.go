package codebuddy

import (
	"bytes"
	"encoding/json"
	"sort"
	"unsafe"
)

// PrepareChat runs before every CodeBuddy upstream request, and replayed agent
// conversations reach several megabytes. Decoding the body into maps and
// encoding it back with encoding/json walks all of it four times, which adds
// tens of milliseconds to time-to-first-token. The scan below splits the body
// in one pass after a single validity check, and appendChatBody writes what
// marshalNoEscape would: map keys in byte order, every raw value with its
// insignificant whitespace removed. A key that is not plain printable ASCII
// (escapes, non-ASCII), or U+2028/U+2029 in a value (the encoder escapes
// them), falls back to encoding/json, so the result is the same either way.

// splitChatBody decodes body as json.Unmarshal would into a
// map[string]json.RawMessage, and its "messages" member into a
// []map[string]json.RawMessage: the last duplicate key wins and values keep
// their exact bytes (they alias body). ok=false when the body is invalid or
// any key or message falls outside the plain shape; decodeChatBody then
// decides, and reports errors.
func splitChatBody(body []byte) (map[string]json.RawMessage, []map[string]json.RawMessage, bool) {
	if !json.Valid(body) {
		return nil, nil, false
	}
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, nil, false
	}
	root := make(map[string]json.RawMessage, 16)
	var messages []map[string]json.RawMessage
	_, ok := scanJSONObject(body, i, func(key string, start int) (int, bool) {
		end := 0
		if key == "messages" {
			// Split the history while finding its end; a later duplicate
			// replaces it, as it replaces the raw member.
			messages, end = nil, -1
			if body[start] == '[' {
				messages, end = scanChatMessages(body, start)
			}
		} else {
			end = jsonValueEnd(body, start)
		}
		if end <= start {
			return 0, false
		}
		root[key] = body[start:end]
		return end, true
	})
	if !ok || len(messages) == 0 {
		return nil, nil, false
	}
	return root, messages, true
}

// scanChatMessages splits the messages array starting at body[start] into
// one map per element. Every element must be an object with plain keys.
func scanChatMessages(body []byte, start int) ([]map[string]json.RawMessage, int) {
	messages := make([]map[string]json.RawMessage, 0, 16)
	for i := start + 1; i < len(body); {
		switch body[i] {
		case ']':
			return messages, i + 1
		case ' ', '\t', '\n', '\r', ',':
			i++
			continue
		case '{':
		default:
			return nil, -1
		}
		message := make(map[string]json.RawMessage, 4)
		end, ok := scanJSONObject(body, i, func(key string, valueStart int) (int, bool) {
			valueEnd := jsonValueEnd(body, valueStart)
			if valueEnd <= valueStart {
				return 0, false
			}
			message[key] = body[valueStart:valueEnd]
			return valueEnd, true
		})
		if !ok {
			return nil, -1
		}
		messages = append(messages, message)
		i = end
	}
	return nil, -1
}

// scanJSONObject walks the members of the valid object at b[start]. member
// receives each plain key and the offset of its value, and returns the offset
// just past the value. Any other key stops the walk with ok=false.
func scanJSONObject(b []byte, start int, member func(key string, valueStart int) (int, bool)) (int, bool) {
	for i := start + 1; i < len(b); {
		switch b[i] {
		case '}':
			return i + 1, true
		case ' ', '\t', '\n', '\r', ',':
			i++
			continue
		case '"':
		default:
			return 0, false
		}
		keyEnd := jsonStringEnd(b, i)
		if keyEnd < 0 || !plainJSONKey(bytesString(b[i+1:keyEnd-1])) {
			return 0, false
		}
		key := internJSONKey(b[i+1 : keyEnd-1])
		i = skipJSONSpace(b, keyEnd)
		if i >= len(b) || b[i] != ':' {
			return 0, false
		}
		i = skipJSONSpace(b, i+1)
		if i >= len(b) {
			return 0, false
		}
		end, ok := member(key, i)
		if !ok {
			return 0, false
		}
		i = end
	}
	return 0, false
}

// plainJSONKey reports a key that encoding/json decodes to its own bytes and
// writes back unchanged: printable ASCII without quote or backslash.
func plainJSONKey(key string) bool {
	for i := 0; i < len(key); i++ {
		if c := key[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

// bytesString views b as a string without copying; b must not change while
// the string is in use.
func bytesString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// internJSONKey returns the key as a string, sharing the common chat keys
// instead of allocating one per message.
func internJSONKey(key []byte) string {
	switch string(key) {
	case "role":
		return "role"
	case "content":
		return "content"
	case "name":
		return "name"
	case "tool_calls":
		return "tool_calls"
	case "tool_call_id":
		return "tool_call_id"
	case "reasoning_content":
		return "reasoning_content"
	}
	return string(key)
}

// appendChatBody writes root, with messages as its "messages" member, exactly
// as marshalNoEscape would. ok=false when a key or value needs the encoder.
func appendChatBody(root map[string]json.RawMessage, messages []map[string]json.RawMessage, sizeHint int) ([]byte, bool) {
	out := make([]byte, 0, sizeHint+256)
	out = append(out, '{')
	keys, ok := sortedPlainKeys(root)
	if !ok {
		return nil, false
	}
	for n, key := range keys {
		if n > 0 {
			out = append(out, ',')
		}
		out = appendPlainJSONKey(out, key)
		if key == "messages" {
			out, ok = appendChatMessages(out, messages)
		} else {
			out, ok = appendCompactJSON(out, root[key])
		}
		if !ok {
			return nil, false
		}
	}
	return append(out, '}'), true
}

func appendChatMessages(out []byte, messages []map[string]json.RawMessage) ([]byte, bool) {
	out = append(out, '[')
	for n, message := range messages {
		if n > 0 {
			out = append(out, ',')
		}
		if message == nil {
			return nil, false
		}
		keys, ok := sortedPlainKeys(message)
		if !ok {
			return nil, false
		}
		out = append(out, '{')
		for m, key := range keys {
			if m > 0 {
				out = append(out, ',')
			}
			out = appendPlainJSONKey(out, key)
			if out, ok = appendCompactJSON(out, message[key]); !ok {
				return nil, false
			}
		}
		out = append(out, '}')
	}
	return append(out, ']'), true
}

// sortedPlainKeys returns the map keys in encoding/json's order (bytewise),
// or ok=false when one of them is not a plain key.
func sortedPlainKeys(m map[string]json.RawMessage) ([]string, bool) {
	keys := make([]string, 0, len(m))
	for key := range m {
		if !plainJSONKey(key) {
			return nil, false
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, true
}

func appendPlainJSONKey(out []byte, key string) []byte {
	out = append(out, '"')
	out = append(out, key...)
	return append(out, '"', ':')
}

var (
	lineSeparator      = []byte(string(rune(0x2028)))
	paragraphSeparator = []byte(string(rune(0x2029)))
)

// appendCompactJSON appends the valid JSON value raw the way encoding/json
// writes a json.RawMessage: whitespace outside strings is dropped and string
// bytes are kept as they are. U+2028 and U+2029, which the encoder escapes,
// return ok=false.
func appendCompactJSON(out, raw []byte) ([]byte, bool) {
	if len(raw) == 0 || bytes.Contains(raw, lineSeparator) || bytes.Contains(raw, paragraphSeparator) {
		return nil, false
	}
	copied := 0
	for i := 0; i < len(raw); {
		switch raw[i] {
		case '"':
			end := jsonStringEnd(raw, i)
			if end < 0 {
				return nil, false
			}
			i = end
		case ' ', '\t', '\n', '\r':
			out = append(out, raw[copied:i]...)
			for i < len(raw) && (raw[i] == ' ' || raw[i] == '\t' || raw[i] == '\n' || raw[i] == '\r') {
				i++
			}
			copied = i
		default:
			i++
		}
	}
	return append(out, raw[copied:]...), true
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// jsonStringEnd returns the offset just past the string literal opening at
// b[i], or -1 when it is unterminated.
func jsonStringEnd(b []byte, i int) int {
	for j := i + 1; j < len(b); {
		k := bytes.IndexByte(b[j:], '"')
		if k < 0 {
			return -1
		}
		quote := j + k
		// An odd run of backslashes escapes the quote.
		backslashes := 0
		for p := quote - 1; p > i && b[p] == '\\'; p-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1
		}
		j = quote + 1
	}
	return -1
}

// jsonValueEnd returns the offset just past the valid JSON value starting at
// b[i], or a value not greater than i when none starts there.
func jsonValueEnd(b []byte, i int) int {
	if i >= len(b) {
		return -1
	}
	switch b[i] {
	case '"':
		return jsonStringEnd(b, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(b); j++ {
			switch b[j] {
			case '"':
				end := jsonStringEnd(b, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		j := i
		for j < len(b) && b[j] != ',' && b[j] != '}' && b[j] != ']' && b[j] != ' ' && b[j] != '\t' && b[j] != '\n' && b[j] != '\r' {
			j++
		}
		return j
	}
}
