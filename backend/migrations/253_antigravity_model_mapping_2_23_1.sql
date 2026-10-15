-- 把 Antigravity 2.23.1 新增的模型补进已持久化的 model_mapping。
-- 只在键缺失时写入，不覆盖账号自定义目标。
-- gpt-oss-120b-medium 仅在仍指向自身时改到 gpt-oss-120b-maas。

UPDATE accounts
SET credentials = jsonb_set(
    credentials,
    '{model_mapping,gpt-oss-120b-medium}',
    '"gpt-oss-120b-maas"'::jsonb,
    true
)
WHERE platform = 'antigravity'
  AND deleted_at IS NULL
  AND jsonb_typeof(credentials->'model_mapping') = 'object'
  AND credentials->'model_mapping'->>'gpt-oss-120b-medium' = 'gpt-oss-120b-medium';

DO $$
DECLARE
    entry jsonb;
    entries jsonb[] := ARRAY[
        '{"k":"claude-opus-5-5","v":"claude-opus-5-5"}'::jsonb,
        '{"k":"gemini-3.1-ultra","v":"gemini-3.1-ultra"}'::jsonb,
        '{"k":"gemini-3.1-flash-lite","v":"gemini-3.1-flash-lite"}'::jsonb,
        '{"k":"gemini-3.1-flash-lite-preview","v":"gemini-3.1-flash-lite"}'::jsonb,
        '{"k":"gemini-3.5-flash","v":"gemini-3.5-flash"}'::jsonb,
        '{"k":"gemini-3.5-flash-low","v":"gemini-3.5-flash-low"}'::jsonb,
        '{"k":"gemini-3.5-flash-lite","v":"gemini-3.5-flash-lite"}'::jsonb,
        '{"k":"gemini-3.5-flash-extra-low","v":"gemini-3.5-flash-extra-low"}'::jsonb,
        '{"k":"gemini-4-argon","v":"gemini-4-argon"}'::jsonb,
        '{"k":"gemini-4-argon-high","v":"gemini-4-argon-high"}'::jsonb,
        '{"k":"gemini-4-argon-low","v":"gemini-4-argon-low"}'::jsonb,
        '{"k":"gemini-4-argon-medium","v":"gemini-4-argon-medium"}'::jsonb,
        '{"k":"gpt-oss-120b-maas","v":"gpt-oss-120b-maas"}'::jsonb,
        '{"k":"gpt-oss-20b-maas","v":"gpt-oss-20b-maas"}'::jsonb
    ];
BEGIN
    FOREACH entry IN ARRAY entries LOOP
        UPDATE accounts
        SET credentials = jsonb_set(
            credentials,
            ARRAY['model_mapping', entry->>'k'],
            to_jsonb(entry->>'v'),
            true
        )
        WHERE platform = 'antigravity'
          AND deleted_at IS NULL
          AND jsonb_typeof(credentials->'model_mapping') = 'object'
          AND credentials->'model_mapping'->>(entry->>'k') IS NULL;
    END LOOP;
END $$;
