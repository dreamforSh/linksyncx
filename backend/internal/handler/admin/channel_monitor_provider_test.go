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
	writes   int
	stored   *service.ChannelMonitor
}

func (r *channelMonitorProviderRepoStub) GetByID(_ context.Context, id int64) (*service.ChannelMonitor, error) {
	r.getCalls++
	if r.stored != nil {
		copy := *r.stored
		return &copy, nil
	}
	return &service.ChannelMonitor{
		ID: id, Provider: service.MonitorProviderOpenAI, APIMode: service.MonitorAPIModeChatCompletions,
		Endpoint: "https://api.openai.com", APIKey: "ENC:sk-openai", PrimaryModel: "gpt-5",
		IntervalSeconds: 60, CheckMode: service.MonitorCheckModeProbe,
	}, nil
}

func (r *channelMonitorProviderRepoStub) Create(_ context.Context, monitor *service.ChannelMonitor) error {
	r.writes++
	monitor.ID = 42
	stored := *monitor
	r.stored = &stored
	return nil
}

func (r *channelMonitorProviderRepoStub) Update(_ context.Context, monitor *service.ChannelMonitor) error {
	r.writes++
	stored := *monitor
	r.stored = &stored
	return nil
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
				if provider == "perplexity" {
					assertPerplexityMonitorProviderPersists(t, endpoint.method, endpoint.path)
					return
				}
				repo := &channelMonitorProviderRepoStub{}
				handler := NewChannelMonitorHandler(service.NewChannelMonitorService(repo, nil))
				router := newChannelMonitorProviderRouter(handler)
				body := fmt.Sprintf(`{"name":"monitor","provider":%q,"interval_seconds":60}`, provider)
				recorder := serveChannelMonitorProvider(router, endpoint.method, endpoint.path, body)

				require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
				var result response.Response
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &result))
				require.Equal(t, "VALIDATION_ERROR", result.Reason)
				require.Contains(t, result.Message, "Provider")
				require.Contains(t, result.Message, "oneof")
				require.Zero(t, repo.getCalls)
				require.Zero(t, repo.writes)
			})
		}
	}
}

func assertPerplexityMonitorProviderPersists(t *testing.T, method, path string) {
	t.Helper()
	repo := &channelMonitorProviderRepoStub{}
	monitorService := service.NewChannelMonitorService(repo, duplicateChannelMonitorHandlerEncryptor{})
	monitorService.SetQuotaFetcher(service.NewChannelMonitorQuotaFetcher(nil, nil, nil, &perplexityMonitorHTTPAccounts{}, nil))
	router := newChannelMonitorProviderRouter(NewChannelMonitorHandler(monitorService))
	body := `{"name":"monitor","provider":"perplexity","interval_seconds":60,"account_id":72,"check_mode":"quota"}`
	if method == http.MethodPut {
		body = `{"provider":"perplexity","check_mode":"quota","account_id":72}`
	}
	recorder := serveChannelMonitorProvider(router, method, path, body)

	require.Contains(t, []int{http.StatusCreated, http.StatusOK}, recorder.Code, recorder.Body.String())
	require.Equal(t, 1, repo.writes)
	require.Equal(t, service.MonitorProviderPerplexity, repo.stored.Provider)
	require.Equal(t, service.MonitorCheckModeQuota, repo.stored.CheckMode)
	require.Equal(t, "ENC:", repo.stored.APIKey)
	require.NotContains(t, recorder.Body.String(), "private-cookie")
	if method == http.MethodPut {
		require.Equal(t, 1, repo.getCalls)
	}
}

func newChannelMonitorProviderRouter(handler *ChannelMonitorHandler) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	router.POST("/api/v1/admin/channel-monitors", handler.Create)
	router.PUT("/api/v1/admin/channel-monitors/:id", handler.Update)
	return router
}

func serveChannelMonitorProvider(router *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}
