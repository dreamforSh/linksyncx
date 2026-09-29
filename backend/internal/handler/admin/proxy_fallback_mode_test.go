package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 过期改投直连（"direct"）已移除：API 入参直接拒绝，旧版导出文件导入时降级为 none 并在结果里提示。

func TestProxyAPIRejectsDirectFallbackMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	h := NewProxyHandler(adminSvc)
	router.POST("/api/v1/admin/proxies", h.Create)
	router.PUT("/api/v1/admin/proxies/:id", h.Update)

	send := func(method, path string, body map[string]any) int {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	create := map[string]any{"name": "p", "protocol": "http", "host": "proxy.example.com", "port": 8080, "fallback_mode": "direct"}
	require.Equal(t, http.StatusBadRequest, send(http.MethodPost, "/api/v1/admin/proxies", create))
	require.Equal(t, http.StatusBadRequest, send(http.MethodPut, "/api/v1/admin/proxies/1", map[string]any{"fallback_mode": "direct"}))

	adminSvc.mu.Lock()
	defer adminSvc.mu.Unlock()
	require.Empty(t, adminSvc.createdProxies, "a rejected request must not reach the service")
	require.Empty(t, adminSvc.updatedProxies)
}

func TestProxyImportDowngradesLegacyDirectFallbackMode(t *testing.T) {
	router, adminSvc := setupProxyDataRouter()
	adminSvc.proxies = []service.Proxy{{
		ID: 1, Name: "existing", Protocol: "http", Host: "127.0.0.1", Port: 8080, Status: service.StatusActive,
	}}

	payload := map[string]any{
		"data": map[string]any{
			"type":    dataType,
			"version": dataVersion,
			"proxies": []map[string]any{
				// Existing proxy whose status changes: goes through UpdateProxy.
				{"proxy_key": "http|127.0.0.1|8080||", "name": "existing", "protocol": "http", "host": "127.0.0.1", "port": 8080, "status": "inactive", "fallback_mode": "direct"},
				// New proxy: goes through CreateProxy.
				{"proxy_key": "http|10.0.0.2|3128||", "name": "fresh", "protocol": "http", "host": "10.0.0.2", "port": 3128, "status": "active", "fallback_mode": "direct"},
			},
			"accounts": []map[string]any{},
		},
	}
	body, _ := json.Marshal(payload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/proxies/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp proxyImportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.Data.ProxyCreated)
	require.Equal(t, 1, resp.Data.ProxyReused)
	require.Zero(t, resp.Data.ProxyFailed, "a legacy export must still import")

	warnings := 0
	for _, e := range resp.Data.Errors {
		if strings.Contains(e.Message, "downgraded to none") {
			warnings++
		}
	}
	require.Equal(t, 2, warnings, "each downgraded proxy is reported")

	adminSvc.mu.Lock()
	defer adminSvc.mu.Unlock()
	require.Len(t, adminSvc.createdProxies, 1)
	require.Equal(t, service.FallbackModeNone, adminSvc.createdProxies[0].FallbackMode)
	require.NotEmpty(t, adminSvc.updatedProxies)
	for _, update := range adminSvc.updatedProxies {
		require.Equal(t, service.FallbackModeNone, update.FallbackMode)
	}
}

// 账号导入里内嵌的代理走的是另一条代码路径，同样要降级旧的 direct。
func TestAccountImportDowngradesLegacyDirectFallbackMode(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	adminSvc.groups = []service.Group{{ID: 7, Name: "openai-pool", Platform: service.PlatformOpenAI, Status: service.StatusActive}}

	body, _ := json.Marshal(map[string]any{
		"data": map[string]any{
			"type":    dataType,
			"version": dataVersion,
			"proxies": []map[string]any{{
				"proxy_key": "socks5|5.6.7.8|1080|u|p", "name": "legacy", "protocol": "socks5",
				"host": "5.6.7.8", "port": 1080, "username": "u", "password": "p",
				"status": "active", "fallback_mode": "direct",
			}},
			"accounts": []map[string]any{{
				"name": "acc", "platform": service.PlatformOpenAI, "type": service.AccountTypeOAuth,
				"credentials": map[string]any{"token": "x"}, "proxy_key": "socks5|5.6.7.8|1080|u|p",
				"concurrency": 1, "priority": 50,
			}},
		},
		"group_ids": []int64{7},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "downgraded to none")

	adminSvc.mu.Lock()
	defer adminSvc.mu.Unlock()
	require.Len(t, adminSvc.createdProxies, 1)
	require.Equal(t, service.FallbackModeNone, adminSvc.createdProxies[0].FallbackMode)
}

func TestNormalizeImportedProxyFallbackMode(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		warn     bool
	}{
		{"", service.FallbackModeNone, false},
		{"none", service.FallbackModeNone, false},
		{" proxy ", service.FallbackModeProxy, false},
		{"direct", service.FallbackModeNone, true},
		{"Direct", service.FallbackModeNone, true},
		{"bogus", service.FallbackModeNone, true},
	} {
		got, warning := normalizeImportedProxyFallbackMode(tc.in)
		require.Equal(t, tc.want, got, tc.in)
		require.Equal(t, tc.warn, warning != "", tc.in)
	}
}
