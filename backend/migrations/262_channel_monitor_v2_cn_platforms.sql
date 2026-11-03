-- Add the remaining first-class platforms to the Channel Monitor V2 list.
--
-- Aggregation already stores usage under these platform keys, but every V2 read
-- is scoped to the platforms listed in channel_monitor_v2_config and the admin
-- UI can only toggle platforms that are already listed, so DeepSeek / Kimi /
-- Zhipu / MiniMax / Perplexity / OpenCode / Command Code never showed on
-- /monitor. Append the missing ones enabled with an empty model list (show
-- every real model name). Platforms already listed, enabled or not, are left
-- untouched, so re-running this is a no-op.

WITH wanted(name) AS (
    VALUES
        ('deepseek'),
        ('kimi'),
        ('zhipu'),
        ('minimax'),
        ('perplexity'),
        ('opencode_go'),
        ('command_code')
),
missing AS (
    SELECT jsonb_build_object('platform', w.name, 'enabled', true, 'models', '[]'::jsonb) AS entry
    FROM wanted w
    WHERE NOT (SELECT platforms @> jsonb_build_array(jsonb_build_object('platform', w.name))
               FROM channel_monitor_v2_config WHERE id = 1)
)
UPDATE channel_monitor_v2_config
SET
    platforms = platforms || (SELECT coalesce(jsonb_agg(entry), '[]'::jsonb) FROM missing),
    version = version + 1,
    updated_at = NOW()
WHERE id = 1
  AND jsonb_typeof(platforms) = 'array'
  AND EXISTS (SELECT 1 FROM missing);
