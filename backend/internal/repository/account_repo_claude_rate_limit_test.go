package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newClaudeRateLimitRepoForTest(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), mock
}

// 窗口时间变化或写入调度相关字段时，与 UpdateSessionWindow / UpdateExtra 一样在事务里入队调度 outbox。
func TestApplyClaudeRateLimitPatchWritesWindowAndExtraWithFence(t *testing.T) {
	repo, mock := newClaudeRateLimitRepoForTest(t)
	start, end := time.Unix(1_800_000_000, 0), time.Unix(1_800_018_000, 0)
	extra := map[string]any{
		"claude_rate_limit":            &service.ClaudeRateLimitSnapshot{AppliedAtMs: 1_800_000_000_123, Status: "allowed"},
		"passive_usage_7d_utilization": 0.5,
	}
	payload, err := json.Marshal(extra)
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(claudeRateLimitPatchSQL)).
		WithArgs(string(payload), "allowed", start, end, int64(27), int64(1_800_000_000_123)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO scheduler_outbox")).
		WithArgs(service.SchedulerOutboxEventAccountChanged, int64(27), nil, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	applied, err := repo.ApplyClaudeRateLimitPatch(context.Background(), 27, service.ClaudeRateLimitPatch{
		AppliedAtMs: 1_800_000_000_123, SessionWindowStatus: "allowed", SessionWindowStart: &start, SessionWindowEnd: &end, Extra: extra,
	})
	require.NoError(t, err)
	require.True(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 库中已是更晚收到的快照：UPDATE 不命中，返回未写入，也不入队 outbox。快照本身不触发 bucket 重建。
func TestApplyClaudeRateLimitPatchSkipsOlderSnapshot(t *testing.T) {
	repo, mock := newClaudeRateLimitRepoForTest(t)
	extra := map[string]any{"claude_rate_limit": &service.ClaudeRateLimitSnapshot{AppliedAtMs: 5}}
	payload, err := json.Marshal(extra)
	require.NoError(t, err)

	mock.ExpectExec(regexp.QuoteMeta(claudeRateLimitPatchSQL)).
		WithArgs(string(payload), "", nil, nil, int64(27), int64(5)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	applied, err := repo.ApplyClaudeRateLimitPatch(context.Background(), 27, service.ClaudeRateLimitPatch{AppliedAtMs: 5, Extra: extra})
	require.NoError(t, err)
	require.False(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())

	sql := normalizeSQLWhitespace(claudeRateLimitPatchSQL)
	require.Contains(t, sql, "COALESCE((extra -> 'claude_rate_limit' ->> 'applied_at_ms')::bigint, 0) < $6")
	require.Contains(t, sql, "session_window_status = CASE WHEN $2::text = '' THEN session_window_status ELSE $2::text END")
	require.Contains(t, sql, "session_window_start = COALESCE($3::timestamptz, session_window_start)")
}
