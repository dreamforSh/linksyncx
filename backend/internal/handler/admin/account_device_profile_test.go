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

func newDeviceProfileRouter(adminSvc *stubAdminService, identity *service.IdentityService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetIdentityService(identity)
	router := gin.New()
	router.GET("/api/v1/admin/accounts/device-profile/random", handler.RandomDeviceProfile)
	router.GET("/api/v1/admin/accounts/device-profile/random-set", handler.RandomDeviceProfiles)
	router.POST("/api/v1/admin/accounts/device-profile/batch", handler.BatchDeviceProfile)
	return router
}

func serveDeviceProfile(router *gin.Engine, req *http.Request) (int, map[string]any) {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestRandomDeviceProfileHandler(t *testing.T) {
	router := newDeviceProfileRouter(newStubAdminService(), nil)

	code, body := serveDeviceProfile(router, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/device-profile/random?platform=anthropic", nil))
	require.Equal(t, http.StatusOK, code)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	require.NotEmpty(t, data["device_os"])
	require.NotEmpty(t, data["device_arch"])
	require.Equal(t, "", data["user_agent"])

	code, body = serveDeviceProfile(router, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/device-profile/random?platform=openai", nil))
	require.Equal(t, http.StatusOK, code)
	data, ok = body["data"].(map[string]any)
	require.True(t, ok)
	ua, _ := data["user_agent"].(string)
	require.True(t, strings.HasPrefix(ua, "codex-tui/"), ua)

	code, body = serveDeviceProfile(router, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/device-profile/random?platform=gemini", nil))
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, "INVALID_DEVICE_PROFILE", body["reason"])
}

func TestRandomDeviceProfilesHandler(t *testing.T) {
	router := newDeviceProfileRouter(newStubAdminService(), nil)
	get := func(query string) (int, map[string]any) {
		return serveDeviceProfile(router, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/device-profile/random-set?"+query, nil))
	}

	code, body := get("platform=openai&count=5")
	require.Equal(t, http.StatusOK, code)
	data, ok := body["data"].(map[string]any)
	require.True(t, ok)
	profiles, ok := data["profiles"].([]any)
	require.True(t, ok)
	require.Len(t, profiles, 5)
	seen := map[string]struct{}{}
	for _, raw := range profiles {
		profile, ok := raw.(map[string]any)
		require.True(t, ok)
		ua, _ := profile["user_agent"].(string)
		require.True(t, strings.HasPrefix(ua, "codex-tui/"), ua)
		seen[ua] = struct{}{}
	}
	require.Len(t, seen, 5)

	code, body = get("platform=anthropic")
	require.Equal(t, http.StatusOK, code)
	data, ok = body["data"].(map[string]any)
	require.True(t, ok)
	profiles, ok = data["profiles"].([]any)
	require.True(t, ok)
	require.Len(t, profiles, 1)

	for _, query := range []string{"platform=openai&count=0", "platform=openai&count=1001", "platform=openai&count=x", "platform=gemini&count=2"} {
		code, _ = get(query)
		require.Equal(t, http.StatusBadRequest, code, query)
	}
}

func TestBatchDeviceProfileHandler(t *testing.T) {
	postBatch := func(router *gin.Engine, payload string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/device-profile/batch", bytes.NewBufferString(payload))
		req.Header.Set("Content-Type", "application/json")
		return serveDeviceProfile(router, req)
	}

	t.Run("rejects unknown actions", func(t *testing.T) {
		code, _ := postBatch(newDeviceProfileRouter(newStubAdminService(), nil), `{"account_ids":[1],"action":"rotate"}`)
		require.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("resets identities of updated Claude accounts on request", func(t *testing.T) {
		adminSvc := newStubAdminService()
		adminSvc.getAccountResult = &service.Account{ID: 7, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
			Extra: map[string]any{"account_uuid": "owner"}}
		adminSvc.deviceProfileBatchResult = &service.DeviceProfileBatchResult{
			Updated: 2,
			Results: []service.DeviceProfileBatchItem{
				{AccountID: 7, Platform: service.PlatformAnthropic, Status: service.DeviceProfileBatchStatusUpdated},
				{AccountID: 8, Platform: service.PlatformOpenAI, Status: service.DeviceProfileBatchStatusUpdated},
			},
		}
		store := &resetIdentityStore{rows: map[int64]service.ClientIdentityRecord{
			7: {AccountID: 7, Platform: service.PlatformAnthropic, DeviceID: strings.Repeat("ab", 32), OwnerRef: "owner"},
		}}
		identity := service.ProvideIdentityService(&resetIdentityCache{fingerprints: map[int64]*service.Fingerprint{}}, store)

		code, body := postBatch(newDeviceProfileRouter(adminSvc, identity),
			`{"account_ids":[7,8],"action":"randomize","reset_client_identity":true}`)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, &service.DeviceProfileBatchInput{AccountIDs: []int64{7, 8}, Action: "randomize"}, adminSvc.lastDeviceProfileBatchInput)
		require.NotEqual(t, strings.Repeat("ab", 32), store.rows[7].DeviceID)

		data, ok := body["data"].(map[string]any)
		require.True(t, ok)
		results, ok := data["results"].([]any)
		require.True(t, ok)
		require.Len(t, results, 2)
		claudeItem, ok := results[0].(map[string]any)
		require.True(t, ok)
		codexItem, ok := results[1].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, claudeItem["identity_reset"])
		require.Nil(t, codexItem["identity_reset"])
	})

	t.Run("keeps identities unless asked", func(t *testing.T) {
		adminSvc := newStubAdminService()
		adminSvc.deviceProfileBatchResult = &service.DeviceProfileBatchResult{
			Updated: 1,
			Results: []service.DeviceProfileBatchItem{{AccountID: 7, Platform: service.PlatformAnthropic, Status: service.DeviceProfileBatchStatusUpdated}},
		}
		store := &resetIdentityStore{rows: map[int64]service.ClientIdentityRecord{
			7: {AccountID: 7, Platform: service.PlatformAnthropic, DeviceID: strings.Repeat("ab", 32), OwnerRef: "owner"},
		}}
		identity := service.ProvideIdentityService(&resetIdentityCache{fingerprints: map[int64]*service.Fingerprint{}}, store)
		code, _ := postBatch(newDeviceProfileRouter(adminSvc, identity), `{"account_ids":[7],"action":"clear"}`)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, strings.Repeat("ab", 32), store.rows[7].DeviceID)
	})
}
