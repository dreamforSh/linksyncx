package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.PerplexityModelStateRepository = (*accountRepository)(nil)

const storePerplexityModelProbeSQL = `
WITH updated AS (
 UPDATE accounts AS a
 SET extra = jsonb_set(COALESCE(a.extra, '{}'::jsonb), '{perplexity_model_health}', (
   SELECT jsonb_object_agg(bounded.key, bounded.value)
   FROM (
    SELECT entry.key, entry.value
    FROM jsonb_each(
      CASE WHEN jsonb_typeof(a.extra->'perplexity_model_health') = 'object'
       THEN a.extra->'perplexity_model_health' ELSE '{}'::jsonb END
      || jsonb_build_object($1::text, $2::jsonb)
    ) AS entry
    ORDER BY (entry.key = $1) DESC,
     CASE WHEN entry.value->>'observed_at_ms' ~ '^[0-9]{1,16}$'
      THEN (entry.value->>'observed_at_ms')::bigint ELSE 0 END DESC, entry.key
    LIMIT 128
   ) AS bounded
  ), true), updated_at = NOW()
 WHERE a.id = $3 AND a.deleted_at IS NULL
  AND a.platform = 'perplexity' AND a.type = 'apikey'
  AND a.credentials = $4::jsonb AND a.proxy_id IS NOT DISTINCT FROM $5
  AND CASE WHEN a.extra->'perplexity_model_health'->$1->>'observed_at_ms' ~ '^[0-9]{1,16}$'
   THEN (a.extra->'perplexity_model_health'->$1->>'observed_at_ms')::bigint ELSE 0 END < $6
 RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT $7, updated.id, NULL, NULL FROM updated`

func (r *accountRepository) StorePerplexityModelProbeIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, result service.PerplexityModelProbeResult) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	if result.Preference == "" || len(result.Preference) > 200 || result.Mode == "" || result.ObservedAtMs <= 0 || result.IdentityFingerprint == "" {
		return false, fmt.Errorf("invalid perplexity model probe observation")
	}
	body, err := json.Marshal(result)
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	key := result.Preference + ":" + result.Mode
	res, err := r.sql.ExecContext(ctx, storePerplexityModelProbeSQL, key, string(body), id, string(expected), expectedProxyID, result.ObservedAtMs, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}

const storePerplexityCatalogSQL = `
WITH updated AS (
 UPDATE accounts AS a
 SET extra = jsonb_set(COALESCE(a.extra, '{}'::jsonb), '{upstream_model_metadata}', $1::jsonb, true), updated_at = NOW()
 WHERE a.id = $2 AND a.deleted_at IS NULL
  AND a.platform = 'perplexity' AND a.type = 'apikey'
  AND a.credentials = $3::jsonb AND a.proxy_id IS NOT DISTINCT FROM $4
  AND CASE WHEN a.extra->'upstream_model_metadata'->>'observed_at_ms' ~ '^[0-9]{1,16}$'
   THEN (a.extra->'upstream_model_metadata'->>'observed_at_ms')::bigint ELSE 0 END < $5
 RETURNING a.id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT $6, updated.id, NULL, NULL FROM updated`

func (r *accountRepository) StorePerplexityCatalogIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, snapshot service.UpstreamModelMetadataSnapshot) (bool, error) {
	if r == nil || r.sql == nil {
		return false, errors.New("account repository SQL executor is not configured")
	}
	if snapshot.Source != "perplexity" || snapshot.IdentityFingerprint == "" || snapshot.ObservedAtMs <= 0 {
		return false, fmt.Errorf("invalid perplexity model catalog observation")
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(normalizeJSONMap(expectedCredentials))
	if err != nil {
		return false, err
	}
	res, err := r.sql.ExecContext(ctx, storePerplexityCatalogSQL, string(body), id, string(expected), expectedProxyID, snapshot.ObservedAtMs, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	count, err := res.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return true, nil
}
