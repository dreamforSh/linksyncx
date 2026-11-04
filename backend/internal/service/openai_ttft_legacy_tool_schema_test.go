//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// Keep this aligned with the gateway's default maximum request-body size. A
	// caller with a larger custom limit must not make this compatibility pass an
	// unbounded parser or patch accumulator.
	legacyTS_openAIResponsesToolSchemaMaxBodySize = 256 << 20
	legacyTS_openAIResponsesToolSchemaMaxDepth    = 128
	legacyTS_openAIResponsesToolSchemaMaxEdits    = 1 << 20
	legacyTS_openAIResponsesObjectUnionMaxSize    = 1 << 20
	legacyTS_openAIResponsesObjectUnionMaxDepth   = 32

	legacyTS_openAIResponsesToolSchemaFallbackType = `"object"`
)

var legacyTS_errOpenAIResponsesToolSchemaLimit = errors.New("OpenAI Responses tool schema safety limit exceeded")

// shouldRepairOpenAIResponsesNullToolSchemaType reports whether the upstream
// path requires a concrete object type at a function tool's parameter root.
// This defect is shared by the OpenAI, Anthropic, Grok, and CN-compatible paths.
func legacyTS_shouldRepairOpenAIResponsesNullToolSchemaType(platform string) bool {
	return platform == PlatformOpenAI || platform == PlatformAnthropic || platform == PlatformGrok || IsCNProvider(platform)
}

// shouldSanitizeOpenAIResponsesToolSchemaPatterns is intentionally narrower:
// regex lookaround rejection is an OpenAI-specific schema constraint.
func legacyTS_shouldSanitizeOpenAIResponsesToolSchemaPatterns(platform string) bool {
	return platform == PlatformOpenAI
}

func legacyTS_sanitizeOpenAIResponsesToolSchemasForPlatform(body []byte, platform string) ([]byte, bool, error) {
	normalized := body
	changed := false
	if legacyTS_shouldRepairOpenAIResponsesNullToolSchemaType(platform) {
		next, repaired, err := legacyTS_sanitizeOpenAIResponsesToolParameterTypes(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool parameters: %w", err)
		}
		if repaired {
			normalized = next
			changed = true
		}
	}
	if legacyTS_shouldSanitizeOpenAIResponsesToolSchemaPatterns(platform) {
		next, sanitized, err := legacyTS_sanitizeOpenAIResponsesToolSchemaPatterns(normalized)
		if err != nil {
			return body, false, fmt.Errorf("sanitize OpenAI Responses tool schema patterns: %w", err)
		}
		if sanitized {
			normalized = next
			changed = true
		}
	}
	return normalized, changed, nil
}

// sanitizeOpenAIResponsesToolSchemaPatterns removes only schema constraints
// containing regex lookaround, which OpenAI rejects. It deliberately does not
// descend into instance-valued keywords such as default, examples, const, or
// enum.
func legacyTS_sanitizeOpenAIResponsesToolSchemaPatterns(body []byte) ([]byte, bool, error) {
	if len(body) == 0 || !legacyTS_openAIResponsesBodyMayContainRegexLookaround(body) {
		return body, false, nil
	}
	return legacyTS_sanitizeOpenAIResponsesToolSchemas(body, legacyTS_openAIResponsesToolSchemaOptions{removeLookaroundPatterns: true})
}

// sanitizeOpenAIResponsesToolParameterTypes replaces an explicitly null type
// on a tool's parameter root with object. It also repairs the known Codex
// missing root type when it is unambiguously implied by an object-only
// oneOf/anyOf. Other missing types and nested property schemas are left
// unchanged.
func legacyTS_sanitizeOpenAIResponsesToolParameterTypes(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	return legacyTS_sanitizeOpenAIResponsesToolSchemas(body, legacyTS_openAIResponsesToolSchemaOptions{
		replaceNullParameterTypes:       true,
		injectObjectUnionRootObjectType: true,
		dropNullRequired:                true,
	})
}

func legacyTS_openAIResponsesBodyMayContainRegexLookaround(body []byte) bool {
	// An opening parenthesis may be literal or encoded as JSON's canonical
	// Unicode escape. Other lookaround characters may themselves be escaped, so
	// the full decoded pattern is checked only after scoped parsing.
	return bytes.Contains(body, []byte("(")) || bytes.Contains(body, []byte(`\u0028`))
}

func legacyTS_hasRegexLookaround(pattern string) bool {
	return strings.Contains(pattern, "(?=") || strings.Contains(pattern, "(?!") ||
		strings.Contains(pattern, "(?<=") || strings.Contains(pattern, "(?<!")
}

type legacyTS_openAIResponsesToolSchemaOptions struct {
	removeLookaroundPatterns        bool
	replaceNullParameterTypes       bool
	injectObjectUnionRootObjectType bool
	dropNullRequired                bool
}

type legacyTS_openAIResponsesToolSchemaContext uint8

const (
	legacyTS_openAIResponsesToolSchemaSkip legacyTS_openAIResponsesToolSchemaContext = iota
	legacyTS_openAIResponsesToolSchemaDocument
	legacyTS_openAIResponsesToolSchemaTools
	legacyTS_openAIResponsesToolSchemaTool
	legacyTS_openAIResponsesToolSchemaInput
	legacyTS_openAIResponsesToolSchemaInputItem
	legacyTS_openAIResponsesToolSchemaToolCarrier
	legacyTS_openAIResponsesToolSchemaTypeProbe
	legacyTS_openAIResponsesToolSchemaFunction
	legacyTS_openAIResponsesToolSchema
	legacyTS_openAIResponsesToolSchemaArray
	legacyTS_openAIResponsesToolSchemaMap
	legacyTS_openAIResponsesToolSchemaOrArray
)

type legacyTS_openAIResponsesToolSchemaEdit struct {
	start       int
	end         int
	replacement string
}

type legacyTS_openAIResponsesToolSchemaParser struct {
	body      []byte
	pos       int
	options   legacyTS_openAIResponsesToolSchemaOptions
	edits     []legacyTS_openAIResponsesToolSchemaEdit
	probeType string
}

func legacyTS_sanitizeOpenAIResponsesToolSchemas(
	body []byte, options legacyTS_openAIResponsesToolSchemaOptions,
) ([]byte, bool, error) {
	if len(body) > legacyTS_openAIResponsesToolSchemaMaxBodySize {
		return body, false, nil
	}
	if !utf8.Valid(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: invalid UTF-8")
	}
	p := legacyTS_openAIResponsesToolSchemaParser{body: body, options: options}
	if err := p.parseValue(legacyTS_openAIResponsesToolSchemaDocument, false, 0); err != nil {
		if errors.Is(err, legacyTS_errOpenAIResponsesToolSchemaLimit) {
			return body, false, nil
		}
		return nil, false, err
	}
	p.skipWhitespace()
	if p.pos != len(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: trailing data at byte %d", p.pos)
	}
	if !json.Valid(body) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: invalid JSON")
	}
	if len(p.edits) == 0 {
		return body, false, nil
	}
	return legacyTS_applyOpenAIResponsesToolSchemaEdits(body, p.edits)
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseValue(
	context legacyTS_openAIResponsesToolSchemaContext, schemaRoot bool, depth int,
) error {
	if depth > legacyTS_openAIResponsesToolSchemaMaxDepth {
		return legacyTS_errOpenAIResponsesToolSchemaLimit
	}
	p.skipWhitespace()
	if p.pos >= len(p.body) {
		return p.syntaxError("expected value")
	}
	if p.body[p.pos] == '{' {
		switch context {
		case legacyTS_openAIResponsesToolSchemaInput:
			context = legacyTS_openAIResponsesToolSchemaInputItem
		case legacyTS_openAIResponsesToolSchemaOrArray, legacyTS_openAIResponsesToolSchemaArray:
			context = legacyTS_openAIResponsesToolSchema
		}
	}
	if context == legacyTS_openAIResponsesToolSchemaInputItem && p.body[p.pos] == '{' {
		probe := legacyTS_openAIResponsesToolSchemaParser{body: p.body, pos: p.pos}
		if err := probe.parseValue(legacyTS_openAIResponsesToolSchemaTypeProbe, false, depth); err != nil {
			return err
		}
		switch probe.probeType {
		case "function", "custom", "tool_search", "namespace":
			context = legacyTS_openAIResponsesToolSchemaTool
		case "additional_tools":
			context = legacyTS_openAIResponsesToolSchemaToolCarrier
		default:
			context = legacyTS_openAIResponsesToolSchemaSkip
		}
	}
	switch p.body[p.pos] {
	case '{':
		return p.parseObject(context, schemaRoot, depth+1)
	case '[':
		return p.parseArray(context, depth+1)
	case '"':
		_, _, err := p.parseString()
		return err
	case 't':
		return p.parseKeyword("true")
	case 'f':
		return p.parseKeyword("false")
	case 'n':
		return p.parseKeyword("null")
	default:
		return p.parseNumber()
	}
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseObject(
	context legacyTS_openAIResponsesToolSchemaContext, schemaRoot bool, depth int,
) error {
	p.pos++
	p.skipWhitespace()
	if p.consume('}') {
		return nil
	}
	previousComma := -1
	deleteRunStart := -1
	deleteRunPreviousComma := -1
	rootTypeCount := 0
	nullRootTypeStart := -1
	nullRootTypeEnd := -1
	parameterCount := 0
	parameterValueStart := -1
	parameterValueEnd := -1
	for {
		p.skipWhitespace()
		memberStart := p.pos
		keyStart, keyEnd, err := p.parseString()
		if err != nil {
			return err
		}
		key := p.body[keyStart:keyEnd]
		p.skipWhitespace()
		if !p.consume(':') {
			return p.syntaxError("expected colon")
		}
		p.skipWhitespace()
		valueStart := p.pos

		childContext, childSchemaRoot := legacyTS_openAIResponsesToolSchemaChildContext(context, key)
		if err := p.parseValue(childContext, childSchemaRoot, depth); err != nil {
			return err
		}
		valueEnd := p.pos
		p.skipWhitespace()

		deleteMember := false
		if context == legacyTS_openAIResponsesToolSchemaTypeProbe && legacyTS_openAIResponsesJSONStringEquals(key, "type") {
			if value, ok := legacyTS_decodeOpenAIResponsesJSONStringValue(p.body[valueStart:valueEnd]); ok {
				p.probeType = strings.TrimSpace(value)
			}
		}
		if (context == legacyTS_openAIResponsesToolSchemaTool || context == legacyTS_openAIResponsesToolSchemaFunction) &&
			legacyTS_openAIResponsesJSONStringEquals(key, "parameters") {
			parameterCount++
			parameterValueStart = valueStart
			parameterValueEnd = valueEnd
		}
		if context == legacyTS_openAIResponsesToolSchema && legacyTS_openAIResponsesJSONStringEquals(key, "pattern") && p.options.removeLookaroundPatterns {
			if pattern, ok := legacyTS_decodeOpenAIResponsesJSONStringValue(p.body[valueStart:valueEnd]); ok && legacyTS_hasRegexLookaround(pattern) {
				deleteMember = true
			}
		}
		// JSON Schema requires "required" to be an array. Clients occasionally emit
		// null, which xAI and Moonshot reject outright with a 400. An absent
		// "required" is the semantic equivalent of an empty one, so dropping the
		// member preserves the client's intent while satisfying strict validators.
		if context == legacyTS_openAIResponsesToolSchema && p.options.dropNullRequired &&
			legacyTS_openAIResponsesJSONStringEquals(key, "required") &&
			bytes.Equal(p.body[valueStart:valueEnd], []byte("null")) {
			deleteMember = true
		}
		if context == legacyTS_openAIResponsesToolSchema && schemaRoot && legacyTS_openAIResponsesJSONStringEquals(key, "type") {
			// Duplicate JSON keys have parser-dependent effective values. Repair a
			// root type only when it is unique rather than choosing a winner and
			// potentially changing the client's effective schema.
			rootTypeCount++
			if bytes.Equal(p.body[valueStart:valueEnd], []byte("null")) {
				nullRootTypeStart = valueStart
				nullRootTypeEnd = valueEnd
			}
		}

		if p.pos >= len(p.body) {
			return p.syntaxError("unterminated object")
		}
		if deleteMember && deleteRunStart < 0 {
			deleteRunStart = memberStart
			deleteRunPreviousComma = previousComma
		}
		if !deleteMember && deleteRunStart >= 0 {
			// Remove through the delimiter after the deleted run, but retain the
			// whitespace that originally preceded the next member.
			if err := p.addEdit(deleteRunStart, previousComma+1, ""); err != nil {
				return err
			}
			deleteRunStart = -1
			deleteRunPreviousComma = -1
		}
		if p.body[p.pos] == ',' {
			comma := p.pos
			p.pos++
			previousComma = comma
			continue
		}
		if p.body[p.pos] != '}' {
			return p.syntaxError("expected comma or closing brace")
		}
		if deleteRunStart >= 0 {
			start := deleteRunStart
			if deleteRunPreviousComma >= 0 {
				start = deleteRunPreviousComma
			}
			if err := p.addEdit(start, p.pos, ""); err != nil {
				return err
			}
		}
		if p.options.replaceNullParameterTypes && rootTypeCount == 1 && nullRootTypeStart >= 0 {
			if err := p.addEdit(nullRootTypeStart, nullRootTypeEnd, legacyTS_openAIResponsesToolSchemaFallbackType); err != nil {
				return err
			}
		}
		if p.options.injectObjectUnionRootObjectType &&
			(context == legacyTS_openAIResponsesToolSchemaTool || context == legacyTS_openAIResponsesToolSchemaFunction) &&
			parameterCount == 1 && parameterValueStart >= 0 {
			if edit, ok := legacyTS_openAIMissingRootObjectUnionTypeEdit(
				p.body[parameterValueStart:parameterValueEnd], parameterValueStart,
			); ok {
				if err := p.addEdit(edit.start, edit.end, edit.replacement); err != nil {
					return err
				}
			}
		}
		p.pos++
		return nil
	}
}

var (
	legacyTS_openAIResponsesJSONOneOfKey     = []byte(`"oneOf"`)
	legacyTS_openAIResponsesJSONAnyOfKey     = []byte(`"anyOf"`)
	legacyTS_openAIResponsesJSONEscapeNeedle = []byte{'\\'}
)

func legacyTS_openAIMissingRootObjectUnionTypeEdit(raw []byte, absoluteStart int) (legacyTS_openAIResponsesToolSchemaEdit, bool) {
	if len(raw) > legacyTS_openAIResponsesObjectUnionMaxSize {
		return legacyTS_openAIResponsesToolSchemaEdit{}, false
	}
	if !bytes.Contains(raw, legacyTS_openAIResponsesJSONOneOfKey) && !bytes.Contains(raw, legacyTS_openAIResponsesJSONAnyOfKey) &&
		!bytes.Contains(raw, legacyTS_openAIResponsesJSONEscapeNeedle) {
		return legacyTS_openAIResponsesToolSchemaEdit{}, false
	}
	var schema map[string]json.RawMessage
	if len(raw) < 2 || raw[0] != '{' || json.Unmarshal(raw, &schema) != nil {
		return legacyTS_openAIResponsesToolSchemaEdit{}, false
	}
	if _, hasType := schema["type"]; hasType || !legacyTS_openAIResponsesSchemaHasObjectOnlyUnion(schema, 0) {
		return legacyTS_openAIResponsesToolSchemaEdit{}, false
	}
	return legacyTS_openAIResponsesToolSchemaEdit{
		start:       absoluteStart + 1,
		end:         absoluteStart + 1,
		replacement: `"type":"object",`,
	}, true
}

func legacyTS_openAIResponsesSchemaHasObjectOnlyUnion(schema map[string]json.RawMessage, depth int) bool {
	if depth > legacyTS_openAIResponsesObjectUnionMaxDepth {
		return false
	}
	found := false
	for _, keyword := range []string{"oneOf", "anyOf"} {
		raw, ok := schema[keyword]
		if !ok {
			continue
		}
		found = true
		var branches []json.RawMessage
		if json.Unmarshal(raw, &branches) != nil || len(branches) == 0 {
			return false
		}
		for _, branch := range branches {
			if !legacyTS_openAIResponsesSchemaIsObjectOnly(branch, depth+1) {
				return false
			}
		}
	}
	return found
}

func legacyTS_openAIResponsesSchemaIsObjectOnly(raw json.RawMessage, depth int) bool {
	if depth > legacyTS_openAIResponsesObjectUnionMaxDepth {
		return false
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(raw, &schema) != nil {
		return false
	}
	if rawType, ok := schema["type"]; ok {
		var schemaType string
		return json.Unmarshal(rawType, &schemaType) == nil && schemaType == "object"
	}
	return legacyTS_openAIResponsesSchemaHasObjectOnlyUnion(schema, depth+1)
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseArray(
	context legacyTS_openAIResponsesToolSchemaContext, depth int,
) error {
	p.pos++
	p.skipWhitespace()
	if p.consume(']') {
		return nil
	}
	childContext := legacyTS_openAIResponsesToolSchemaSkip
	switch context {
	case legacyTS_openAIResponsesToolSchemaTools:
		childContext = legacyTS_openAIResponsesToolSchemaTool
	case legacyTS_openAIResponsesToolSchemaInput:
		childContext = legacyTS_openAIResponsesToolSchemaInputItem
	case legacyTS_openAIResponsesToolSchemaArray, legacyTS_openAIResponsesToolSchemaOrArray:
		childContext = legacyTS_openAIResponsesToolSchema
	}
	for {
		if err := p.parseValue(childContext, false, depth); err != nil {
			return err
		}
		p.skipWhitespace()
		if p.consume(',') {
			continue
		}
		if !p.consume(']') {
			return p.syntaxError("expected comma or closing bracket")
		}
		return nil
	}
}

func legacyTS_openAIResponsesToolSchemaChildContext(
	context legacyTS_openAIResponsesToolSchemaContext, key []byte,
) (legacyTS_openAIResponsesToolSchemaContext, bool) {
	switch context {
	case legacyTS_openAIResponsesToolSchemaDocument:
		switch {
		case legacyTS_openAIResponsesJSONStringEquals(key, "tools"):
			return legacyTS_openAIResponsesToolSchemaTools, false
		case legacyTS_openAIResponsesJSONStringEquals(key, "input"):
			return legacyTS_openAIResponsesToolSchemaInput, false
		}
	case legacyTS_openAIResponsesToolSchemaTool:
		switch {
		// Anthropic Messages carries the same JSON Schema under input_schema, so
		// /v1/messages tools must reach the sanitizer too.
		case legacyTS_openAIResponsesJSONStringMatchesAny(key, "parameters", "input_schema"):
			return legacyTS_openAIResponsesToolSchema, true
		case legacyTS_openAIResponsesJSONStringEquals(key, "function"):
			return legacyTS_openAIResponsesToolSchemaFunction, false
		case legacyTS_openAIResponsesJSONStringEquals(key, "tools"):
			return legacyTS_openAIResponsesToolSchemaTools, false
		}
	case legacyTS_openAIResponsesToolSchemaToolCarrier:
		if legacyTS_openAIResponsesJSONStringEquals(key, "tools") {
			return legacyTS_openAIResponsesToolSchemaTools, false
		}
	case legacyTS_openAIResponsesToolSchemaFunction:
		switch {
		case legacyTS_openAIResponsesJSONStringEquals(key, "parameters"):
			return legacyTS_openAIResponsesToolSchema, true
		case legacyTS_openAIResponsesJSONStringEquals(key, "tools"):
			return legacyTS_openAIResponsesToolSchemaTools, false
		}
	case legacyTS_openAIResponsesToolSchema:
		switch {
		case legacyTS_openAIResponsesJSONStringMatchesAny(key,
			"additionalProperties", "additionalItems", "contains", "not", "if", "then", "else",
			"propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema"):
			return legacyTS_openAIResponsesToolSchema, false
		case legacyTS_openAIResponsesJSONStringEquals(key, "items"):
			return legacyTS_openAIResponsesToolSchemaOrArray, false
		case legacyTS_openAIResponsesJSONStringMatchesAny(key, "anyOf", "oneOf", "allOf", "prefixItems"):
			return legacyTS_openAIResponsesToolSchemaArray, false
		case legacyTS_openAIResponsesJSONStringMatchesAny(key,
			"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"):
			return legacyTS_openAIResponsesToolSchemaMap, false
		}
	case legacyTS_openAIResponsesToolSchemaMap:
		return legacyTS_openAIResponsesToolSchema, false
	}
	return legacyTS_openAIResponsesToolSchemaSkip, false
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseString() (int, int, error) {
	if p.pos >= len(p.body) || p.body[p.pos] != '"' {
		return 0, 0, p.syntaxError("expected string")
	}
	start := p.pos
	p.pos++
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case '"':
			p.pos++
			return start, p.pos, nil
		case '\\':
			p.pos++
			if p.pos >= len(p.body) {
				return 0, 0, p.syntaxError("unterminated string escape")
			}
			switch p.body[p.pos] {
			case 'u':
				if p.pos+4 >= len(p.body) {
					return 0, 0, p.syntaxError("short unicode escape")
				}
				for _, digit := range p.body[p.pos+1 : p.pos+5] {
					if !legacyTS_isOpenAIResponsesJSONHexDigit(digit) {
						return 0, 0, p.syntaxError("invalid unicode escape")
					}
				}
				p.pos += 5
				continue
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.pos++
			default:
				return 0, 0, p.syntaxError("invalid string escape")
			}
		default:
			if p.body[p.pos] < 0x20 {
				return 0, 0, p.syntaxError("control character in string")
			}
			p.pos++
		}
	}
	return 0, 0, p.syntaxError("unterminated string")
}

func legacyTS_isOpenAIResponsesJSONHexDigit(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseKeyword(keyword string) error {
	if len(p.body)-p.pos < len(keyword) || string(p.body[p.pos:p.pos+len(keyword)]) != keyword {
		return p.syntaxError("invalid literal")
	}
	p.pos += len(keyword)
	return nil
}

func (p *legacyTS_openAIResponsesToolSchemaParser) parseNumber() error {
	start := p.pos
	if p.consume('-') && p.pos >= len(p.body) {
		return p.syntaxError("invalid number")
	}
	if p.consume('0') {
		if p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			return p.syntaxError("invalid leading zero")
		}
	} else {
		if p.pos >= len(p.body) || p.body[p.pos] < '1' || p.body[p.pos] > '9' {
			return p.syntaxError("invalid number")
		}
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.consume('.') {
		fractionStart := p.pos
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == fractionStart {
			return p.syntaxError("invalid fraction")
		}
	}
	if p.pos < len(p.body) && (p.body[p.pos] == 'e' || p.body[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.body) && (p.body[p.pos] == '+' || p.body[p.pos] == '-') {
			p.pos++
		}
		exponentStart := p.pos
		for p.pos < len(p.body) && p.body[p.pos] >= '0' && p.body[p.pos] <= '9' {
			p.pos++
		}
		if p.pos == exponentStart {
			return p.syntaxError("invalid exponent")
		}
	}
	if p.pos == start {
		return p.syntaxError("invalid value")
	}
	return nil
}

func (p *legacyTS_openAIResponsesToolSchemaParser) addEdit(start, end int, replacement string) error {
	if start < 0 || end < start || end > len(p.body) {
		return p.syntaxError("invalid patch span")
	}
	if len(p.edits) >= legacyTS_openAIResponsesToolSchemaMaxEdits {
		return legacyTS_errOpenAIResponsesToolSchemaLimit
	}
	p.edits = append(p.edits, legacyTS_openAIResponsesToolSchemaEdit{start: start, end: end, replacement: replacement})
	return nil
}

func (p *legacyTS_openAIResponsesToolSchemaParser) skipWhitespace() {
	for p.pos < len(p.body) {
		switch p.body[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *legacyTS_openAIResponsesToolSchemaParser) consume(want byte) bool {
	if p.pos < len(p.body) && p.body[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func (p *legacyTS_openAIResponsesToolSchemaParser) syntaxError(message string) error {
	return fmt.Errorf("sanitize OpenAI Responses tool schemas: %s at byte %d", message, p.pos)
}

func legacyTS_decodeOpenAIResponsesJSONString(raw []byte) (string, error) {
	decoded, err := strconv.Unquote(string(raw))
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func legacyTS_openAIResponsesJSONStringEquals(raw []byte, want string) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	if !bytes.Contains(raw, []byte{'\\'}) {
		return bytes.Equal(raw[1:len(raw)-1], []byte(want))
	}
	decoded, err := legacyTS_decodeOpenAIResponsesJSONString(raw)
	return err == nil && decoded == want
}

func legacyTS_openAIResponsesJSONStringMatchesAny(raw []byte, candidates ...string) bool {
	for _, candidate := range candidates {
		if legacyTS_openAIResponsesJSONStringEquals(raw, candidate) {
			return true
		}
	}
	return false
}

func legacyTS_decodeOpenAIResponsesJSONStringValue(raw []byte) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	decoded, err := legacyTS_decodeOpenAIResponsesJSONString(raw)
	return decoded, err == nil
}

func legacyTS_applyOpenAIResponsesToolSchemaEdits(
	body []byte, edits []legacyTS_openAIResponsesToolSchemaEdit,
) ([]byte, bool, error) {
	sort.Slice(edits, func(i, j int) bool {
		if edits[i].start != edits[j].start {
			return edits[i].start < edits[j].start
		}
		return edits[i].end < edits[j].end
	})

	merged := edits[:0]
	for _, edit := range edits {
		if len(merged) == 0 || edit.start >= merged[len(merged)-1].end {
			merged = append(merged, edit)
			continue
		}
		previous := &merged[len(merged)-1]
		if previous.replacement != "" || edit.replacement != "" {
			return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: overlapping patches")
		}
		if edit.end > previous.end {
			previous.end = edit.end
		}
	}

	delta := 0
	for _, edit := range merged {
		delta += len(edit.replacement) - (edit.end - edit.start)
	}
	sanitized := make([]byte, 0, len(body)+delta)
	cursor := 0
	for _, edit := range merged {
		sanitized = append(sanitized, body[cursor:edit.start]...)
		sanitized = append(sanitized, edit.replacement...)
		cursor = edit.end
	}
	sanitized = append(sanitized, body[cursor:]...)
	if !json.Valid(sanitized) {
		return nil, false, fmt.Errorf("sanitize OpenAI Responses tool schemas: patch produced invalid JSON")
	}
	return sanitized, true, nil
}
