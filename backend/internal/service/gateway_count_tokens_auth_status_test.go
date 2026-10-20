//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// count_tokens 的上游 401/403 是网关所用账号的问题，不能原样回给下游（客户端会以为自己的 key 失效），
// 与 /v1/messages 一致映射为 502；管理员的错误透传规则优先；其余状态码不变。

func countTokensUpstreamResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

var countTokensUpstreamStatusCases = []struct {
	upstream   int
	wantStatus int
	wantMsg    string
}{
	{http.StatusUnauthorized, http.StatusBadGateway, "Upstream authentication failed, please contact administrator"},
	{http.StatusForbidden, http.StatusBadGateway, "Upstream access forbidden, please contact administrator"},
	{http.StatusTooManyRequests, http.StatusTooManyRequests, "Rate limit exceeded"},
	{529, 529, "Service overloaded"},
	{http.StatusInternalServerError, http.StatusInternalServerError, "Upstream request failed"},
}

func TestForwardCountTokens_UpstreamAuthFailureIsGatewaySideError(t *testing.T) {
	for _, tc := range countTokensUpstreamStatusCases {
		t.Run(strconv.Itoa(tc.upstream), func(t *testing.T) {
			c, rec := newAnthropicOAuthMappingTestContext("/v1/messages/count_tokens")
			parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
			upstream := &anthropicHTTPUpstreamRecorder{resp: countTokensUpstreamResponse(tc.upstream,
				`{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`)}
			svc := newAnthropicOAuthMappingGatewayService(upstream)
			repo := &rateLimitAccountRepoStub{}
			svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)

			err := svc.ForwardCountTokens(context.Background(), c, account, parsed)

			require.Error(t, err)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
			require.Equal(t, tc.wantMsg, gjson.Get(rec.Body.String(), "error.message").String())
			if tc.upstream == http.StatusUnauthorized {
				// 账号侧处理不变：仍按上游真实状态标记账号。
				require.Equal(t, 1, repo.setErrorCalls)
			}
		})
	}
}

func TestForwardCountTokens_APIKeyPassthroughUpstreamAuthFailureIsGatewaySideError(t *testing.T) {
	for _, tc := range countTokensUpstreamStatusCases[:2] {
		t.Run(strconv.Itoa(tc.upstream), func(t *testing.T) {
			c, rec := newAnthropicOAuthMappingTestContext("/v1/messages/count_tokens")
			parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
			upstream := &anthropicHTTPUpstreamRecorder{resp: countTokensUpstreamResponse(tc.upstream,
				`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)}
			svc := newAnthropicOAuthMappingGatewayService(upstream)
			svc.rateLimitService = NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)
			account := newAnthropicAPIKeyAccountForTest()

			err := svc.ForwardCountTokens(context.Background(), c, account, parsed)

			require.Error(t, err)
			require.Equal(t, tc.wantStatus, rec.Code)
			require.Equal(t, tc.wantMsg, gjson.Get(rec.Body.String(), "error.message").String())
		})
	}
}

// 管理员配置的错误透传规则优先于默认映射。
func TestForwardCountTokens_ErrorPassthroughRuleOverridesDefaultMapping(t *testing.T) {
	c, rec := newAnthropicOAuthMappingTestContext("/v1/messages/count_tokens")
	ruleSvc := &ErrorPassthroughService{}
	ruleSvc.setLocalCache([]*model.ErrorPassthroughRule{newNonFailoverPassthroughRule(http.StatusUnauthorized, "invalid", http.StatusTeapot, "账号凭据失效")})
	BindErrorPassthroughService(c, ruleSvc)
	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	upstream := &anthropicHTTPUpstreamRecorder{resp: countTokensUpstreamResponse(http.StatusUnauthorized,
		`{"type":"error","error":{"type":"authentication_error","message":"invalid"}}`)}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	svc.rateLimitService = NewRateLimitService(&rateLimitAccountRepoStub{}, nil, &config.Config{}, nil, nil)

	err := svc.ForwardCountTokens(context.Background(), c, newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil), parsed)

	require.Error(t, err)
	require.Equal(t, http.StatusTeapot, rec.Code)
	require.Equal(t, "账号凭据失效", gjson.Get(rec.Body.String(), "error.message").String())
}
