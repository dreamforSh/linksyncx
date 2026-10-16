//go:build unit

package repository

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUsageLogRawUsageRoundTrip(t *testing.T) {
	for _, known := range []bool{false, true} {
		name := "historical_unknown"
		if known {
			name = "observed_zero_cache_and_compensation"
		}
		t.Run(name, func(t *testing.T) {
			log := &service.UsageLog{UserID: 1, APIKeyID: 2, AccountID: 3, RequestID: name, Model: "claude-sonnet-4", CacheReadTokens: 100, CreatedAt: time.Now().UTC()}
			if known {
				input, output, zero, compensation := 100, 7, 0, 100
				reason := "sticky_session_failover"
				log.RawInputTokens, log.RawOutputTokens = &input, &output
				log.RawCacheCreationTokens, log.RawCacheReadTokens = &zero, &zero
				log.CacheBillingCompensationTokens, log.CacheBillingCompensationReason = &compensation, &reason
				log.UsageIncomplete = true
			}
			prepared := prepareUsageLogInsert(log)
			require.Len(t, prepared.args, len(usageLogInsertArgTypes))
			for i := 62; i < 67; i++ {
				value, ok := prepared.args[i].(sql.NullInt64)
				require.True(t, ok)
				require.Equal(t, known, value.Valid)
			}

			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			values := []driver.Value{int64(42)}
			for _, arg := range prepared.args {
				value, err := driver.DefaultParameterConverter.ConvertValue(arg)
				require.NoError(t, err)
				values = append(values, value)
			}
			cols := strings.Split(usageLogSelectColumns, ", ")
			mock.ExpectQuery("SELECT .* FROM usage_logs WHERE id =").WithArgs(int64(42)).
				WillReturnRows(sqlmock.NewRows(cols).AddRow(values...))
			got, err := repo.GetByID(context.Background(), 42)
			require.NoError(t, err)
			require.Equal(t, log.RawInputTokens, got.RawInputTokens)
			require.Equal(t, log.RawOutputTokens, got.RawOutputTokens)
			require.Equal(t, log.RawCacheCreationTokens, got.RawCacheCreationTokens)
			require.Equal(t, log.RawCacheReadTokens, got.RawCacheReadTokens)
			require.Equal(t, log.CacheBillingCompensationTokens, got.CacheBillingCompensationTokens)
			require.Equal(t, log.CacheBillingCompensationReason, got.CacheBillingCompensationReason)
			require.Equal(t, log.UsageIncomplete, got.UsageIncomplete)
			require.Equal(t, 100, got.CacheReadTokens, "billing details remain independently available")
			require.NoError(t, mock.ExpectationsWereMet())

			// Both generated CTE insert paths carry exactly the same nullable snapshot.
			batchSQL, batchArgs := buildUsageLogBatchInsertQuery([]string{"key"}, map[string]usageLogInsertPrepared{"key": prepared})
			bestEffortSQL, bestEffortArgs := buildUsageLogBestEffortInsertQuery([]usageLogInsertPrepared{prepared})
			require.Equal(t, prepared.args, batchArgs[1:])
			require.Equal(t, prepared.args, bestEffortArgs)
			for _, query := range []string{batchSQL, bestEffortSQL} {
				for _, field := range []string{"raw_input_tokens", "raw_output_tokens", "raw_cache_creation_tokens", "raw_cache_read_tokens", "cache_billing_compensation_tokens", "cache_billing_compensation_reason", "usage_incomplete"} {
					require.Equal(t, 3, strings.Count(query, field), "CTE/INSERT/SELECT must all carry %s", field)
				}
			}
		})
	}
}

func TestRawUsageAggregateSQLDoesNotInventHistoricalCacheHits(t *testing.T) {
	for _, alias := range []string{"", "ul"} {
		query := rawUsageAggregateSQL(alias)
		require.Contains(t, query, "raw_usage_known_requests")
		require.Contains(t, query, "raw_usage_unknown_requests")
		prefix := ""
		if alias != "" {
			prefix = alias + "."
		}
		require.Contains(t, query, "NOT "+prefix+"usage_incomplete")
		require.NotContains(t, query, "SUM(input_tokens)")
		require.NotContains(t, query, "SUM(cache_read_tokens)")
		require.NotContains(t, query, "COALESCE(raw_input_tokens, input_tokens)")
	}
}

func TestChannelMonitorRawCacheRateExcludesBillingCompensation(t *testing.T) {
	for _, tt := range []struct {
		name            string
		raw             usagestats.RawUsageStats
		wantKnown       bool
		wantRate        float64
		wantDenominator int64
	}{
		{name: "100_prompt_zero_cached_switch", raw: usagestats.RawUsageStats{RawInputTokens: 100, RawUsageKnownRequests: 1}, wantKnown: true, wantDenominator: 100},
		{name: "mixed_cached", raw: usagestats.RawUsageStats{RawInputTokens: 80, RawCacheReadTokens: 20, RawUsageKnownRequests: 1}, wantKnown: true, wantRate: 0.2, wantDenominator: 100},
		{name: "input_zero_real_cache", raw: usagestats.RawUsageStats{RawCacheReadTokens: 100, RawUsageKnownRequests: 1}, wantKnown: true, wantRate: 1, wantDenominator: 100},
		{name: "known_empty_prompt", raw: usagestats.RawUsageStats{RawUsageKnownRequests: 1}},
		{name: "historical_unknown", raw: usagestats.RawUsageStats{RawUsageUnknownRequests: 1}},
		{name: "mixed_history_is_not_complete", raw: usagestats.RawUsageStats{RawInputTokens: 100, RawUsageKnownRequests: 1, RawUsageUnknownRequests: 1}, wantDenominator: 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			acc := newMetricAccumulator()
			acc.addFact(channelMonitorV2Fact{Success: 1, CacheRead: 100, RawUsageStats: tt.raw})
			metric := acc.metric(1, true)
			require.Equal(t, int64(100), metric.CacheReadTokens, "throughput remains billable totals")
			require.Equal(t, tt.wantKnown, metric.CacheRateKnown)
			require.Equal(t, tt.wantDenominator, metric.CacheRateDenominator)
			require.Equal(t, tt.raw.RawCacheReadTokens, metric.CacheRateNumerator)
			require.InDelta(t, tt.wantRate, metric.CacheRate, 1e-12)
		})
	}
}
