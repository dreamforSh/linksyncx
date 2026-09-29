//go:build unit

package service

import (
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

type forbiddenUpstream struct {
	HTTPUpstream
	calls int
}

func (u *forbiddenUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"permission_error","message":"Access forbidden"}}`)),
	}, nil
}

// OAuth 账号收到 403 后不再用同一个 token 重试：只发一次，立刻标记账号并 failover 到其它账号。
func TestOAuthForbiddenRequestIsSentOnceAndFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", claude.DefaultUserAgent())
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"user_device_account__session_11111111-2222-4333-8444-555555555555"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	upstream := &forbiddenUpstream{}
	cfg := &config.Config{}
	repo := &rateLimitAccountRepoStub{}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil)}
	_, err = svc.Forward(t.Context(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)

	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, http.StatusForbidden, failure.StatusCode)
	require.False(t, failure.RetryableOnSameAccount)
	require.Equal(t, 1, upstream.calls, "403 must not be retried on the same OAuth account")
	require.Equal(t, 1, repo.setErrorCalls, "403 marks the OAuth account before failing over")
}
