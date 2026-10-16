package repository

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type perplexityProbePayloadArgument struct{}

func (perplexityProbePayloadArgument) Match(value driver.Value) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	var body map[string]any
	if json.Unmarshal([]byte(text), &body) != nil {
		return false
	}
	_, hasSnippet := body["response_text"]
	return body["preference"] == "gpt54" && body["identity_fingerprint"] == "fingerprint" && !hasSnippet
}

func TestPerplexityModelHealthWritesAreFencedAndDoNotStoreAnswers(t *testing.T) {
	for _, count := range []int64{0, 1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		repo := &accountRepository{sql: db}
		proxy := int64(8)
		mock.ExpectExec(regexp.QuoteMeta(storePerplexityModelProbeSQL)).WithArgs("gpt54:copilot", perplexityProbePayloadArgument{}, int64(5), `{"api_key":"fixture"}`, proxy, int64(12345), service.SchedulerOutboxEventAccountChanged).WillReturnResult(sqlmock.NewResult(0, count))
		result := service.PerplexityModelProbeResult{Preference: "gpt54", Mode: "copilot", Status: "verified", ObservedAtMs: 12345, IdentityFingerprint: "fingerprint", ResponseText: "do-not-persist-this-answer"}
		applied, err := repo.StorePerplexityModelProbeIfUnchanged(context.Background(), 5, map[string]any{"api_key": "fixture"}, &proxy, result)
		require.NoError(t, err)
		require.Equal(t, count == 1, applied)
		require.NoError(t, mock.ExpectationsWereMet())
	}
	require.Contains(t, storePerplexityModelProbeSQL, "a.credentials = $4::jsonb")
	require.Contains(t, storePerplexityModelProbeSQL, "a.proxy_id IS NOT DISTINCT FROM $5")
	require.Contains(t, storePerplexityModelProbeSQL, "LIMIT 128")
	require.Contains(t, storePerplexityModelProbeSQL, "INSERT INTO scheduler_outbox")
}

func TestPerplexityModelCatalogConditionalPersistence(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &accountRepository{sql: db}
	mock.ExpectExec(regexp.QuoteMeta(storePerplexityCatalogSQL)).WithArgs(sqlmock.AnyArg(), int64(5), `{"api_key":"fixture"}`, nil, int64(12345), service.SchedulerOutboxEventAccountChanged).WillReturnResult(sqlmock.NewResult(0, 1))
	applied, err := repo.StorePerplexityCatalogIfUnchanged(context.Background(), 5, map[string]any{"api_key": "fixture"}, nil, service.UpstreamModelMetadataSnapshot{Source: "perplexity", IdentityFingerprint: "fingerprint", ObservedAtMs: 12345})
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPerplexityScheduledRepositoryPreservesLegacyDefaultMode(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	repo := &scheduledTestPlanRepository{db: db}
	now := time.Now().UTC()
	columns := []string{"id", "account_id", "model_id", "test_mode", "cron_expression", "enabled", "max_results", "auto_recover", "last_run_at", "next_run_at", "created_at", "updated_at"}
	mock.ExpectQuery(`(?s)INSERT INTO scheduled_test_plans`).WithArgs(int64(5), "public", "default", "7 * * * *", true, 50, false, now).WillReturnRows(sqlmock.NewRows(columns).AddRow(9, 5, "public", "default", "7 * * * *", true, 50, false, nil, now, now, now))
	plan, err := repo.Create(context.Background(), &service.ScheduledTestPlan{AccountID: 5, ModelID: "public", CronExpression: "7 * * * *", Enabled: true, MaxResults: 50, NextRunAt: &now})
	require.NoError(t, err)
	require.Equal(t, "default", plan.TestMode)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPerplexityScheduledResultsStoreStructuredMetadataAndFenceAdvance(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	results := &scheduledTestResultRepository{db: db}
	columns := []string{"id", "plan_id", "status", "response_text", "error_message", "latency_ms", "started_at", "finished_at", "metadata", "created_at"}
	mock.ExpectQuery(`(?s)INSERT INTO scheduled_test_results`).WithArgs(int64(9), "warning", "OK", "", int64(12), now, now, `{"platform":"perplexity"}`).WillReturnRows(sqlmock.NewRows(columns).AddRow(1, 9, "warning", "OK", "", 12, now, now, []byte(`{"platform":"perplexity"}`), now))
	result, err := results.Create(context.Background(), &service.ScheduledTestResult{PlanID: 9, Status: "warning", ResponseText: "OK", LatencyMs: 12, StartedAt: now, FinishedAt: now, Metadata: map[string]any{"platform": "perplexity"}})
	require.NoError(t, err)
	require.Equal(t, "perplexity", result.Metadata["platform"])
	plans := &scheduledTestPlanRepository{db: db}
	mock.ExpectExec(`(?s)UPDATE scheduled_test_plans.*updated_at = \$2 AND enabled = true`).WithArgs(int64(9), now, now, now).WillReturnResult(sqlmock.NewResult(0, 0))
	applied, err := plans.UpdateAfterRunIfUnchanged(context.Background(), 9, now, now, now)
	require.NoError(t, err)
	require.False(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
