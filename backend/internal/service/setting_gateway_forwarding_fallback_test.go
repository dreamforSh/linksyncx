//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本文件钉住网关转发设置读库失败时的回退策略：读失败 == 未配置时的内置默认值
// （指纹统一、system 注入、dateline/userEmail 清洗开启，metadata 透传关闭），即伪装方向。
// 曾有改动把回退值反转成"读失败就不改写请求"（fp=false、mp=true、注入与清洗关闭），
// 一次 5s 的数据库抖动就会把下游客户端自己的 UA、device_id、account_uuid 与 email
// 发往共享 OAuth 账号的上游。转发设置缓存是包级全局变量，本文件的测试不能 t.Parallel。

func newFailingForwardingSettingsRepo() *forwardedIPMigrationRepoStub {
	return &forwardedIPMigrationRepoStub{
		values:         map[string]string{},
		getMultipleErr: errors.New("settings db unavailable"),
	}
}

func expireGatewayForwardingCacheForTest(t *testing.T) {
	t.Helper()
	cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	require.True(t, ok)
	require.NotNil(t, cached)
	expired := *cached
	expired.expiresAt = time.Now().Add(-time.Second).UnixNano()
	gatewayForwardingCache.Store(&expired)
}

func TestSettingService_GatewayForwardingReadFailureUsesDefaults(t *testing.T) {
	ctx := context.Background()

	resetGatewayForwardingSettingsCacheForTest(t)
	unset := NewSettingService(&forwardedIPMigrationRepoStub{values: map[string]string{}}, &config.Config{}).getGatewayForwardingSettingsCached(ctx)

	resetGatewayForwardingSettingsCacheForTest(t)
	repo := newFailingForwardingSettingsRepo()
	svc := NewSettingService(repo, &config.Config{})
	failedAt := time.Now()
	got := svc.getGatewayForwardingSettingsCached(ctx)

	// 读失败与未配置必须是同一套策略，不能出现只在数据库故障时才生效的第三套行为。
	assert.Equal(t, unset, got, "read-failure fallback must equal the unset defaults")
	assert.True(t, got.fp, "fingerprint unification stays on during a settings read failure")
	assert.False(t, got.mp, "metadata passthrough stays off during a settings read failure")
	assert.True(t, got.claudeOAuthSystemPromptInjection, "system prompt injection stays on during a settings read failure")
	assert.True(t, got.clientDatelineNormalization, "dateline/userEmail sanitizing stays on during a settings read failure")

	// 失败结果只短暂缓存（错误 TTL），避免故障期间每个请求都打数据库，又能尽快恢复。
	require.Less(t, gatewayForwardingErrorTTL, gatewayForwardingCacheTTL)
	cached, ok := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	require.True(t, ok)
	require.WithinDuration(t, failedAt.Add(gatewayForwardingErrorTTL), time.Unix(0, cached.expiresAt), time.Second)

	// 错误 TTL 窗口内即使数据库已恢复，也直接用缓存的回退值。
	repo.getMultipleErr = nil
	repo.values = map[string]string{
		SettingKeyEnableMetadataPassthrough:    "true",
		SettingKeyEnableFingerprintUnification: "false",
	}
	require.Equal(t, got, svc.getGatewayForwardingSettingsCached(ctx), "the error entry is served from cache inside its TTL")

	// 窗口过期后重新读库，拿到运营方显式配置，并按正常 TTL 缓存。
	expireGatewayForwardingCacheForTest(t)
	recoveredAt := time.Now()
	recovered := svc.getGatewayForwardingSettingsCached(ctx)
	require.True(t, recovered.mp)
	require.False(t, recovered.fp)
	cached, ok = gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	require.True(t, ok)
	require.WithinDuration(t, recoveredAt.Add(gatewayForwardingCacheTTL), time.Unix(0, cached.expiresAt), time.Second)
}

// 端到端钉住读库失败期间 OAuth 出站仍保持伪装：这几条正是反转回退值时会破的出站不变量。
func TestGatewayForwardingReadFailureKeepsOAuthEgressMimicry(t *testing.T) {
	newService := func(t *testing.T) *GatewayService {
		t.Helper()
		resetGatewayForwardingSettingsCacheForTest(t)
		svc := newAnthropicOAuthMappingGatewayService(&anthropicHTTPUpstreamRecorder{})
		svc.identityService = NewIdentityService(newMemoryIdentityCache())
		svc.settingService = NewSettingService(newFailingForwardingSettingsRepo(), &config.Config{})
		return svc
	}
	newAccount := func() *Account {
		account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
		account.Extra = map[string]any{"account_uuid": "upstream-account-uuid", "email_address": "upstream@example.com"}
		return account
	}

	// Claude Code 客户端：出站 UA 与 metadata.user_id 必须是账号身份，不能透出客户端自己的设备与账号。
	t.Run("claude_code_client_identity_is_rewritten", func(t *testing.T) {
		svc := newService(t)
		account := newAccount()
		c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
		c.Request.Header.Set("User-Agent", p1ClientUA)
		clientDevice := strings.Repeat("cd", 32)
		const clientAccountUUID = "client-account-uuid"
		const clientSession = "11111111-2222-4333-8444-555555555555"
		uid := FormatMetadataUserID(clientDevice, clientAccountUUID, clientSession, "2.1.287")
		body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":` + jsonQuote(uid) + `},"messages":[{"role":"user","content":"hi"}]}`)

		req, out, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "tok", "oauth", "claude-sonnet-4-5", false, false)
		require.NoError(t, err)

		assert.NotEqual(t, p1ClientUA, getHeaderRaw(req.Header, "User-Agent"), "egress UA must be the account fingerprint, not the client UA")
		egressUID := gjson.GetBytes(out, "metadata.user_id").String()
		assert.NotContains(t, egressUID, clientDevice, "client device_id must not reach upstream")
		assert.NotContains(t, egressUID, clientAccountUUID, "client account_uuid must not reach upstream")
		assert.NotContains(t, egressUID, clientSession, "raw client session id must not reach upstream")
	})

	// 第三方客户端：仍要注入 Claude Code system（含 billing 块）与账号 metadata.user_id。
	t.Run("third_party_client_still_gets_claude_code_mimicry", func(t *testing.T) {
		svc := newService(t)
		account := newAccount()
		c, _ := newAnthropicOAuthMappingTestContext("/v1/chat/completions")
		c.Request.Header.Set("User-Agent", "opencode/1.0")
		body := []byte(`{"model":"claude-sonnet-4-5","system":"You are a helpful third-party agent.","messages":[{"role":"user","content":"hi"}]}`)

		out, err := svc.applyClaudeCodeOAuthMimicryToBody(context.Background(), c, account, body, gjson.GetBytes(body, "system").Value(), "claude-sonnet-4-5")
		require.NoError(t, err)

		system := gjson.GetBytes(out, "system").Raw
		assert.Contains(t, system, claudeCodeBillingHeaderPrefix, "third-party traffic must carry the billing attribution block")
		assert.Contains(t, system, strings.TrimSpace(claudeCodeSystemPrompt), "third-party traffic must carry the Claude Code identity block")
		assert.NotEmpty(t, gjson.GetBytes(out, "metadata.user_id").String(), "third-party traffic must carry the account metadata.user_id")
	})

	// userEmail 脱敏与 dateline 归一化共用一个开关：读库失败时也不能把终端用户 email 发往上游。
	t.Run("client_context_email_is_sanitized", func(t *testing.T) {
		svc := newService(t)
		body := []byte(clientContextRequestBody(t, ""))

		out := body
		next, changed := svc.sanitizeClientContextIfEnabled(context.Background(), newAccount(), body)
		if changed {
			out = next
		}
		assert.True(t, changed, "client context must be sanitized during a settings read failure")
		assert.NotContains(t, string(out), clientContextRelayEmail, "downstream user email must not reach upstream")
		assert.Contains(t, string(out), "upstream@example.com", "the userEmail segment carries the account email instead")
	})
}
