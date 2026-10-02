package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type costTrendRepoCapture struct {
	service.UsageLogRepository
	granularity string
	calls       int
}

func (r *costTrendRepoCapture) GetAdminCostTrend(_ context.Context, _, _ time.Time, granularity string) ([]usagestats.CostTrendPoint, error) {
	r.granularity = granularity
	r.calls++
	return []usagestats.CostTrendPoint{{Date: "2031-04-02", Requests: 3, Cost: 2, ActualCost: 1.5, AccountCost: 0.5}}, nil
}

func newCostTrendRouter(repo *costTrendRepoCapture) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewDashboardHandler(service.NewDashboardService(repo, nil, nil, nil), nil)
	router := gin.New()
	router.GET("/admin/dashboard/cost-trend", h.GetCostTrend)
	return router
}

func TestGetCostTrendReturnsAccountCost(t *testing.T) {
	repo := &costTrendRepoCapture{}
	router := newCostTrendRouter(repo)

	// A date range no other test uses keeps the 30s snapshot cache out of the way.
	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/cost-trend?start_date=2031-04-01&end_date=2031-04-03&granularity=day", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "day", repo.granularity)
	var body struct {
		Data struct {
			Trend []usagestats.CostTrendPoint `json:"trend"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data.Trend, 1)
	require.Equal(t, 0.5, body.Data.Trend[0].AccountCost)
}

func TestGetCostTrendRejectsUnknownGranularity(t *testing.T) {
	repo := &costTrendRepoCapture{}
	router := newCostTrendRouter(repo)

	req := httptest.NewRequest(http.MethodGet, "/admin/dashboard/cost-trend?start_date=2031-05-01&end_date=2031-05-03&granularity=week", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, repo.calls)
}

// The usage trend is shared with user-facing endpoints, so the upstream
// account cost must stay out of it; admins read it from /cost-trend.
func TestTrendDataPointNeverCarriesAccountCost(t *testing.T) {
	raw, err := json.Marshal(usagestats.TrendDataPoint{Date: "2026-01-01", ActualCost: 1})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "account_cost")
}
