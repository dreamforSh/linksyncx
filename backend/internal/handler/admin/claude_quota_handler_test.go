//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type claudeQuotaServiceStub struct {
	calls        []string
	programs     []string
	resetResult  *service.ClaudeQuotaResetResult
	resetErr     error
	refresh      *service.ClaudeQuotaRefreshResult
	refreshErr   error
	refreshCalls int
}

func (s *claudeQuotaServiceStub) RefreshQuota(context.Context, int64) (*service.ClaudeQuotaRefreshResult, error) {
	s.calls = append(s.calls, "refresh")
	s.refreshCalls++
	return s.refresh, s.refreshErr
}

func (s *claudeQuotaServiceStub) ResetCredit(_ context.Context, _ int64, program string) (*service.ClaudeQuotaResetResult, error) {
	s.calls = append(s.calls, "reset")
	s.programs = append(s.programs, program)
	return s.resetResult, s.resetErr
}

type claudeQuotaRecovererStub struct {
	calls *[]string
}

func (s claudeQuotaRecovererStub) RecoverAccountState(context.Context, int64, service.AccountRecoveryOptions) (*service.SuccessfulTestRecoveryResult, error) {
	*s.calls = append(*s.calls, "recover")
	return &service.SuccessfulTestRecoveryResult{}, nil
}

type claudeQuotaAdminStub struct {
	service.AdminService
	account *service.Account
}

func (s *claudeQuotaAdminStub) GetAccount(context.Context, int64) (*service.Account, error) {
	if s.account == nil {
		return nil, errors.New("not found")
	}
	return s.account, nil
}

type claudeQuotaResetEnvelope struct {
	Code    int                      `json:"code"`
	Reason  string                   `json:"reason"`
	Message string                   `json:"message"`
	Data    claudeQuotaResetResponse `json:"data"`
}

func performClaudeQuotaRequest(t *testing.T, handler gin.HandlerFunc, path, body string) (int, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST(path, handler)
	target := strings.Replace(path, ":id", "41", 1)
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(http.MethodPost, target, nil)
	} else {
		request = httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func newClaudeQuotaTestHandler(stub *claudeQuotaServiceStub, calls *[]string) *ClaudeQuotaHandler {
	handler := &ClaudeQuotaHandler{
		quotaService: stub,
		adminService: &claudeQuotaAdminStub{account: &service.Account{ID: 41, Name: "claude-max", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth}},
	}
	if calls != nil {
		handler.rateLimitService = claudeQuotaRecovererStub{calls: calls}
	}
	return handler
}

const claudeResetPath = "/api/v1/admin/anthropic/accounts/:id/reset-quota"

func TestClaudeQuotaResetRunsPostProcessWhenConsumed(t *testing.T) {
	stub := &claudeQuotaServiceStub{
		resetResult: &service.ClaudeQuotaResetResult{Program: service.ClaudeResetProgramCedarEmber, Result: service.ClaudeResetResultReset, Cleared: []string{"five_hour"}},
		refresh: &service.ClaudeQuotaRefreshResult{
			Snapshot:       &service.ClaudeResetSnapshot{AvailableCount: 0},
			Usage:          &service.UsageInfo{FiveHour: &service.UsageProgress{Utilization: 0}},
			CachePersisted: true,
		},
	}
	stub.calls = nil
	handler := newClaudeQuotaTestHandler(stub, &stub.calls)

	status, body := performClaudeQuotaRequest(t, handler.ResetQuota, claudeResetPath, `{"program":"cedar_ember"}`)
	require.Equal(t, http.StatusOK, status)
	var envelope claudeQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, []string{"reset", "recover", "refresh"}, stub.calls)
	require.Equal(t, []string{service.ClaudeResetProgramCedarEmber}, stub.programs)
	require.Equal(t, service.ClaudeResetResultReset, envelope.Data.Result)
	require.True(t, envelope.Data.AccountStateRecovered)
	require.True(t, envelope.Data.CacheRefreshed)
	require.Empty(t, envelope.Data.WarningCode)
	require.NotNil(t, envelope.Data.Snapshot)
	require.NotNil(t, envelope.Data.Usage)
	require.NotNil(t, envelope.Data.Account)
	require.Equal(t, int64(41), envelope.Data.Account.ID)
}

func TestClaudeQuotaResetSkipsPostProcessWhenNotConsumed(t *testing.T) {
	stub := &claudeQuotaServiceStub{
		resetResult: &service.ClaudeQuotaResetResult{Program: service.ClaudeResetProgramCedarEmber, Result: service.ClaudeResetResultNotLimited},
	}
	var calls []string
	handler := newClaudeQuotaTestHandler(stub, &calls)

	status, body := performClaudeQuotaRequest(t, handler.ResetQuota, claudeResetPath, "")
	require.Equal(t, http.StatusOK, status)
	var envelope claudeQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, service.ClaudeResetResultNotLimited, envelope.Data.Result)
	require.Equal(t, []string{""}, stub.programs, "an empty body lets the service pick the reset")
	require.Empty(t, calls, "nothing to recover when no reset was spent")
	require.Zero(t, stub.refreshCalls)
	require.Nil(t, envelope.Data.Account)
}

func TestClaudeQuotaResetPropagatesServiceErrors(t *testing.T) {
	stub := &claudeQuotaServiceStub{resetErr: service.ErrClaudeResetRequiresLimit}
	handler := newClaudeQuotaTestHandler(stub, nil)

	status, body := performClaudeQuotaRequest(t, handler.ResetQuota, claudeResetPath, "")
	require.Equal(t, http.StatusConflict, status)
	var envelope claudeQuotaResetEnvelope
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, "CLAUDE_RESET_REQUIRES_LIMIT", envelope.Reason)

	status, _ = performClaudeQuotaRequest(t, handler.ResetQuota, claudeResetPath, `{"program":`)
	require.Equal(t, http.StatusBadRequest, status)
}

func TestClaudeQuotaRefreshReturnsSnapshotAndAccount(t *testing.T) {
	stub := &claudeQuotaServiceStub{refresh: &service.ClaudeQuotaRefreshResult{
		Snapshot:       &service.ClaudeResetSnapshot{AvailableCount: 2},
		Subscription:   &service.ClaudeSubscriptionInfo{PlanType: service.ClaudePlanMax20x},
		CachePersisted: true,
	}}
	handler := newClaudeQuotaTestHandler(stub, nil)

	status, body := performClaudeQuotaRequest(t, handler.RefreshQuota, "/api/v1/admin/anthropic/accounts/:id/quota/refresh", "")
	require.Equal(t, http.StatusOK, status)
	var envelope struct {
		Data claudeQuotaRefreshResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, 2, envelope.Data.Snapshot.AvailableCount)
	require.Equal(t, service.ClaudePlanMax20x, envelope.Data.Subscription.PlanType)
	require.True(t, envelope.Data.CachePersisted)
	require.NotNil(t, envelope.Data.Account)

	disabled := &ClaudeQuotaHandler{}
	status, _ = performClaudeQuotaRequest(t, disabled.RefreshQuota, "/api/v1/admin/anthropic/accounts/:id/quota/refresh", "")
	require.Equal(t, http.StatusBadRequest, status)
}

func TestNewClaudeQuotaHandlerAvoidsTypedNilDependencies(t *testing.T) {
	handler := NewClaudeQuotaHandler(nil, nil, nil)
	require.Nil(t, handler.quotaService)
	require.Nil(t, handler.rateLimitService)
}
