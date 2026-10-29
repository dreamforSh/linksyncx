package service

import "strings"

// Grok's Responses pipeline runs on every turn before the upstream request is
// sent, and several stages decode the whole request into maps only to look for
// shapes most bodies never carry. On a multi-megabyte replayed conversation
// each decode-and-rewrite pass adds tens of milliseconds to time-to-first-token.
// The scans below answer "could this stage change anything?" without decoding
// values: keys are read the way encoding/json reads them (json_scan.go), every
// occurrence of a duplicated key is inspected, and a value spelled through
// \u escapes always falls back to the full stage. A stage is skipped only when
// its decode-and-rewrite would have returned the body unchanged.

// grokResponsesClientToolsNeedAdaptation reports whether
// adaptGrokResponsesClientTools could rewrite body: client-only tool
// declarations (custom, tool_search, namespace), defer_loading flags, or
// custom_tool_call_output history, which is lowered whenever tools are
// declared. Bodies without a tools array never change.
func grokResponsesClientToolsNeedAdaptation(body []byte) bool {
	root, ok := jsonRootObject(bytesView(body))
	if !ok {
		return true
	}
	tools, toolsPresent := "", false
	var inputs []string
	forEachJSONMemberRaw(root.Raw, func(key, raw string) bool {
		switch key {
		case "tools":
			// encoding/json keeps the last duplicate.
			tools, toolsPresent = raw, true
		case "input":
			inputs = append(inputs, raw)
		}
		return true
	})
	if !toolsPresent || !strings.HasPrefix(tools, "[") {
		return false
	}
	declared, needs := false, false
	forEachJSONArrayElementRaw(tools, func(raw string) bool {
		declared = true
		if !strings.HasPrefix(raw, "{") {
			return true
		}
		toolType := ""
		forEachJSONMemberRaw(raw, func(key, value string) bool {
			switch key {
			case "type":
				// Non-string types decode to "", as stringValue reads them.
				toolType, _ = decodeJSONStringLiteral(value)
			case "defer_loading":
				needs = true
			}
			return true
		})
		switch strings.TrimSpace(toolType) {
		case "custom", "tool_search", "namespace":
			needs = true
		}
		return !needs
	})
	if needs || !declared {
		return needs
	}
	for _, input := range inputs {
		if strings.Contains(input, "custom_tool_call_output") || grokJSONMayEscapeIdentifier(input) {
			return true
		}
	}
	return false
}

// grokResponsesInputMayContainCompaction reports whether
// convertOpenAICompactInputsForGrok could find an OpenAI compaction item in the
// top-level input of body.
func grokResponsesInputMayContainCompaction(body []byte) bool {
	return anyGrokResponsesInputMember(body, func(raw string) bool {
		return strings.Contains(raw, "compaction") || grokJSONMayEscapeIdentifier(raw)
	})
}

// grokResponsesInputHasNullLiteral reports whether the top-level input of body
// carries an explicit JSON null for sanitizeGrokReasoningNullContent to drop.
func grokResponsesInputHasNullLiteral(body []byte) bool {
	return anyGrokResponsesInputMember(body, grokJSONHasNullLiteral)
}

// anyGrokResponsesInputMember reports whether match holds for any top-level
// input member of body. A body that is not a JSON object always matches, so
// the caller's full stage reports it.
func anyGrokResponsesInputMember(body []byte, match func(raw string) bool) bool {
	root, ok := jsonRootObject(bytesView(body))
	if !ok {
		return true
	}
	found := false
	forEachJSONMemberRaw(root.Raw, func(key, raw string) bool {
		if key == "input" && match(raw) {
			found = true
		}
		return !found
	})
	return found
}

// grokJSONHasNullLiteral reports whether raw contains a null literal outside
// string literals. Outside strings, the letter n can only start null; malformed
// input reports true.
func grokJSONHasNullLiteral(raw string) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '"':
			end := jsonStringLiteralEnd(raw, i)
			if end < 0 {
				return true
			}
			i = end - 1
		case 'n':
			return true
		}
	}
	return false
}

// grokJSONMayEscapeIdentifier reports whether raw contains a \u escape for a
// lowercase ASCII letter or underscore: such an escape can spell a type name
// that a plain byte search for it would miss. Encoders do not escape these
// characters, so real request bodies practically never take this path.
func grokJSONMayEscapeIdentifier(raw string) bool {
	for i := 0; ; {
		j := strings.Index(raw[i:], `\u00`)
		if j < 0 {
			return false
		}
		i += j + len(`\u00`)
		if i+2 > len(raw) {
			return false
		}
		high, highOK := grokHexDigitValue(raw[i])
		low, lowOK := grokHexDigitValue(raw[i+1])
		if !highOK || !lowOK {
			continue
		}
		if c := high<<4 | low; c == '_' || ('a' <= c && c <= 'z') {
			return true
		}
	}
}

func grokHexDigitValue(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// forEachJSONArrayElementRaw calls fn with the raw text of each element of the
// JSON array arr, without decoding string values. arr must be a valid array;
// malformed input only guarantees termination.
func forEachJSONArrayElementRaw(arr string, fn func(raw string) bool) {
	i := strings.IndexByte(arr, '[') + 1
	if i == 0 {
		return
	}
	for i < len(arr) {
		switch arr[i] {
		case ']':
			return
		case ' ', '\t', '\n', '\r', ',':
			i++
			continue
		}
		end := jsonValueEnd(arr, i)
		if end <= i {
			return
		}
		if !fn(arr[i:end]) {
			return
		}
		i = end
	}
}
