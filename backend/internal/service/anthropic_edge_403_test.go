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
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Anthropic OAuth/setup-token 的边缘 403（Cloudflare 拦截页等）说明代理出口被拦，不是账号证据：
// 只计数，窗口内满阈值才临时停调，永不 SetError；JSON 403 仍首发即禁用。

const anthropicCloudflareBlockHTML = "<!DOCTYPE html>\n<html><head><title>Attention Required! | Cloudflare</title></head>" +
	"<body><div id=\"cf-error-details\">Sorry, you have been blocked</div></body></html>"

const anthropicOAuthForbiddenJSON = `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."}}`

type windowRecordingCounter struct {
	openAI403CounterCacheStub
	windows []int
}

func (c *windowRecordingCounter) IncrementOpenAI403Count(ctx context.Context, accountID int64, window int) (int64, error) {
	c.windows = append(c.windows, window)
	return c.openAI403CounterCacheStub.IncrementOpenAI403Count(ctx, accountID, window)
}

func TestAnthropicEdge403_CountsThenCoolsDownAndNeverMarksError(t *testing.T) {
	edgeResponses := []struct {
		name    string
		headers http.Header
		body    string
	}{
		{"cloudflare_html", http.Header{"Cf-Ray": []string{"8f00aa-LAX"}}, anthropicCloudflareBlockHTML},
		{"cloudflare_1010", http.Header{}, "error code: 1010"},
		{"cloudflare_1020", http.Header{}, "error code: 1020\n"},
		{"cf_mitigated_challenge", http.Header{"Cf-Mitigated": []string{"challenge"}}, `{"challenge":true}`},
	}
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		for _, edge := range edgeResponses {
			t.Run(accountType+"/"+edge.name, func(t *testing.T) {
				repo := &rateLimitAccountRepoStub{}
				counter := &windowRecordingCounter{openAI403CounterCacheStub: openAI403CounterCacheStub{counts: []int64{1, 2, 3, 4}}}
				blocker := &runtimeBlockRecorder{}
				svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
				svc.SetOpenAI403CounterCache(counter)
				svc.SetAccountRuntimeBlocker(blocker)
				account := &Account{ID: 701, Platform: PlatformAnthropic, Type: accountType}
				handle := func() bool {
					return svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, edge.headers, []byte(edge.body))
				}

				// 前两次：只换号，不处罚账号。
				require.False(t, handle())
				require.False(t, handle())
				require.Zero(t, repo.tempCalls)
				require.Empty(t, blocker.accounts)

				// 第三次：临时停调，原因写明疑似代理出口被拦。
				require.True(t, handle())
				require.Equal(t, 1, repo.tempCalls)
				require.Contains(t, repo.lastTempReason, "Anthropic edge 403 x3 within 10m")
				require.Contains(t, repo.lastTempReason, "proxy/egress IP likely blocked")
				require.Equal(t, []string{"anthropic_edge_403"}, blocker.reasons)

				// 之后继续停调，但永不升级为 error。
				require.True(t, handle())
				require.Equal(t, 2, repo.tempCalls)
				require.Zero(t, repo.setErrorCalls, "边缘 403 永不永久禁用账号")
				require.Equal(t, []int{anthropicEdge403WindowMinutes, anthropicEdge403WindowMinutes,
					anthropicEdge403WindowMinutes, anthropicEdge403WindowMinutes}, counter.windows)
			})
		}
	}
}

func TestAnthropicEdge403_ReasonIsConcise(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetOpenAI403CounterCache(&openAI403CounterCacheStub{counts: []int64{3}})
	account := &Account{ID: 702, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden,
		http.Header{"Cf-Ray": []string{"8f00aa-LAX"}}, []byte(anthropicCloudflareBlockHTML))

	require.Contains(t, repo.lastTempReason, "Attention Required! | Cloudflare cf-ray=8f00aa-LAX")
	require.NotContains(t, repo.lastTempReason, "<html", "不把整页 HTML 写进停调原因")
}

func TestAnthropicJSON403StillMarksErrorOnFirstHit(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			counter := &windowRecordingCounter{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetOpenAI403CounterCache(counter)
			account := &Account{ID: 703, Platform: PlatformAnthropic, Type: accountType}

			require.True(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden,
				http.Header{}, []byte(anthropicOAuthForbiddenJSON)))

			require.Equal(t, 1, repo.setErrorCalls)
			require.Contains(t, repo.lastErrorMsg, "OAuth authentication is currently not allowed")
			require.Empty(t, counter.windows, "结构化 403 不走边缘计数")
		})
	}
}

func TestAnthropicEdge403WithoutUsableCounterOnlyFailsOver(t *testing.T) {
	cases := map[string]OpenAI403CounterCache{
		"no_counter":    nil,
		"counter_error": &openAI403CounterCacheStub{err: context.DeadlineExceeded},
	}
	for name, counter := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			if counter != nil {
				svc.SetOpenAI403CounterCache(counter)
			}
			account := &Account{ID: 704, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

			require.False(t, svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden,
				http.Header{}, []byte(anthropicCloudflareBlockHTML)))
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
}

// 作用域守卫：只改 Anthropic OAuth/setup-token。Anthropic API Key（含中转）与 Antigravity 不变。
func TestAnthropicEdge403ScopeGuard(t *testing.T) {
	for _, account := range []*Account{
		{ID: 705, Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
		{ID: 706, Platform: PlatformAntigravity, Type: AccountTypeOAuth},
		{ID: 707, Platform: PlatformAntigravity, Type: AccountTypeSetupToken},
	} {
		t.Run(account.Platform+"/"+account.Type, func(t *testing.T) {
			repo := &rateLimitAccountRepoStub{}
			svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
			svc.SetOpenAI403CounterCache(&openAI403CounterCacheStub{})

			svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, []byte(anthropicCloudflareBlockHTML))

			require.Equal(t, 1, repo.setErrorCalls)
		})
	}
}

func TestIsAnthropicEdgeForbidden(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		body    string
		want    bool
	}{
		{"cloudflare_html", nil, anthropicCloudflareBlockHTML, true},
		{"bare_html", nil, "<html><body>403 Forbidden</body></html>", true},
		{"cloudflare_1010", nil, "error code: 1010", true},
		{"cloudflare_1020_uppercase", nil, "Error Code: 1020", true},
		{"cf_mitigated_challenge", http.Header{"Cf-Mitigated": []string{"challenge"}}, "", true},
		{"anthropic_permission_error", nil, anthropicOAuthForbiddenJSON, false},
		{"anthropic_json_with_cf_ray_only", http.Header{"Cf-Ray": []string{"x"}}, anthropicOAuthForbiddenJSON, false},
		{"plain_forbidden", nil, "Forbidden", false},
		{"error_code_with_suffix", nil, "error code: 1010 please contact admin", false},
		{"non_cloudflare_error_code", nil, "error code: 403", false},
		{"empty", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isAnthropicEdgeForbidden(tc.headers, []byte(tc.body)))
		})
	}
}

type anthropicEdgeForbiddenUpstream struct {
	HTTPUpstream
	calls int
}

func (u *anthropicEdgeForbiddenUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
		Body:       io.NopCloser(strings.NewReader(anthropicCloudflareBlockHTML)),
	}, nil
}

// 数据面不变：边缘 403 照常 failover 到其它账号，只是不再永久禁用当前账号。
func TestForwardAnthropicOAuthEdge403FailsOverWithoutMarkingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", claude.DefaultUserAgent())
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"user_device_account__session_11111111-2222-4333-8444-555555555555"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicEdgeForbiddenUpstream{}
	cfg := &config.Config{}
	repo := &rateLimitAccountRepoStub{}
	rateLimit := NewRateLimitService(repo, nil, cfg, nil, nil)
	rateLimit.SetOpenAI403CounterCache(&openAI403CounterCacheStub{})
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: rateLimit}
	_, err = svc.Forward(t.Context(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)

	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusForbidden, failure.StatusCode)
	require.Equal(t, 1, upstream.calls)
	require.Zero(t, repo.setErrorCalls)
}

// 手动 / 定时测试不能比运行时更严：边缘 403 只报告，不标错；JSON 403 仍标错。
func TestAccountTestService_AnthropicOAuthEdge403DoesNotMarkError(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantError   bool
		wantMessage string
	}{
		{"cloudflare_html", "text/html", anthropicCloudflareBlockHTML, false, "403 from the edge (proxy/egress IP likely blocked; account not marked): Attention Required! | Cloudflare"},
		{"cloudflare_1010", "text/plain", "error code: 1010", false, "account not marked): error code: 1010"},
		{"json_permission_error", "application/json", anthropicOAuthForbiddenJSON, true, "API returned 403"},
	}
	for i, tc := range cases {
		for j, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
			t.Run(tc.name+"/"+accountType, func(t *testing.T) {
				account := &Account{
					ID:          int64(1700 + i*10 + j),
					Platform:    PlatformAnthropic,
					Type:        accountType,
					Status:      StatusActive,
					Concurrency: 1,
					Credentials: map[string]any{"access_token": "at"},
				}
				svc, upstream := adaptiveCNAccountTestService(account, manualTestStatusResponse(http.StatusForbidden, tc.contentType, tc.body))
				c, recorder := newTestContext()

				err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)

				require.Error(t, err)
				require.Len(t, upstream.requests, 1)
				require.Contains(t, recorder.Body.String(), tc.wantMessage)
				repo, ok := svc.accountRepo.(*openAIAccountTestRepo)
				require.True(t, ok)
				if tc.wantError {
					require.Equal(t, account.ID, repo.setErrorID)
				} else {
					require.Zero(t, repo.setErrorID)
				}
			})
		}
	}
}
