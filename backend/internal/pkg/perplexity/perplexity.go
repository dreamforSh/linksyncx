// Package perplexity 实现 Perplexity.ai 网页订阅 API（逆向、Cookie 认证）的
// 协议封装：会话凭证、/rest/sse/perplexity_ask 请求体构造、SSE 事件解析与
// OpenAI 消息折叠。该端点是单次搜索问答，不持有会话状态；网关侧把 OpenAI
// 消息历史折叠为单个 query_str。
package perplexity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// DefaultBaseURL 是 Perplexity Web 端的官方地址。
const DefaultBaseURL = "https://www.perplexity.ai"

// AskPath 是网页端问答 SSE 端点（相对 base_url）。
const AskPath = "/rest/sse/perplexity_ask"

// SessionPath / RateLimitAllPath 是会话探测与配额快照端点。
// CreditsBalancePath 是 Computer 积分余额端点（include_credits=true 返回积分明细）。
// ModelsConfigPath 是模型配置端点：服务端下发当前账号可见的模型列表
// （含 label/subscription_tier），模型清单随上游版本漂移，以此为准。
const (
	SessionPath        = "/api/auth/session"
	RateLimitAllPath   = "/rest/rate-limit/all"
	CreditsBalancePath = "/rest/billing/credits/balance"
	ModelsConfigPath   = "/rest/models/config/v2"
)

// DefaultUserAgent 是固定的桌面 Chrome UA。绝不能透传下游客户端 UA——
// 编程库 UA 是反爬（Cloudflare）的强信号。
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// SSE 事件字段名（fetch 风格）。
const (
	SSEEventMessage     = "message"
	SSEEventEndOfStream = "end_of_stream"
)

// Credentials 是 Perplexity 网页会话凭证。
type Credentials struct {
	// SessionToken 是 __Secure-next-auth.session-token 的值（必填）。
	SessionToken string
	// CSRFToken 是 next-auth.csrf-token 的值（可选）。
	CSRFToken string
	// BaseURL 是自定义站点地址（可选，默认 DefaultBaseURL）。
	BaseURL string
}

// NormalizedBaseURL 返回去掉尾斜杠的 base URL，空值回落到默认地址。
func (c Credentials) NormalizedBaseURL() string {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return DefaultBaseURL
	}
	return base
}

// CookieHeader 构造请求用 Cookie 头。
func (c Credentials) CookieHeader() string {
	parts := []string{"__Secure-next-auth.session-token=" + strings.TrimSpace(c.SessionToken)}
	if csrf := strings.TrimSpace(c.CSRFToken); csrf != "" {
		parts = append(parts, "next-auth.csrf-token="+csrf)
	}
	return strings.Join(parts, "; ")
}

// SetFetchStyleHeaders 设置 fetch 风格的反爬相关请求头（含 Cookie）。
// Origin/Referer 由 baseURL 推导。
func (c Credentials) SetFetchStyleHeaders(h http.Header, accept string) {
	base := c.NormalizedBaseURL()
	h.Set("Content-Type", "application/json")
	if accept == "" {
		accept = "text/event-stream"
	}
	h.Set("Accept", accept)
	h.Set("Origin", base)
	h.Set("Referer", base+"/")
	h.Set("sec-fetch-dest", "empty")
	h.Set("sec-fetch-mode", "cors")
	h.Set("sec-fetch-site", "same-origin")
	h.Set("accept-language", "en-US,en;q=0.9")
	h.Set("User-Agent", DefaultUserAgent)
	h.Set("Cookie", c.CookieHeader())
}

// 模式常量：concise 用于 auto/turbo，copilot 用于 pro/reasoning/research，
// computer 用于 Computer 代理任务（流里携带 pro_search_steps 工具步骤块）。
const (
	ModeConcise  = "concise"
	ModeCopilot  = "copilot"
	ModeComputer = "computer"
)

// askVersion 是网页端协议版本号，随官方前端升级需要跟进。
const askVersion = "2.18"

// modelPreferenceEntry 描述一个 model_preference 值及其对应的 mode。
type modelPreferenceEntry struct {
	pref string
	mode string
}

// aliasToPreference 是面向客户端的模型别名 → 上游 model_preference 映射。
// 上游取值会随官方前端迭代而漂移，运营可通过账号 model_mapping 把任意
// 客户端模型名映射到裸 model_preference（见 rawPreferences）以绕过本表。
// -computer 后缀别名走 Computer 代理端点（同一 perplexity_ask，mode=computer）。
var aliasToPreference = map[string]modelPreferenceEntry{
	"sonar":               {pref: "turbo", mode: ModeConcise},
	"sonar-pro":           {pref: "pplx_pro", mode: ModeCopilot},
	"sonar-reasoning":     {pref: "pplx_reasoning", mode: ModeCopilot},
	"sonar-deep-research": {pref: "pplx_alpha", mode: ModeCopilot},
	"turbo":               {pref: "turbo", mode: ModeConcise},
	// Computer 代理模式（消耗 Computer 积分/代理配额，配额池与搜索不同）。
	"sonar-computer":           {pref: "turbo", mode: ModeComputer},
	"sonar-pro-computer":       {pref: "pplx_pro", mode: ModeComputer},
	"sonar-reasoning-computer": {pref: "pplx_reasoning", mode: ModeComputer},
}

// rawPreferences 是当前已知的裸 model_preference 值（直接透传，mode 按类推断）。
var rawPreferences = map[string]string{
	"turbo":                  ModeConcise,
	"pplx_pro":               ModeCopilot,
	"pplx_pro_upgraded":      ModeCopilot,
	"experimental":           ModeCopilot,
	"gpt54":                  ModeCopilot,
	"claude46sonnet":         ModeCopilot,
	"gemini31pro_high":       ModeCopilot,
	"gpt54_thinking":         ModeCopilot,
	"claude46sonnetthinking": ModeCopilot,
	"pplx_reasoning":         ModeCopilot,
	"pplx_alpha":             ModeCopilot,
}

// DefaultModelIDs 是分组默认模型列表（客户端可见别名）。
func DefaultModelIDs() []string {
	return []string{"sonar", "sonar-pro", "sonar-reasoning", "sonar-deep-research", "sonar-computer", "sonar-pro-computer"}
}

// ResolveModelPreference 把（可能已被 model_mapping 改写过的）模型名解析为
// 上游 model_preference + mode。未知模型回落 turbo/concise（auto）。
// 支持 "模型名:模式" 后缀覆盖（如 "gpt54:computer"），模式取 knownModes 之一。
func ResolveModelPreference(mappedModel string) (pref string, mode string) {
	name := strings.ToLower(strings.TrimSpace(mappedModel))
	if name == "" {
		return "turbo", ModeConcise
	}
	// 冒号后缀显式指定模式（model_mapping 值里可用，如 "pplx_pro:computer"）。
	explicitMode := ""
	if i := strings.LastIndex(name, ":"); i >= 0 {
		suffix := name[i+1:]
		if suffix == ModeConcise || suffix == ModeCopilot || suffix == ModeComputer {
			explicitMode = suffix
			name = name[:i]
		}
	}
	resolve := func(n string) (string, string) {
		if entry, ok := aliasToPreference[n]; ok {
			return entry.pref, entry.mode
		}
		if m, ok := rawPreferences[n]; ok {
			return n, m
		}
		// 裸 model_preference 透传：上游模型列表由服务端驱动、频繁上新
		// （如 claude55opus 这类新值），运营用 model_mapping 直达，无需发版。
		// 只接受形如裸 preference 的名字（小写字母/数字/下划线，无连字符），
		// 避免把 "gpt-5" 这类客户端随意拼写原样打到上游。
		if isRawPreferenceName(n) {
			return n, ModeCopilot
		}
		return "turbo", ModeConcise
	}
	pref, m := resolve(name)
	if explicitMode != "" {
		m = explicitMode
	}
	return pref, m
}

// isRawPreferenceName 报告 name 是否形如裸 model_preference（小写字母开头的
// 字母/数字/下划线，无连字符/空格），如 "claude55opus"、"gpt54_thinking"。
func isRawPreferenceName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

// BuildAskBody 构造 /rest/sse/perplexity_ask 请求体。
func BuildAskBody(query string, modelPref string, mode string) ([]byte, error) {
	if mode == "" {
		mode = ModeConcise
	}
	payload := map[string]any{
		"query_str": query,
		"params": map[string]any{
			"attachments":           []any{},
			"frontend_context_uuid": uuid.NewString(),
			"frontend_uuid":         uuid.NewString(),
			"is_incognito":          false,
			"language":              "en-US",
			"last_backend_uuid":     nil,
			"mode":                  mode,
			"model_preference":      modelPref,
			"source":                "default",
			"sources":               []string{"web"},
			"search_focus":          "internet",
			"version":               askVersion,
		},
	}
	return json.Marshal(payload)
}

// Event 是一个已解析的 SSE message 事件。
type Event struct {
	// Text 是截至本事件的完整回答快照（累积式，非增量）。
	Text string
	// BackendUUID 是上游线程句柄（日志用；MVP 不做 last_backend_uuid 链式回话）。
	BackendUUID string
	// UUID 是事件自身 uuid（如有）。
	UUID string
	// HasText 表示本事件是否携带了回答文本。
	HasText bool
	// Steps 是 pro_search_steps 块里的工具执行步骤（Computer/代理模式的 tool call
	// 轨迹：SEARCH_WEB / BROWSER_* / CODE / MCP_TOOL / THOUGHT / FINAL 等）。
	// 事件间步骤按 UUID 累积更新，调用方按 UUID 去重。
	Steps []Step
}

// Step 是代理执行轨迹中的一步工具调用。
type Step struct {
	UUID string
	// Type 是原始 step_type（大写下划线，如 SEARCH_WEB）。
	Type string
	// Content 是与 step_type 对应的 *_content 字段（首个对象值）。
	Content map[string]any
}

// ToolNameForStep 把 step_type 映射为面向客户端的工具名（pplx_ 前缀小写），
// 与客户端自带工具命名空间隔离，避免被当作客户端声明的 function 执行。
func ToolNameForStep(stepType string) string {
	t := strings.ToLower(strings.TrimSpace(stepType))
	if t == "" {
		return "pplx_step"
	}
	return "pplx_" + t
}

// ParseModelsConfigIDs 解析 /rest/models/config/v2 响应，返回 models[].model
// 裸 model_preference 列表（去重、保序）。这些值是 model_mapping 的映射目标。
func ParseModelsConfigIDs(body []byte) []string {
	if !gjson.ValidBytes(body) {
		return nil
	}
	models := gjson.GetBytes(body, "models")
	if !models.IsArray() {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, m := range models.Array() {
		id := strings.TrimSpace(m.Get("model").String())
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

// parseStep 解析 plan_block.steps[] 的单个步骤：取首个以 _content 结尾的对象字段
// 作为步骤载荷（search_web_content / browser_open_tab_content / code_content 等）。
func parseStep(step gjson.Result) (Step, bool) {
	stepType := strings.TrimSpace(step.Get("step_type").String())
	if stepType == "" {
		return Step{}, false
	}
	s := Step{
		UUID: strings.TrimSpace(step.Get("uuid").String()),
		Type: stepType,
	}
	step.ForEach(func(key, value gjson.Result) bool {
		if s.Content == nil && strings.HasSuffix(key.String(), "_content") && value.IsObject() {
			s.Content = map[string]any{}
			_ = json.Unmarshal([]byte(value.Raw), &s.Content)
		}
		return true
	})
	return s, true
}

// ParseEvent 解析单个 SSE data 载荷，同时支持两种线上形态：
//
//  1. 现代 blocks 形态：blocks[] 中 intended_usage=="ask_text" 的块携带
//     markdown_block.answer（累积全文）和/或 markdown_block.chunks（文本片段数组）。
//  2. 旧形态：顶层 text 字段本身是一个 JSON 字符串，内含 steps[]；其中
//     step_type=="FINAL" 的 step 的 content.answer 又是一个 JSON 字符串
//     {"answer": "...", "web_results": [...]}。
func ParseEvent(data []byte) (Event, bool) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || !gjson.Valid(trimmed) {
		return Event{}, false
	}
	root := gjson.Parse(trimmed)
	ev := Event{
		BackendUUID: strings.TrimSpace(root.Get("backend_uuid").String()),
		UUID:        strings.TrimSpace(root.Get("uuid").String()),
	}

	// 形态 1：blocks[]
	if blocks := root.Get("blocks"); blocks.IsArray() {
		for _, block := range blocks.Array() {
			switch block.Get("intended_usage").String() {
			case "ask_text":
				md := block.Get("markdown_block")
				if answer := md.Get("answer").String(); answer != "" {
					ev.Text = answer
					ev.HasText = true
				} else if chunks := md.Get("chunks"); chunks.IsArray() {
					var sb strings.Builder
					for _, chunk := range chunks.Array() {
						_, _ = sb.WriteString(chunk.String())
					}
					if sb.Len() > 0 {
						ev.Text = sb.String()
						ev.HasText = true
					}
				}
			case "pro_search_steps":
				// 工具执行轨迹（Computer/代理模式）：plan_block.steps[] 累积更新。
				for _, step := range block.Get("plan_block.steps").Array() {
					if s, ok := parseStep(step); ok {
						ev.Steps = append(ev.Steps, s)
					}
				}
			}
		}
		return ev, true
	}

	// 形态 2：text 为内嵌 JSON 字符串
	rawText := root.Get("text")
	if rawText.Type == gjson.String {
		inner := strings.TrimSpace(rawText.String())
		if inner != "" && gjson.Valid(inner) {
			steps := gjson.Get(inner, "steps")
			if steps.IsArray() {
				for i, step := range steps.Array() {
					stepType := step.Get("step_type").String()
					if stepType != "FINAL" && stepType != "" {
						// 非 FINAL 步骤是工具执行轨迹（旧形态的 content 是单对象）。
						// 旧形态步骤可能没有 uuid：按数组序号兜底（步骤数组累积
						// 有序，序号在事件间稳定），保证翻译器能按 uuid 去重。
						s := Step{
							UUID: strings.TrimSpace(step.Get("uuid").String()),
							Type: stepType,
						}
						if s.UUID == "" {
							s.UUID = "legacy-" + strconv.Itoa(i)
						}
						if content := step.Get("content"); content.IsObject() {
							s.Content = map[string]any{}
							_ = json.Unmarshal([]byte(content.Raw), &s.Content)
						}
						ev.Steps = append(ev.Steps, s)
						continue
					}
					answerRaw := step.Get("content.answer").String()
					if answerRaw == "" {
						continue
					}
					// content.answer 是又一个 JSON 字符串：{"answer": "...", ...}
					if gjson.Valid(answerRaw) {
						ev.Text = gjson.Get(answerRaw, "answer").String()
					} else {
						ev.Text = answerRaw
					}
					ev.HasText = ev.Text != ""
				}
			}
			return ev, true
		}
	}

	// 其他事件（状态/心跳等）：合法 JSON 但无回答文本。
	return ev, true
}

// DeltaFromSnapshot 对累积快照做最长公共前缀差分：快照通常是 prev 的前缀扩展，
// 直接返回增量；上游偶发重写中段时（不共享前缀尾部）也只重复分叉点之后的
// 内容，避免整段重发。返回增量按 rune 边界对齐，不切开多字节字符。
func DeltaFromSnapshot(prev, snapshot string) string {
	n := 0
	maxN := min(len(prev), len(snapshot))
	for n < maxN && prev[n] == snapshot[n] {
		n++
	}
	for n > 0 && n < len(snapshot) && !utf8.RuneStart(snapshot[n]) {
		n--
	}
	return snapshot[n:]
}

// ErrImageOnlyFinalMessage 表示最终用户消息仅包含图片——MVP 不支持上传。
var ErrImageOnlyFinalMessage = errors.New("perplexity web subscription does not support image inputs; send text instead")

// FoldMessagesToQuery 把 OpenAI Chat Completions messages 折叠为单个 query_str：
//
//   - system → 头部 "Instructions: ..." 块
//   - 历史 user/assistant → "User: ..." / "Assistant: ..." 行
//   - 最后一条 user 消息是实际问题（不再加前缀）
//   - tool 角色与纯 tool_calls 消息被丢弃；多模态 content 数组拼接 text 部分、
//     忽略 image 部分；最终 user 消息仅含图片时报错。
//   - 只有一条 user 消息且无 system 时原样发送。
func FoldMessagesToQuery(messages gjson.Result) (string, error) {
	if !messages.IsArray() {
		return "", fmt.Errorf("messages is not an array")
	}
	type turn struct {
		role      string
		text      string
		imageOnly bool
	}
	var instructions []string
	var turns []turn

	extractText := func(content gjson.Result) (text string, hasImage bool, hasText bool) {
		if content.Type == gjson.String {
			return content.String(), false, strings.TrimSpace(content.String()) != ""
		}
		if content.IsArray() {
			var sb strings.Builder
			for _, part := range content.Array() {
				switch part.Get("type").String() {
				case "text":
					_, _ = sb.WriteString(part.Get("text").String())
					hasText = true
				case "image_url", "input_image", "image":
					hasImage = true
				}
			}
			return sb.String(), hasImage, hasText
		}
		return "", false, false
	}

	for _, msg := range messages.Array() {
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		switch role {
		case "system", "developer":
			text, _, _ := extractText(msg.Get("content"))
			if t := strings.TrimSpace(text); t != "" {
				instructions = append(instructions, t)
			}
		case "user":
			content := msg.Get("content")
			text, hasImage, hasText := extractText(content)
			turns = append(turns, turn{role: "user", text: text, imageOnly: hasImage && !hasText})
		case "assistant":
			// 纯 tool_calls 消息（无文本内容）丢弃。
			text, _, _ := extractText(msg.Get("content"))
			if strings.TrimSpace(text) == "" && len(msg.Get("tool_calls").Array()) > 0 {
				continue
			}
			turns = append(turns, turn{role: "assistant", text: text})
		default:
			// tool / function 等其他角色丢弃。
			continue
		}
	}

	// 最终 user 消息仅含图片 → 明确报错（400）。
	lastUser := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return "", fmt.Errorf("no user message found")
	}
	if turns[lastUser].imageOnly && strings.TrimSpace(turns[lastUser].text) == "" {
		return "", ErrImageOnlyFinalMessage
	}

	// 单条 user 且无 system：原样发送。
	if len(turns) == 1 && len(instructions) == 0 && turns[0].role == "user" {
		q := strings.TrimSpace(turns[0].text)
		if q == "" {
			return "", fmt.Errorf("empty user message")
		}
		return q, nil
	}

	var sb strings.Builder
	if len(instructions) > 0 {
		_, _ = sb.WriteString("Instructions: ")
		_, _ = sb.WriteString(strings.Join(instructions, "\n"))
		_, _ = sb.WriteString("\n\n")
	}
	for i, t := range turns {
		line := strings.TrimSpace(t.text)
		if line == "" {
			continue
		}
		if i == lastUser {
			_, _ = sb.WriteString(line)
		} else {
			if t.role == "user" {
				_, _ = sb.WriteString("User: ")
			} else {
				_, _ = sb.WriteString("Assistant: ")
			}
			_, _ = sb.WriteString(line)
			_, _ = sb.WriteString("\n")
		}
	}
	query := strings.TrimSpace(sb.String())
	if query == "" {
		return "", fmt.Errorf("folded query is empty")
	}
	return query, nil
}

// EstimateTokens 按 rune 计数粗估 token 数（上游不返回用量，用于计费兜底）。
// 与 service 侧 estimateTokensForText 同一惯例：~4 rune/token，非空至少为 1。
func EstimateTokens(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	runes := utf8.RuneCountInString(s)
	if runes == 0 {
		return 0
	}
	n := runes / 4
	if n < 1 {
		return 1
	}
	return n
}

// ErrorClass 是上游 HTTP 错误的分类。
type ErrorClass int

const (
	ErrClassOther ErrorClass = iota
	// ErrClassAuth：401/403，会话 Cookie 已失效。
	ErrClassAuth
	// ErrClassRateLimited：429，遵守 Retry-After。
	ErrClassRateLimited
	// ErrClassCloudflare：Cloudflare 质询/封锁（403 + cf 特征，或 503 挑战页）。
	ErrClassCloudflare
	// ErrClassServer：5xx 服务端错误。
	ErrClassServer
)

func (c ErrorClass) String() string {
	switch c {
	case ErrClassAuth:
		return "auth"
	case ErrClassRateLimited:
		return "rate_limited"
	case ErrClassCloudflare:
		return "cloudflare"
	case ErrClassServer:
		return "server"
	default:
		return "other"
	}
}

// ClassifyHTTPError 分类上游错误并解析 Retry-After（秒或 HTTP 日期）。
func ClassifyHTTPError(status int, headers http.Header, body []byte) (ErrorClass, time.Duration) {
	var retryAfter time.Duration
	if headers != nil {
		retryAfter = parseRetryAfter(headers.Get("Retry-After"))
	}
	server := ""
	cfRay := ""
	if headers != nil {
		server = strings.ToLower(headers.Get("Server"))
		cfRay = headers.Get("CF-RAY")
	}
	bodyLower := ""
	if len(body) > 0 && len(body) < 64*1024 {
		bodyLower = strings.ToLower(string(body))
	}
	isCloudflare := strings.Contains(server, "cloudflare") || cfRay != "" ||
		strings.Contains(bodyLower, "cf-chl") || strings.Contains(bodyLower, "cloudflare")

	switch {
	case status == http.StatusUnauthorized:
		return ErrClassAuth, retryAfter
	case status == http.StatusForbidden:
		if isCloudflare {
			return ErrClassCloudflare, retryAfter
		}
		return ErrClassAuth, retryAfter
	case status == http.StatusTooManyRequests:
		return ErrClassRateLimited, retryAfter
	case status >= 500:
		if isCloudflare {
			return ErrClassCloudflare, retryAfter
		}
		return ErrClassServer, retryAfter
	default:
		return ErrClassOther, retryAfter
	}
}

// parseRetryAfter 解析 Retry-After 头（秒数或 HTTP 日期），无效值返回 0。
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
