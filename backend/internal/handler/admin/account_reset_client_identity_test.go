package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// resetIdentityCache / resetIdentityStore 是 IdentityService 依赖的最小内存实现。
type resetIdentityCache struct {
	fingerprints map[int64]*service.Fingerprint
}

func (c *resetIdentityCache) GetFingerprint(_ context.Context, id int64) (*service.Fingerprint, error) {
	return c.fingerprints[id], nil
}
func (c *resetIdentityCache) SetFingerprint(_ context.Context, id int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	c.fingerprints[id] = fp
	return fp, nil
}
func (c *resetIdentityCache) CreateFingerprint(_ context.Context, id int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	c.fingerprints[id] = fp
	return fp, nil
}
func (c *resetIdentityCache) GetOrCreateMaskedSessionID(_ context.Context, _ int64, candidate string) (string, error) {
	return candidate, nil
}
func (c *resetIdentityCache) GetOrCreateAmbientSessionID(_ context.Context, _ int64, candidate string) (string, error) {
	return candidate, nil
}
func (c *resetIdentityCache) SetLastActiveSessionID(context.Context, int64, string) error { return nil }
func (c *resetIdentityCache) GetLastActiveSessionID(context.Context, int64) (string, error) {
	return "", nil
}
func (c *resetIdentityCache) ReplaceFingerprint(_ context.Context, id int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	c.fingerprints[id] = fp
	return fp, nil
}
func (c *resetIdentityCache) OverwriteFingerprint(_ context.Context, id int64, fp *service.Fingerprint) error {
	c.fingerprints[id] = fp
	return nil
}
func (c *resetIdentityCache) DeleteAccountSessions(context.Context, int64) error { return nil }
func (c *resetIdentityCache) GetClaudeSessionMigration(context.Context, int64, string, time.Duration) (*service.ClaudeSessionMigration, error) {
	return nil, nil
}
func (c *resetIdentityCache) SetClaudeSessionMigration(context.Context, int64, string, service.ClaudeSessionMigration, time.Duration) error {
	return nil
}
func (c *resetIdentityCache) DeleteClaudeSessionMigration(context.Context, int64, string) error {
	return nil
}

type resetIdentityStore struct {
	rows map[int64]service.ClientIdentityRecord
}

func (s *resetIdentityStore) get(id int64) *service.ClientIdentityRecord {
	if rec, ok := s.rows[id]; ok {
		return &rec
	}
	return nil
}
func (s *resetIdentityStore) Get(_ context.Context, id int64) (*service.ClientIdentityRecord, error) {
	return s.get(id), nil
}
func (s *resetIdentityStore) Insert(_ context.Context, rec *service.ClientIdentityRecord) (*service.ClientIdentityRecord, error) {
	if _, ok := s.rows[rec.AccountID]; !ok {
		s.rows[rec.AccountID] = *rec
	}
	return s.get(rec.AccountID), nil
}
func (s *resetIdentityStore) UpdateHeaders(_ context.Context, id, _ int64, _, _, _ string, _ service.ClientIdentityHeaders) (*service.ClientIdentityRecord, error) {
	return s.get(id), nil
}
func (s *resetIdentityStore) Rotate(_ context.Context, expectedEpoch int64, next *service.ClientIdentityRecord) (*service.ClientIdentityRecord, error) {
	rec := *next
	rec.IdentityEpoch = expectedEpoch + 1
	s.rows[next.AccountID] = rec
	return s.get(next.AccountID), nil
}

func newResetClientIdentityRouter(t *testing.T, account *service.Account, identity *service.IdentityService) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	adminSvc.getAccountResult = account
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetIdentityService(identity)
	router := gin.New()
	router.POST("/api/v1/admin/accounts/:id/reset-client-identity", handler.ResetClientIdentity)
	return router
}

func postResetClientIdentity(router *gin.Engine) (int, map[string]any) {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/7/reset-client-identity", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestResetClientIdentityHandler(t *testing.T) {
	claudeAccount := &service.Account{ID: 7, Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Extra: map[string]any{"account_uuid": "owner"}}

	t.Run("rotates the identity", func(t *testing.T) {
		store := &resetIdentityStore{rows: map[int64]service.ClientIdentityRecord{
			7: {AccountID: 7, Platform: service.PlatformAnthropic, DeviceID: strings.Repeat("ab", 32), OwnerRef: "owner"},
		}}
		identity := service.ProvideIdentityService(&resetIdentityCache{fingerprints: map[int64]*service.Fingerprint{}}, store)
		code, body := postResetClientIdentity(newResetClientIdentityRouter(t, claudeAccount, identity))
		require.Equal(t, http.StatusOK, code)
		data, ok := body["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, data["reset"])
		require.Equal(t, float64(1), data["identity_epoch"])
		prefix, ok := data["device_id_prefix"].(string)
		require.True(t, ok)
		require.Len(t, prefix, 8)
		require.True(t, strings.HasPrefix(store.rows[7].DeviceID, prefix))
		require.NotEqual(t, strings.Repeat("ab", 32), store.rows[7].DeviceID)
	})

	t.Run("no identity yet", func(t *testing.T) {
		identity := service.ProvideIdentityService(&resetIdentityCache{fingerprints: map[int64]*service.Fingerprint{}}, &resetIdentityStore{rows: map[int64]service.ClientIdentityRecord{}})
		code, body := postResetClientIdentity(newResetClientIdentityRouter(t, claudeAccount, identity))
		require.Equal(t, http.StatusOK, code)
		data, ok := body["data"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, false, data["reset"])
	})

	t.Run("only Claude OAuth and setup-token accounts", func(t *testing.T) {
		openai := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
		identity := service.ProvideIdentityService(&resetIdentityCache{fingerprints: map[int64]*service.Fingerprint{}}, &resetIdentityStore{rows: map[int64]service.ClientIdentityRecord{}})
		code, body := postResetClientIdentity(newResetClientIdentityRouter(t, openai, identity))
		require.Equal(t, http.StatusBadRequest, code)
		require.Equal(t, "CLIENT_IDENTITY_UNSUPPORTED", body["reason"])
	})

	t.Run("identity service not configured", func(t *testing.T) {
		code, _ := postResetClientIdentity(newResetClientIdentityRouter(t, claudeAccount, nil))
		require.Equal(t, http.StatusServiceUnavailable, code)
	})
}
