//go:build unit

package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestResolveEndpointColumn(t *testing.T) {
	tests := []struct {
		endpointType string
		want         string
	}{
		{"inbound", "ul.inbound_endpoint"},
		{"upstream", "ul.upstream_endpoint"},
		{"path", "ul.inbound_endpoint || ' -> ' || ul.upstream_endpoint"},
		{"", "ul.inbound_endpoint"},        // default
		{"unknown", "ul.inbound_endpoint"}, // fallback
	}

	for _, tc := range tests {
		t.Run(tc.endpointType, func(t *testing.T) {
			got := resolveEndpointColumn(tc.endpointType)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestResolveModelDimensionExpression(t *testing.T) {
	tests := []struct {
		modelType string
		want      string
	}{
		{usagestats.ModelSourceRequested, "COALESCE(NULLIF(TRIM(requested_model), ''), model)"},
		{usagestats.ModelSourceUpstream, "COALESCE(NULLIF(TRIM(upstream_model), ''), model)"},
		{usagestats.ModelSourceMapping, "(COALESCE(NULLIF(TRIM(requested_model), ''), model) || ' -> ' || COALESCE(NULLIF(TRIM(upstream_model), ''), model))"},
		{"", "COALESCE(NULLIF(TRIM(requested_model), ''), model)"},
		{"invalid", "COALESCE(NULLIF(TRIM(requested_model), ''), model)"},
	}

	for _, tc := range tests {
		t.Run(tc.modelType, func(t *testing.T) {
			got := resolveModelDimensionExpression(tc.modelType)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestGetUserBreakdownStatsRequestTypeIncludesLegacyFallback(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType := int16(service.RequestTypeStream)

	legacyFilter := `(ul.request_type = $3 OR (ul.request_type = 0 AND ul.stream = TRUE AND ul.openai_ws_mode = FALSE))`
	mock.ExpectQuery(regexp.QuoteMeta(legacyFilter)).
		WithArgs(start, end, requestType).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "email", "requests", "input_tokens", "output_tokens",
			"cache_tokens", "total_tokens", "cost", "actual_cost", "account_cost",
			"cache_creation_tokens", "cache_read_tokens",
		}))

	rows, err := repo.GetUserBreakdownStats(context.Background(), start, end, usagestats.UserBreakdownDimension{
		RequestType: &requestType,
	}, 0)

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserBreakdownStatsFiltersNativeCompactionV2(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	nativeCompactionV2 := true

	mock.ExpectQuery(regexp.QuoteMeta("AND ul.native_compaction_v2 = $3")).
		WithArgs(start, end, true).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "email", "requests", "input_tokens", "output_tokens",
			"cache_tokens", "total_tokens", "cost", "actual_cost", "account_cost",
			"cache_creation_tokens", "cache_read_tokens",
		}))

	rows, err := repo.GetUserBreakdownStats(context.Background(), start, end, usagestats.UserBreakdownDimension{
		NativeCompactionV2: &nativeCompactionV2,
	}, 0)

	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetUserBreakdownStatsScansCacheSplit(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(SUM(ul.cache_read_tokens), 0) as cache_read_tokens")).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{
			"user_id", "email", "requests", "input_tokens", "output_tokens",
			"cache_tokens", "total_tokens", "cost", "actual_cost", "account_cost",
			"cache_creation_tokens", "cache_read_tokens",
		}).AddRow(int64(7), "a@example.test", int64(3), int64(100), int64(50), int64(900), int64(1050), 1.5, 1.2, 0.4, int64(200), int64(700)))

	rows, err := repo.GetUserBreakdownStats(context.Background(), start, end, usagestats.UserBreakdownDimension{SortBy: "account_cost"}, 20)

	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(200), rows[0].CacheCreationTokens)
	require.Equal(t, int64(700), rows[0].CacheReadTokens)
	require.Equal(t, 0.4, rows[0].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAdminCostTrendReadsAggregates(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	cols := []string{"date", "requests", "cost", "actual_cost", "account_cost"}

	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_dashboard_hourly")).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(cols).AddRow("2026-07-01 10:00", int64(4), 2.0, 1.8, 0.6))

	trend, err := repo.GetAdminCostTrend(context.Background(), start, end, "hour")

	require.NoError(t, err)
	require.Equal(t, []usagestats.CostTrendPoint{{Date: "2026-07-01 10:00", Requests: 4, Cost: 2.0, ActualCost: 1.8, AccountCost: 0.6}}, trend)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetAdminCostTrendFallsBackToUsageLogs(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(48 * time.Hour)
	cols := []string{"date", "requests", "cost", "actual_cost", "account_cost"}

	// Unknown granularities read daily buckets; empty aggregates fall back to raw logs.
	mock.ExpectQuery(regexp.QuoteMeta("FROM usage_dashboard_daily")).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(cols))
	mock.ExpectQuery(regexp.QuoteMeta("COALESCE(SUM(COALESCE(account_stats_cost, total_cost) * COALESCE(account_rate_multiplier, 1)), 0) as account_cost")).
		WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows(cols).AddRow("2026-07-01", int64(9), 3.0, 2.5, 1.0))

	trend, err := repo.GetAdminCostTrend(context.Background(), start, end, "week")

	require.NoError(t, err)
	require.Len(t, trend, 1)
	require.Equal(t, 1.0, trend[0].AccountCost)
	require.NoError(t, mock.ExpectationsWereMet())
}
