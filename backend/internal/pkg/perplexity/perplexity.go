// Package perplexity 实现 Perplexity.ai 网页订阅 API（逆向、Cookie 认证）的
// 协议封装：会话凭证、/rest/sse/perplexity_ask 请求体构造、SSE 事件解析与
// OpenAI 消息折叠。该端点是单次搜索问答，不持有会话状态；网关侧把 OpenAI
// 消息历史折叠为单个 query_str。
package perplexity

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	parts := []string{SessionCookieHeader(c.SessionToken)}
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
	return BuildAskBodyWithOptions(query, modelPref, mode, DefaultRequestOptions())
}

// Event 是一个已解帧的 Perplexity 事件；内部步骤不是客户端工具调用。
type Event struct {
	// Text 在 IsDelta 为 true 时是增量，否则是权威的完整快照（可为空）。
	Text string
	// IsDelta 仅用于未完成的 markdown_block.chunks；answer 和 DONE 均为快照。
	IsDelta bool
	// AnswerUsage 标识已选定的 ask_text 镜像通道。
	AnswerUsage       string
	BackendUUID       string
	UUID              string
	DisplayModel      string
	UserSelectedModel string
	HasText           bool
	EndOfStream       bool
	// Final 表示上游已标记这是最终消息（final_sse_message=true）。推理模型等不发
	// 独立的 end_of_stream 事件，靠此字段作为等价终态，避免被判 ErrIncompleteStream。
	Final bool
	// Error 只标记上游失败，不保留可能含凭据的原始错误载荷。
	Error   bool
	Steps   []Step
	Sources []Source
	// Plan 是推理模型的计划/思考目标（plan_block.goals），作为"思考部分"下发给
	// 客户端的 reasoning_content，不是客户端可执行工具，也不是最终答案正文。
	Plan []PlanGoal
}

// Step 是上游内部执行步骤，Content 不可直接透传到下游。
type Step struct {
	UUID    string
	Type    string
	Status  string
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

// ParseModelsConfigIDs returns explicit model preferences from the web catalog.
func ParseModelsConfigIDs(body []byte) []string {
	models, err := ParseModelsConfig(body)
	if err != nil {
		return nil
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

// ParseEvent 解析单个 SSE data 载荷。流式调用方应使用 Decoder，以固定选择一个
// ask_text 镜像通道，并把 JSON 错误和缺少 end_of_stream 作为失败处理。
func ParseEvent(data []byte) (Event, bool) {
	return parseEvent(data, "")
}

// DeltaFromSnapshot 仅接受已发送内容的前缀扩展；中段改写或截短必须显式失败，
// 否则追加公共前缀之后的文本会产生不可恢复的混合回答。
func DeltaFromSnapshot(prev, snapshot string) (string, error) {
	if !strings.HasPrefix(snapshot, prev) {
		return "", ErrAnswerRewritten
	}
	return snapshot[len(prev):], nil
}

// ErrUnsupportedImageInput 表示图片尚未由服务端上传助手替换为文本占位符。
var ErrUnsupportedImageInput = errors.New("perplexity image inputs must be processed by the upload helper before folding")

// ErrImageOnlyFinalMessage 保留旧调用方的错误判断；任何未处理图片都会报错。
var ErrImageOnlyFinalMessage = ErrUnsupportedImageInput

// ErrUnsupportedToolHistory 表示客户端工具历史无法作为普通搜索历史处理。
var ErrUnsupportedToolHistory = errors.New("perplexity web subscription does not support client tool call or tool result history")

// foldTurn 是折叠中的一个对话轮次。
type foldTurn struct {
	role string
	text string
}

// 折叠长度限制常量：网页端 query_str 实测上限约 96000 字符。
const (
	// HardMaxQueryChars 是上游硬上限，超过会被直接拒绝。
	HardMaxQueryChars = 96000
	// DefaultMaxQueryChars 是默认折叠上限（留出安全余量）。
	DefaultMaxQueryChars = 90000
	// MinMaxQueryChars 是允许配置的最小上限（小于它没有实用意义）。
	MinMaxQueryChars = 1000
)

// FoldOptions 控制折叠行为。
type FoldOptions struct {
	// StripSystem 为 true 时剥离 system/developer 消息（Perplexity 对 query 全文
	// 做检索，样板 system prompt 会污染搜索结果——实测会召回聊天机器人教程）。
	StripSystem bool
	// MaxChars 是折叠结果的 rune 上限：超限时从最老历史轮次开始丢弃（最终用户
	// 问题始终完整保留），有丢弃时在头部插入省略标记。0 表示不限制。
	MaxChars int
}

// FoldMessagesToQuery 把 OpenAI Chat Completions messages 折叠为单个 query_str：
//
//   - system → 头部 "Instructions: ..." 块
//   - 历史 user/assistant → "User: ..." / "Assistant: ..." 行
//   - 最后一条 user 消息是实际问题（不再加前缀）
//   - 未处理图片、工具历史和未知内容块明确报错，不能静默丢失请求语义。
//   - 只有一条 user 消息且无 system 时原样发送。
//
// 注意：本函数保留 system 且不限长。转发路径应使用 FoldMessagesToQueryWithOptions
// （默认剥离 system + 90k 上限）；本函数仍服务于上传前的请求校验等兼容场景。
func FoldMessagesToQuery(messages gjson.Result) (string, error) {
	instructions, turns, lastUser, err := collectFoldTurns(messages)
	if err != nil {
		return "", err
	}
	return renderFoldedQuery(instructions, turns, lastUser, false)
}

// FoldMessagesToQueryWithOptions 是 FoldMessagesToQuery 的选项化版本：
// 先按 StripSystem 剥离 system/developer，再按 MaxChars 从最老历史轮次开始丢弃
// （历史丢光后仍超限则丢弃 instructions，最终用户问题始终完整保留，仍超限报错）。
func FoldMessagesToQueryWithOptions(messages gjson.Result, fo FoldOptions) (string, error) {
	instructions, turns, lastUser, err := collectFoldTurns(messages)
	if err != nil {
		return "", err
	}
	if fo.StripSystem {
		instructions = nil
	}
	query, err := renderFoldedQuery(instructions, turns, lastUser, false)
	if err != nil {
		return "", err
	}
	if fo.MaxChars <= 0 || utf8.RuneCountInString(query) <= fo.MaxChars {
		return query, nil
	}

	turnsDropped := false
	for {
		// 最老的可丢轮次（最终用户问题不可丢）。
		idx := -1
		for i := range turns {
			if i != lastUser {
				idx = i
				break
			}
		}
		switch {
		case idx >= 0:
			turns = append(turns[:idx], turns[idx+1:]...)
			if idx < lastUser {
				lastUser--
			}
			turnsDropped = true
		case len(instructions) > 0:
			instructions = nil
		default:
			// 只剩最终问题：省略标记本身也占预算，去掉标记再试；仍超限才报错。
			finalOnly, rErr := renderFoldedQuery(nil, []foldTurn{turns[lastUser]}, 0, false)
			if rErr != nil {
				return "", rErr
			}
			if utf8.RuneCountInString(finalOnly) > fo.MaxChars {
				return "", fmt.Errorf("the final user message alone exceeds the %d char query limit", fo.MaxChars)
			}
			return finalOnly, nil
		}
		query, err = renderFoldedQuery(instructions, turns, lastUser, turnsDropped)
		if err != nil {
			return "", err
		}
		if utf8.RuneCountInString(query) <= fo.MaxChars {
			return query, nil
		}
	}
}

// collectFoldTurns 解析并校验消息数组，产出 instructions、对话轮次与最终用户
// 消息下标。所有内容/角色/工具历史的严格校验都在此处完成。
func collectFoldTurns(messages gjson.Result) ([]string, []foldTurn, int, error) {
	if !messages.IsArray() {
		return nil, nil, -1, fmt.Errorf("messages is not an array")
	}
	var instructions []string
	var turns []foldTurn

	extractText := func(content gjson.Result) (string, error) {
		if content.Type == gjson.String {
			return content.String(), nil
		}
		if content.Type == gjson.Null {
			return "", nil
		}
		if !content.IsArray() {
			return "", fmt.Errorf("perplexity message content must be text or an array of text blocks")
		}
		var sb strings.Builder
		for _, part := range content.Array() {
			switch part.Get("type").String() {
			case "text":
				if part.Get("text").Type != gjson.String {
					return "", fmt.Errorf("perplexity text blocks require a string text field")
				}
				_, _ = sb.WriteString(part.Get("text").String())
			case "image_url", "input_image", "image":
				return "", ErrUnsupportedImageInput
			case "tool_use", "tool_result", "function_call", "function_call_output":
				return "", ErrUnsupportedToolHistory
			default:
				return "", fmt.Errorf("perplexity does not support message content type %q", part.Get("type").String())
			}
		}
		return sb.String(), nil
	}

	for _, msg := range messages.Array() {
		if calls := msg.Get("tool_calls"); calls.Exists() && (!calls.IsArray() || len(calls.Array()) > 0) {
			return nil, nil, -1, ErrUnsupportedToolHistory
		}
		if msg.Get("function_call").Exists() || msg.Get("tool_call_id").Exists() {
			return nil, nil, -1, ErrUnsupportedToolHistory
		}
		role := strings.ToLower(strings.TrimSpace(msg.Get("role").String()))
		if role == "tool" || role == "function" {
			return nil, nil, -1, ErrUnsupportedToolHistory
		}
		text, err := extractText(msg.Get("content"))
		if err != nil {
			return nil, nil, -1, err
		}
		switch role {
		case "system", "developer":
			if t := strings.TrimSpace(text); t != "" {
				instructions = append(instructions, t)
			}
		case "user", "assistant":
			turns = append(turns, foldTurn{role: role, text: text})
		default:
			return nil, nil, -1, fmt.Errorf("perplexity does not support message role %q", role)
		}
	}

	lastUser := -1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser < 0 {
		return nil, nil, -1, fmt.Errorf("no user message found")
	}
	if strings.TrimSpace(turns[lastUser].text) == "" {
		return nil, nil, -1, fmt.Errorf("empty user message")
	}
	return instructions, turns, lastUser, nil
}

// renderFoldedQuery 渲染折叠结果。omittedTurns 为 true 时在头部插入省略标记
// （标记会一并被上游检索，保持短而中性）。
func renderFoldedQuery(instructions []string, turns []foldTurn, lastUser int, omittedTurns bool) (string, error) {
	// 单条 user 且无 system：原样发送。
	if len(turns) == 1 && len(instructions) == 0 && turns[0].role == "user" && !omittedTurns {
		q := strings.TrimSpace(turns[0].text)
		if q == "" {
			return "", fmt.Errorf("empty user message")
		}
		return q, nil
	}

	var sb strings.Builder
	if omittedTurns {
		_, _ = sb.WriteString("[Earlier conversation omitted for length]\n\n")
	}
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

// citationMarkerPattern 匹配 Perplexity 答案里的引用角标（[1]、[2]...），
// 连同角标前的一个空格一起移除（"text [1]." → "text."）。不动换行与缩进。
var citationMarkerPattern = regexp.MustCompile(`[ \t]?\[\d+\]`)

// citationTailPattern 匹配结尾处可能未完成的角标前缀（"["、"[1"、"[12"），
// 连同其前一个空格一起暂存（"See [2" 的空格不能先下发，否则清洗后残留双空格）。
var citationTailPattern = regexp.MustCompile(`[ \t]?\[\d*$`)

// StripCitationMarkers 移除文本里的完整引用角标。
func StripCitationMarkers(s string) string {
	return citationMarkerPattern.ReplaceAllString(s, "")
}

// StripCitationMarkersStreaming 是 StripCitationMarkers 的流式变体：完整角标
// 立即清洗，结尾处可能未完成的角标前缀（"["、"[1"）留待下一段。返回可安全
// 下发的部分与需要暂存的尾巴；流结束时对尾巴再调用一次 StripCitationMarkers
// 并按原样下发残余（未完成的角标按普通文本处理）。
func StripCitationMarkersStreaming(s string) (emit string, pending string) {
	tail := citationTailPattern.FindString(s)
	body := strings.TrimSuffix(s, tail)
	return StripCitationMarkers(body), tail
}
