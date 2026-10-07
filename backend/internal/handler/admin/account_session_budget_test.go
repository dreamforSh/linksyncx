package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type sessionBudgetCountStub struct {
	service.SessionLimitCache
	counts    map[int64]int
	requested []int64
}

func (s *sessionBudgetCountStub) GetActiveSessionCountBatch(_ context.Context, accountIDs []int64, _ map[int64]time.Duration) (map[int64]int, error) {
	s.requested = append(s.requested, accountIDs...)
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = s.counts[id]
	}
	return out, nil
}

func newSessionBudgetAccountHandler(t *testing.T, adminSvc *stubAdminService, sessions service.SessionLimitCache, defaultBudget string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, sessions, nil, nil)
	if defaultBudget != "" {
		repo := &settingHandlerRepoStub{values: map[string]string{service.SettingKeyClaudeDefaultMaxSessions: defaultBudget}}
		handler.SetSettingService(service.NewSettingService(repo, &config.Config{}))
	}
	router := gin.New()
	router.GET("/api/v1/admin/accounts", handler.List)
	router.GET("/api/v1/admin/accounts/:id", handler.GetByID)
	return router
}

// getData 发起 GET 请求并返回响应中的 data 字段。
func getData(t *testing.T, router *gin.Engine, path string) any {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Data any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body.Data
}

func asJSONObject(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected a JSON object, got %T", v)
	return m
}

// 账号列表按生效预算返回 session_budget 与活跃会话数：未单独配置的 OAuth 账号用系统默认值，
// 单会话模式为 1，账号自己的 max_sessions 优先；非 Claude OAuth 账号不返回。
func TestAccountListReportsEffectiveSessionBudget(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{
		{ID: 1, Name: "default", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive},
		{ID: 2, Name: "explicit", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive,
			Extra: map[string]any{"max_sessions": 3}},
		{ID: 3, Name: "single", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Extra: map[string]any{"session_id_masking_enabled": true}},
		{ID: 4, Name: "apikey", Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey, Status: service.StatusActive,
			Extra: map[string]any{"max_sessions": 3}},
	}
	sessions := &sessionBudgetCountStub{counts: map[int64]int{1: 2, 2: 1, 3: 1}}
	router := newSessionBudgetAccountHandler(t, adminSvc, sessions, "4")

	for _, lite := range []string{"", "&lite=1"} {
		items, ok := asJSONObject(t, getData(t, router, "/api/v1/admin/accounts?page=1&page_size=10"+lite))["items"].([]any)
		require.True(t, ok)
		require.Len(t, items, 4)
		budgets := map[any]any{}
		active := map[any]any{}
		for _, raw := range items {
			item := asJSONObject(t, raw)
			budgets[item["id"]] = item["session_budget"]
			active[item["id"]] = item["active_sessions"]
		}
		require.Equal(t, map[any]any{float64(1): float64(4), float64(2): float64(3), float64(3): float64(1), float64(4): nil}, budgets, "lite=%q", lite)
		require.Equal(t, map[any]any{float64(1): float64(2), float64(2): float64(1), float64(3): float64(1), float64(4): nil}, active, "lite=%q", lite)
	}
	require.ElementsMatch(t, []int64{1, 2, 3, 1, 2, 3}, sessions.requested, "API key accounts are never counted")
}

func TestAccountDetailReportsEffectiveSessionBudget(t *testing.T) {
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = &service.Account{ID: 7, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive}
	sessions := &sessionBudgetCountStub{counts: map[int64]int{7: 3}}

	data := asJSONObject(t, getData(t, newSessionBudgetAccountHandler(t, adminSvc, sessions, ""), "/api/v1/admin/accounts/7"))
	require.Equal(t, float64(service.DefaultClaudeMaxSessions), data["session_budget"], "without settings the built-in default applies")
	require.Equal(t, float64(3), data["active_sessions"])

	data = asJSONObject(t, getData(t, newSessionBudgetAccountHandler(t, adminSvc, sessions, "0"), "/api/v1/admin/accounts/7"))
	require.NotContains(t, data, "session_budget", "default 0 means unlimited")
	require.NotContains(t, data, "active_sessions")
}

func TestUpdateSettingsClaudeDefaultMaxSessions(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})

	rec := doUpdateSettings(t, h, map[string]any{"claude_default_max_sessions": 8}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "8", repo.values[service.SettingKeyClaudeDefaultMaxSessions])
	require.Contains(t, rec.Body.String(), `"claude_default_max_sessions":8`)

	rec = doUpdateSettings(t, h, map[string]any{"risk_control_enabled": true}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "8", repo.values[service.SettingKeyClaudeDefaultMaxSessions], "an omitted value keeps the stored one")

	rec = doUpdateSettings(t, h, map[string]any{"claude_default_max_sessions": 0}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "0", repo.values[service.SettingKeyClaudeDefaultMaxSessions], "0 turns the default budget off")

	for _, invalid := range []int{-1, service.MaxClaudeDefaultMaxSessions + 1} {
		rec = doUpdateSettings(t, h, map[string]any{"claude_default_max_sessions": invalid}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code, "value %d", invalid)
		require.Contains(t, rec.Body.String(), "claude_default_max_sessions")
	}
	require.Equal(t, "0", repo.values[service.SettingKeyClaudeDefaultMaxSessions])
}
