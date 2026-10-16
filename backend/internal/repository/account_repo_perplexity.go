package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.PerplexityAccountStateRepository = (*accountRepository)(nil)

const updatePerplexityCookiesSQL = `
	WITH updated AS (
		UPDATE accounts AS a
		SET credentials = a.credentials || $1::jsonb, updated_at = NOW()
		WHERE a.id = $2 AND a.deleted_at IS NULL
			AND a.platform = 'perplexity' AND a.type = 'apikey'
			AND a.credentials = $3::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $4
		RETURNING a.id
	)
	INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
	SELECT $5, updated.id, NULL, NULL FROM updated`

func (r *accountRepository) UpdatePerplexityCookiesIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, patch map[string]string) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	if len(patch) == 0 {
		return false, nil
	}
	for key, value := range patch {
		if (key != "api_key" && key != "csrf_token") || value == "" {
			return false, fmt.Errorf("invalid perplexity cookie credential patch")
		}
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	patchJSON, err := json.Marshal(patch)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, updatePerplexityCookiesSQL, string(patchJSON), id, string(expectedJSON), expectedProxyID, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

const applyPerplexityProfileSQL = `
	WITH updated AS (
		UPDATE accounts AS a
		SET extra = jsonb_set(COALESCE(a.extra, '{}'::jsonb), '{perplexity_quota_snapshot}', $1::jsonb, true),
			status = CASE
				WHEN $2::boolean IS FALSE AND (a.status = 'active' OR (a.status = 'error' AND a.error_message = $3)) THEN 'error'
				WHEN $2::boolean IS TRUE AND a.status = 'error' AND a.error_message = $3 THEN 'active'
				ELSE a.status END,
			error_message = CASE
				WHEN $2::boolean IS FALSE AND (a.status = 'active' OR (a.status = 'error' AND a.error_message = $3)) THEN $3
				WHEN $2::boolean IS TRUE AND a.status = 'error' AND a.error_message = $3 THEN ''
				ELSE a.error_message END,
			updated_at = NOW()
		WHERE a.id = $4 AND a.deleted_at IS NULL
			AND a.platform = 'perplexity' AND a.type = 'apikey'
			AND a.credentials = $5::jsonb
			AND a.proxy_id IS NOT DISTINCT FROM $6
			AND CASE
				WHEN a.extra->'perplexity_quota_snapshot'->>'observed_at_ms' ~ '^[0-9]{1,16}$'
				THEN (a.extra->'perplexity_quota_snapshot'->>'observed_at_ms')::bigint
				ELSE 0 END < $7
		RETURNING a.id
	)
	INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
	SELECT $8, updated.id, NULL, NULL FROM updated`

func (r *accountRepository) ApplyPerplexityProfileIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, update service.PerplexityProfileUpdate) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	if update.ObservedAtMs <= 0 || update.Snapshot == nil {
		return false, fmt.Errorf("invalid perplexity profile observation")
	}
	expectedJSON, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	snapshot := make(map[string]any, len(update.Snapshot)+1)
	for key, value := range update.Snapshot {
		snapshot[key] = value
	}
	snapshot["observed_at_ms"] = update.ObservedAtMs
	body, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	result, err := r.sql.ExecContext(ctx, applyPerplexityProfileSQL, string(body), update.SessionValid, service.PerplexityInvalidSessionMessage, id, string(expectedJSON), expectedProxyID, update.ObservedAtMs, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}
