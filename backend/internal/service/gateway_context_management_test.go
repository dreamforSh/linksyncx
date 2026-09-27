//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ============================================================================
// 背景
// ============================================================================
//
// Anthropic 上游对 body.context_management 字段实施 Pydantic schema 校验：
// 当且仅当 anthropic-beta header 含 context-management-2025-06-27 时接受。
// 否则报：
//   "context_management: Extra inputs are not permitted"
//
// 本仓采用能力维度对称约束（与 Bedrock 路径的 sanitizeBedrockFieldsForBetaTokens
// 对称）：在所有 Anthropic 直连出口，按最终 anthropic-beta header 是否含上述 token
// 决定 body 是否保留同名字段。
//
// 本文件覆盖：
//   1) sanitizeAnthropicBodyForBetaTokens 纯函数
//   2) anthropicBetaTokensContains 解析辅助函数
//   3) computeFinalAnthropicBeta / computeFinalCountTokensAnthropicBeta 各路径
//   4) normalizeClaudeOAuthRequestBody 的 context_management 补齐行为（不再按 model 短路）

// ============================================================================
// anthropicBetaTokensContains
// ============================================================================

func TestAnthropicBetaTokensContains_EmptyInputs(t *testing.T) {
	require.False(t, anthropicBetaTokensContains("", "context-management-2025-06-27"))
	require.False(t, anthropicBetaTokensContains("oauth-2025-04-20", ""))
}

func TestAnthropicBetaTokensContains_SingleToken(t *testing.T) {
	require.True(t, anthropicBetaTokensContains("context-management-2025-06-27", "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_MultiTokenComma(t *testing.T) {
	header := "oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14"
	require.True(t, anthropicBetaTokensContains(header, "context-management-2025-06-27"))
	require.True(t, anthropicBetaTokensContains(header, "oauth-2025-04-20"))
	require.False(t, anthropicBetaTokensContains(header, "fast-mode-2026-02-01"))
}

func TestAnthropicBetaTokensContains_ToleratesWhitespace(t *testing.T) {
	header := "oauth-2025-04-20 , context-management-2025-06-27 ,  interleaved-thinking-2025-05-14"
	require.True(t, anthropicBetaTokensContains(header, "context-management-2025-06-27"))
}

func TestAnthropicBetaTokensContains_SubstringNotMatched(t *testing.T) {
	// 严格 token 比较，不应被子串误匹配
	require.False(t, anthropicBetaTokensContains("context-management-2025-06-27-rev2", "context-management-2025-06-27"),
		"必须按 token 边界匹配，不允许 prefix 子串误命中")
}

// ============================================================================
// sanitizeAnthropicBodyForBetaTokens
// ============================================================================

func TestSanitizeAnthropicBodyForBetaTokens_NoFieldNoChange(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.False(t, changed)
	require.Equal(t, string(body), string(out))
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body,
		"oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	require.False(t, changed)
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20,interleaved-thinking-2025-05-14")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists(),
		"header 不含 context-management beta 时必须 strip 同名字段")
}

func TestSanitizeAnthropicBodyForBetaTokens_FieldStrippedWhenBetaEmpty(t *testing.T) {
	body := []byte(`{"context_management":{"edits":[]},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "")
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestSanitizeAnthropicBodyForBetaTokens_EmptyBody(t *testing.T) {
	out, changed := sanitizeAnthropicBodyForBetaTokens([]byte{}, "")
	require.False(t, changed)
	require.Empty(t, out)

	out, changed = sanitizeAnthropicBodyForBetaTokens(nil, "")
	require.False(t, changed)
	require.Empty(t, out)
}

func TestSanitizeAnthropicBodyForBetaTokens_ThinkingBlockBindingKeptWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaThinkingBindingControls)
	require.False(t, changed)
	require.Equal(t, "drop_block",
		gjson.GetBytes(out, "thinking.block_binding.prefix_mismatch_behavior").String())
}

func TestSanitizeAnthropicBodyForBetaTokens_ThinkingBlockBindingStrippedWhenBetaMissing(t *testing.T) {
	body := []byte(`{"model":"claude-fable-5-1","thinking":{"type":"adaptive","display":"summarized","block_binding":{"prefix_mismatch_behavior":"drop_block"}},"messages":[]}`)
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, claude.BetaContextManagement)
	require.True(t, changed)
	require.False(t, gjson.GetBytes(out, "thinking.block_binding").Exists())
	require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
	require.Equal(t, "summarized", gjson.GetBytes(out, "thinking.display").String())
}

// ★ 关键回归断言：能力维度 sanitize 解决了 "真 CC + haiku" 路径的过度删除问题。
// 真实 Claude Code CLI 2.1.87+ 客户端 header 含 context-management beta；
// 即使 model 是 haiku，sanitize 也不应剥离功能字段。
func TestSanitizeAnthropicBodyForBetaTokens_HaikuRealCCClientPreservesField(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	// 真 Claude Code CLI 2.1.87+ 客户端 header 含 context-management beta
	clientBeta := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27"
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, clientBeta)
	require.False(t, changed,
		"真 CC 客户端 header 含 context-management beta 时，haiku body 字段必须保留（功能不丢）")
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

// ============================================================================
// computeFinalAnthropicBeta — 关键路径
// ============================================================================

func newTestGatewayServiceForBeta(injectBetaForAPIKey bool) *GatewayService {
	cfg := &config.Config{}
	cfg.Gateway.InjectBetaForAPIKey = injectBetaForAPIKey
	return &GatewayService{cfg: cfg}
}

func TestComputeFinalAnthropicBeta_OAuthMimic_NonHaiku_IncludesContextManagement(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"OAuth mimic non-haiku 必须注入完整 CC mimicry beta，含 context-management-2025-06-27")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaOAuth))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaClaudeCode))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaThinkingBindingControls),
		"OAuth mimic 非 haiku 必须注入 thinking-binding-controls beta（2.1.280 抓包实证）")
}

func TestComputeFinalAnthropicBeta_OAuthMimic_Haiku_IncludesFullClaudeCodeBetas(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	// 2.1.280 抓包实证：haiku 的 claude-code 挪到末尾而非缺席，oauth 居首。
	require.Equal(t, strings.Join(claude.ClaudeCodeMimicryBetas("claude-haiku-4-5", false), ","), final)
	for _, beta := range claude.ClaudeCodeMimicryBetas("claude-haiku-4-5", false) {
		require.Truef(t, anthropicBetaTokensContains(final, beta),
			"OAuth mimic Haiku 必须包含完整 Claude Code beta 集合，缺少 %s", beta)
	}
}

func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBeta(t *testing.T) {
	// mimic 路径下原代码白名单透传被跳过，client beta 应被忽略
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.False(t, strings.Contains(final, "custom-experimental-beta"),
		"mimic 路径必须忽略客户端 anthropic-beta header")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaPromptCachingScope),
		"mimic 必须注入 2.1.280 注册表序的恒定 beta")

	// 显式 dropSet 仍能移除 mimic 注入的 beta，且不会因此放行客户端未知 beta。
	dropped, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`),
		map[string]struct{}{claude.BetaPromptCachingScope: {}})
	require.True(t, ok)
	require.False(t, anthropicBetaTokensContains(dropped, claude.BetaPromptCachingScope),
		"显式 dropSet 必须能移除 mimic 注入的 beta")
	require.False(t, strings.Contains(dropped, "custom-experimental-beta"),
		"dropSet 存在时 mimic 路径仍必须忽略客户端 anthropic-beta header")
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_NonHaiku_PreservesClientContextManagement(t *testing.T) {
	// 真 CC 客户端透传：客户端 header 中的 context-management beta 必须保留
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27")
	final, ok := s.computeFinalAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement))
}

func TestComputeFinalAnthropicBeta_OAuthTransparent_Haiku_RealCCPreservesContextManagement(t *testing.T) {
	// haiku 透传 + 客户端带 context-management beta → 必须保留
	// （能力维度核心场景：避免 model-name 误删客户端透传的功能 beta）
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,interleaved-thinking-2025-05-14")
	final, ok := s.computeFinalAnthropicBeta("oauth", false, "claude-haiku-4-5", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"真 CC + haiku + 客户端带 context-management beta → 透传必须保留")
}

func TestComputeFinalAnthropicBeta_APIKey_PassesClientBetaThroughDropSet(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,custom-beta")
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, "oauth-2025-04-20"))
	require.True(t, anthropicBetaTokensContains(final, "custom-beta"))
}

func TestComputeFinalAnthropicBeta_APIKey_NoClientBetaInjectOff_ShouldNotSet(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-sonnet-4-6", http.Header{}, []byte(`{}`), nil)
	require.False(t, ok, "API-key + 客户端未传 + InjectBetaForAPIKey 关 → 不应主动设置 anthropic-beta")
	require.Equal(t, "", final)
}

func TestComputeFinalAnthropicBeta_APIKeyHaiku_StillUsesAPIKeyBetas(t *testing.T) {
	s := newTestGatewayServiceForBeta(true)
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"messages":[]}`)
	final, ok := s.computeFinalAnthropicBeta("apikey", false, "claude-haiku-4-5", http.Header{}, body, nil)
	require.True(t, ok)
	require.Equal(t, claude.APIKeyHaikuBetaHeader, final)
	require.False(t, anthropicBetaTokensContains(final, claude.BetaOAuth))
	// 2.1.280 抓包实证：haiku 的 claude-code 在末尾而非缺席。
	parts := strings.Split(final, ",")
	require.Equal(t, claude.BetaClaudeCode, parts[len(parts)-1])
}

// ============================================================================
// computeFinalCountTokensAnthropicBeta
// ============================================================================

func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_AlwaysIncludesContextManagement(t *testing.T) {
	// count_tokens mimic 继续注入完整 mimicry beta，并额外携带 token-counting beta。
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"count_tokens + mimic Haiku 必须保留 context-management beta")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"count_tokens 路径必须含 token-counting beta")
}

// count_tokens mimic 与 messages mimic 对齐：不信任客户端透传的 beta，
// 使用实测固定集合（CountTokensBetaHeader）。
func TestComputeFinalCountTokensAnthropicBeta_OAuthMimic_IgnoresClientBeta(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta,context-1m-2025-08-07")
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", true, "claude-haiku-4-5", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.Equal(t, claude.CountTokensBetaHeader, final)
	require.False(t, anthropicBetaTokensContains(final, "custom-experimental-beta"),
		"count_tokens mimic 与 messages mimic 同设计：忽略客户端 beta")
	require.False(t, anthropicBetaTokensContains(final, "context-1m-2025-08-07"))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement))
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"同时补齐 token-counting beta")
}

// messages 与 count_tokens 的 mimic 路径同设计：都忽略客户端 beta。
func TestComputeFinalAnthropicBeta_OAuthMimic_IgnoresClientBetaExplicit(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "custom-experimental-beta")
	final, ok := s.computeFinalAnthropicBeta("oauth", true, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.False(t, anthropicBetaTokensContains(final, "custom-experimental-beta"),
		"messages mimic 跳过白名单透传 → 客户端 beta 不进入计算")
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_NoClientBetaInjectsDefault(t *testing.T) {
	// 真 CC 客户端透传 + 客户端未传 anthropic-beta → 用 CountTokensBetaHeader 兜底
	s := newTestGatewayServiceForBeta(false)
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", false, "claude-haiku-4-5", http.Header{}, []byte(`{}`), nil)
	require.True(t, ok)
	require.Equal(t, claude.CountTokensBetaHeader, final)
	// 2.1.280 实测 count_tokens 固定集合含 context-management
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement))
}

func TestComputeFinalCountTokensAnthropicBeta_OAuthTransparent_AppendsBetaTokenCounting(t *testing.T) {
	s := newTestGatewayServiceForBeta(false)
	hdr := http.Header{}
	hdr.Set("anthropic-beta", "oauth-2025-04-20,context-management-2025-06-27")
	final, ok := s.computeFinalCountTokensAnthropicBeta("oauth", false, "claude-sonnet-4-6", hdr, []byte(`{}`), nil)
	require.True(t, ok)
	require.True(t, anthropicBetaTokensContains(final, claude.BetaTokenCounting),
		"客户端未带 token-counting beta 时必须补齐")
	require.True(t, anthropicBetaTokensContains(final, claude.BetaContextManagement),
		"客户端带的 context-management beta 必须保留")
}

// ============================================================================
// normalizeClaudeOAuthRequestBody — 回归：context_management 补齐恢复原行为
// ============================================================================
//
// 重构后该函数不再按 model 名短路：thinking=enabled/adaptive 时补齐 context_management，
// 与 model 无关。strip 责任移交 sanitizeAnthropicBodyForBetaTokens（在
// buildUpstreamRequest 层按最终 beta header 执行）。

func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingEnabled(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
	require.Equal(t, "clear_thinking_20251015",
		gjson.GetBytes(out, "context_management.edits.0.type").String())
}

func TestNormalizeClaudeOAuthRequestBody_InjectsContextManagement_ThinkingAdaptive(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","thinking":{"type":"adaptive"},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-4-7", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestNormalizeClaudeOAuthRequestBody_HaikuStillInjects_StripDeferredToSanitize(t *testing.T) {
	// Haiku + thinking=enabled：normalize 阶段仍按 CLI mimicry 行为补齐字段；
	// 最终是否保留仍由 beta 能力对称的 sanitize 统一决定。
	body := []byte(`{"model":"claude-haiku-4-5","thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})
	require.True(t, gjson.GetBytes(out, "context_management").Exists(),
		"normalize 不再按 model 名短路；strip 责任移交 sanitize 层")
}

func TestNormalizeClaudeOAuthRequestBody_PreservesClientContextManagement(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","context_management":{"edits":[{"type":"custom_strategy"}]},"thinking":{"type":"enabled","budget_tokens":1000},"messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-opus-4-7", claudeOAuthNormalizeOptions{})
	require.Equal(t, "custom_strategy",
		gjson.GetBytes(out, "context_management.edits.0.type").String(),
		"客户端透传的 context_management 内容必须原样保留")
}

func TestNormalizeClaudeOAuthRequestBody_NoThinking_NoInject(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[]}`)
	out, _ := normalizeClaudeOAuthRequestBody(body, "claude-sonnet-4-6", claudeOAuthNormalizeOptions{})
	require.False(t, gjson.GetBytes(out, "context_management").Exists())
}

func TestNormalizeClaudeOAuthRequestBody_HaikuShortModelStillNormalizesToDatedID(t *testing.T) {
	body := []byte(`{"model":"claude-haiku-4-5","messages":[]}`)
	out, modelID := normalizeClaudeOAuthRequestBody(body, "claude-haiku-4-5", claudeOAuthNormalizeOptions{})
	require.Equal(t, "claude-haiku-4-5-20251001", modelID)
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(out, "model").String())
}

func TestApplyClaudeCodeOAuthMimicryToBody_HaikuRewritesSystem(t *testing.T) {
	account := &Account{ID: 405, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"model":"claude-haiku-4-5","system":"Pi project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{cfg: &config.Config{}}

	out := svc.applyClaudeCodeOAuthMimicryToBody(
		context.Background(), nil, account, body, "Pi project instructions", "claude-haiku-4-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	// 2.1.280 4-block 形态：billing / 身份 / "# Reporting outcomes" / 静态核心
	require.Len(t, system, 4)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.Equal(t, claudeCodeReportingOutcomesPrompt, system[2].Get("text").String())
	require.False(t, system[2].Get("cache_control").Exists(), "# Reporting outcomes 块不带 cache_control")
	require.Equal(t, "global", system[3].Get("cache_control.scope").String(), "静态核心块 cache_control 带 scope:global")
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Pi project instructions")
	require.Equal(t, "claude-haiku-4-5-20251001", gjson.GetBytes(out, "model").String())
}

func TestApplyClaudeCodeOAuthMimicryToBody_FableOmitsRefusedExpansion(t *testing.T) {
	account := &Account{ID: 406, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	body := []byte(`{"model":"claude-fable-5","system":"Project instructions","messages":[{"role":"user","content":"hello"}]}`)
	svc := &GatewayService{cfg: &config.Config{}}

	out := svc.applyClaudeCodeOAuthMimicryToBody(
		context.Background(), nil, account, body, "Project instructions", "claude-fable-5",
	)

	system := gjson.GetBytes(out, "system").Array()
	require.Len(t, system, 2)
	require.Contains(t, system[0].Get("text").String(), "x-anthropic-billing-header:")
	require.Equal(t, claudeCodeSystemPrompt, system[1].Get("text").String())
	require.NotContains(t, string(out), claudeCodeSystemPromptExpansion)
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), "Project instructions")
	require.Equal(t, "Understood. I will follow these instructions.", gjson.GetBytes(out, "messages.1.content.0.text").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.2.content").String())
}

// ============================================================================
// passthrough 集成测试：buildUpstreamRequest-
// AnthropicAPIKeyPassthrough 与 buildCountTokensRequestAnthropicAPIKeyPassthrough
// 路径上 sanitize 是否生效。
// ============================================================================

// passthrough 集成测试不设 base_url，避开 validateUpstreamBaseURL 对 cfg.Security 的依赖。
// targetURL 会走默认 claudeAPIURL，sanitize 逻辑与 baseURL 是否存在无关。
func newAnthropicAPIKeyPassthroughAccountForBetaTest() *Account {
	return &Account{
		ID:       501,
		Name:     "anthropic-apikey-passthrough-ctxmgmt-test",
		Platform: PlatformAnthropic,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "upstream-key",
		},
		Extra:       map[string]any{"anthropic_passthrough": true},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func readUpstreamBodyForTest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	require.NotNil(t, req.Body)
	b, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	return b
}

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_StripsContextManagementWhenClientHeaderMissingBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	// 客户端仅带 oauth beta，不带 context-management-2025-06-27
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20")

	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "context_management").Exists(),
		"API-key passthrough + 客户端未带 context-management beta → strip body 字段")
}

func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_PreservesContextManagementWhenClientHeaderHasBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20,context-management-2025-06-27")

	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "context_management").Exists(),
		"API-key passthrough + 客户端带 context-management beta → 字段保留（不过度删除）")
}

func TestBuildCountTokensRequestAnthropicAPIKeyPassthrough_StripsContextManagementWhenClientHeaderMissingBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20,token-counting-2024-11-01")

	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "context_management").Exists(),
		"count_tokens passthrough + 客户端未带 context-management beta → strip")
}

// ============================================================================
// 集成测试：buildUpstreamRequest
// 全路径验证上游 outgoing body 与 anthropic-beta header 严格对称。
// 这个测试能挡住未来某人忘调 sanitize / 将 sanitize 挪到 CCH 之后 等 regression。
// ============================================================================

func TestBuildUpstreamRequest_OAuthMimicHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 401, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// Haiku + mimic CC 使用完整 beta，其中包含 context-management；body 必须对称保留。
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, true, // mimicClaudeCode=true
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"OAuth mimic + Haiku 端到端：outgoing body 必须保留 context_management")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"对称约束：outgoing anthropic-beta header 必须包含 context-management beta")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaClaudeCode),
		"Haiku mimic 必须携带 claude-code beta")
}

func TestBuildUpstreamRequest_APIKeyHaiku_RemainsUnmimicked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{
		ID: 404, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","system":"API-key client system","thinking":{"type":"enabled"},"messages":[]}`)
	svc := newTestGatewayServiceForBeta(true)
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false, false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.Equal(t, "API-key client system", gjson.GetBytes(outBody, "system").String())
	require.Equal(t, claude.APIKeyHaikuBetaHeader, getHeaderRaw(req.Header, "anthropic-beta"))
	require.False(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), claude.BetaOAuth))
	require.NotContains(t, string(outBody), "x-anthropic-billing-header:")
}

func TestBuildUpstreamRequest_OAuthMimicNonHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	account := &Account{ID: 402, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive,
		Schedulable: true,
	}
	// sonnet + mimic CC → final beta = FullClaudeCodeMimicryBetas（含 context-management）→
	// body 保留。
	body := []byte(`{"model":"claude-sonnet-4-6","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-sonnet-4-6", false, true,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"OAuth mimic + non-haiku：outgoing body 必须保留 context_management。")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"对称约束：outgoing anthropic-beta header 同时含 context-management beta")
}

func TestBuildUpstreamRequest_OAuthTransparentHaikuWithRealCCBeta_PreservesField(t *testing.T) {
	// 端到端验证：真 CC 客户端 + haiku + 客户端 header 带 context-management beta
	// → final beta 透传 → 不应该过度删除 body 字段
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta",
		"claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27")

	account := &Account{ID: 403, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", false, false, // mimicClaudeCode=false（真 CC）
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"真 CC 透传路径：客户端 header 中的 context-management beta 必须保留")
	require.True(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"回归保护：真 CC + haiku + 客户端带 beta token 时，clear_thinking_20251015 功能不能静默失效")
}

// count_tokens 主路径 E2E 集成测试
func TestBuildCountTokensRequest_OAuthMimicHaiku_PreservesContextManagementEndToEnd(t *testing.T) {
	// count_tokens 仍注入 BetaContextManagement 和 BetaTokenCounting（header 层）。
	// 但 2.1.280 二进制实证：真实 CLI 的 messages.countTokens({...}) 仅传 model/messages/
	// tools/betas?/thinking?，body 不含 system/metadata/context_management/max_tokens，
	// 故 OAuth 路径会剥离 context_management（详见 stripCountTokensOAuthOnlyFields）。
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	account := &Account{ID: 411, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, body,
		"oauth-tok", "oauth", "claude-haiku-4-5", true, // mimicClaudeCode=true
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	outBeta := getHeaderRaw(req.Header, "anthropic-beta")

	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaContextManagement),
		"count_tokens mimic 始终注入 context-management beta")
	require.False(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"真实 CLI count_tokens body 不含 context_management（OAuth 路径剥离）")
	require.True(t, anthropicBetaTokensContains(outBeta, claude.BetaTokenCounting),
		"count_tokens 路径必须含 token-counting beta")
}

func TestBuildCountTokensRequest_OAuthMimic_DropsInjectedMaxTokens(t *testing.T) {
	// OAuth mimicry injects max_tokens=128000 for normal messages requests. It is
	// invalid for Anthropic's count_tokens endpoint and must be stripped on wire.
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	account := &Account{ID: 413, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "oauth-tok"},
		Status:      StatusActive, Schedulable: true,
	}
	normalized, _ := normalizeClaudeOAuthRequestBody(
		[]byte(`{"model":"claude-sonnet-4-5","messages":[]}`),
		"claude-sonnet-4-5", claudeOAuthNormalizeOptions{},
	)
	require.Equal(t, int64(128000), gjson.GetBytes(normalized, "max_tokens").Int(),
		"precondition: OAuth mimicry injects the Claude Code default")

	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, normalized,
		"oauth-tok", "oauth", "claude-sonnet-4-5", true,
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "max_tokens").Exists(),
		"count_tokens wire body must not contain max_tokens")
}

func TestBuildCountTokensRequest_APIKeyHaiku_StripsContextManagementEndToEnd(t *testing.T) {
	// API-key + haiku + 客户端 header 不带 context-management beta → final beta 不含 → strip
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{ID: 412, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildCountTokensRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.False(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"count_tokens API-key + 客户端未带 beta token → body strip")
}

func TestBuildCountTokensRequest_StripsCacheControlOnlyFromLiteralDeferredTools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-haiku-4-5","messages":[],"tools":[{"name":"deferred","custom":{"defer_loading":true},"cache_control":{"type":"ephemeral"}},{"name":"ordinary","custom":{"defer_loading":false},"cache_control":{"type":"ephemeral"}},{"name":"string","custom":{"defer_loading":"true"},"cache_control":{"type":"ephemeral"}},{"name":"number","custom":{"defer_loading":1},"cache_control":{"type":"ephemeral"}},{"name":"object","custom":{"defer_loading":{}},"cache_control":{"type":"ephemeral"}}]}`)

	tests := []struct {
		name      string
		account   *Account
		token     string
		tokenType string
	}{
		{
			name:      "generic API key",
			account:   &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			token:     "sk-ant-test",
			tokenType: "apikey",
		},
		{
			name:      "recognized Claude Code OAuth without mimicry",
			account:   &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			token:     "oauth-token",
			tokenType: "oauth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			svc := &GatewayService{cfg: &config.Config{}}

			req, wireBody, err := svc.buildCountTokensRequest(
				context.Background(), c, tt.account, body,
				tt.token, tt.tokenType, "claude-haiku-4-5", false,
			)
			require.NoError(t, err)
			require.False(t, gjson.GetBytes(wireBody, "tools.0.cache_control").Exists())
			for idx := 1; idx < 5; idx++ {
				require.Equal(t, "ephemeral", gjson.GetBytes(wireBody, fmt.Sprintf("tools.%d.cache_control.type", idx)).String())
			}
			require.JSONEq(t, string(wireBody), string(readUpstreamBodyForTest(t, req)))
		})
	}
}

// count_tokens passthrough preserve 测试
func TestBuildCountTokensRequestAnthropicAPIKeyPassthrough_PreservesContextManagementWhenClientHeaderHasBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("Anthropic-Beta", "oauth-2025-04-20,context-management-2025-06-27,token-counting-2024-11-01")

	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[{"type":"clear_thinking_20251015"}]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(
		context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
	)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(readUpstreamBodyForTest(t, req), "context_management").Exists(),
		"count_tokens passthrough + 客户端带 context-management beta → 字段保留")
}

func TestBuildUpstreamRequest_APIKeyHaikuWithContextManagement_StripsField(t *testing.T) {
	// API-key + haiku + body 带 context_management + 客户端 header 未带 context-management beta
	// → final beta 不含 → body 字段被 strip
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	account := &Account{ID: 404, Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-ant-xxx"},
		Status:      StatusActive, Schedulable: true,
	}
	body := []byte(`{"model":"claude-haiku-4-5","context_management":{"edits":[]},"messages":[]}`)
	svc := &GatewayService{cfg: &config.Config{}}
	req, _, err := svc.buildUpstreamRequest(
		context.Background(), c, account, body,
		"sk-ant-xxx", "apikey", "claude-haiku-4-5", false, false,
	)
	require.NoError(t, err)

	outBody := readUpstreamBodyForTest(t, req)
	require.False(t, gjson.GetBytes(outBody, "context_management").Exists(),
		"API-key + haiku + 客户端未带 beta token → body 字段必须被 strip")
}
