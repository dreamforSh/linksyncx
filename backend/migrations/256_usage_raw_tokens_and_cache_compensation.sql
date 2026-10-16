-- Keep billing usage unchanged. NULL means upstream telemetry/adjustment was not
-- recorded; historical billable cache reads cannot be reconstructed as raw hits.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS raw_input_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS raw_output_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS raw_cache_creation_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS raw_cache_read_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS cache_billing_compensation_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS cache_billing_compensation_reason VARCHAR(64),
    ADD COLUMN IF NOT EXISTS usage_incomplete BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN usage_logs.raw_input_tokens IS 'Upstream input before billing adjustments; NULL is unknown, not billing input';
COMMENT ON COLUMN usage_logs.raw_cache_read_tokens IS 'Observed upstream cache reads; never includes sticky-session billing compensation';
COMMENT ON COLUMN usage_logs.cache_billing_compensation_tokens IS 'Input tokens billed as cache reads; NULL unknown, 0 known no adjustment';
COMMENT ON COLUMN usage_logs.cache_billing_compensation_reason IS 'sticky_session_failover when compensation was applied; NULL otherwise/unknown';

-- Existing aggregate token columns remain billing totals (including throughput).
-- Unrecomputed legacy buckets have NULL raw sums/counts, interpreted as zero known
-- and total_requests/success_requests unknown, never as a measured zero hit rate.
DO $$
DECLARE
    table_name TEXT;
BEGIN
    FOREACH table_name IN ARRAY ARRAY[
        'usage_dashboard_hourly',
        'usage_dashboard_daily',
        'channel_monitor_v2_metrics_1m',
        'channel_monitor_v2_user_metrics_1m',
        'channel_monitor_v2_metrics_rollup',
        'channel_monitor_v2_user_metrics_rollup'
    ] LOOP
        EXECUTE format('ALTER TABLE %I
            ADD COLUMN IF NOT EXISTS raw_input_tokens BIGINT,
            ADD COLUMN IF NOT EXISTS raw_output_tokens BIGINT,
            ADD COLUMN IF NOT EXISTS raw_cache_creation_tokens BIGINT,
            ADD COLUMN IF NOT EXISTS raw_cache_read_tokens BIGINT,
            ADD COLUMN IF NOT EXISTS raw_usage_known_requests BIGINT,
            ADD COLUMN IF NOT EXISTS raw_usage_unknown_requests BIGINT', table_name);
    END LOOP;
END $$;
