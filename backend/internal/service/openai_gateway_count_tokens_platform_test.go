//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// /responses/input_tokens 与 count_tokens 桥接都是 OpenAI 专有端点：只有 OpenAI
// 平台账号（OAuth，或 base_url 为空 / api.openai.com 的 API Key）发上游，其余
// 平台一律本地估算。

func inputTokensPlatformTestAccount(id int64, platform, accountType string, credentials map[string]any) *Account {
	return &Account{
		ID:          id,
		Name:        platform + "-" + accountType,
		Platform:    platform,
		Type:        accountType,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: credentials,
	}
}

func nonOpenAIInputTokensTestAccounts() map[string]*Account {
	return map[string]*Account{
		"opencode_zen_empty_base_url": inputTokensPlatformTestAccount(601, PlatformOpenCodeGo, AccountTypeAPIKey, map[string]any{
			"api_key": "sk-opencode-zen", "account_mode": AccountModeZen,
		}),
		"opencode_go_empty_base_url": inputTokensPlatformTestAccount(602, PlatformOpenCodeGo, AccountTypeAPIKey, map[string]any{
			"api_key": "sk-opencode-go", "account_mode": AccountModeGo,
		}),
		"codebuddy_oauth": inputTokensPlatformTestAccount(603, PlatformCodeBuddy, AccountTypeOAuth, map[string]any{
			"access_token": "codebuddy-token",
		}),
		"perplexity_empty_base_url": inputTokensPlatformTestAccount(604, PlatformPerplexity, AccountTypeAPIKey, map[string]any{
			"api_key": "pplx-session",
		}),
		"command_code": inputTokensPlatformTestAccount(605, PlatformCommandCode, AccountTypeAPIKey, map[string]any{
			"api_key": "user_test_key",
		}),
		"kimi_empty_base_url": inputTokensPlatformTestAccount(606, PlatformKimi, AccountTypeAPIKey, map[string]any{
			"api_key": "sk-kimi",
		}),
		"grok_oauth": inputTokensPlatformTestAccount(607, PlatformGrok, AccountTypeOAuth, map[string]any{
			"access_token": "grok-token",
		}),
		"grok_apikey_empty_base_url": inputTokensPlatformTestAccount(608, PlatformGrok, AccountTypeAPIKey, map[string]any{
			"api_key": "xai-key",
		}),
	}
}

func TestShouldEstimateOpenAIInputTokensLocally_OnlyOpenAIPlatformGoesUpstream(t *testing.T) {
	require.True(t, shouldEstimateOpenAIInputTokensLocally(nil))
	for name, account := range nonOpenAIInputTokensTestAccounts() {
		require.True(t, shouldEstimateOpenAIInputTokensLocally(account), name)
	}

	cases := []struct {
		name    string
		account *Account
		local   bool
	}{
		{
			name:    "openai_oauth",
			account: inputTokensPlatformTestAccount(701, PlatformOpenAI, AccountTypeOAuth, map[string]any{"access_token": "oauth-token"}),
			local:   false,
		},
		{
			name:    "openai_apikey_empty_base_url",
			account: inputTokensPlatformTestAccount(702, PlatformOpenAI, AccountTypeAPIKey, map[string]any{"api_key": "sk-official"}),
			local:   false,
		},
		{
			name: "openai_apikey_official_base_url",
			account: inputTokensPlatformTestAccount(703, PlatformOpenAI, AccountTypeAPIKey, map[string]any{
				"api_key": "sk-official", "base_url": "https://API.openai.com/v1",
			}),
			local: false,
		},
		{
			name: "openai_apikey_relay_base_url",
			account: inputTokensPlatformTestAccount(704, PlatformOpenAI, AccountTypeAPIKey, map[string]any{
				"api_key": "relay-key", "base_url": "https://relay.example/v1",
			}),
			local: true,
		},
		{
			name: "openai_apikey_unparseable_base_url",
			account: inputTokensPlatformTestAccount(705, PlatformOpenAI, AccountTypeAPIKey, map[string]any{
				"api_key": "relay-key", "base_url": "http://[::1",
			}),
			local: true,
		},
		{
			name:    "openai_upstream",
			account: inputTokensPlatformTestAccount(706, PlatformOpenAI, AccountTypeUpstream, map[string]any{"api_key": "up-key"}),
			local:   true,
		},
	}
	for _, tc := range cases {
		require.Equal(t, tc.local, shouldEstimateOpenAIInputTokensLocally(tc.account), tc.name)
	}
}

func TestForwardResponsesInputTokens_NonOpenAIPlatformsEstimateLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","instructions":"Be concise.","input":"hello world"}`)

	for name, account := range nonOpenAIInputTokensTestAccounts() {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
			c.Request.Header.Set("User-Agent", "codex_cli_rs/0.200.0")
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"must not be called"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}

			err := svc.ForwardResponsesInputTokens(context.Background(), c, account, body)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "response.input_tokens", gjson.Get(recorder.Body.String(), "object").String())
			require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
			require.Nil(t, upstream.lastReq, "%s must not receive /v1/responses/input_tokens", account.Platform)
		})
	}
}

func TestForwardResponsesInputTokens_OpenAIOfficialAccountsGoUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.4","input":"hello world"}`)

	cases := []struct {
		name     string
		account  *Account
		wantAuth string
	}{
		{
			name:     "openai_oauth",
			account:  inputTokensPlatformTestAccount(711, PlatformOpenAI, AccountTypeOAuth, map[string]any{"access_token": "oauth-token"}),
			wantAuth: "Bearer oauth-token",
		},
		{
			name:     "openai_apikey_empty_base_url",
			account:  inputTokensPlatformTestAccount(712, PlatformOpenAI, AccountTypeAPIKey, map[string]any{"api_key": "sk-official"}),
			wantAuth: "Bearer sk-official",
		},
		{
			name: "openai_apikey_official_base_url",
			account: inputTokensPlatformTestAccount(713, PlatformOpenAI, AccountTypeAPIKey, map[string]any{
				"api_key": "sk-official", "base_url": "https://api.openai.com/v1",
			}),
			wantAuth: "Bearer sk-official",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"object":"response.input_tokens","input_tokens":42}`)),
			}}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
				httpUpstream: upstream,
			}

			err := svc.ForwardResponsesInputTokens(context.Background(), c, tc.account, body)

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, int64(42), gjson.Get(recorder.Body.String(), "input_tokens").Int())
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "https://api.openai.com/v1/responses/input_tokens", upstream.lastReq.URL.String())
			require.Equal(t, tc.wantAuth, upstream.lastReq.Header.Get("Authorization"))
		})
	}
}

func TestForwardCountTokensAsAnthropic_NonOpenAIPlatformsEstimateLocally(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-6","system":"Be concise.","messages":[{"role":"user","content":"hello world"}]}`)

	for name, account := range nonOpenAIInputTokensTestAccounts() {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"must not be called"}}`)),
			}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}

			err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, account, body, "")

			require.NoError(t, err)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Positive(t, gjson.Get(recorder.Body.String(), "input_tokens").Int())
			require.Nil(t, upstream.lastReq, "%s must not receive /v1/responses/input_tokens", account.Platform)
		})
	}
}
