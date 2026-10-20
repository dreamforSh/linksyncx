//go:build unit

package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// authRejectingUpstream 对每次上游发送计数，并固定返回 401/403，用来数同账号到底发了几次。
type authRejectingUpstream struct {
	HTTPUpstream
	status int
	calls  int
}

func (u *authRejectingUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return u.reject(), nil
}

func (u *authRejectingUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	return u.reject(), nil
}

func (u *authRejectingUpstream) reject() *http.Response {
	u.calls++
	errType := "permission_error"
	if u.status == http.StatusUnauthorized {
		errType = "authentication_error"
	}
	return &http.Response{
		StatusCode: u.status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"` + errType + `","message":"rejected"}}`)),
	}
}

const (
	anthropicAuthEntryMessages    = "messages"
	anthropicAuthEntryChat        = "chat_completions"
	anthropicAuthEntryResponses   = "responses"
	anthropicAuthEntryCountTokens = "count_tokens"
)

var anthropicAuthEntryPoints = []string{
	anthropicAuthEntryMessages,
	anthropicAuthEntryChat,
	anthropicAuthEntryResponses,
	anthropicAuthEntryCountTokens,
}

type anthropicAuthRejectionOutcome struct {
	err   error
	calls int
	repo  *rateLimitAccountRepoStub
}

// runAnthropicAuthRejection 用全新的 GatewayService/账号仓库把一次请求送进指定入口，上游固定回 status。
func runAnthropicAuthRejection(t *testing.T, account *Account, entry string, status int) anthropicAuthRejectionOutcome {
	t.Helper()
	gin.SetMode(gin.TestMode)

	paths := map[string]string{
		anthropicAuthEntryMessages:    "/v1/messages",
		anthropicAuthEntryChat:        "/v1/chat/completions",
		anthropicAuthEntryResponses:   "/v1/responses",
		anthropicAuthEntryCountTokens: "/v1/messages/count_tokens",
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, paths[entry], nil)
	c.Request.Header.Set("User-Agent", claude.DefaultUserAgent())

	upstream := &authRejectingUpstream{status: status}
	cfg := &config.Config{}
	repo := &rateLimitAccountRepoStub{}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil)}

	messagesBody := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"user_device_account__session_11111111-2222-4333-8444-555555555555"}}`)
	var err error
	switch entry {
	case anthropicAuthEntryMessages:
		parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(messagesBody), PlatformAnthropic)
		require.NoError(t, parseErr)
		_, err = svc.Forward(t.Context(), c, account, parsed)
	case anthropicAuthEntryChat:
		_, err = svc.ForwardAsChatCompletions(t.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`), nil)
	case anthropicAuthEntryResponses:
		_, err = svc.ForwardAsResponses(t.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`), nil)
	case anthropicAuthEntryCountTokens:
		parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(messagesBody), PlatformAnthropic)
		require.NoError(t, parseErr)
		err = svc.ForwardCountTokens(t.Context(), c, account, parsed)
	default:
		t.Fatalf("unknown entry point %q", entry)
	}
	return anthropicAuthRejectionOutcome{err: err, calls: upstream.calls, repo: repo}
}

func newAnthropicAuthRetryAPIKeyAccount(passthrough bool) *Account {
	account := newAnthropicAPIKeyAccountForTest()
	if !passthrough {
		account.Extra = map[string]any{}
	}
	return account
}

// OAuth / Setup Token（含带 refresh_token 的 setup-token）收到 401/403：同一个 token 原地重发结果不变，
// 只会多打上游风控。四个入口都只能发一次，且不允许 handler 回到同账号再试。
func TestAnthropicOAuthAuthRejectionIsSentOnceAtEveryEntryPoint(t *testing.T) {
	accounts := []struct {
		name    string
		account func() *Account
	}{
		{"oauth", func() *Account {
			account := newAnthropicOAuthAccountForPartialUsageTest()
			account.Credentials["refresh_token"] = "oauth-refresh-token"
			return account
		}},
		{"setup_token", func() *Account {
			account := newAnthropicOAuthAccountForPartialUsageTest()
			account.Type = AccountTypeSetupToken
			return account
		}},
		{"setup_token_refreshable", func() *Account {
			account := newAnthropicOAuthAccountForPartialUsageTest()
			account.Type = AccountTypeSetupToken
			account.Credentials["refresh_token"] = "setup-refresh-token"
			return account
		}},
	}
	require.True(t, ClaudeSetupTokenRefreshable(accounts[2].account()))
	for _, tc := range accounts {
		for _, entry := range anthropicAuthEntryPoints {
			for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
				t.Run(fmt.Sprintf("%s/%s/%d", tc.name, entry, status), func(t *testing.T) {
					out := runAnthropicAuthRejection(t, tc.account(), entry, status)

					require.Error(t, out.err)
					require.Equal(t, 1, out.calls, "401/403 must not be resent on the same OAuth/setup-token credential")
					var failure *UpstreamFailoverError
					if entry == anthropicAuthEntryCountTokens {
						// count_tokens 不做 failover；这里只钉住“只发一次、不回同账号”，不钉它的错误形态。
						if errors.As(out.err, &failure) {
							require.False(t, failure.RetryableOnSameAccount)
						}
						return
					}
					require.ErrorAs(t, out.err, &failure)
					require.Equal(t, status, failure.StatusCode)
					require.False(t, failure.RetryableOnSameAccount)
				})
			}
		}
	}
}

// 池模式 / 自定义错误码是管理员的显式配置，401/403 不得越过它们：
//   - 池模式：不标记账号，同账号重试与否完全由 pool_mode_retry_status_codes 决定（默认含 401/403）；
//   - 自定义错误码未选中 401/403：不标记账号，直接 failover，不在请求内原地重发 5 次；
//   - 两者同时开启：每次 Forward 只发一次，同账号重试只走池模式那一层（否则会 5×(1+N) 次放大）。
func TestAnthropicAPIKeyAuthRejectionRespectsPoolAndCustomErrorCodes(t *testing.T) {
	cases := []struct {
		name          string
		account       func() *Account
		wantSameRetry bool
	}{
		{"pool_default", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(false)
			account.Credentials["pool_mode"] = true
			return account
		}, true},
		{"pool_default_passthrough", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(true)
			account.Credentials["pool_mode"] = true
			return account
		}, true},
		{"pool_retry_codes_429_only", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(false)
			account.Credentials["pool_mode"] = true
			account.Credentials["pool_mode_retry_status_codes"] = []any{float64(429)}
			return account
		}, false},
		{"custom_codes_429_only", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(false)
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(429)}
			return account
		}, false},
		{"custom_codes_429_only_passthrough", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(true)
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(429)}
			return account
		}, false},
		{"pool_and_custom_codes_429_only", func() *Account {
			account := newAnthropicAuthRetryAPIKeyAccount(false)
			account.Credentials["pool_mode"] = true
			account.Credentials["custom_error_codes_enabled"] = true
			account.Credentials["custom_error_codes"] = []any{float64(429)}
			return account
		}, true},
	}
	for _, tc := range cases {
		for _, entry := range anthropicAuthEntryPoints {
			for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
				t.Run(fmt.Sprintf("%s/%s/%d", tc.name, entry, status), func(t *testing.T) {
					out := runAnthropicAuthRejection(t, tc.account(), entry, status)

					require.Error(t, out.err)
					require.Equal(t, 1, out.calls, "401/403 must not be retried in place within one Forward")
					require.Zero(t, out.repo.setErrorCalls, "admin pool/custom-code settings must keep the account unmarked")
					require.Zero(t, out.repo.tempCalls, "admin pool/custom-code settings must keep the account unmarked")
					require.Zero(t, out.repo.rateLimitedCalls, "admin pool/custom-code settings must keep the account unmarked")
					if entry == anthropicAuthEntryCountTokens {
						return
					}
					var failure *UpstreamFailoverError
					require.ErrorAs(t, out.err, &failure)
					require.Equal(t, status, failure.StatusCode)
					require.Equal(t, tc.wantSameRetry, failure.RetryableOnSameAccount)
				})
			}
		}
	}
}

// 修复面只收窄到 Anthropic 的 401/403：自定义错误码未选中的其它状态码仍按旧语义原地重试。
func TestShouldRetryUpstreamErrorOnlyStopsAnthropicAuthRejections(t *testing.T) {
	svc := &GatewayService{}
	account := newAnthropicAuthRetryAPIKeyAccount(false)
	account.Credentials["custom_error_codes_enabled"] = true
	account.Credentials["custom_error_codes"] = []any{float64(429)}

	require.False(t, svc.shouldRetryUpstreamError(account, http.StatusUnauthorized))
	require.False(t, svc.shouldRetryUpstreamError(account, http.StatusForbidden))
	require.False(t, svc.shouldRetryUpstreamError(account, http.StatusTooManyRequests), "selected codes are handled, not retried")
	require.True(t, svc.shouldRetryUpstreamError(account, http.StatusInternalServerError), "unselected non-auth codes keep the in-place retry")
	require.True(t, svc.shouldRetryUpstreamError(account, 529), "unselected non-auth codes keep the in-place retry")
}
