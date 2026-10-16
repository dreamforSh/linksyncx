//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityAuditFirstErrorFrameKeepsUsage(t *testing.T) {
	protocols := []struct {
		name string
		run  func(*AntigravityGatewayService, *gin.Context, *http.Response) (*antigravityStreamResult, error)
	}{
		{"messages", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleClaudeStreamingResponse(c, r, time.Now(), "gemini-3.1-pro-high")
		}},
		{"gemini", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleGeminiStreamingResponse(c, r, time.Now())
		}},
		{"chat", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleChatCompletionsStreamingFromAntigravity(c, r, time.Now(), "gemini-3.1-pro-high", true)
		}},
		{"responses", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleResponsesStreamingFromAntigravity(c, r, time.Now(), "gemini-3.1-pro-high")
		}},
	}
	for _, protocol := range protocols {
		for _, frame := range []string{
			`{"error":{"code":500},"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`,
			`{"error":{"code":500},"response":{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}`,
			`{"response":{"error":{"code":500},"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}`,
		} {
			t.Run(protocol.name, func(t *testing.T) {
				svc := newAntigravityCompatService(config.GatewayConfig{}, nil)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/", nil)
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: " + frame + "\n\n"))}
				defer func() { _ = resp.Body.Close() }()
				result, err := protocol.run(svc, c, resp)
				require.Error(t, err)
				require.NotNil(t, result)
				require.Equal(t, 8, result.usage.InputTokens)
				require.Equal(t, 3, result.usage.OutputTokens)
				require.True(t, result.usageIncomplete)
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				require.NotContains(t, recorder.Body.String(), "response.completed")
				require.NotContains(t, recorder.Body.String(), "message_stop")
				require.NotContains(t, recorder.Body.String(), "data: [DONE]")
			})
		}
	}
}

type antigravityAuditFallbackUpstream struct {
	HTTPUpstream
	requests []*http.Request
	cancel   context.CancelFunc
}

func (u *antigravityAuditFallbackUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.requests = append(u.requests, req)
	if len(u.requests) == 1 {
		if u.cancel != nil {
			u.cancel()
		}
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":404,"message":"model not found"}}`))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(
		`data: {"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n",
	))}, nil
}

func TestAntigravityAuditModelFallbackSharesLifecycle(t *testing.T) {
	t.Setenv(antigravityForwardBaseURLEnv, "")
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "cancel_before_fallback"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			upstream := &antigravityAuditFallbackUpstream{}
			if canceled {
				upstream.cancel = cancel
			}
			settings := &antigravitySettingRepoStub{values: map[string]string{
				SettingKeyEnableModelFallback: "true", SettingKeyFallbackModelAntigravity: "gemini-fallback-upstream",
			}}
			svc := &AntigravityGatewayService{
				settingService: NewSettingService(settings, &config.Config{}), tokenProvider: &AntigravityTokenProvider{}, httpUpstream: upstream,
			}
			account := newAntigravityCompatAccount(AccountTypeOAuth)
			account.Credentials["plan_type"] = "pro"
			account.Credentials["model_mapping"] = map[string]any{"gemini-primary": "gemini-primary-upstream"}
			body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
			c, _ := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-primary:streamGenerateContent", body)
			c.Request = c.Request.WithContext(ctx)
			result, err := svc.ForwardGemini(ctx, c, account, "gemini-primary", "streamGenerateContent", true, body, false)
			if canceled {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, result)
				require.Len(t, upstream.requests, 1)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Len(t, upstream.requests, 2)
			for _, request := range upstream.requests {
				require.True(t, strings.HasPrefix(request.URL.String(), antigravity.BaseURLs[1]))
				require.Equal(t, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileFromContext(request.Context()))
				_, bounded := request.Context().Deadline()
				require.True(t, bounded)
				require.NotNil(t, antigravityLifecycle(request.Context()))
			}
		})
	}
}
