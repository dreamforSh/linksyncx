-- Clash 订阅代理池：订阅、节点，以及托管代理的来源标记。
-- 每个节点物化为一条 source='clash' 的 proxies 记录，账号沿用 accounts.proxy_id 绑定。
-- 托管代理的 host/port/凭据永久稳定：调度快照缓存了它们，端口只在代理删除且无引用后回收。

ALTER TABLE proxies ADD COLUMN IF NOT EXISTS source VARCHAR(16) NOT NULL DEFAULT 'manual';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'proxies_source_check') THEN
        ALTER TABLE proxies ADD CONSTRAINT proxies_source_check CHECK (source IN ('manual', 'clash'));
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_proxies_source ON proxies(source);

CREATE TABLE IF NOT EXISTS clash_profiles (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    url_encrypted TEXT NOT NULL,
    url_fingerprint VARCHAR(64) NOT NULL,
    url_masked VARCHAR(512) NOT NULL DEFAULT '',
    user_agent VARCHAR(200) NOT NULL DEFAULT 'clash.meta',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    refresh_interval_minutes INTEGER NOT NULL DEFAULT 360,
    include_pattern TEXT NOT NULL DEFAULT '',
    exclude_pattern TEXT NOT NULL DEFAULT '',
    fetch_proxy_id BIGINT REFERENCES proxies(id) ON DELETE SET NULL,
    notes TEXT NOT NULL DEFAULT '',
    last_refresh_at TIMESTAMPTZ,
    last_refresh_status VARCHAR(16) NOT NULL DEFAULT 'never',
    last_refresh_error TEXT NOT NULL DEFAULT '',
    last_format VARCHAR(32) NOT NULL DEFAULT '',
    upload_bytes BIGINT NOT NULL DEFAULT 0,
    download_bytes BIGINT NOT NULL DEFAULT 0,
    total_bytes BIGINT NOT NULL DEFAULT 0,
    expire_at TIMESTAMPTZ,
    node_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT clash_profiles_refresh_status_check
        CHECK (last_refresh_status IN ('never', 'ok', 'error', 'skipped')),
    CONSTRAINT clash_profiles_refresh_interval_check
        CHECK (refresh_interval_minutes >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_profiles_name_active
    ON clash_profiles(name) WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_profiles_url_active
    ON clash_profiles(url_fingerprint) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS clash_nodes (
    id BIGSERIAL PRIMARY KEY,
    profile_id BIGINT NOT NULL REFERENCES clash_profiles(id) ON DELETE CASCADE,
    name VARCHAR(255) NOT NULL,
    type VARCHAR(32) NOT NULL,
    server VARCHAR(255) NOT NULL,
    server_port INTEGER NOT NULL DEFAULT 0,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    config_hash VARCHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    status_reason TEXT NOT NULL DEFAULT '',
    missing_since TIMESTAMPTZ,
    listen_port INTEGER NOT NULL,
    proxy_id BIGINT NOT NULL REFERENCES proxies(id),
    health_status VARCHAR(16) NOT NULL DEFAULT 'unknown',
    latency_ms INTEGER,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    consecutive_successes INTEGER NOT NULL DEFAULT 0,
    last_checked_at TIMESTAMPTZ,
    last_check_error TEXT NOT NULL DEFAULT '',
    exit_ip VARCHAR(64) NOT NULL DEFAULT '',
    exit_country VARCHAR(64) NOT NULL DEFAULT '',
    exit_country_code VARCHAR(8) NOT NULL DEFAULT '',
    exit_region VARCHAR(128) NOT NULL DEFAULT '',
    exit_city VARCHAR(128) NOT NULL DEFAULT '',
    exit_status VARCHAR(16) NOT NULL DEFAULT 'unknown',
    exit_checked_at TIMESTAMPTZ,
    exit_pending_ip VARCHAR(64) NOT NULL DEFAULT '',
    exit_changed_at TIMESTAMPTZ,
    platform_checks JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT clash_nodes_status_check
        CHECK (status IN ('active', 'missing', 'disabled', 'invalid')),
    CONSTRAINT clash_nodes_health_status_check
        CHECK (health_status IN ('unknown', 'healthy', 'unhealthy')),
    CONSTRAINT clash_nodes_exit_status_check
        CHECK (exit_status IN ('unknown', 'ok', 'stale', 'changed')),
    CONSTRAINT clash_nodes_listen_port_check
        CHECK (listen_port BETWEEN 1 AND 65535)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_nodes_listen_port ON clash_nodes(listen_port);
CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_nodes_proxy_id ON clash_nodes(proxy_id);
CREATE INDEX IF NOT EXISTS idx_clash_nodes_profile_status ON clash_nodes(profile_id, status);
CREATE INDEX IF NOT EXISTS idx_clash_nodes_exit_ip ON clash_nodes(exit_ip) WHERE exit_ip <> '';
