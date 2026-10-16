package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestPerplexityCookiePersistenceIsFencedAndAtomicWithOutbox(t *testing.T) {
	for _, affected := range []int64{0, 1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		repo := &accountRepository{sql: db}
		proxyID := int64(4)
		mock.ExpectExec(regexp.QuoteMeta(updatePerplexityCookiesSQL)).
			WithArgs(`{"api_key":"replacement","csrf_token":"new-csrf"}`, int64(7), `{"api_key":"previous","model_mapping":{"public":"gpt54"}}`, proxyID, service.SchedulerOutboxEventAccountChanged).
			WillReturnResult(sqlmock.NewResult(0, affected))
		applied, err := repo.UpdatePerplexityCookiesIfUnchanged(context.Background(), 7,
			map[string]any{"api_key": "previous", "model_mapping": map[string]any{"public": "gpt54"}}, &proxyID,
			map[string]string{"api_key": "replacement", "csrf_token": "new-csrf"})
		require.NoError(t, err)
		require.Equal(t, affected == 1, applied)
		require.NoError(t, mock.ExpectationsWereMet())
	}
	require.Contains(t, updatePerplexityCookiesSQL, "credentials = a.credentials || $1::jsonb")
	require.Contains(t, updatePerplexityCookiesSQL, "a.credentials = $3::jsonb")
	require.Contains(t, updatePerplexityCookiesSQL, "a.proxy_id IS NOT DISTINCT FROM $4")
	require.Contains(t, updatePerplexityCookiesSQL, "INSERT INTO scheduler_outbox")
}

func TestPerplexityCookiePersistenceRejectsNonCookieFields(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &accountRepository{sql: db}
	for _, patch := range []map[string]string{{"model_mapping": "overwrite"}, {"api_key": ""}, {"Authorization": "secret"}} {
		applied, err := repo.UpdatePerplexityCookiesIfUnchanged(context.Background(), 7, nil, nil, patch)
		require.Error(t, err)
		require.False(t, applied)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPerplexityProfilePersistenceFencesHealthAndObservationOrder(t *testing.T) {
	for _, affected := range []int64{0, 1} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() { _ = db.Close() })
		repo := &accountRepository{sql: db}
		valid := true
		mock.ExpectExec(regexp.QuoteMeta(applyPerplexityProfileSQL)).
			WithArgs(`{"identity_fingerprint":"fingerprint","observed_at_ms":12345}`, true, service.PerplexityInvalidSessionMessage,
				int64(7), `{"api_key":"previous"}`, nil, int64(12345), service.SchedulerOutboxEventAccountChanged).
			WillReturnResult(sqlmock.NewResult(0, affected))
		applied, err := repo.ApplyPerplexityProfileIfUnchanged(context.Background(), 7, map[string]any{"api_key": "previous"}, nil, service.PerplexityProfileUpdate{
			Snapshot: map[string]any{"identity_fingerprint": "fingerprint"}, SessionValid: &valid, ObservedAtMs: 12345,
		})
		require.NoError(t, err)
		require.Equal(t, affected == 1, applied)
		require.NoError(t, mock.ExpectationsWereMet())
	}
	require.Contains(t, applyPerplexityProfileSQL, "a.credentials = $5::jsonb")
	require.Contains(t, applyPerplexityProfileSQL, "a.proxy_id IS NOT DISTINCT FROM $6")
	require.Contains(t, applyPerplexityProfileSQL, "a.error_message = $3")
	require.NotContains(t, applyPerplexityProfileSQL, "schedulable =")
	require.Contains(t, applyPerplexityProfileSQL, "ELSE 0 END < $7")
	require.Contains(t, applyPerplexityProfileSQL, "INSERT INTO scheduler_outbox")
}

func TestPerplexityStatePersistenceDoesNotReportOutboxFailureAsApplied(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &accountRepository{sql: db}
	mock.ExpectExec(regexp.QuoteMeta(updatePerplexityCookiesSQL)).WillReturnError(errors.New("outbox failed"))
	applied, err := repo.UpdatePerplexityCookiesIfUnchanged(context.Background(), 7, nil, nil, map[string]string{"api_key": "new"})
	require.Error(t, err)
	require.False(t, applied)
	mock.ExpectExec(regexp.QuoteMeta(applyPerplexityProfileSQL)).WillReturnError(errors.New("outbox failed"))
	applied, err = repo.ApplyPerplexityProfileIfUnchanged(context.Background(), 7, nil, nil, service.PerplexityProfileUpdate{Snapshot: map[string]any{}, ObservedAtMs: 1})
	require.Error(t, err)
	require.False(t, applied)
	require.NoError(t, mock.ExpectationsWereMet())
}
