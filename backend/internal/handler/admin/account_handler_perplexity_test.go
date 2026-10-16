//go:build unit

package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func perplexityModelsAdminRouter(adminSvc service.AdminService, upstream service.HTTPUpstream) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	tests := service.NewAccountTestService(nil, nil, nil, nil, nil, upstream,
		&config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}, nil)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, tests, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts/:id/models", handler.GetAvailableModels)
	router.POST("/api/v1/admin/accounts/models/sync-upstream-preview", handler.SyncUpstreamModelsPreview)
	return router
}

func perplexityModelsHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestAccountHandlerGetAvailableModels_PerplexityDiscoversAndProjectsMapping(t *testing.T) {
	adminSvc := &availableModelsAdminService{
		stubAdminService: newStubAdminService(),
		account: service.Account{ID: 81, Platform: service.PlatformPerplexity, Type: service.AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "fake-cookie", "csrf_token": "fake-csrf", "base_url": "https://web.example", "model_mapping": map[string]any{
				"configured-alias": "future_model", "missing-alias": "missing_model",
			}},
		},
	}
	upstream := &syncUpstreamHTTPUpstream{resp: perplexityModelsHTTPResponse(http.StatusOK,
		`{"models":[{"model":"future_model","label":"Future Label","subscription_tier":"max","capabilities":{"image":true}},{"model":"turbo","label":"Auto"}]}`)}
	router := perplexityModelsAdminRouter(adminSvc, upstream)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/81/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var response struct {
		Data []service.PerplexityAccountModel `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.Len(t, response.Data, 1)
	require.Equal(t, "configured-alias", response.Data[0].ID)
	require.Equal(t, "Future Label", response.Data[0].DisplayName)
	require.Equal(t, "max", response.Data[0].SubscriptionTier)
	require.JSONEq(t, `{"image":true}`, string(response.Data[0].Capabilities))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, "https://web.example/rest/models/config/v2", upstream.requests[0].URL.String())
	require.Contains(t, upstream.requests[0].Header.Get("Cookie"), "fake-csrf")
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"))
}

func TestAccountHandlerGetAvailableModels_PerplexityDefaultsNeverFallThroughToClaude(t *testing.T) {
	for _, noSender := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream failure", true: "service unavailable"}[noSender], func(t *testing.T) {
			adminSvc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
				ID: 82, Platform: service.PlatformPerplexity, Type: service.AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "fake-cookie"},
			}}
			upstream := &syncUpstreamHTTPUpstream{err: errors.New("fake transport failure")}
			router := perplexityModelsAdminRouter(adminSvc, upstream)
			if noSender {
				router = setupAvailableModelsRouter(adminSvc)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/82/models", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			var response struct {
				Data []service.PerplexityAccountModel `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			ids := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				ids = append(ids, model.ID)
				require.Equal(t, "perplexity", model.OwnedBy)
			}
			require.Equal(t, perplexity.DefaultModelIDs(), ids)
			require.NotContains(t, rec.Body.String(), "claude")
		})
	}
}

func TestAccountHandlerGetAvailableModels_PerplexityMissingAssignedProxyDoesNotSend(t *testing.T) {
	proxyID := int64(91)
	adminSvc := &availableModelsAdminService{stubAdminService: newStubAdminService(), account: service.Account{
		ID: 83, Platform: service.PlatformPerplexity, Type: service.AccountTypeAPIKey, ProxyID: &proxyID,
		Credentials: map[string]any{"api_key": "fake-cookie"},
	}}
	upstream := &syncUpstreamHTTPUpstream{err: errors.New("must not be called")}
	router := perplexityModelsAdminRouter(adminSvc, upstream)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/83/models", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Empty(t, upstream.requests)
	require.Contains(t, rec.Body.String(), "sonar")
}

func TestAccountHandlerSyncUpstreamModelsPreview_PerplexityUsesCookieCSRFBaseAndProxy(t *testing.T) {
	upstream := &syncUpstreamHTTPUpstream{resp: perplexityModelsHTTPResponse(http.StatusOK,
		`{"models":[{"model":"future_model","label":"Future Label","subscription_tier":"max"}]}`)}
	adminSvc := &previewProxyAdminService{stubAdminService: newStubAdminService(), proxy: &service.Proxy{
		ID: 9, Protocol: "socks5", Host: "127.0.0.1", Port: 7891, Status: service.StatusActive,
	}}
	router := perplexityModelsAdminRouter(adminSvc, upstream)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview",
		strings.NewReader(`{"platform":"perplexity","type":"apikey","api_key":"fake-session","csrf_token":"fake-csrf|fake-hash","base_url":"https://preview.example","proxy_id":9}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, upstream.requests, 1, "metadata must not trigger a public API registry request")
	require.Equal(t, "https://preview.example/rest/models/config/v2", upstream.requests[0].URL.String())
	require.Equal(t, "__Secure-next-auth.session-token=fake-session; next-auth.csrf-token=fake-csrf|fake-hash", upstream.requests[0].Header.Get("Cookie"))
	require.Empty(t, upstream.requests[0].Header.Get("Authorization"), "neither session nor CSRF is a Bearer token")
	require.Equal(t, "socks5://127.0.0.1:7891", upstream.proxyURLs[0])
	require.Contains(t, rec.Body.String(), "Future Label")
	require.Contains(t, rec.Body.String(), "subscription_tier")
	require.NotContains(t, rec.Body.String(), "fake-session")
}

func TestAccountHandlerSyncUpstreamModelsPreview_PerplexityMissingProxyNeverSends(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		proxy *service.Proxy
		err   error
	}{
		{name: "lookup failed", id: "9", err: errors.New("not found")},
		{name: "lookup nil", id: "9"},
		{name: "zero id", id: "0"},
		{name: "negative id", id: "-1"},
		{name: "mismatched relation", id: "9", proxy: &service.Proxy{ID: 10, Protocol: "socks5", Host: "localhost", Port: 7891}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &syncUpstreamHTTPUpstream{err: errors.New("must not send")}
			adminSvc := &previewProxyAdminService{stubAdminService: newStubAdminService(), proxy: tc.proxy, err: tc.err}
			router := perplexityModelsAdminRouter(adminSvc, upstream)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/models/sync-upstream-preview",
				strings.NewReader(`{"platform":"perplexity","type":"apikey","api_key":"fake-session","csrf_token":"fake-csrf","proxy_id":`+tc.id+`}`))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Empty(t, upstream.requests)
			require.NotContains(t, rec.Body.String(), "fake-session")
		})
	}
}
