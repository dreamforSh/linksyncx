// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code 客户端相关常量

// Beta header 常量
//
// 对齐真实 Claude Code CLI 2.1.280 的实测流量（2026-09：本机二进制拆解 +
// claude.exe 抓包 + CLIProxyAPI 2026-09-23 第一方实测序）。
// 原因：Anthropic 上游会基于 anthropic-beta 的完整集合判定请求来源；
// 缺少任何"官方 Claude Code 请求才会带"的 beta，都会被降级到第三方额度，
// 对应报错：`Third-party apps now draw from your extra usage, not your plan limits.`
const (
	BetaOAuth               = "oauth-2025-04-20"
	BetaClaudeCode          = "claude-code-20250219"
	BetaInterleavedThinking = "interleaved-thinking-2025-05-14"
	BetaTokenCounting       = "token-counting-2024-11-01"
	BetaContext1M           = "context-1m-2025-08-07"
	BetaFastMode            = "fast-mode-2026-02-01"

	// 2.1.280 注册表实测项
	BetaRedactThinking             = "redact-thinking-2026-02-12"
	BetaThinkingTokenCount         = "thinking-token-count-2026-05-13"
	BetaContextManagement          = "context-management-2025-06-27"
	BetaPromptCachingScope         = "prompt-caching-scope-2026-01-05"
	BetaMidConversationSystem      = "mid-conversation-system-2026-04-07"
	BetaMidConversationToolChanges = "mid-conversation-tool-changes-2026-07-01"
	BetaEffort                     = "effort-2025-11-24"
	BetaExtendedCacheTTL           = "extended-cache-ttl-2025-04-11"
	BetaPromptCachingEvict         = "prompt-caching-evict-2026-05-12"
	BetaThinkingBindingControls    = "thinking-binding-controls-2026-08-01"

	// SDK surfaceCapabilities.sdkBetas 能力位（2.1.280 第一方抓包实证：每次
	// /v1/messages?beta=true 均携带，经 mf()→surfaceCapabilities.sdkBetas() 注入）。
	BetaAdvancedToolUse              = "advanced-tool-use-2025-11-20"
	BetaMidConversationSystemClearAt = "mid-conversation-system-clear-at-2026-08-21"
	BetaCacheDiagnosis               = "cache-diagnosis-2026-04-07"

	// fine-grained-tool-streaming：2.1.280 注册表已移除（二进制与抓包均无），
	// 保留常量仅供历史数据/测试对照，禁止加入任何默认/伪装列表。
	BetaFineGrainedToolStreaming = "fine-grained-tool-streaming-2025-05-14"

	// mid-conversation-output-config：pi-ai 等第三方客户端使用；不在真实 CLI
	// 2.1.280 的 beta 注册表内，仅作 sanitize 门控常量（strip-or-keep），
	// 禁止加入任何默认/伪装列表。
	BetaMidConversationOutputConfig = "mid-conversation-output-config-2026-07-01"

	// server-side refusal fallback beta 字段族（beta Messages API 专有）。
	// 客户端（Claude Code / SDK / OpenCode 等）会默认透传 body.fallbacks /
	// body.fallback_credit_token，上游仅在 anthropic-beta 携带对应 token 时接受；
	// 缺 token 时 Pydantic 拒收："fallbacks: Extra inputs are not permitted"。
	// BetaServerSideFallback / BetaFallbackCredit（2026-07-01）仅用于 sanitize 的条件
	// 判断（strip-or-keep），禁止加入 DefaultBetaHeader / APIKeyBetaHeader / Bedrock
	// 白名单：body.fallbacks 会触发 server-side fallback（换模型、改计费）。
	//
	// 例外：BetaFallbackCreditLegacy（fallback-credit-2026-06-01）在 2.1.280 第一方抓包
	// 中作为 SDK 能力位每次都出现（header only，body 不带 fallbacks），故纳入
	// ClaudeCodeMimicryBetas。仅 header 令牌不会触发 fallback；body.fallbacks 仍由上述
	// 门控独立剥离。
	BetaServerSideFallback   = "server-side-fallback-2026-07-01"
	BetaFallbackCredit       = "fallback-credit-2026-07-01"
	BetaFallbackCreditLegacy = "fallback-credit-2026-06-01"
)

// DroppedBetas 是转发时需要从 anthropic-beta header 中移除的 beta token 列表。
// 这些 token 是客户端特有的，不应透传给上游 API。
var DroppedBetas = []string{}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header
// （2.1.280 实测：非 haiku + thinking 开启的第一方形态，注册表序）。
const DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaEffort

// MessageBetaHeaderNoTools /v1/messages 在无工具时的 beta header
//
// NOTE: Claude Code OAuth credentials are scoped to Claude Code. When we "mimic"
// Claude Code for non-Claude-Code clients, we must include the claude-code beta
// even if the request doesn't use tools, otherwise upstream may reject the
// request as a non-Claude-Code API request.
const MessageBetaHeaderNoTools = DefaultBetaHeader

// MessageBetaHeaderWithTools /v1/messages 在有工具时的 beta header
const MessageBetaHeaderWithTools = DefaultBetaHeader

// CountTokensBetaHeader count_tokens 请求使用的 anthropic-beta header
// （CLIProxyAPI 对 37 次真实调用验证的固定集合）。
const CountTokensBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaContextManagement + "," + BetaTokenCounting

// HaikuBetaHeader Haiku 模型在 OAuth 真实客户端透传路径上的默认 anthropic-beta header。
// 2.1.280 抓包实证：haiku 不剔除 claude-code，而是挪到末尾；oauth 随之居首。
// OAuth mimic 路径统一使用 ClaudeCodeMimicryBetas。
const HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaClaudeCode

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header（不包含 oauth）
const APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaEffort

// APIKeyHaikuBetaHeader Haiku 模型在 API-key 账号下使用的 anthropic-beta header
// （不包含 oauth；claude-code 在末尾，与真实客户端一致）
const APIKeyHaikuBetaHeader = BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaClaudeCode

// DefaultCacheControlTTL 是网关代理为自己生成的 cache_control 块默认使用的 ttl。
// 真实 Claude Code CLI 当前使用 "1h"，但本仓策略是"客户端透传 ttl 优先；
// 客户端缺省时统一使用 5m"，这样既不浪费 1h 缓存额度，也保留客户端自定义能力。
const DefaultCacheControlTTL = "5m"

// CLICurrentVersion 是内置的 Claude Code CLI 伪装版本号基线（三段 semver）。
// 用于 billing attribution block 中的 cc_version=X.Y.Z.{fp} 前缀以及 fingerprint 计算。
// 必须与 DefaultHeaders["User-Agent"] 中的版本号严格一致；不一致会被 Anthropic 判第三方。
//
// ⚠️ 读取实际生效的版本号请用 CLIVersion()，它会叠加 SUB2API_CLAUDE_CLI_VERSION 覆盖。
// 直接引用本常量只在"表达内置基线"时才正确（例如覆盖值的下限校验）。
const CLICurrentVersion = "2.1.280"

// ClaudeCodeMimicryBetas 按真实 Claude Code 2.1.280 的 beta 规则计算 OAuth mimic
// 请求的 anthropic-beta 集合（不再是固定列表）。
//
// 依据：本机 claude.exe 2.1.280 第一方实测抓包（MITM 拦截 /v1/messages?beta=true）
// 叠加二进制反编译。顺序与抓包逐字节一致。
//
// kw/Aw 基础选择表（模型相关）：
//   - claude-code-20250219：非 haiku 时携带；haiku 从头部剔除，agentic 请求在末尾补回
//   - oauth-2025-04-20：OAuth 路径携带（claude-code 不在首位时——haiku——居首）
//   - context-1m：仅模型名带 [1m] 后缀
//   - thinking 开启：interleaved-thinking（抓包中 display=omitted 时不带 redact-thinking，
//     且 HEAD 旧列表亦无，故本集合不含 redact-thinking；客户端显式传入时由合并逻辑保留）
//   - thinking-token-count / context-management / prompt-caching-scope：第一方携带
//   - mid-conversation-system 仅非 haiku；mid-conversation-tool-changes 再排除 sonnet-5
//
// SDK surfaceCapabilities.sdkBetas 能力位（抓包顺序，非 kw/Aw 选择表；二进制单看选择
// 表会漏掉，故以抓包为准）：advanced-tool-use、mid-conversation-system-clear-at、
// effort（thinking）、fallback-credit(2026-06-01)、thinking-binding-controls、cache-diagnosis。
//
// ⚠️ prompt-caching-evict-2026-05-12、extended-cache-ttl-2025-04-11、
// mid-conversation-output-config-2026-07-01 在多次第一方抓包中均未出现，故不纳入本集合。
//
// 已知未对齐项（无法从本函数入参重现，见变更说明）：body 顶层不带 betas 字段（已在
// gateway_upstream_request 移除注入）；cch 为逐请求计算的哈希（网关无算法，维持占位）；
// SDK 能力位对 haiku 的取舍缺抓包样本，暂只对非 haiku 生效。
//
// 使用建议：
//   - OAuth mimic：使用本函数按请求计算。
//   - OAuth 真实客户端透传：保留客户端 beta；未提供时使用模型对应默认值。
//   - API-key 账号：不要使用本函数，参见 APIKeyBetaHeader。
func ClaudeCodeMimicryBetas(modelID string, thinkingEnabled bool) []string {
	lower := strings.ToLower(modelID)
	isHaiku := strings.Contains(lower, "haiku")
	out := make([]string, 0, 16)
	if !isHaiku {
		out = append(out, BetaClaudeCode)
	}
	out = append(out, BetaOAuth)
	if strings.Contains(lower, "[1m]") {
		out = append(out, BetaContext1M)
	}
	if thinkingEnabled {
		out = append(out, BetaInterleavedThinking)
	}
	out = append(out, BetaThinkingTokenCount, BetaContextManagement, BetaPromptCachingScope)
	if !isHaiku {
		out = append(out, BetaMidConversationSystem)
		if !strings.Contains(lower, "sonnet-5") {
			out = append(out, BetaMidConversationToolChanges)
		}
		// SDK 能力位（抓包顺序）
		out = append(out, BetaAdvancedToolUse, BetaMidConversationSystemClearAt)
		if thinkingEnabled {
			out = append(out, BetaEffort)
		}
		out = append(out, BetaFallbackCreditLegacy, BetaThinkingBindingControls, BetaCacheDiagnosis)
	}
	if isHaiku {
		out = append(out, BetaClaudeCode)
	}
	return out
}

// SDKTSVersion 是真实 CLI 2.1.280 内置的 @anthropic-ai/sdk 版本。
// SDK 版本与 CLI 版本绑定发布，更新 CLICurrentVersion 时必须成对更新。
const SDKTSVersion = "0.112.1"

// SDKTSRuntimeVersion 是真实客户端上报的运行时版本。npm 安装形态为 Node v26.x；
// Bun 原生二进制上报 Bun 的 node-compat 版本（如 v22.x）。默认画像取 npm 形态。
const SDKTSRuntimeVersion = "v26.3.0"

// OAuthHelperUserAgent 是真实 CLI 控制面（token 交换/刷新等）的 UA
// （2.1.280 二进制实证：裸 fetch + sdk-ts UA，带 anthropic-beta: oauth-2025-04-20，
// 无 x-stainless-*、无显式 Accept）。
const OAuthHelperUserAgent = "anthropic-sdk-typescript/" + SDKTSVersion + " userOAuthProvider"

// DefaultHeaders 是 Claude Code 客户端默认请求头。
// 每次调用现构造：User-Agent 走 DefaultUserAgent()（运行期可变版本号），
// 不再在包 init 时固化。同一次请求内应只取一次 UA 字符串并在出站头与
// billing 两条路径间复用，避免版本缓存翻转瞬间头/体不一致。
func DefaultHeaders() map[string]string {
	return map[string]string{
		// Keep these in sync with recent Claude CLI traffic to reduce the chance
		// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
		// 版本组参考：本机 claude.exe 2.1.280 抓包（sdk 0.112.1 / node v26.3.0）。
		"User-Agent":                                DefaultUserAgent(),
		"X-Stainless-Lang":                          "js",
		"X-Stainless-Package-Version":               SDKTSVersion,
		"X-Stainless-OS":                            "Linux",
		"X-Stainless-Arch":                          "arm64",
		"X-Stainless-Runtime":                       "node",
		"X-Stainless-Runtime-Version":               SDKTSRuntimeVersion,
		"X-Stainless-Retry-Count":                   "0",
		"X-Stainless-Timeout":                       "600",
		"X-App":                                     "cli",
		"Anthropic-Dangerous-Direct-Browser-Access": "true",
	}
}

// Model 表示一个 Claude 模型
type Model struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

// DefaultModels Claude Code 客户端支持的默认模型列表
var DefaultModels = []Model{
	{
		ID:          "claude-fable-5-1",
		Type:        "model",
		DisplayName: "Claude Fable 5.1",
		CreatedAt:   "2026-09-01T00:00:00Z",
	},
	{
		ID:          "claude-fable-5",
		Type:        "model",
		DisplayName: "Claude Fable 5",
		CreatedAt:   "2026-06-09T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-5-20251101",
		Type:        "model",
		DisplayName: "Claude Opus 4.5",
		CreatedAt:   "2025-11-01T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-6",
		Type:        "model",
		DisplayName: "Claude Opus 4.6",
		CreatedAt:   "2026-02-06T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-7",
		Type:        "model",
		DisplayName: "Claude Opus 4.7",
		CreatedAt:   "2026-04-17T00:00:00Z",
	},
	{
		ID:          "claude-opus-4-8",
		Type:        "model",
		DisplayName: "Claude Opus 4.8",
		CreatedAt:   "2026-05-29T00:00:00Z",
	},
	{
		ID:          "claude-opus-5-5",
		Type:        "model",
		DisplayName: "Claude Opus 5.5",
		CreatedAt:   "2026-09-22T00:00:00Z",
	},
	{
		ID:          "claude-opus-5",
		Type:        "model",
		DisplayName: "Claude Opus 5",
		CreatedAt:   "2026-07-25T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-5",
		Type:        "model",
		DisplayName: "Claude Sonnet 5",
		CreatedAt:   "2026-07-01T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-6",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.6",
		CreatedAt:   "2026-02-18T00:00:00Z",
	},
	{
		ID:          "claude-sonnet-4-5-20250929",
		Type:        "model",
		DisplayName: "Claude Sonnet 4.5",
		CreatedAt:   "2025-09-29T00:00:00Z",
	},
	{
		ID:          "claude-haiku-4-5-20251001",
		Type:        "model",
		DisplayName: "Claude Haiku 4.5",
		CreatedAt:   "2025-10-01T00:00:00Z",
	},
}

// DefaultModelIDs 返回默认模型的 ID 列表
func DefaultModelIDs() []string {
	ids := make([]string, len(DefaultModels))
	for i, m := range DefaultModels {
		ids[i] = m.ID
	}
	return ids
}

// DefaultTestModel 测试时使用的默认模型
const DefaultTestModel = "claude-sonnet-4-5-20250929"

// ModelIDOverrides Claude OAuth 请求需要的模型 ID 映射
var ModelIDOverrides = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// ModelIDReverseOverrides 用于将上游模型 ID 还原为短名
var ModelIDReverseOverrides = map[string]string{
	"claude-sonnet-4-5-20250929": "claude-sonnet-4-5",
	"claude-opus-4-5-20251101":   "claude-opus-4-5",
	"claude-haiku-4-5-20251001":  "claude-haiku-4-5",
}

// NormalizeModelID 根据 Claude OAuth 规则映射模型
func NormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDOverrides[id]; ok {
		return mapped
	}
	return id
}

// DenormalizeModelID 将上游模型 ID 转换为短名
func DenormalizeModelID(id string) string {
	if id == "" {
		return id
	}
	if mapped, ok := ModelIDReverseOverrides[id]; ok {
		return mapped
	}
	return id
}
