package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestSyncBillingHeaderVersion(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		userAgent string
		wantSub   string // substring expected in result
		unchanged bool   // expect body to remain the same
	}{
		{
			name:      "replaces cc_version and recomputes message-derived suffix",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code.","cache_control":{"type":"ephemeral"}}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22 (external, cli)",
			wantSub:   "cc_version=2.1.22." + computeClaudeCodeFingerprint([]byte(`{"messages":[]}`), "2.1.22"),
		},
		{
			name:      "no billing header in system",
			body:      `{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "no system field",
			body:      `{"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
		{
			name:      "user-agent without version",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "Mozilla/5.0",
			unchanged: true,
		},
		{
			name:      "empty user-agent",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "",
			unchanged: true,
		},
		{
			name:      "version already matches",
			body:      `{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.22; cc_entrypoint=cli; cch=00000;"}],"messages":[]}`,
			userAgent: "claude-cli/2.1.22",
			unchanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := syncBillingHeaderVersion([]byte(tt.body), tt.userAgent, billingFingerprintFromClient)
			if tt.unchanged {
				assert.Equal(t, tt.body, string(result), "body should remain unchanged")
			} else {
				assert.Contains(t, string(result), tt.wantSub)
				// Ensure old semver is gone
				assert.NotContains(t, string(result), "cc_version=2.1.81")
			}
		})
	}
}

func TestSyncBillingHeaderVersion_RecomputesSuffixAndIsIdempotent(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.df2; cc_entrypoint=cli;"}],"messages":[{"role":"user","content":"hello world"}]}`)
	version := "2.1.22"
	result := syncBillingHeaderVersion(body, "claude-cli/"+version, billingFingerprintFromClient)
	require.Contains(t, gjson.GetBytes(result, "system.0.text").String(),
		"cc_version="+version+"."+computeClaudeCodeFingerprint(body, version)+";")
	require.Equal(t, string(result), string(syncBillingHeaderVersion(result, "claude-cli/"+version, billingFingerprintFromClient)))
	require.JSONEq(t, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(result, "messages").Raw)
}

func TestBuildOAuthRequest_BillingMatchesWireUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, tc := range []struct {
			name      string
			mimic     bool
			identity  bool
			disableFP bool
		}{
			{name: "mimic_overrides_cached_version", mimic: true, identity: true},
			{name: "mimic_without_identity", mimic: true},
			{name: "mimic_with_fingerprint_disabled", mimic: true, identity: true, disableFP: true},
			{name: "passthrough_uses_cached_version", identity: true},
		} {
			t.Run(endpoint+"/"+tc.name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
				billing, err := buildBillingAttributionText(body, "2.1.81")
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "system.0.text", billing)
				require.NoError(t, err)

				cfg := &config.Config{}
				svc := &GatewayService{cfg: cfg}
				cachedUA := "claude-cli/2.9.0 (external, cli)"
				if tc.identity {
					svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
						UserAgent: cachedUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
					}})
				}
				if tc.disableFP {
					svc.settingService = NewSettingService(&gatewayTTLSettingRepo{data: map[string]string{
						SettingKeyEnableFingerprintUnification: "false",
					}}, cfg)
				}
				account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, tc.mimic)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", tc.mimic)
				}
				require.NoError(t, err)
				defer func() { require.NoError(t, req.Body.Close()) }()
				wantUA := cachedUA
				if tc.mimic {
					wantUA = claude.DefaultHeaders()["User-Agent"]
				}
				require.Equal(t, wantUA, getHeaderRaw(req.Header, "User-Agent"))
				version := ExtractCLIVersion(wantUA)
				if endpoint == "count_tokens" {
					// 真实 CLI 2.1.280 的 count_tokens 不带 system/billing 块
					require.False(t, gjson.GetBytes(wireBody, "system").Exists())
				} else {
					require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(),
						"cc_version="+version+"."+computeClaudeCodeFingerprint(wireBody, version)+";")
				}
				actualBody, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, wireBody, actualBody)
			})
		}
	}
}

// ============================================================================
// 2.1.290 billing 块扩展字段（2026-10-06 第一方直连 MITM 抓包实证）：
//   cc_prompt_id / cc_turn_origin / cc_prompt_index / cc_turn_index
// 以及 x-claude-code-prompt-id 头与块内 cc_prompt_id 的一致性镜像。
// ============================================================================

func TestBuildBillingAttributionText_290ExtendedFields(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","messages":[{"role":"user","content":"hello world"},{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"tool_result","content":"ok"}]},{"role":"user","content":"second prompt"}]}`)
	text, err := buildBillingAttributionText(body, "2.1.290")
	require.NoError(t, err)
	require.Contains(t, text, "cc_version=2.1.290.")
	require.Contains(t, text, "cc_entrypoint=cli;")
	require.Contains(t, text, "cch=00000;")
	// prompt_index = 非 tool_result 的 user 消息数 - 1（本例 2 条真实 prompt → 1）
	require.Contains(t, text, "cc_prompt_index=1;")
	require.Contains(t, text, "cc_turn_origin=cli;")
	require.Contains(t, text, "cc_turn_index=1;")
	// cc_prompt_id 为 UUID 且可被镜像提取
	pid := extractBillingPromptIDView(newJSONBodyView([]byte(`{"system":[{"type":"text","text":"`+text+`"}]}`), nil))
	require.Regexp(t, `^[0-9a-f-]{36}$`, pid)
	require.Contains(t, text, "cc_prompt_id="+pid+";")
}

func TestBuildUpstreamRequest_PromptIDHeaderMirrorsBillingBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
	billing, err := buildBillingAttributionText(body, "2.1.290")
	require.NoError(t, err)
	body, err = sjson.SetBytes(body, "system.0.text", billing)
	require.NoError(t, err)

	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	req, _, err := svc.buildUpstreamRequest(context.Background(), c, account,
		body, "test-token", "oauth", "claude-haiku-4-5", true, true)
	require.NoError(t, err)

	wantPID := extractBillingPromptIDView(newJSONBodyView(reqBody(t, req), nil))
	require.NotEmpty(t, wantPID)
	require.Equal(t, wantPID, getHeaderRaw(req.Header, "x-claude-code-prompt-id"),
		"prompt-id 头必须与 billing 块 cc_prompt_id 逐字一致")
	require.Equal(t, "main", getHeaderRaw(req.Header, "x-claude-code-request-class"))
}

// ============================================================================
// cc_version 指纹后缀与真实 CLI 对齐。参考值由 node 按 claude.exe 2.1.296 内嵌 JS 的
// cg() 原文计算（盐 59cf53e54c78）：
//
//	s = [4,7,20].map(i => e[i] || "0").join("")
//	createHash("sha256").update(salt + s + version).digest("hex").slice(0, 3)
// ============================================================================

// claudeCodeSessionContextMetaText 模拟 CLI 前置到首条 user 消息的会话上下文 isMeta 块。
const claudeCodeSessionContextMetaText = "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# currentDate\nToday's date is 2026-10-19.\n</system-reminder>\n"

const (
	fingerprintTestPromptASCII = "fix the bug in foo.go please now"
	fingerprintTestPromptCJK   = "请帮我审计这个拉取请求的所有改动并给出最终结论"
	fingerprintTestPromptEmoji = "hi 👋 there friend, how are you doing"
)

func claudeCodeTextBlocks(texts ...string) []map[string]any {
	blocks := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

// nativeClaudeCodeTestBody 构造真实 Claude Code 风格的请求体：billing 块 + 身份块，
// messages 依次为给定的 user/assistant 交替内容（从 user 开始）。
func nativeClaudeCodeTestBody(t *testing.T, billing string, turns ...any) []byte {
	t.Helper()
	messages := make([]map[string]any, 0, len(turns))
	for i, content := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	body, err := json.Marshal(map[string]any{
		"model": "claude-haiku-4-5",
		"system": []map[string]any{
			{"type": "text", "text": billing},
			{"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude.", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"messages": messages,
	})
	require.NoError(t, err)
	return body
}

func TestClaudeCodeFingerprintChars_MatchCLIReferenceValues(t *testing.T) {
	for _, tc := range []struct {
		name, text, version, want string
	}{
		{name: "ascii", text: fingerprintTestPromptASCII, version: "2.1.296", want: "dfd"},
		{name: "ascii_next_version", text: fingerprintTestPromptASCII, version: "2.1.297", want: "37d"},
		{name: "cjk_indexes_utf16_units", text: fingerprintTestPromptCJK, version: "2.1.296", want: "5eb"},
		{name: "emoji_surrogate_half_is_replacement_char", text: fingerprintTestPromptEmoji, version: "2.1.296", want: "5bc"},
		{name: "session_context_meta_block", text: "<system-reminder>\nAs you answer the user's questions, you can use the following context:", version: "2.1.296", want: "40c"},
		{name: "empty_pads_with_zero", text: "", version: "2.1.296", want: "406"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claudeCodeFingerprintForChars(claudeCodeFingerprintChars(tc.text), tc.version))
		})
	}
}

func TestComputeClaudeCodeFingerprint_SkipsLeadingMetaBlocksLikeCLI(t *testing.T) {
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "AAAA"}}
	for _, tc := range []struct {
		name  string
		first any
		want  string
	}{
		{name: "string_content", first: fingerprintTestPromptASCII, want: "dfd"},
		{name: "meta_then_prompt", first: claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptASCII), want: "dfd"},
		{name: "meta_image_prompt", first: []any{claudeCodeTextBlocks(claudeCodeSessionContextMetaText)[0], image, claudeCodeTextBlocks(fingerprintTestPromptCJK)[0]}, want: "5eb"},
		{name: "prompt_then_trailing_meta", first: claudeCodeTextBlocks(fingerprintTestPromptEmoji, claudeCodeSessionContextMetaText), want: "5bc"},
		// 纯图片 prompt：CLI 的 RY 找不到 text 块时取 ""。
		{name: "image_only_prompt", first: []any{claudeCodeTextBlocks(claudeCodeSessionContextMetaText)[0], image}, want: "406"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := nativeClaudeCodeTestBody(t, "x-anthropic-billing-header: cc_version=2.1.296.000; cc_entrypoint=cli;", tc.first)
			require.Equal(t, tc.want, computeClaudeCodeFingerprint(body, "2.1.296"))
		})
	}
}

func TestSyncBillingHeaderVersion_ClientFingerprintSuffix(t *testing.T) {
	firstTurn := claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptASCII)
	for _, tc := range []struct {
		name    string
		billing string
		first   any
		ua      string
		want    string
	}{
		{
			name:    "same_version_keeps_client_suffix",
			billing: "x-anthropic-billing-header: cc_version=2.1.296.abc; cc_entrypoint=cli; cch=abcde;",
			first:   firstTurn,
			ua:      "claude-cli/2.1.296 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.296.abc; cc_entrypoint=cli; cch=abcde;",
		},
		{
			name:    "version_change_rederives_from_prompt",
			billing: "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli; cch=abcde;",
			first:   firstTurn,
			ua:      "claude-cli/2.1.297 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.297.37d; cc_entrypoint=cli; cch=abcde;",
		},
		{
			// 客户端的输入不是 CLI 规则下的首个正文块（a59）：按复现了 513 的那个块重算。
			name:    "version_change_finds_matching_block",
			billing: "x-anthropic-billing-header: cc_version=2.1.290.513; cc_entrypoint=cli; cch=abcde;",
			first:   claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptCJK, fingerprintTestPromptASCII),
			ua:      "claude-cli/2.1.296 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli; cch=abcde;",
		},
		{
			// 没有任何块能复现 fff：退回 CLI 规则下的首个正文块（CJK prompt）。
			name:    "version_change_without_match_uses_first_prompt",
			billing: "x-anthropic-billing-header: cc_version=2.1.290.fff; cc_entrypoint=cli; cch=abcde;",
			first:   claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptCJK, fingerprintTestPromptASCII),
			ua:      "claude-cli/2.1.296 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.296.5eb; cc_entrypoint=cli; cch=abcde;",
		},
		{
			// 指向自定义 base URL 的客户端不带 cch（与 cc_prompt_id 等同一门控），不补。
			name:    "version_change_does_not_add_cch",
			billing: "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli;",
			first:   firstTurn,
			ua:      "claude-cli/2.1.297 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.297.37d; cc_entrypoint=cli;",
		},
		{
			name:    "same_version_without_cch_unchanged",
			billing: "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli;",
			first:   firstTurn,
			ua:      "claude-cli/2.1.296 (external, cli)",
			want:    "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli;",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := nativeClaudeCodeTestBody(t, tc.billing, tc.first)
			result := syncBillingHeaderVersion(body, tc.ua, billingFingerprintFromClient)
			require.Equal(t, tc.want, gjson.GetBytes(result, "system.0.text").String())
			if tc.want == tc.billing {
				require.Equal(t, string(body), string(result))
			}
		})
	}
}

// mimic 路径的 billing 块由网关在注入 [System Instructions] 消息对之前合成，版本同步时
// 按最终 body 重算后缀；透传路径同一输入则保留客户端的值。
func TestSyncBillingHeaderVersion_GatewayBuiltSuffixFollowsFinalBody(t *testing.T) {
	const billing = "x-anthropic-billing-header: cc_version=2.1.296.dfd; cc_entrypoint=cli; cch=00000;"
	body := nativeClaudeCodeTestBody(t, billing,
		claudeCodeTextBlocks("[System Instructions]\nYou are a helpful assistant."),
		claudeCodeTextBlocks("Understood."),
		fingerprintTestPromptASCII,
	)

	gatewayBuilt := syncBillingHeaderVersion(body, "claude-cli/2.1.296 (external, cli)", billingFingerprintFromBody)
	require.Equal(t, "x-anthropic-billing-header: cc_version=2.1.296.ecb; cc_entrypoint=cli; cch=00000;",
		gjson.GetBytes(gatewayBuilt, "system.0.text").String())

	clientBuilt := syncBillingHeaderVersion(body, "claude-cli/2.1.296 (external, cli)", billingFingerprintFromClient)
	require.Equal(t, string(body), string(clientBuilt))
}

// 透传真实 Claude Code（fp=true）：客户端 2.1.290 的指纹 UA 被抬到 EffectiveCLIVersion，
// billing cc_version 必须跟随出站 User-Agent，后缀按真实 prompt（而非会话上下文 meta 块）换算。
func TestBuildUpstreamRequest_NativeBillingFollowsFlooredUserAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	const clientUA = "claude-cli/2.1.290 (external, cli)"
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", clientUA)

	promptChars := claudeCodeFingerprintChars(fingerprintTestPromptASCII)
	body := nativeClaudeCodeTestBody(t,
		"x-anthropic-billing-header: cc_version=2.1.290."+claudeCodeFingerprintForChars(promptChars, "2.1.290")+"; cc_entrypoint=cli;",
		claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptASCII))

	svc := &GatewayService{cfg: &config.Config{}}
	svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
		UserAgent: clientUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
	}})
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account,
		body, "test-token", "oauth", "claude-haiku-4-5", false, false)
	require.NoError(t, err)

	version := ExtractCLIVersion(getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, claude.EffectiveCLIVersion(), version)
	require.NotEqual(t, "2.1.290", version, "指纹 UA 应被抬到运行期版本下限")
	wantSuffix := claudeCodeFingerprintForChars(promptChars, version)
	require.NotEqual(t, claudeCodeFingerprintForChars(claudeCodeFingerprintChars(claudeCodeSessionContextMetaText), version), wantSuffix)
	// 客户端没带 cch，出站也不带（不补 00000 占位符）。
	require.Equal(t, "x-anthropic-billing-header: cc_version="+version+"."+wantSuffix+"; cc_entrypoint=cli;",
		gjson.GetBytes(wireBody, "system.0.text").String())
}

// 透传真实 Claude Code 且版本无需变化时，客户端的 billing 块逐字节保留（含 cch 有无）。
func TestBuildUpstreamRequest_NativeBillingUnchangedWhenVersionMatches(t *testing.T) {
	version := claude.EffectiveCLIVersion()
	for _, tc := range []struct {
		name    string
		billing string
	}{
		{name: "with_cch", billing: "x-anthropic-billing-header: cc_version=" + version + ".abc; cc_entrypoint=cli; cch=abcde;"},
		{name: "without_cch", billing: "x-anthropic-billing-header: cc_version=" + version + ".abc; cc_entrypoint=cli;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			resetGatewayForwardingSettingsCacheForTest(t)
			clientUA := "claude-cli/" + version + " (external, cli)"
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("User-Agent", clientUA)

			body := nativeClaudeCodeTestBody(t, tc.billing,
				claudeCodeTextBlocks(claudeCodeSessionContextMetaText, fingerprintTestPromptASCII))

			svc := &GatewayService{cfg: &config.Config{}}
			svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
				UserAgent: clientUA, ClientID: "test-client", UpdatedAt: time.Now().Unix(),
			}})
			account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
			req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account,
				body, "test-token", "oauth", "claude-haiku-4-5", false, false)
			require.NoError(t, err)

			require.Equal(t, clientUA, getHeaderRaw(req.Header, "User-Agent"))
			require.Equal(t, tc.billing, gjson.GetBytes(wireBody, "system.0.text").String())
		})
	}
}

// mimic 路径的 billing 块由网关合成，仍带 cch=00000 占位符（buildBillingAttributionText 模板），
// 经版本同步后不变。
func TestBuildUpstreamRequest_MimicBillingKeepsCCHPlaceholder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetGatewayForwardingSettingsCacheForTest(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "opencode/1.0")

	body := []byte(`{"model":"claude-haiku-4-5","system":"You are a helpful assistant.","messages":[{"role":"user","content":"hello world"}]}`)
	body = rewriteSystemForNonClaudeCode(body, "You are a helpful assistant.")

	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account,
		body, "test-token", "oauth", "claude-haiku-4-5", false, true)
	require.NoError(t, err)

	version := ExtractCLIVersion(getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, claude.EffectiveCLIVersion(), version)
	billing := gjson.GetBytes(wireBody, "system.0.text").String()
	require.Contains(t, billing,
		"x-anthropic-billing-header: cc_version="+version+"."+computeClaudeCodeFingerprint(wireBody, version)+"; cc_entrypoint=cli; cch=00000; cc_prompt_id=")
}

func reqBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	rc, err := req.GetBody()
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return b
}
