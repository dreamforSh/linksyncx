-- Add Tencent CodeBuddy / WorkBuddy (scan-login OAuth subscription, domestic and
-- international sites) as a first-class platform.
--
-- 1. user_platform_quotas.platform CHECK
-- 2. composite_model_routes.target_platform CHECK
--
-- Runs after 255_perplexity_platform.sql. DROP ... IF EXISTS 保证可重入；新约束是
-- 255 的超集，必须同时保留 perplexity 与 opencode_go。渠道监控暂不支持 codebuddy，
-- 其 provider 约束保持不变。

ALTER TABLE user_platform_quotas
    DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check;

ALTER TABLE user_platform_quotas
    ADD CONSTRAINT user_platform_quotas_platform_check
    CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                        'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));

ALTER TABLE composite_model_routes
    DROP CONSTRAINT IF EXISTS composite_model_routes_target_platform_check;

ALTER TABLE composite_model_routes
    ADD CONSTRAINT composite_model_routes_target_platform_check
    CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok',
                               'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'));
