//go:build unit

package service

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
)

// 手动测试对国产供应商 / OpenCode / Command Code 的 403 处置必须与运行时
// （handleOpenAI403 / handleCommandCodeUsageError）同口径：403 只回报给管理员，
// 不永久标错；只有 401 永久标错。Claude/Anthropic OAuth 的「403 = 封号」语义
// 不在此列。

type manualTestAccountCase struct {
	name    string
	account func(id int64) *Account
	model   string
}

func anthropicProtocolManualTestCases() []manualTestAccountCase {
	return []manualTestAccountCase{
		{
			name: "kimi_anthropic_protocol",
			account: func(id int64) *Account {
				return anthropicProtocolCNAccount(id, PlatformKimi, map[string]any{
					"base_url": "https://api.moonshot.cn/anthropic",
				})
			},
			model: "kimi-k2.5",
		},
		{
			name: "command_code_anthropic_protocol",
			account: func(id int64) *Account {
				account := commandCodeTestAccount(id)
				account.Credentials["api_protocol"] = APIProtocolAnthropic
				return account
			},
			model: "claude-sonnet-4-6",
		},
		{
			// 默认 adaptive：claude 模型按目录分流到原生 Anthropic 端点。
			name:    "command_code_adaptive_claude_model",
			account: commandCodeTestAccount,
			model:   "claude-sonnet-4-6",
		},
		{
			name: "opencode_go_anthropic_protocol",
			account: func(id int64) *Account {
				account := openCodeGoTestAccount(id)
				account.Credentials["api_protocol"] = APIProtocolAnthropic
				return account
			},
			model: "minimax-m2.5",
		},
	}
}

func manualTestStatusResponse(status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

var manualTest403Bodies = []struct {
	name        string
	contentType string
	body        string
}{
	{name: "cloudflare_1010", contentType: "text/plain; charset=UTF-8", body: "error code: 1010"},
	{name: "cloudflare_html", contentType: "text/html", body: "<!DOCTYPE html><html><head><title>Attention Required! | Cloudflare</title></head><body>blocked</body></html>"},
	{name: "json_permission_error", contentType: "application/json", body: `{"type":"error","error":{"type":"permission_error","message":"Your request was blocked."}}`},
	{name: "command_code_weekly_usage_limit", contentType: "application/json", body: `{"error":{"message":"You've reached your weekly usage limit"}}`},
}

func TestAccountTestService_AnthropicProtocol403DoesNotMarkAccountError(t *testing.T) {
	for i, tc := range anthropicProtocolManualTestCases() {
		for j, body := range manualTest403Bodies {
			t.Run(tc.name+"/"+body.name, func(t *testing.T) {
				account := tc.account(int64(900 + i*10 + j))
				svc, upstream := adaptiveCNAccountTestService(account,
					manualTestStatusResponse(http.StatusForbidden, body.contentType, body.body))
				c, recorder := newTestContext()

				err := svc.TestAccountConnection(c, account.ID, tc.model, "", AccountTestModeDefault)

				require.Error(t, err)
				require.Len(t, upstream.requests, 1)
				require.True(t, strings.HasSuffix(upstream.requests[0].URL.Path, "/v1/messages"), upstream.requests[0].URL.String())
				require.Contains(t, recorder.Body.String(), "Anthropic endpoint returned 403")
				repo := svc.accountRepo.(*openAIAccountTestRepo)
				require.Zero(t, repo.setErrorID, "a single manual-test 403 must not permanently error the account")
				require.Empty(t, repo.setErrorMsg)
			})
		}
	}
}

func TestAccountTestService_AnthropicProtocol401StillMarksAccountError(t *testing.T) {
	for i, tc := range anthropicProtocolManualTestCases() {
		t.Run(tc.name, func(t *testing.T) {
			account := tc.account(int64(950 + i))
			svc, upstream := adaptiveCNAccountTestService(account,
				manualTestStatusResponse(http.StatusUnauthorized, "application/json", `{"error":{"message":"invalid api key"}}`))
			c, _ := newTestContext()

			err := svc.TestAccountConnection(c, account.ID, tc.model, "", AccountTestModeDefault)

			require.Error(t, err)
			require.Len(t, upstream.requests, 1)
			repo := svc.accountRepo.(*openAIAccountTestRepo)
			require.Equal(t, account.ID, repo.setErrorID)
			require.Contains(t, repo.setErrorMsg, "Anthropic endpoint returned 401")
		})
	}
}

// 审计其余可被国产供应商 / OpenCode / Command Code 账号走到的手动测试路径：
// Chat Completions、Responses（含 CN responses 协议走的 OpenAI 探测）与
// adaptive 各端点，403 一律只回报不标错。
func TestAccountTestService_NonClaudeManualTestPaths403DoNotMarkAccountError(t *testing.T) {
	forbidden := func() *http.Response {
		return manualTestStatusResponse(http.StatusForbidden, "application/json",
			`{"error":{"type":"permission_error","message":"forbidden"}}`)
	}
	cases := []struct {
		name       string
		account    func(id int64) *Account
		model      string
		responses  func() []*http.Response
		wantSuffix string
		wantMsg    string
	}{
		{
			name: "kimi_chat_completions",
			account: func(id int64) *Account {
				return anthropicProtocolCNAccount(id, PlatformKimi, map[string]any{
					"api_protocol": APIProtocolChatCompletions,
					"base_url":     "https://api.moonshot.cn/v1",
				})
			},
			model:      "kimi-k2.5",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/chat/completions",
			wantMsg:    "Chat Completions API (/v1/chat/completions) returned 403",
		},
		{
			name: "kimi_responses_protocol",
			account: func(id int64) *Account {
				account := anthropicProtocolCNAccount(id, PlatformKimi, map[string]any{
					"api_protocol": APIProtocolResponses,
					"base_url":     "https://api.moonshot.cn/v1",
				})
				account.Extra = map[string]any{openai_compat.ExtraKeyResponsesSupported: true}
				return account
			},
			model:      "kimi-k2.5",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/responses",
			wantMsg:    "API returned 403",
		},
		{
			name:    "zhipu_adaptive_anthropic_leg",
			account: func(id int64) *Account { return adaptiveCNAccountTestAccount(id, PlatformZhipu) },
			model:   "glm-4.7",
			responses: func() []*http.Response {
				return []*http.Response{adaptiveCNChatTestResponse(), forbidden()}
			},
			wantSuffix: "/v1/messages",
			wantMsg:    "Adaptive Anthropic endpoint returned 403",
		},
		{
			name:    "kimi_adaptive_responses_leg",
			account: func(id int64) *Account { return adaptiveCNAccountTestAccount(id, PlatformKimi) },
			model:   "kimi-k2.5",
			responses: func() []*http.Response {
				return []*http.Response{adaptiveCNChatTestResponse(), adaptiveCNAnthropicTestResponse(), forbidden()}
			},
			wantSuffix: "/responses",
			wantMsg:    "Adaptive Responses endpoint returned 403",
		},
		{
			name:       "command_code_default_model_chat",
			account:    commandCodeTestAccount,
			model:      "",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/chat/completions",
			wantMsg:    "Chat Completions API (/v1/chat/completions) returned 403",
		},
		{
			name:       "command_code_gpt_responses",
			account:    commandCodeTestAccount,
			model:      "gpt-5.5",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/responses",
			wantMsg:    "Adaptive Responses endpoint returned 403",
		},
		{
			name:       "opencode_go_grok_responses",
			account:    openCodeGoTestAccount,
			model:      "grok-4.6",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/responses",
			wantMsg:    "Adaptive Responses endpoint returned 403",
		},
		{
			name:       "opencode_go_deepseek_chat",
			account:    openCodeGoTestAccount,
			model:      "deepseek-v4-flash",
			responses:  func() []*http.Response { return []*http.Response{forbidden()} },
			wantSuffix: "/chat/completions",
			wantMsg:    "Chat Completions API (/v1/chat/completions) returned 403",
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := tc.account(int64(970 + i))
			responses := tc.responses()
			svc, upstream := adaptiveCNAccountTestService(account, responses...)
			c, recorder := newTestContext()

			err := svc.TestAccountConnection(c, account.ID, tc.model, "hi", AccountTestModeDefault)

			require.Error(t, err)
			require.Len(t, upstream.requests, len(responses))
			last := upstream.requests[len(upstream.requests)-1]
			require.True(t, strings.HasSuffix(last.URL.Path, tc.wantSuffix), last.URL.String())
			require.Contains(t, recorder.Body.String(), tc.wantMsg)
			repo := svc.accountRepo.(*openAIAccountTestRepo)
			require.Zero(t, repo.setErrorID, "a single manual-test 403 must not permanently error the account")
		})
	}
}
