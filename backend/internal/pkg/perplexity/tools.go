package perplexity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// 本文件实现客户端函数工具（function tool call）的提示词协议模拟。
//
// Perplexity 网页 Ask 端点没有原生函数调用能力，网关侧做三件事：
//  1. 把客户端声明的 function 工具渲染为查询头部的文本协议说明；
//  2. 把 assistant.tool_calls / role=tool 历史折叠为 <tool_call> / <tool_result> 文本；
//  3. 在上游回答中识别 <tool_call>{...}</tool_call> 块，还原为 CC 协议 tool_calls。
//
// 这是模拟能力，不是上游原生函数调用：格式遵从度取决于所选模型，解析失败时整段
// 按普通文本下发，绝不伪造调用。上游内部步骤（ToolNameForStep 的 pplx_ 命名空间）
// 与客户端工具隔离，客户端不能声明 pplx_ 前缀的工具名。

const (
	// ToolCallOpenTag / ToolCallCloseTag 是模型输出工具调用时使用的标签。
	ToolCallOpenTag  = "<tool_call>"
	ToolCallCloseTag = "</tool_call>"

	// MaxEmulatedTools 是单次请求允许声明的函数工具上限。工具说明会占用 query 预算，
	// 过多工具会显著降低格式遵从度。
	MaxEmulatedTools = 64

	// MaxToolCallsPerTurn 是单轮回答允许解析出的工具调用数上限。
	MaxToolCallsPerTurn = 16

	reservedToolPrefix     = "pplx_"
	maxToolNameLen         = 64
	maxToolDescriptionLen  = 1024
	maxToolCallBufferBytes = 64 * 1024
	defaultToolParameters  = `{"type":"object","properties":{}}`
)

// ToolSpec 是已校验的客户端函数工具声明（CC function 形态）。
type ToolSpec struct {
	Name        string
	Description string
	// Parameters 是紧凑化的 JSON Schema 对象。
	Parameters string
}

// ToolCall 是从上游回答中还原出的一次客户端工具调用。
type ToolCall struct {
	ID   string
	Name string
	// Arguments 是紧凑化的 JSON 对象字符串（与 CC function.arguments 语义一致）。
	Arguments string
}

// ToolChoiceMode 是归一化后的 tool_choice 模式。
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceFunction ToolChoiceMode = "function"
)

// ToolChoice 是归一化后的 tool_choice。Mode 为 function 时 Name 为指定工具名。
type ToolChoice struct {
	Mode ToolChoiceMode
	Name string
}

// IsNativeWebSearchToolType 报告 tool.type 是否为可映射到 Perplexity 原生搜索的类型。
func IsNativeWebSearchToolType(kind string) bool {
	switch kind {
	case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_20250305", "web_search_20260209":
		return true
	}
	return false
}

// IsValidToolName 校验工具名：1–64 个字母/数字/下划线/连字符，且不使用保留前缀。
func IsValidToolName(name string) bool {
	if name == "" || len(name) > maxToolNameLen || strings.HasPrefix(strings.ToLower(name), reservedToolPrefix) {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}

// ParseToolSpecs 从 CC 请求体的 tools 数组解析函数工具。原生 web_search 声明被跳过
// （由搜索设置映射处理）；其他未知类型明确报错。
func ParseToolSpecs(tools gjson.Result) ([]ToolSpec, error) {
	if !tools.Exists() || tools.Type == gjson.Null {
		return nil, nil
	}
	if !tools.IsArray() {
		return nil, fmt.Errorf("tools must be an array")
	}
	seen := make(map[string]bool)
	var specs []ToolSpec
	for i, tool := range tools.Array() {
		if !tool.IsObject() {
			return nil, fmt.Errorf("tools[%d] must be an object", i)
		}
		kind := tool.Get("type").String()
		if IsNativeWebSearchToolType(kind) {
			continue
		}
		if kind != "function" {
			return nil, fmt.Errorf("tools[%d].type %q cannot be emulated for Perplexity; only function tools are supported", i, kind)
		}
		fn := tool.Get("function")
		if !fn.IsObject() {
			return nil, fmt.Errorf("tools[%d].function must be an object", i)
		}
		name := fn.Get("name").String()
		if !IsValidToolName(name) {
			return nil, fmt.Errorf("tools[%d].function.name %q is invalid or uses the reserved %q prefix", i, name, reservedToolPrefix)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate function tool %q", name)
		}
		seen[name] = true
		params := defaultToolParameters
		if p := fn.Get("parameters"); p.Exists() && p.Type != gjson.Null {
			if !p.IsObject() {
				return nil, fmt.Errorf("tools[%d].function.parameters must be a JSON Schema object", i)
			}
			params = p.Raw
		}
		compacted, err := compactJSON(params)
		if err != nil {
			return nil, fmt.Errorf("tools[%d].function.parameters: %w", i, err)
		}
		specs = append(specs, ToolSpec{
			Name:        name,
			Description: oneLine(fn.Get("description").String(), maxToolDescriptionLen),
			Parameters:  compacted,
		})
	}
	if len(specs) > MaxEmulatedTools {
		return nil, fmt.Errorf("at most %d function tools can be emulated for Perplexity", MaxEmulatedTools)
	}
	return specs, nil
}

// ParseToolChoice 归一化 CC / Anthropic 风格的 tool_choice。
func ParseToolChoice(choice gjson.Result, specs []ToolSpec) (ToolChoice, error) {
	if !choice.Exists() || choice.Type == gjson.Null {
		return ToolChoice{Mode: ToolChoiceAuto}, nil
	}
	kind := ""
	name := ""
	switch {
	case choice.Type == gjson.String:
		kind = choice.String()
	case choice.IsObject():
		kind = choice.Get("type").String()
		name = choice.Get("function.name").String()
		if name == "" {
			name = choice.Get("name").String()
		}
	default:
		return ToolChoice{}, fmt.Errorf("invalid tool_choice")
	}
	switch kind {
	case "auto":
		return ToolChoice{Mode: ToolChoiceAuto}, nil
	case "none":
		return ToolChoice{Mode: ToolChoiceNone}, nil
	case "required", "any":
		if len(specs) == 0 {
			return ToolChoice{}, fmt.Errorf("tool_choice=%s requires at least one function tool", kind)
		}
		return ToolChoice{Mode: ToolChoiceRequired}, nil
	case "function", "tool":
		for _, spec := range specs {
			if spec.Name == name {
				return ToolChoice{Mode: ToolChoiceFunction, Name: name}, nil
			}
		}
		return ToolChoice{}, fmt.Errorf("tool_choice names undeclared function %q", name)
	}
	return ToolChoice{}, fmt.Errorf("unsupported tool_choice %q", kind)
}

// AllowedSpecs 返回本轮允许模型调用的工具。
func (c ToolChoice) AllowedSpecs(specs []ToolSpec) []ToolSpec {
	switch c.Mode {
	case ToolChoiceNone:
		return nil
	case ToolChoiceFunction:
		for _, spec := range specs {
			if spec.Name == c.Name {
				return []ToolSpec{spec}
			}
		}
		return nil
	}
	return specs
}

// Satisfied 报告解析出的调用是否满足 tool_choice 约束。调用方在不满足时应返回
// 明确错误，而不是伪造调用。
func (c ToolChoice) Satisfied(calls []ToolCall) bool {
	switch c.Mode {
	case ToolChoiceRequired:
		return len(calls) > 0
	case ToolChoiceFunction:
		if len(calls) == 0 {
			return false
		}
		for _, call := range calls {
			if call.Name != c.Name {
				return false
			}
		}
		return true
	case ToolChoiceNone:
		return len(calls) == 0
	}
	return true
}

// RenderToolInstructions 渲染查询头部的工具协议说明；无可调用工具时返回空串。
func RenderToolInstructions(specs []ToolSpec, choice ToolChoice) string {
	allowed := choice.AllowedSpecs(specs)
	if len(allowed) == 0 {
		return ""
	}
	var sb strings.Builder
	_, _ = sb.WriteString("[Tool protocol]\n")
	_, _ = sb.WriteString("You can call the client tools listed below. The client runs them and returns results in <tool_result> blocks.\n")
	_, _ = sb.WriteString("To call tools, reply with ONLY one or more lines in exactly this format and no other text:\n")
	_, _ = sb.WriteString(`<tool_call>{"name":"TOOL_NAME","arguments":{...}}</tool_call>` + "\n")
	_, _ = sb.WriteString("The arguments must be a JSON object matching the tool's JSON Schema. Never invent tool results.\n")
	switch choice.Mode {
	case ToolChoiceRequired:
		_, _ = sb.WriteString("You MUST call at least one tool in this reply.\n")
	case ToolChoiceFunction:
		_, _ = fmt.Fprintf(&sb, "You MUST call the tool %q in this reply.\n", choice.Name)
	default:
		_, _ = sb.WriteString("Call a tool only when it is needed; otherwise answer normally in plain text.\n")
	}
	_, _ = sb.WriteString("\nTools:\n")
	for _, spec := range allowed {
		_, _ = sb.WriteString("- ")
		_, _ = sb.WriteString(spec.Name)
		if spec.Description != "" {
			_, _ = sb.WriteString(": ")
			_, _ = sb.WriteString(spec.Description)
		}
		_, _ = sb.WriteString("\n  parameters: ")
		_, _ = sb.WriteString(spec.Parameters)
		_, _ = sb.WriteString("\n")
	}
	_, _ = sb.WriteString("[End of tool protocol]")
	return sb.String()
}

// NormalizeToolHistory 把 CC 消息中的工具历史改写为纯文本消息，使其可被
// FoldMessagesToQueryWithOptions 折叠：
//   - assistant.tool_calls / function_call → assistant 文本中的 <tool_call> 行
//   - role=tool / role=function → user 文本中的 <tool_result> 块
//
// changed 报告是否发生了改写。其余消息只保留 role 与 content。
func NormalizeToolHistory(messages gjson.Result) (normalized []byte, changed bool, err error) {
	if !messages.IsArray() {
		return nil, false, fmt.Errorf("messages is not an array")
	}
	names := make(map[string]string)
	out := make([]map[string]any, 0, len(messages.Array()))
	for i, msg := range messages.Array() {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		calls := msg.Get("tool_calls")
		hasCalls := calls.IsArray() && len(calls.Array()) > 0
		switch {
		case role == "tool" || role == "function":
			text, tErr := toolContentText(msg.Get("content"))
			if tErr != nil {
				return nil, false, fmt.Errorf("messages[%d]: %w", i, tErr)
			}
			id := msg.Get("tool_call_id").String()
			name := msg.Get("name").String()
			if name == "" {
				name = names[id]
			}
			out = append(out, map[string]any{"role": "user", "content": renderToolResult(name, id, text)})
			changed = true
		case role == "assistant" && (hasCalls || msg.Get("function_call").Exists()):
			text, tErr := toolContentText(msg.Get("content"))
			if tErr != nil {
				return nil, false, fmt.Errorf("messages[%d]: %w", i, tErr)
			}
			var sb strings.Builder
			if t := strings.TrimSpace(text); t != "" {
				_, _ = sb.WriteString(t)
				_, _ = sb.WriteString("\n")
			}
			for j, call := range calls.Array() {
				if kind := call.Get("type"); kind.Exists() && kind.String() != "function" {
					return nil, false, fmt.Errorf("messages[%d].tool_calls[%d]: unsupported type %q", i, j, kind.String())
				}
				name := call.Get("function.name").String()
				if !IsValidToolName(name) {
					return nil, false, fmt.Errorf("messages[%d].tool_calls[%d]: invalid function name %q", i, j, name)
				}
				if id := call.Get("id").String(); id != "" {
					names[id] = name
				}
				line, rErr := renderToolCallText(name, call.Get("function.arguments"))
				if rErr != nil {
					return nil, false, fmt.Errorf("messages[%d].tool_calls[%d]: %w", i, j, rErr)
				}
				_, _ = sb.WriteString(line)
				_, _ = sb.WriteString("\n")
			}
			if fc := msg.Get("function_call"); fc.Exists() {
				name := fc.Get("name").String()
				if !IsValidToolName(name) {
					return nil, false, fmt.Errorf("messages[%d].function_call: invalid function name %q", i, name)
				}
				line, rErr := renderToolCallText(name, fc.Get("arguments"))
				if rErr != nil {
					return nil, false, fmt.Errorf("messages[%d].function_call: %w", i, rErr)
				}
				_, _ = sb.WriteString(line)
			}
			out = append(out, map[string]any{"role": "assistant", "content": strings.TrimSpace(sb.String())})
			changed = true
		default:
			entry := map[string]any{"role": msg.Get("role").String()}
			if content := msg.Get("content"); content.Exists() {
				entry["content"] = json.RawMessage(content.Raw)
			}
			if calls.Exists() || msg.Get("tool_call_id").Exists() {
				changed = true
			}
			out = append(out, entry)
		}
	}
	data, mErr := marshalNoEscape(out)
	if mErr != nil {
		return nil, false, mErr
	}
	return data, changed, nil
}

// FoldMessagesWithTools 在 FoldMessagesToQueryWithOptions 之上增加工具协议：
// 先改写工具历史，再把工具说明放在查询头部，并从 MaxChars 预算中扣除说明长度
// （工具说明不可丢弃，超限时直接报错）。
func FoldMessagesWithTools(messages gjson.Result, specs []ToolSpec, choice ToolChoice, fo FoldOptions) (string, error) {
	normalized, _, err := NormalizeToolHistory(messages)
	if err != nil {
		return "", err
	}
	instructions := RenderToolInstructions(specs, choice)
	if instructions != "" && fo.MaxChars > 0 {
		remaining := fo.MaxChars - utf8.RuneCountInString(instructions) - 2
		if remaining < 1 {
			return "", fmt.Errorf("function tool definitions alone exceed the %d char query limit", fo.MaxChars)
		}
		fo.MaxChars = remaining
	}
	query, err := FoldMessagesToQueryWithOptions(gjson.ParseBytes(normalized), fo)
	if err != nil {
		return "", err
	}
	if instructions == "" {
		return query, nil
	}
	return instructions + "\n\n" + query, nil
}

type toolParserState int

const (
	toolParserUndecided toolParserState = iota
	toolParserText
	toolParserTool
)

// ToolCallParser 是流式工具调用识别器。回答开头（忽略前导空白）出现
// <tool_call> 时进入缓冲模式，直到 Finish 再整体解析；否则立即按文本透传。
// 解析失败（JSON 非法、工具未声明、标签未闭合、标签后有多余文本）时，
// Finish 把全部缓冲作为普通文本返回。
//
// 注意：输入必须是未经引用角标清洗的原文，StripCitationMarkers 会破坏
// 形如 {"ids":[1]} 的参数；清洗只应作用于 Feed/Finish 返回的文本部分。
type ToolCallParser struct {
	allowed map[string]bool
	state   toolParserState
	buf     strings.Builder
	newID   func() string
}

// NewToolCallParser 创建解析器；allowed 为空时解析器退化为纯文本透传。
func NewToolCallParser(allowed []ToolSpec) *ToolCallParser {
	p := &ToolCallParser{allowed: make(map[string]bool, len(allowed)), newID: NewToolCallID}
	for _, spec := range allowed {
		p.allowed[spec.Name] = true
	}
	if len(p.allowed) == 0 {
		p.state = toolParserText
	}
	return p
}

// NewToolCallID 生成 CC 风格的工具调用 ID。
func NewToolCallID() string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

// Buffering 报告解析器当前是否在暂存疑似工具调用（调用方可据此发送保活）。
func (p *ToolCallParser) Buffering() bool {
	return p.state != toolParserText && p.buf.Len() > 0
}

// Feed 输入一段增量文本，返回可立即作为普通文本下发的部分。
func (p *ToolCallParser) Feed(delta string) string {
	switch p.state {
	case toolParserText:
		return delta
	case toolParserTool:
		_, _ = p.buf.WriteString(delta)
		if p.buf.Len() > maxToolCallBufferBytes {
			return p.releaseAsText()
		}
		return ""
	}
	_, _ = p.buf.WriteString(delta)
	head := strings.TrimLeft(p.buf.String(), " \t\r\n")
	switch {
	case head == "":
		return ""
	case strings.HasPrefix(head, ToolCallOpenTag):
		p.state = toolParserTool
		if p.buf.Len() > maxToolCallBufferBytes {
			return p.releaseAsText()
		}
		return ""
	case strings.HasPrefix(ToolCallOpenTag, head):
		return ""
	default:
		return p.releaseAsText()
	}
}

// Finish 在上游明确结束后调用，返回剩余文本与解析出的工具调用（二者互斥）。
func (p *ToolCallParser) Finish() (string, []ToolCall) {
	buffered := p.buf.String()
	p.buf.Reset()
	wasTool := p.state == toolParserTool
	p.state = toolParserText
	if !wasTool {
		return buffered, nil
	}
	calls, ok := p.parseBlocks(buffered)
	if !ok {
		return buffered, nil
	}
	return "", calls
}

func (p *ToolCallParser) releaseAsText() string {
	out := p.buf.String()
	p.buf.Reset()
	p.state = toolParserText
	return out
}

func (p *ToolCallParser) parseBlocks(s string) ([]ToolCall, bool) {
	var calls []ToolCall
	rest := s
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		if !strings.HasPrefix(rest, ToolCallOpenTag) {
			return nil, false
		}
		rest = rest[len(ToolCallOpenTag):]
		end := strings.Index(rest, ToolCallCloseTag)
		if end < 0 {
			return nil, false
		}
		call, ok := p.parseCall(rest[:end])
		if !ok {
			return nil, false
		}
		calls = append(calls, call)
		if len(calls) > MaxToolCallsPerTurn {
			return nil, false
		}
		rest = rest[end+len(ToolCallCloseTag):]
	}
	return calls, len(calls) > 0
}

func (p *ToolCallParser) parseCall(inner string) (ToolCall, bool) {
	inner = stripCodeFence(strings.TrimSpace(inner))
	if !gjson.Valid(inner) {
		return ToolCall{}, false
	}
	root := gjson.Parse(inner)
	if !root.IsObject() {
		return ToolCall{}, false
	}
	nameField := root.Get("name")
	if nameField.Type != gjson.String || !p.allowed[nameField.String()] {
		return ToolCall{}, false
	}
	args := root.Get("arguments")
	if !args.Exists() {
		args = root.Get("parameters")
	}
	raw := ""
	switch {
	case !args.Exists() || args.Type == gjson.Null:
		raw = "{}"
	case args.IsObject():
		raw = args.Raw
	case args.Type == gjson.String:
		s := strings.TrimSpace(args.String())
		if s == "" {
			raw = "{}"
		} else if gjson.Valid(s) && gjson.Parse(s).IsObject() {
			raw = s
		} else {
			return ToolCall{}, false
		}
	default:
		return ToolCall{}, false
	}
	compacted, err := compactJSON(raw)
	if err != nil {
		return ToolCall{}, false
	}
	return ToolCall{ID: p.newID(), Name: nameField.String(), Arguments: compacted}, true
}

// ToolCallsChatDelta 返回流式 CC chunk 的 delta.tool_calls（带 index）。
func ToolCallsChatDelta(calls []ToolCall) []any {
	out := make([]any, 0, len(calls))
	for i, call := range calls {
		out = append(out, map[string]any{
			"index": i,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	return out
}

// ToolCallsChatMessage 返回非流式 CC message.tool_calls。
func ToolCallsChatMessage(calls []ToolCall) []any {
	out := make([]any, 0, len(calls))
	for _, call := range calls {
		out = append(out, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	return out
}

type toolCallWire struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func renderToolCallText(name string, args gjson.Result) (string, error) {
	raw := "{}"
	switch {
	case !args.Exists() || args.Type == gjson.Null:
	case args.Type == gjson.String:
		s := strings.TrimSpace(args.String())
		switch {
		case s == "":
		case gjson.Valid(s):
			raw = s
		default:
			quoted, err := marshalNoEscape(s)
			if err != nil {
				return "", err
			}
			raw = string(quoted)
		}
	default:
		raw = args.Raw
	}
	data, err := marshalNoEscape(toolCallWire{Name: name, Arguments: json.RawMessage(raw)})
	if err != nil {
		return "", err
	}
	return ToolCallOpenTag + string(data) + ToolCallCloseTag, nil
}

func renderToolResult(name, id, text string) string {
	if name == "" {
		name = "unknown"
	}
	header := fmt.Sprintf("<tool_result name=%q", name)
	if id != "" {
		header += fmt.Sprintf(" id=%q", id)
	}
	return header + ">\n" + strings.TrimSpace(text) + "\n</tool_result>"
}

func toolContentText(content gjson.Result) (string, error) {
	switch {
	case !content.Exists() || content.Type == gjson.Null:
		return "", nil
	case content.Type == gjson.String:
		return content.String(), nil
	case content.IsArray():
		var sb strings.Builder
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "text", "input_text", "output_text":
				_, _ = sb.WriteString(part.Get("text").String())
			default:
				return "", fmt.Errorf("tool message content type %q is not supported by Perplexity tool emulation", part.Get("type").String())
			}
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("tool message content must be text")
}

func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "json")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func compactJSON(raw string) (string, error) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(raw)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func oneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…"
}
