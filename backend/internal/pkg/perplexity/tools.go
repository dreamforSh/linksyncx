package perplexity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	_, _ = sb.WriteString("The arguments must be a JSON object matching the tool's JSON Schema. Do not wrap tool calls in code fences.\n")
	_, _ = sb.WriteString("Stop right after the last </tool_call> and wait: never write <tool_result> blocks yourself and never guess what a tool returns.\n")
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
	// toolParserText 按文本透传，但暂存行首可能开始工具调用的尾巴（"<tool"、"```xml"）。
	toolParserText toolParserState = iota
	// toolParserTool 已在行首识别到 <tool_call>，缓冲直到调用块完整。
	toolParserTool
	// toolParserDone 调用块已完整且其后出现了非调用文本；后续输出一律丢弃。
	toolParserDone
	// toolParserOff 未声明可调用工具，原样透传。
	toolParserOff
)

type toolBlockStatus int

const (
	toolBlocksIncomplete toolBlockStatus = iota
	toolBlocksInvalid
	toolBlocksDone
)

// ToolCallParser 是流式工具调用识别器。
//
// 工具调用块必须从行首开始（允许缩进，允许包在 ``` 代码围栏里）；之前的文字作为
// 普通正文下发（"我来查一下：\n<tool_call>…"），调用块之后的文字视为模型臆造的
// 结果并丢弃。调用块不合法（JSON 非法、工具未声明、标签未闭合）时整段按普通文本
// 下发，绝不伪造调用。JSON 按语法完整解码，参数字符串里出现 </tool_call> 也不会
// 截断。
//
// 注意：输入必须是未经引用角标清洗的原文，StripCitationMarkers 会破坏
// 形如 {"ids":[1]} 的参数；清洗只应作用于 Feed/Finish 返回的文本部分。
type ToolCallParser struct {
	// allowed 是声明的工具名；folded 是小写名到声明名的映射（仅大小写唯一时）。
	allowed map[string]bool
	folded  map[string]string
	state   toolParserState
	// pending 在 text 状态是尚未下发的尾巴，在 tool 状态是调用块缓冲。
	pending strings.Builder
	// emitted 表示已下发过非空白正文；lineStart 表示已下发正文以换行结尾（或尚未下发）。
	emitted   bool
	lineStart bool
	calls     []ToolCall
	newID     func() string
}

// NewToolCallParser 创建解析器；allowed 为空时解析器退化为纯文本透传。
func NewToolCallParser(allowed []ToolSpec) *ToolCallParser {
	p := &ToolCallParser{
		allowed:   make(map[string]bool, len(allowed)),
		folded:    make(map[string]string, len(allowed)),
		lineStart: true,
		newID:     NewToolCallID,
	}
	ambiguous := make(map[string]bool)
	for _, spec := range allowed {
		p.allowed[spec.Name] = true
		key := strings.ToLower(spec.Name)
		if _, dup := p.folded[key]; dup {
			ambiguous[key] = true
		}
		p.folded[key] = spec.Name
	}
	for key := range ambiguous {
		delete(p.folded, key)
	}
	if len(p.allowed) == 0 {
		p.state = toolParserOff
	}
	return p
}

// NewToolCallID 生成 CC 风格的工具调用 ID。
func NewToolCallID() string {
	return "call_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24]
}

// Buffering 报告解析器当前是否在暂存疑似工具调用（调用方可据此发送保活）。
func (p *ToolCallParser) Buffering() bool {
	return p.state == toolParserTool
}

// Feed 输入一段增量文本，返回可立即作为普通文本下发的部分。
func (p *ToolCallParser) Feed(delta string) string {
	switch p.state {
	case toolParserOff:
		return delta
	case toolParserDone:
		return ""
	case toolParserTool:
		_, _ = p.pending.WriteString(delta)
		return p.advanceTool(false)
	}
	_, _ = p.pending.WriteString(delta)
	return p.scanText()
}

// Finish 在上游明确结束后调用，返回剩余待下发文本与解析出的工具调用。二者可能
// 同时为空；有调用时剩余文本恒为空（调用块之后的文字被丢弃）。Feed 已放行的
// 前置正文不在返回值里。
func (p *ToolCallParser) Finish() (string, []ToolCall) {
	switch p.state {
	case toolParserTool:
		out := p.advanceTool(true)
		if p.state == toolParserDone {
			return out, p.takeCalls()
		}
		return out, nil
	case toolParserDone:
		return "", p.takeCalls()
	}
	out := p.pending.String()
	p.pending.Reset()
	return out, nil
}

func (p *ToolCallParser) takeCalls() []ToolCall {
	calls := p.calls
	p.calls = nil
	p.state = toolParserOff
	return calls
}

// scanText 在 text 状态下放行确定是正文的部分，暂存行首可能开始调用块的尾巴；
// 发现完整的行首 <tool_call> 时切换到 tool 状态。
func (p *ToolCallParser) scanText() string {
	s := p.pending.String()
	hold := -1
	for i := 0; i <= len(s); {
		if i > 0 || p.lineStart {
			full, partial := toolStartAt(s[i:])
			if full {
				prefix := s[:i]
				p.pending.Reset()
				_, _ = p.pending.WriteString(s[i:])
				p.state = toolParserTool
				if !p.emitted && strings.TrimSpace(prefix) == "" {
					prefix = ""
				}
				p.markEmitted(prefix)
				return prefix + p.advanceTool(false)
			}
			if partial {
				hold = i
				break
			}
		}
		next := strings.IndexByte(s[i:], '\n')
		if next < 0 {
			break
		}
		i += next + 1
	}
	if hold < 0 {
		hold = len(s)
	}
	// 尚未下发任何正文时，纯空白前缀继续暂存：调用块前的空行不算前置正文。
	if !p.emitted && strings.TrimSpace(s[:hold]) == "" {
		return ""
	}
	out := s[:hold]
	p.pending.Reset()
	_, _ = p.pending.WriteString(s[hold:])
	p.markEmitted(out)
	return out
}

func (p *ToolCallParser) markEmitted(text string) {
	if text == "" {
		return
	}
	p.lineStart = text[len(text)-1] == '\n'
	if strings.TrimSpace(text) != "" {
		p.emitted = true
	}
}

// advanceTool 尝试解析缓冲的调用块。final 表示上游已结束，不完整即不合法。
func (p *ToolCallParser) advanceTool(final bool) string {
	s := p.pending.String()
	calls, status := p.parseBlocks(s, final)
	switch status {
	case toolBlocksIncomplete:
		if len(s) > maxToolCallBufferBytes {
			return p.releaseAsText()
		}
		return ""
	case toolBlocksInvalid:
		return p.releaseAsText()
	}
	p.calls = calls
	p.pending.Reset()
	p.state = toolParserDone
	return ""
}

func (p *ToolCallParser) releaseAsText() string {
	out := p.pending.String()
	p.pending.Reset()
	p.state = toolParserText
	p.markEmitted(out)
	return out
}

// toolStartAt 判断从行首开始的 s 是否以调用块开头：可选缩进、可选一行 ``` 围栏
// （可带语言名），然后是 <tool_call>。partial 表示 s 目前是这种开头的前缀。
func toolStartAt(s string) (full, partial bool) {
	t := strings.TrimLeft(s, " \t")
	switch {
	case strings.HasPrefix(t, "```"):
		rest := t[3:]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return false, isFenceInfo(strings.TrimRight(rest, " \t\r"))
		}
		if !isFenceInfo(strings.TrimRight(rest[:nl], " \t\r")) {
			return false, false
		}
		t = strings.TrimLeft(rest[nl+1:], " \t")
	case t != "" && strings.HasPrefix("```", t):
		return false, true
	}
	if strings.HasPrefix(t, ToolCallOpenTag) {
		return true, false
	}
	return false, strings.HasPrefix(ToolCallOpenTag, t)
}

func isFenceInfo(s string) bool {
	if len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '_', ch == '-', ch == '+':
		default:
			return false
		}
	}
	return true
}

// parseBlocks 解析连续的调用块。非 final 时，缓冲恰好停在一个完整块之后也算不完整
// （后面可能还有块）；只有看到块后的非调用文本才判定完成。
func (p *ToolCallParser) parseBlocks(s string, final bool) ([]ToolCall, toolBlockStatus) {
	var calls []ToolCall
	rest := s
	// 缓冲到头：final 时有调用即完成；否则等待更多输入。
	ended := func() ([]ToolCall, toolBlockStatus) {
		switch {
		case !final:
			return nil, toolBlocksIncomplete
		case len(calls) > 0:
			return calls, toolBlocksDone
		}
		return nil, toolBlocksInvalid
	}
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" {
			return ended()
		}
		full, partial := toolStartAt(rest)
		if !full {
			if partial {
				return ended()
			}
			if len(calls) > 0 {
				return calls, toolBlocksDone
			}
			return nil, toolBlocksInvalid
		}
		body := rest[strings.Index(rest, ToolCallOpenTag)+len(ToolCallOpenTag):]
		body = strings.TrimLeft(body, " \t\r\n")
		innerFence := false
		if len(body) < 3 && strings.HasPrefix("```", body) {
			return ended()
		}
		if strings.HasPrefix(body, "```") {
			nl := strings.IndexByte(body, '\n')
			if nl < 0 {
				return ended()
			}
			body = body[nl+1:]
			innerFence = true
		}
		if strings.TrimSpace(body) == "" {
			return ended()
		}
		dec := json.NewDecoder(strings.NewReader(body))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
				return ended()
			}
			return nil, toolBlocksInvalid
		}
		after := strings.TrimLeft(body[dec.InputOffset():], " \t\r\n")
		if innerFence {
			if strings.HasPrefix(after, "```") {
				after = strings.TrimLeft(after[3:], " \t\r\n")
			} else if strings.HasPrefix("```", after) {
				return ended()
			}
		}
		if !strings.HasPrefix(after, ToolCallCloseTag) {
			if strings.HasPrefix(ToolCallCloseTag, after) {
				return ended()
			}
			return nil, toolBlocksInvalid
		}
		call, ok := p.parseCall(raw)
		if !ok {
			return nil, toolBlocksInvalid
		}
		calls = append(calls, call)
		if len(calls) > MaxToolCallsPerTurn {
			return nil, toolBlocksInvalid
		}
		rest = after[len(ToolCallCloseTag):]
	}
}

// parseCall 解析一个调用对象。除规范形态 {"name","arguments"} 外，也接受模型常见的
// 变体：OpenAI 嵌套 {"function":{...}}、tool/tool_name 名字段、parameters/args/input
// 参数字段、字符串化参数、functions. 前缀与大小写差异（仅映射到已声明工具）。
func (p *ToolCallParser) parseCall(raw []byte) (ToolCall, bool) {
	if !gjson.ValidBytes(raw) {
		return ToolCall{}, false
	}
	root := gjson.ParseBytes(raw)
	if !root.IsObject() {
		return ToolCall{}, false
	}
	if fn := root.Get("function"); fn.IsObject() {
		root = fn
	}
	var name string
	for _, key := range []string{"name", "tool", "tool_name", "function"} {
		if field := root.Get(key); field.Type == gjson.String {
			name = field.String()
			break
		}
	}
	declared, ok := p.declaredName(name)
	if !ok {
		return ToolCall{}, false
	}
	var args gjson.Result
	for _, key := range []string{"arguments", "parameters", "args", "input"} {
		if args = root.Get(key); args.Exists() {
			break
		}
	}
	raw2 := ""
	switch {
	case !args.Exists() || args.Type == gjson.Null:
		raw2 = "{}"
	case args.IsObject():
		raw2 = args.Raw
	case args.Type == gjson.String:
		s := strings.TrimSpace(args.String())
		if s == "" {
			raw2 = "{}"
		} else if gjson.Valid(s) && gjson.Parse(s).IsObject() {
			raw2 = s
		} else {
			return ToolCall{}, false
		}
	default:
		return ToolCall{}, false
	}
	compacted, err := compactJSON(raw2)
	if err != nil {
		return ToolCall{}, false
	}
	return ToolCall{ID: p.newID(), Name: declared, Arguments: compacted}, true
}

func (p *ToolCallParser) declaredName(name string) (string, bool) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "functions.")
	if name == "" {
		return "", false
	}
	if p.allowed[name] {
		return name, true
	}
	declared, ok := p.folded[strings.ToLower(name)]
	return declared, ok
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
	return header + ">\n" + toolTagEscaper.Replace(strings.TrimSpace(text)) + "\n</tool_result>"
}

// toolTagEscaper 转义工具结果里的协议标签：结果来自外部（网页、文件、用户数据），
// 原样嵌入会提前闭合 <tool_result> 或伪造一次调用，等同提示词注入。
var toolTagEscaper = strings.NewReplacer(
	ToolCallOpenTag, "&lt;tool_call&gt;",
	ToolCallCloseTag, "&lt;/tool_call&gt;",
	"<tool_result", "&lt;tool_result",
	"</tool_result", "&lt;/tool_result",
)

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
