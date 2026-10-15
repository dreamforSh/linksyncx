-- 撤回 253 插入的 14 条默认自映射。
-- 253 在键缺失时写入，会把空映射 {} 变成只含新模型的自定义映射，
-- 运行时因此不再回落到完整默认目录；精确自映射也会抢在已有通配符之前。
-- 只删除值仍等于 253 默认值的键。用户改过的目标保留。
-- gpt-oss-120b-medium → gpt-oss-120b-maas 的更正不在此列，保持不动。
-- 已应用的 253 不能改写（checksum），新鲜安装会先执行 253 再执行本文件，净效果是不留下这些键。

UPDATE accounts AS a
SET credentials = jsonb_set(
    a.credentials,
    '{model_mapping}',
    COALESCE((
        SELECT jsonb_object_agg(e.key, e.value)
        FROM jsonb_each(a.credentials->'model_mapping') AS e
        WHERE NOT EXISTS (
            SELECT 1
            FROM (VALUES
                ('claude-opus-5-5', 'claude-opus-5-5'),
                ('gemini-3.1-ultra', 'gemini-3.1-ultra'),
                ('gemini-3.1-flash-lite', 'gemini-3.1-flash-lite'),
                ('gemini-3.1-flash-lite-preview', 'gemini-3.1-flash-lite'),
                ('gemini-3.5-flash', 'gemini-3.5-flash'),
                ('gemini-3.5-flash-low', 'gemini-3.5-flash-low'),
                ('gemini-3.5-flash-lite', 'gemini-3.5-flash-lite'),
                ('gemini-3.5-flash-extra-low', 'gemini-3.5-flash-extra-low'),
                ('gemini-4-argon', 'gemini-4-argon'),
                ('gemini-4-argon-high', 'gemini-4-argon-high'),
                ('gemini-4-argon-low', 'gemini-4-argon-low'),
                ('gemini-4-argon-medium', 'gemini-4-argon-medium'),
                ('gpt-oss-120b-maas', 'gpt-oss-120b-maas'),
                ('gpt-oss-20b-maas', 'gpt-oss-20b-maas')
            ) AS defaults(k, v)
            WHERE e.key = defaults.k
              AND e.value = to_jsonb(defaults.v)
        )
    ), '{}'::jsonb)
)
WHERE a.platform = 'antigravity'
  AND a.deleted_at IS NULL
  AND jsonb_typeof(a.credentials->'model_mapping') = 'object'
  AND EXISTS (
      SELECT 1
      FROM jsonb_each(a.credentials->'model_mapping') AS e
      JOIN (VALUES
          ('claude-opus-5-5', 'claude-opus-5-5'),
          ('gemini-3.1-ultra', 'gemini-3.1-ultra'),
          ('gemini-3.1-flash-lite', 'gemini-3.1-flash-lite'),
          ('gemini-3.1-flash-lite-preview', 'gemini-3.1-flash-lite'),
          ('gemini-3.5-flash', 'gemini-3.5-flash'),
          ('gemini-3.5-flash-low', 'gemini-3.5-flash-low'),
          ('gemini-3.5-flash-lite', 'gemini-3.5-flash-lite'),
          ('gemini-3.5-flash-extra-low', 'gemini-3.5-flash-extra-low'),
          ('gemini-4-argon', 'gemini-4-argon'),
          ('gemini-4-argon-high', 'gemini-4-argon-high'),
          ('gemini-4-argon-low', 'gemini-4-argon-low'),
          ('gemini-4-argon-medium', 'gemini-4-argon-medium'),
          ('gpt-oss-120b-maas', 'gpt-oss-120b-maas'),
          ('gpt-oss-20b-maas', 'gpt-oss-20b-maas')
      ) AS defaults(k, v)
        ON e.key = defaults.k
       AND e.value = to_jsonb(defaults.v)
  );
