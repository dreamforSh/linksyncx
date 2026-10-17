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
	"github.com/stretchr/testify/require"
)

type perplexityMonitorHTTPRepo struct {
	service.ChannelMonitorRepository
	monitor *service.ChannelMonitor
	writes  int
}

func (r *perplexityMonitorHTTPRepo) Create(_ context.Context, m *service.ChannelMonitor) error {
	m.ID = 42
	stored := *m
	r.monitor = &stored
	r.writes++
	return nil
}
func (r *perplexityMonitorHTTPRepo) GetByID(_ context.Context, _ int64) (*service.ChannelMonitor, error) {
	if r.monitor == nil {
		return nil, service.ErrChannelMonitorNotFound
	}
	copy := *r.monitor
	return &copy, nil
}
func (r *perplexityMonitorHTTPRepo) Update(_ context.Context, m *service.ChannelMonitor) error {
	stored := *m
	r.monitor = &stored
	r.writes++
	return nil
}

type perplexityMonitorHTTPAccounts struct{ service.AccountRepository }

func (*perplexityMonitorHTTPAccounts) GetByID(_ context.Context, id int64) (*service.Account, error) {
	return &service.Account{ID: id, Platform: service.PlatformPerplexity, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "private-cookie-not-for-monitor-response"}}, nil
}

func TestPerplexityMonitorHTTPCreateAndUpdateReachPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &perplexityMonitorHTTPRepo{}
	svc := service.NewChannelMonitorService(repo, duplicateChannelMonitorHandlerEncryptor{})
	svc.SetQuotaFetcher(service.NewChannelMonitorQuotaFetcher(nil, nil, nil, &perplexityMonitorHTTPAccounts{}, nil))
	handler := NewChannelMonitorHandler(svc)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 77})
		c.Next()
	})
	router.POST("/monitors", handler.Create)
	router.PUT("/monitors/:id", handler.Update)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	created := call(http.MethodPost, "/monitors", `{"name":"Perplexity 配额","provider":"perplexity","account_id":72,"interval_seconds":60}`)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Equal(t, 1, repo.writes)
	require.Equal(t, "perplexity", repo.monitor.Provider)
	require.Equal(t, "quota", repo.monitor.CheckMode)
	require.Equal(t, "ENC:", repo.monitor.APIKey)
	require.Contains(t, created.Body.String(), `"api_key_masked":"***"`)
	require.NotContains(t, created.Body.String(), "private-cookie")
	updated := call(http.MethodPut, "/monitors/42", `{"provider":"perplexity","name":"updated","check_mode":"quota","account_id":72}`)
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())
	require.Equal(t, 2, repo.writes)
	for _, tc := range []struct{ body, reason string }{
		{`{"provider":"bogus"}`, "VALIDATION_ERROR"},
		{`{"provider":"perplexity","account_id":0}`, "CHANNEL_MONITOR_ACCOUNT_REQUIRED"},
		{`{"provider":"perplexity","api_key":"cookie"}`, "CHANNEL_MONITOR_PERPLEXITY_CONFIG"},
		{`{"provider":"perplexity","extra_headers":{"Cookie":"cookie"}}`, "CHANNEL_MONITOR_PERPLEXITY_CONFIG"},
		{`{"provider":"perplexity","api_mode":"responses"}`, "CHANNEL_MONITOR_INVALID_API_MODE"},
	} {
		rec := call(http.MethodPut, "/monitors/42", tc.body)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		var result response.Response
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, tc.reason, result.Reason)
	}
	require.Equal(t, 2, repo.writes, "invalid configurations must not write")
}

func TestPerplexityMonitorBindingPreservesExistingProviders(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini", "grok", "perplexity", "antigravity", "kimi", "zhipu", "deepseek", "minimax", "opencode_go"} {
		for _, update := range []bool{false, true} {
			body := fmt.Sprintf(`{"provider":%q,"name":"monitor","interval_seconds":60}`, provider)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			var target any = &channelMonitorCreateRequest{}
			if update {
				target = &channelMonitorUpdateRequest{}
			}
			require.NoError(t, binding.JSON.Bind(req, target), "%s update=%v", provider, update)
		}
	}
}

func TestPerplexityMonitorTemplateHTTPReportsCapabilityNotBindingFailure(t *testing.T) {
	handler := NewChannelMonitorRequestTemplateHandler(service.NewChannelMonitorRequestTemplateService(nil))
	router := gin.New()
	router.POST("/templates", handler.Create)
	req := httptest.NewRequest(http.MethodPost, "/templates", strings.NewReader(`{"name":"native","provider":"perplexity"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	var result response.Response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	require.Equal(t, "CHANNEL_MONITOR_PERPLEXITY_CONFIG", result.Reason)
}
