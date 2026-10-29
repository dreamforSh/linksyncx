-- Add CodeBuddy to the Channel Monitor V2 platform list.
--
-- Aggregation already stores CodeBuddy usage under platform 'codebuddy', but
-- every V2 read is scoped to the enabled platforms in channel_monitor_v2_config
-- and the admin UI can only toggle platforms that are already listed, so
-- CodeBuddy never showed on /monitor. Append it enabled with an empty model
-- list (show every real model name). Configs that already list codebuddy,
-- enabled or not, are left alone.

UPDATE channel_monitor_v2_config
SET
    platforms = platforms || '[{"platform":"codebuddy","enabled":true,"models":[]}]'::jsonb,
    version = version + 1,
    updated_at = NOW()
WHERE id = 1
  AND jsonb_typeof(platforms) = 'array'
  AND NOT platforms @> '[{"platform":"codebuddy"}]'::jsonb;
