-- Clash 订阅支持本地上传的配置文件（Clash YAML / Base64 / URI 列表）。
-- 文件里含节点密码，与订阅链接一样加密存储；内容只在刷新（重新解析）时读取，不进入订阅列表查询。
-- 文件订阅的 url_fingerprint 取内容摘要，沿用同一个唯一索引防止重复导入同一份文件。

ALTER TABLE clash_profiles
    ADD COLUMN IF NOT EXISTS source_type VARCHAR(16) NOT NULL DEFAULT 'url',
    ADD COLUMN IF NOT EXISTS source_name VARCHAR(255) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_size BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS content_encrypted TEXT NOT NULL DEFAULT '';

ALTER TABLE clash_profiles DROP CONSTRAINT IF EXISTS clash_profiles_source_type_check;
ALTER TABLE clash_profiles
    ADD CONSTRAINT clash_profiles_source_type_check CHECK (source_type IN ('url', 'file'));
