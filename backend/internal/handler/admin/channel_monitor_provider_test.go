//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/stretchr/testify/require"
)

func bindChannelMonitorProviderJSON(body string, target any) error {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return binding.JSON.Bind(request, target)
}

func TestChannelMonitorProviderBinding_AllowedProviders(t *testing.T) {
	for _, provider := range []string{
		"openai", "anthropic", "gemini", "grok", "perplexity", "antigravity",
		"kimi", "zhipu", "deepseek", "minimax", "opencode_go",
	} {
		t.Run("create_"+provider, func(t *testing.T) {
			var req channelMonitorCreateRequest
			body := fmt.Sprintf(`{"name":"monitor","provider":%q,"interval_seconds":60}`, provider)
			require.NoError(t, bindChannelMonitorProviderJSON(body, &req))
			require.Equal(t, provider, req.Provider)
		})
		t.Run("update_"+provider, func(t *testing.T) {
			var req channelMonitorUpdateRequest
			body := fmt.Sprintf(`{"provider":%q}`, provider)
			require.NoError(t, bindChannelMonitorProviderJSON(body, &req))
			require.NotNil(t, req.Provider)
			require.Equal(t, provider, *req.Provider)
		})
	}
}

func TestChannelMonitorProviderBinding_RejectsInvalidProviders(t *testing.T) {
	for _, provider := range []string{"bogus", "pplx", "Perplexity", "perplexity ", "composite"} {
		t.Run("create_"+provider, func(t *testing.T) {
			var req channelMonitorCreateRequest
			body := fmt.Sprintf(`{"name":"monitor","provider":%q,"interval_seconds":60}`, provider)
			var validationErrors validator.ValidationErrors
			require.ErrorAs(t, bindChannelMonitorProviderJSON(body, &req), &validationErrors)
			require.Len(t, validationErrors, 1)
			require.Equal(t, "Provider", validationErrors[0].Field())
			require.Equal(t, "oneof", validationErrors[0].Tag())
		})
		t.Run("update_"+provider, func(t *testing.T) {
			var req channelMonitorUpdateRequest
			body := fmt.Sprintf(`{"provider":%q}`, provider)
			var validationErrors validator.ValidationErrors
			require.ErrorAs(t, bindChannelMonitorProviderJSON(body, &req), &validationErrors)
			require.Len(t, validationErrors, 1)
			require.Equal(t, "Provider", validationErrors[0].Field())
			require.Equal(t, "oneof", validationErrors[0].Tag())
		})
	}
}

func TestChannelMonitorProviderBinding_RequiredAndOptional(t *testing.T) {
	for _, tt := range []struct {
		name     string
		provider string
	}{
		{name: "omitted"},
		{name: "null", provider: `,"provider":null`},
		{name: "empty", provider: `,"provider":""`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"name":"monitor","interval_seconds":60` + tt.provider + `}`
			var create channelMonitorCreateRequest
			var validationErrors validator.ValidationErrors
			require.ErrorAs(t, bindChannelMonitorProviderJSON(body, &create), &validationErrors)
			require.Len(t, validationErrors, 1)
			require.Equal(t, "Provider", validationErrors[0].Field())
			require.Equal(t, "required", validationErrors[0].Tag())

			var update channelMonitorUpdateRequest
			err := bindChannelMonitorProviderJSON(body, &update)
			if tt.name == "empty" {
				require.ErrorAs(t, err, &validationErrors)
				require.Len(t, validationErrors, 1)
				require.Equal(t, "Provider", validationErrors[0].Field())
				require.Equal(t, "oneof", validationErrors[0].Tag())
				return
			}
			require.NoError(t, err)
			require.Nil(t, update.Provider)
		})
	}
}

type channelMonitorProviderRepoStub struct {
	service.ChannelMonitorRepository
	getCalls int
}

func (r *channelMonitorProviderRepoStub) GetByID(_ context.Context, id int64) (*service.ChannelMonitor, error) {
	r.getCalls++
	return &service.ChannelMonitor{ID: id, Provider: service.MonitorProviderOpenAI}, nil
}

func TestChannelMonitorProviderHTTP_BindingBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/v1/admin/channel-monitors"},
		{name: "update", method: http.MethodPut, path: "/api/v1/admin/channel-monitors/42"},
	} {
		for _, provider := range []string{"perplexity", "bogus", "pplx", "Perplexity", "perplexity ", "composite"} {
			t.Run(endpoint.name+"_"+provider, func(t *testing.T) {
				repo := &channelMonitorProviderRepoStub{}
				handler := NewChannelMonitorHandler(service.NewChannelMonitorService(repo, nil))
				router := gin.New()
				router.Use(func(c *gin.Context) {
					c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
					c.Next()
				})
				router.POST("/api/v1/admin/channel-monitors", handler.Create)
				router.PUT("/api/v1/admin/channel-monitors/:id", handler.Update)

				body := fmt.Sprintf(`{"name":"monitor","provider":%q,"interval_seconds":60}`, provider)
				request := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)

				require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
				var result response.Response
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
				if provider == "perplexity" {
					// 仅回归绑定入口：服务层尚未注册 perplexity，业务 400 不能误当成绑定 400。
					require.Equal(t, infraerrors.Reason(service.ErrChannelMonitorInvalidProvider), result.Reason)
					if endpoint.method == http.MethodPut {
						require.Equal(t, 1, repo.getCalls)
					}
					return
				}
				require.Equal(t, "VALIDATION_ERROR", result.Reason)
				require.Contains(t, result.Message, "Provider")
				require.Contains(t, result.Message, "oneof")
				require.Zero(t, repo.getCalls)
			})
		}
	}
}
