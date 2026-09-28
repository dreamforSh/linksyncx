//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本文件覆盖 enable_client_dateline_normalization 开关下客户端上下文清洗的服务层接线：
// userEmail 脱敏读取哪个账号 email、开关与账号范围，以及 Forward / ForwardCountTokens
// 两个出口都把终端用户 email 挡在上游之外。段模板与作用范围等纯函数细节见
// pkg/anthropicfp/useremail_test.go。

const clientContextRelayEmail = "relay-user@example.com"

// clientContextBlock 复刻 Claude Code 2.1.283 首条消息前的会话上下文块，dateline 带指纹
// （U+2019 撇号 + "/" 分隔符）。
func clientContextBlock(email string) string {
	return "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n" +
		"# claudeMd\nproject instructions\n" +
		"# userEmail\nThe user's email address is " + email + ". Use it only to identify the user, such as for authorship, attribution, or filtering their own work. Never send it to an unrelated service, such as in a request header, URL, or payload, unless the user explicitly asks.\n" +
		"# currentDate\nToday’s date is 2026/09/28.\n\n" +
		"      IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.\n</system-reminder>\n"
}

func clientContextRequestBody(t *testing.T, extraFields string) string {
	t.Helper()
	block, err := json.Marshal(clientContextBlock(clientContextRelayEmail))
	require.NoError(t, err)
	return `{"model":"claude-sonnet-4-6",` + extraFields + `"messages":[{"role":"user","content":[{"type":"text","text":` +
		string(block) + `},{"type":"text","text":"hi"}]}]}`
}

// resetGatewayForwardingCache 清空进程级转发设置缓存，让下一次读取落到本测试的 repo。
func resetGatewayForwardingCache(t *testing.T) {
	t.Helper()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
}

func newClientContextTestService(t *testing.T, upstream *anthropicHTTPUpstreamRecorder, switchValue string) *GatewayService {
	t.Helper()
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	if switchValue != "" {
		repo.data[SettingKeyEnableClientDatelineNormalization] = switchValue
	}
	resetGatewayForwardingCache(t)
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	svc.settingService = NewSettingService(repo, &config.Config{})
	return svc
}

func TestClaudeOAuthAccountEmail_ReadOrder(t *testing.T) {
	require.Empty(t, claudeOAuthAccountEmail(nil))
	require.Empty(t, claudeOAuthAccountEmail(&Account{}))

	// OAuth 授权流程写入 extra.email_address（前端 useAccountOAuth），优先级最高。
	require.Equal(t, "oauth@example.com", claudeOAuthAccountEmail(&Account{
		Extra:       map[string]any{"email_address": " oauth@example.com ", "email": "crs@example.com"},
		Credentials: map[string]any{"email": "cred@example.com"},
	}))
	// CRS 同步写入 extra.email；空白值视为未记录。
	require.Equal(t, "crs@example.com", claudeOAuthAccountEmail(&Account{
		Extra:       map[string]any{"email_address": "  ", "email": "crs@example.com"},
		Credentials: map[string]any{"email": "cred@example.com"},
	}))
	require.Equal(t, "cred@example.com", claudeOAuthAccountEmail(&Account{
		Credentials: map[string]any{"email": " cred@example.com"},
	}))
}

func TestSanitizeUserEmailForOAuth_ScopeAndSwitch(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	resetGatewayForwardingCache(t)
	svc := &GatewayService{settingService: NewSettingService(repo, &config.Config{})}
	ctx := context.Background()
	body := []byte(clientContextRequestBody(t, ""))
	oauth := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"email_address": "upstream@example.com"}}

	// API Key / 非 Anthropic / nil 账号：不处理。
	for _, account := range []*Account{
		{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"email_address": "upstream@example.com"}},
		{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		nil,
	} {
		next, ok := svc.sanitizeUserEmailForOAuth(ctx, account, body)
		require.False(t, ok)
		require.Nil(t, next)
	}

	// 账号记录了 email：替换为账号 email。
	next, ok := svc.sanitizeUserEmailForOAuth(ctx, oauth, body)
	require.True(t, ok)
	require.NotContains(t, string(next), clientContextRelayEmail)
	require.Contains(t, string(next), "The user's email address is upstream@example.com. Use it only")

	// 账号未记录 email：整段删除。
	next, ok = svc.sanitizeUserEmailForOAuth(ctx, &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, body)
	require.True(t, ok)
	require.NotContains(t, string(next), clientContextRelayEmail)
	require.NotContains(t, string(next), "# userEmail")

	// 没有 userEmail 段：(nil, false)，与 normalizeClientDatelineIfEnabled 同约定。
	next, ok = svc.sanitizeUserEmailForOAuth(ctx, oauth, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	require.False(t, ok)
	require.Nil(t, next)

	// 开关关闭：不处理。
	repo.data[SettingKeyEnableClientDatelineNormalization] = "false"
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	next, ok = svc.sanitizeUserEmailForOAuth(ctx, oauth, body)
	require.False(t, ok)
	require.Nil(t, next)
}

func TestSanitizeClientContextIfEnabled_AppliesDatelineAndUserEmail(t *testing.T) {
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	resetGatewayForwardingCache(t)
	svc := &GatewayService{settingService: NewSettingService(repo, &config.Config{})}
	ctx := context.Background()
	oauth := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	next, ok := svc.sanitizeClientContextIfEnabled(ctx, oauth, []byte(clientContextRequestBody(t, "")))
	require.True(t, ok)
	text := gjson.GetBytes(next, "messages.0.content.0.text").String()
	require.Contains(t, text, "Today's date is 2026-09-28.")
	require.NotContains(t, text, "# userEmail")
	require.NotContains(t, string(next), clientContextRelayEmail)

	next, ok = svc.sanitizeClientContextIfEnabled(ctx, oauth, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	require.False(t, ok)
	require.Nil(t, next)
}

// 清洗运行在 mimicry 分支之外：第三方客户端（伪装路径）与真实 Claude Code 客户端都要清洗，
// 开关关闭时原样透传。
func TestGatewayService_Forward_SanitizesClientContextForOAuth(t *testing.T) {
	cases := []struct {
		name        string
		claudeCode  bool
		switchValue string
		wantClean   bool
	}{
		{name: "mimic", wantClean: true},
		{name: "claude code client", claudeCode: true, wantClean: true},
		{name: "switch off", switchValue: "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
			parsed := parseAnthropicOAuthMappingRequest(t, clientContextRequestBody(t, `"max_tokens":64,`))
			upstream := &anthropicHTTPUpstreamRecorder{resp: newAnthropicOAuthMappingMessageResponse("claude-sonnet-4-6")}
			svc := newClientContextTestService(t, upstream, tc.switchValue)
			account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
			account.Extra = map[string]any{"email_address": "upstream@example.com"}

			_, err := svc.Forward(SetClaudeCodeClient(context.Background(), tc.claudeCode), c, account, parsed)
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)

			sent := string(upstream.lastBody)
			if !tc.wantClean {
				require.Contains(t, sent, clientContextRelayEmail)
				return
			}
			require.NotContains(t, sent, clientContextRelayEmail)
			require.Contains(t, sent, "The user's email address is upstream@example.com. Use it only")
			require.Contains(t, sent, "Today's date is 2026-09-28.")
		})
	}
}

func TestGatewayService_ForwardCountTokens_SanitizesClientContextForOAuth(t *testing.T) {
	c, rec := newAnthropicOAuthMappingTestContext("/v1/messages/count_tokens")
	parsed := parseAnthropicOAuthMappingRequest(t, clientContextRequestBody(t, ""))
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"input_tokens":7}`)),
	}}
	svc := newClientContextTestService(t, upstream, "")
	// 账号未记录 email：整段删除。
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)

	require.NoError(t, svc.ForwardCountTokens(context.Background(), c, account, parsed))
	require.Equal(t, http.StatusOK, rec.Code)

	sent := string(upstream.lastBody)
	require.NotContains(t, sent, clientContextRelayEmail)
	require.NotContains(t, sent, "# userEmail")
	require.Contains(t, sent, "Today's date is 2026-09-28.")
}
