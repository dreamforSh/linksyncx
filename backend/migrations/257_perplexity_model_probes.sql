-- Existing plans keep their previous, non-billable Perplexity session test.
ALTER TABLE scheduled_test_plans
    ADD COLUMN IF NOT EXISTS test_mode VARCHAR(32) NOT NULL DEFAULT 'default';

ALTER TABLE scheduled_test_plans
    ADD CONSTRAINT scheduled_test_plans_test_mode_check
    CHECK (test_mode IN ('default', 'perplexity_model'));

ALTER TABLE scheduled_test_results
    ADD COLUMN IF NOT EXISTS metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
