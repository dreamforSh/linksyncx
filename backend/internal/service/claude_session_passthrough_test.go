//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// setGatewayForwardingForTest 把全局转发设置缓存固定为给定的指纹统一 / metadata 透传开关。
func setGatewayForwardingForTest(t *testing.T, fingerprintUnification, metadataPassthrough bool) {
	t.Helper()
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		fingerprintUnification:           fingerprintUnification,
		metadataPassthrough:              metadataPassthrough,
		claudeOAuthSystemPromptInjection: true,
		clientDatelineNormalization:      true,
		expiresAt:                        time.Now().Add(time.Minute).UnixNano(),
	})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
}

// metadata 透传开启、客户端带了 metadata 时，同一对话的 messages 与 count_tokens
// 必须向上游发同一个 X-Claude-Code-Session-Id（PR #4 审计在 1a72f4b79 上复现过：
// messages 发客户端原值，count_tokens 发映射值，上游看到一个客户端用两个会话 ID）。
// 单会话模式不改变这一点：透传优先，两条路径都发客户端原值。
func TestMetadataPassthroughSessionHeaderConsistentAcrossMessagesAndCountTokens(t *testing.T) {
	const clientSession = "11111111-2222-4333-8444-555555555555"
	for _, fp := range []bool{true, false} {
		for _, masking := range []bool{false, true} {
			t.Run(fmt.Sprintf("fingerprint_unification=%v/single_session=%v", fp, masking), func(t *testing.T) {
				setGatewayForwardingForTest(t, fp, true)
				ctx := context.Background()
				cache := newMemoryIdentityCache()
				svc := newAnthropicOAuthMappingGatewayService(&anthropicHTTPUpstreamRecorder{})
				svc.settingService = NewSettingService(&antigravitySettingRepoStub{}, svc.cfg)
				svc.identityService = NewIdentityService(cache)
				account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
				account.Extra = map[string]any{"account_uuid": "acc", "session_id_masking_enabled": masking}

				c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
				c.Request.Header.Set("User-Agent", p1ClientUA)
				c.Request.Header.Set("X-Claude-Code-Session-Id", clientSession)
				uid := FormatMetadataUserID(strings.Repeat("ab", 32), "", clientSession, "2.1.287")
				msgBody := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":` + jsonQuote(uid) + `},"messages":[{"role":"user","content":"hi"}]}`)
				msgReq, msgOut, err := svc.buildUpstreamRequest(ctx, c, account, msgBody, "tok", "oauth", "claude-sonnet-4-5", false, false)
				require.NoError(t, err)
				msgSession := getHeaderRaw(msgReq.Header, "X-Claude-Code-Session-Id")
				require.Equal(t, uid, gjson.GetBytes(msgOut, "metadata.user_id").String(), "passthrough keeps metadata.user_id untouched")
				require.Equal(t, clientSession, msgSession, "header matches the passed-through metadata session")

				// 真实 CLI 的 count_tokens body 不带 metadata，只带会话头。
				ctBody := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
				ctReq, _, err := svc.buildCountTokensRequest(ctx, c, account, ctBody, "tok", "oauth", "claude-sonnet-4-5", false)
				require.NoError(t, err)
				require.Equal(t, msgSession, getHeaderRaw(ctReq.Header, "X-Claude-Code-Session-Id"),
					"count_tokens must reuse the session upstream already saw on messages")
				if masking {
					require.Empty(t, cache.masked[account.ID], "a client-supplied session never allocates the masked session")
				}
			})
		}
	}
}

// claudeUpstreamSessionID 的规则表：metadata 会话优先；透传开启时客户端会话头原样使用且
// 先于单会话模式；客户端没给有效会话时才落到 ResolveSessionIDWithoutMetadata。
func TestClaudeUpstreamSessionIDRule(t *testing.T) {
	const clientSession = "11111111-2222-4333-8444-555555555555"
	const metadataSession = "66666666-7777-4888-8999-aaaaaaaaaaaa"
	oversized := strings.Repeat("x", 129)
	ctx := context.Background()

	type want int
	const (
		wantClient want = iota
		wantMetadata
		wantMapped
		wantMasked
		wantAmbient
	)
	cases := []struct {
		name        string
		passthrough bool
		masking     bool
		header      string
		metadata    string
		want        want
	}{
		{"passthrough: header passes through unmapped", true, false, clientSession, "", wantClient},
		{"passthrough: header is trimmed", true, false, "  " + clientSession + " ", "", wantClient},
		{"passthrough: header beats single-session mode", true, true, clientSession, "", wantClient},
		{"passthrough: no header falls back to single-session mode", true, true, "", "", wantMasked},
		{"passthrough: oversized header is treated as absent", true, false, oversized, "", wantAmbient},
		{"passthrough: metadata session wins over header", true, true, clientSession, metadataSession, wantMetadata},
		{"rewrite: header is mapped to the account", false, false, clientSession, "", wantMapped},
		{"rewrite: single-session mode masks the header", false, true, clientSession, "", wantMasked},
		{"rewrite: metadata session wins over header", false, false, clientSession, metadataSession, wantMetadata},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newMemoryIdentityCache()
			svc := &GatewayService{identityService: NewIdentityService(cache)}
			account := &Account{ID: 21, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"session_id_masking_enabled": tc.masking}}
			headers := http.Header{}
			if tc.header != "" {
				headers.Set("X-Claude-Code-Session-Id", tc.header)
			}

			got := svc.claudeUpstreamSessionID(ctx, account, 3, headers, tc.metadata, tc.passthrough)
			switch tc.want {
			case wantClient:
				require.Equal(t, clientSession, got)
			case wantMetadata:
				require.Equal(t, metadataSession, got)
				require.Equal(t, metadataSession, cache.last[account.ID], "metadata session is recorded as the last active session")
			case wantMapped:
				require.Equal(t, upstreamSessionIDFor(account.ID, 3, clientSession), got)
			case wantMasked:
				require.NotEmpty(t, got)
				require.Equal(t, cache.masked[account.ID], got)
			case wantAmbient:
				require.NotEmpty(t, got)
				require.Equal(t, cache.ambient[account.ID], got)
			}
		})
	}
}
