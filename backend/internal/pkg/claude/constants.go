// Package claude provides constants and helpers for Claude API integration.
package claude

import "strings"

// Claude Code 客户端相关常量

// Beta header 常量
//
// 对齐真实 Claude Code CLI 2.1.290 的实测流量（2026-10-05：本机 win32-x64 2.1.290
// 二进制注册表/选择表拆解 + 本地捕获服务器第一方形态抓包；早期基线为 2.1.280/
// 2.1.287 claude.exe 抓包 + CLIProxyAPI 2026-09-23 第一方实测序）。
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

	// structured-outputs：条件携带，禁止加入任何默认/伪装固定列表。2.1.283 二进制实证：
	//   - sideQuery 仅在请求带 output_format（发出时为 output_config.format）时把它
	//     push 到 beta 列表末尾；SDK messages.parse() 同样追加在末尾；
	//   - 主循环选择表里它受 GrowthBook 开关 tengu_tool_pear 控制，与请求体无关，
	//     默认关闭，普通对话流量不携带。
	BetaStructuredOutputs = "structured-outputs-2025-12-15"

	// per-turn-control：2.1.287 抓包实证 sonnet-5-5 携带（另有 opus-5-5/fable-5-1
	// 或调用方显式请求时携带）。仅按模型/请求条件加入，不进固定默认列表。
	BetaPerTurnControl = "per-turn-control-2026-07-01"

	// SDK surfaceCapabilities.sdkBetas 能力位（2.1.280 第一方抓包实证：每次
	// /v1/messages?beta=true 均携带，经 mf()→surfaceCapabilities.sdkBetas() 注入；
	// 2.1.290 二进制 $Pn 实证 SDK 位仅第一方发送——第三方连接被
	// "SDK beta dropped on 3P" 白名单过滤，3P 抓包中只见 effort 及下列三个新位）。
	BetaAdvancedToolUse              = "advanced-tool-use-2025-11-20"
	BetaMidConversationSystemClearAt = "mid-conversation-system-clear-at-2026-08-21"
	BetaCacheDiagnosis               = "cache-diagnosis-2026-04-07"

	// dangerous-tool-use / afk-mode 是 2.1.290 新增的"第三方形态"位（2026-10-05
	// 双形态抓包实证：仅自定义 base URL 的 3P 请求携带，与 body.safeguards 配对；
	// 第一方直连由服务端完成对应分类，客户端不携带）。禁止加入第一方 mimic/默认列表。
	BetaDangerousToolUse = "dangerous-tool-use-2026-09-03"
	BetaAfkMode          = "afk-mode-2026-01-31"
	// BetaExtendedCacheTTL（见上文 2.1.280 注册表块）自 2.1.290 起进入第一方默认
	// 发送集（SDK 位，位于 thinking-binding-controls 与 cache-diagnosis 之间，
	// 双形态抓包均携带）；早期"抓包未出现、禁止入列"的注记作废。

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
	// 中作为 SDK 能力位每次都出现（header only，body 不带 fallbacks）。2.1.290 起默认
	// 发送集不再携带（2026-10-05 第一方/第三方双形态抓包均无），故从
	// ClaudeCodeMimicryBetas 移除；仅 header 令牌不会触发 fallback，body.fallbacks
	// 仍由上述门控独立剥离。
	// 注：fallback-credit-2026-07-01 在 2.1.290 二进制注册表中已移除（仅剩
	// 2026-06-01），BetaFallbackCredit 仅留作历史 sanitize 对照，禁止加入任何列表。
	BetaServerSideFallback   = "server-side-fallback-2026-07-01"
	BetaFallbackCredit       = "fallback-credit-2026-07-01"
	BetaFallbackCreditLegacy = "fallback-credit-2026-06-01"
)

// DroppedBetas 是转发时需要从 anthropic-beta header 中移除的 beta token 列表。
// 这些 token 是客户端特有的，不应透传给上游 API。
var DroppedBetas = []string{}

// DefaultBetaHeader Claude Code 客户端默认的 anthropic-beta header
// （2.1.290 第一方直连抓包实证：非 haiku + thinking 开启形态 = 基础选择表 +
// SDK 能力位 advanced-tool-use/clear-at/effort/thinking-binding-controls/
// extended-cache-ttl/cache-diagnosis；不含模型门控的 per-turn-control）。
const DefaultBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaMidConversationToolChanges + "," + BetaAdvancedToolUse + "," + BetaMidConversationSystemClearAt + "," + BetaEffort + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

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
// （2.1.290 二进制实证：count_tokens 走 lte(model) 基础位 ∩ {claude-code,
// interleaved-thinking, context-management, oauth} 后由 SDK 追加 token-counting。
// 本固定值对应"非 haiku + thinking 开启"形态；两个未对齐的细节：thinking 关闭时
// 真实客户端不含 interleaved-thinking、haiku 模型基础位无 claude-code——
// count_tokens 均为辅助请求，固定值影响可忽略）。
const CountTokensBetaHeader = BetaClaudeCode + "," + BetaOAuth + "," + BetaInterleavedThinking + "," + BetaContextManagement + "," + BetaTokenCounting

// HaikuBetaHeader Haiku 4.x 模型在 OAuth 真实客户端透传路径上的默认 anthropic-beta header。
// 2.1.290 第一方直连抓包实证：haiku 不剔除 claude-code，而是挪到基础位末尾；
// oauth 居首；SDK 能力位（advanced-tool-use/thinking-binding-controls/
// extended-cache-ttl/cache-diagnosis）殿后。
// haiku-5-5 起改用 Haiku5BetaHeader。
// OAuth mimic 路径统一使用 ClaudeCodeMimicryBetas。
const HaikuBetaHeader = BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaClaudeCode + "," + BetaAdvancedToolUse + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

// Haiku5BetaHeader Haiku 5.5+（2.1.293 起默认 Haiku，能力集对齐非 haiku）在 OAuth
// 透传路径的默认 anthropic-beta header。2.1.293 第一方抓包实证序。
const Haiku5BetaHeader = BetaOAuth + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaClaudeCode + "," + BetaPerTurnControl + "," + BetaMidConversationToolChanges + "," + BetaAdvancedToolUse + "," + BetaMidConversationSystemClearAt + "," + BetaEffort + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

// APIKeyBetaHeader API-key 账号建议使用的 anthropic-beta header（不包含 oauth）
const APIKeyBetaHeader = BetaClaudeCode + "," + BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaMidConversationToolChanges + "," + BetaAdvancedToolUse + "," + BetaMidConversationSystemClearAt + "," + BetaEffort + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

// APIKeyHaikuBetaHeader Haiku 4.x 模型在 API-key 账号下使用的 anthropic-beta header
// （不包含 oauth；序与 2.1.290 第一方 haiku 抓包一致）
const APIKeyHaikuBetaHeader = BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaClaudeCode + "," + BetaAdvancedToolUse + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

// APIKeyHaiku5BetaHeader Haiku 5.5+ 在 API-key 账号下使用的 anthropic-beta header
// （不包含 oauth；序与 2.1.293 第一方 haiku-5-5 抓包一致）
const APIKeyHaiku5BetaHeader = BetaInterleavedThinking + "," + BetaThinkingTokenCount + "," + BetaContextManagement + "," + BetaPromptCachingScope + "," + BetaMidConversationSystem + "," + BetaClaudeCode + "," + BetaPerTurnControl + "," + BetaMidConversationToolChanges + "," + BetaAdvancedToolUse + "," + BetaMidConversationSystemClearAt + "," + BetaEffort + "," + BetaThinkingBindingControls + "," + BetaExtendedCacheTTL + "," + BetaCacheDiagnosis

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
const CLICurrentVersion = "2.1.296"

// ClaudeCodeMimicryBetas 按真实 Claude Code 2.1.290 的 beta 规则计算 OAuth mimic
// 请求的 anthropic-beta 集合（不再是固定列表）。
//
// 依据：本机 2.1.290 win32-x64 二进制注册表/选择表拆解 + 本地捕获服务器第一方
// 形态抓包（2026-10-05），叠加 2.1.280/2.1.287 claude.exe 抓包基线。
// 顺序与抓包逐字节一致。
//
// 基础选择表 aN（模型相关，2.1.290 二进制实证）：
//   - claude-code-20250219：非 haiku 时携带；haiku 从头部剔除，agentic 请求在末尾补回
//   - oauth-2025-04-20：OAuth 路径携带（claude-code 不在首位时——haiku——居首）
//   - context-1m：仅模型名带 [1m] 后缀
//   - thinking 开启：interleaved-thinking（抓包中 display=omitted 时不带 redact-thinking，
//     且 HEAD 旧列表亦无，故本集合不含 redact-thinking；客户端显式传入时由合并逻辑保留）；
//     thinking-token-count 自 2.1.290 起与 interleaved-thinking 同门控
//     （aN: interleavedThinking && !experimentalBetasOff）
//   - context-management / prompt-caching-scope：第一方携带
//   - mid-conversation-system 仅非 haiku；per-turn-control 仅 sonnet-5-5/
//     opus-5-5/fable-5-1（2.1.287 抓包实证）；mid-conversation-tool-changes
//     排除 sonnet-5（sonnet-5-5 起恢复携带，2.1.287 实证）
//
// SDK surfaceCapabilities.sdkBetas 能力位（仅第一方直连发送；2.1.290 二进制 $Pn
// 实证第三方连接被 "SDK beta dropped on 3P" 白名单过滤。第一方抓包顺序，非 aN
// 选择表；二进制单看选择表会漏掉，故以抓包为准）：
//
//   - 非 haiku：advanced-tool-use、mid-conversation-system-clear-at、
//     effort（thinking）、thinking-binding-controls、extended-cache-ttl、cache-diagnosis
//   - haiku-4-x：advanced-tool-use、thinking-binding-controls、extended-cache-ttl、
//     cache-diagnosis（无 clear-at/effort，2026-10-05 第一方抓包实证）；
//     haiku-5-5 起能力集对齐非 haiku（含 clear-at/effort，2026-10-07 抓包实证）
//   - fallback-credit(2026-06-01) 自 2.1.290 起不再携带（2.1.280 携带）；
//     dangerous-tool-use / afk-mode 为 3P 形态位（与 body.safeguards 配对，
//     第一方直连无此 body 字段），第一方不携带。
//
// ⚠️ prompt-caching-evict-2026-05-12、mid-conversation-output-config-2026-07-01
// 在历次第一方抓包中均未出现，故不纳入本集合。
//
// 已知未对齐项（无法从本函数入参重现，见变更说明）：body 顶层不带 betas 字段（已在
// gateway_upstream_request 移除注入）；cch 在 2.1.290 官方构建上为逐请求签名哈希
// （5 位 hex），JS 层字面量 00000 仅为签名不可用/vertex 的回退形态，网关无签名
// 模块故维持 00000 占位（已知残留差异）。
//
// 使用建议：
//   - OAuth mimic：使用本函数按请求计算。
//   - OAuth 真实客户端透传：保留客户端 beta；未提供时使用模型对应默认值。
//   - API-key 账号：不要使用本函数，参见 APIKeyBetaHeader。
func ClaudeCodeMimicryBetas(modelID string, thinkingEnabled bool) []string {
	lower := strings.ToLower(modelID)
	isHaiku := strings.Contains(lower, "haiku")
	// haiku-5-5 起（2.1.293 第一方抓包实证）：能力集与非 haiku 对齐（adaptive
	// thinking、effort、mid-conversation-system、per-turn-control、完整 SDK 位），
	// 仅 claude-code/oauth 排序规则不变。haiku-4-x 维持旧形态（budget thinking、
	// 精简能力集）。
	newGenHaiku := strings.Contains(lower, "haiku-5")
	fullCaps := !isHaiku || newGenHaiku
	out := make([]string, 0, 20)
	if !isHaiku {
		out = append(out, BetaClaudeCode)
	}
	out = append(out, BetaOAuth)
	if strings.Contains(lower, "[1m]") {
		out = append(out, BetaContext1M)
	}
	if thinkingEnabled {
		out = append(out, BetaInterleavedThinking)
		// thinking-token-count 与 interleaved-thinking 同门控（2.1.290 aN 表实证）
		out = append(out, BetaThinkingTokenCount)
	}
	out = append(out, BetaContextManagement, BetaPromptCachingScope)
	if fullCaps {
		out = append(out, BetaMidConversationSystem)
	}
	if isHaiku {
		// haiku 的 claude-code 从头部剔除，在基础位末尾补回（agentic 规则；
		// 抓包实证：haiku-4-5 位于 prompt-caching-scope 之后，haiku-5-5 位于
		// mid-conversation-system 之后）
		out = append(out, BetaClaudeCode)
	}
	if fullCaps {
		// per-turn-control：2.1.287 抓包实证 sonnet-5-5 携带（另有 opus-5-5/fable-5-1）;
		// 2.1.293 抓包实证 haiku-5-5 同样携带
		if strings.Contains(lower, "sonnet-5-5") || strings.Contains(lower, "opus-5-5") || strings.Contains(lower, "fable-5-1") || newGenHaiku {
			out = append(out, BetaPerTurnControl)
		}
		// sonnet-5 不带 mid-conversation-tool-changes；sonnet-5-5 起恢复携带（2.1.287 实证）
		isSonnet5Legacy := strings.Contains(lower, "sonnet-5") && !strings.Contains(lower, "sonnet-5-5")
		if !isSonnet5Legacy {
			out = append(out, BetaMidConversationToolChanges)
		}
		// SDK 能力位（2.1.290 第一方直连抓包顺序）
		out = append(out, BetaAdvancedToolUse, BetaMidConversationSystemClearAt)
		if thinkingEnabled {
			out = append(out, BetaEffort)
		}
		out = append(out, BetaThinkingBindingControls, BetaExtendedCacheTTL, BetaCacheDiagnosis)
	}
	if isHaiku && !newGenHaiku {
		// haiku-4-x 第一方抓包实证 SDK 位：advanced-tool-use / thinking-binding-controls /
		// extended-cache-ttl / cache-diagnosis（无 clear-at/effort）
		out = append(out, BetaAdvancedToolUse, BetaThinkingBindingControls, BetaExtendedCacheTTL, BetaCacheDiagnosis)
	}
	return out
}

// SDKTSVersion 是真实 CLI 2.1.296 内置的 @anthropic-ai/sdk 版本
// （win32-x64 二进制实证：2.1.290/2.1.293/2.1.296 均为 0.128.0）。
// SDK 版本与 CLI 版本绑定发布，更新 CLICurrentVersion 时必须成对更新。
const SDKTSVersion = "0.128.0"

// SDKTSRuntimeVersion 是真实客户端上报的 X-Stainless-Runtime-Version。
// SDK 的运行时探测 oa() 只区分 deno/edge/node，无 bun 分支：Bun 提供
// globalThis.process（[object process]）→ 判定为 "node"，版本取 process.version。
// 本机 Bun 1.4.3 打包的 claude.exe 2.1.283 的 process.version = v26.3.0
// （二进制内唯一出现的 v2x.y.z，出现 6 次），故 X-Stainless-Runtime: node +
// v26.3.0 与 Bun TLS/线级指纹是自洽的一对，不是 npm/Bun 画像混用。
const SDKTSRuntimeVersion = "v26.3.0"

// OAuthLoginAxiosVersion / OAuthLoginUserAgent 对应真实 CLI 的 OAuth token 端点调用：
// 授权码交换与 token 刷新都用 axios `_t.post(TOKEN_URL, body, {headers:{Content-Type}})`
// （2.1.287 二进制实证：交换 zqr、刷新 npe），axios 1.9.0 的 Node http 适配器自动补
// `User-Agent: axios/1.9.0`、`Accept: application/json, text/plain, */*`、
// `Accept-Encoding: gzip, compress, deflate, br`；**不带 anthropic-beta**。
// 二进制里带 anthropic-beta 的 `userOAuthProvider` 裸 fetch 是 SDK 凭据文件
// profile 的刷新 helper，不是 Claude Code 自己登录态的刷新路径，不能拿来模仿。
const (
	OAuthLoginAxiosVersion = "1.9.0"
	OAuthLoginUserAgent    = "axios/" + OAuthLoginAxiosVersion
	// OAuthLoginAccept / OAuthLoginAcceptEncoding 是 axios Node 适配器的默认值。
	OAuthLoginAccept         = "application/json, text/plain, */*"
	OAuthLoginAcceptEncoding = "gzip, compress, deflate, br"
)

// DefaultHeaders 是 Claude Code 客户端默认请求头。
// 每次调用现构造：User-Agent 走 DefaultUserAgent()（运行期可变版本号），
// 不再在包 init 时固化。同一次请求内应只取一次 UA 字符串并在出站头与
// billing 两条路径间复用，避免版本缓存翻转瞬间头/体不一致。
func DefaultHeaders() map[string]string {
	return map[string]string{
		// Keep these in sync with recent Claude CLI traffic to reduce the chance
		// that Claude Code-scoped OAuth credentials are rejected as "non-CLI" usage.
		// 版本组参考：本机 claude.exe 2.1.290 抓包（sdk 0.128.0 / node v26.3.0）。
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
		ID:          "claude-sonnet-5-5",
		Type:        "model",
		DisplayName: "Claude Sonnet 5.5",
		CreatedAt:   "2026-09-29T00:00:00Z",
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
	{
		ID:          "claude-haiku-5-5",
		Type:        "model",
		DisplayName: "Claude Haiku 5.5",
		CreatedAt:   "2026-10-06T00:00:00Z",
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
