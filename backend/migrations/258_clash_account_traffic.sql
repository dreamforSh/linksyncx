-- Clash 账号级流量：一个节点可以被多个账号共用后，按账号拆分流量。
-- 每个账号经节点监听器出站时使用自己的认证用户名，采样器按连接的入站用户把增量
-- 记到「账号 + 节点 + 日期」日桶；节点级总量仍以 clash_node_traffic_daily 为准，
-- 两者之差是未归属到账号的流量（如使用节点共用凭证的探测或配置下发前的请求）。
-- 节点被回收或账号被物理删除时日桶随之级联删除。

CREATE TABLE IF NOT EXISTS clash_account_traffic_daily (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    node_id BIGINT NOT NULL REFERENCES clash_nodes(id) ON DELETE CASCADE,
    bucket_date DATE NOT NULL,
    upload_bytes BIGINT NOT NULL DEFAULT 0,
    download_bytes BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, node_id, bucket_date),
    CONSTRAINT clash_account_traffic_daily_bytes_check
        CHECK (upload_bytes >= 0 AND download_bytes >= 0)
);

CREATE INDEX IF NOT EXISTS idx_clash_account_traffic_daily_node_date ON clash_account_traffic_daily(node_id, bucket_date);
CREATE INDEX IF NOT EXISTS idx_clash_account_traffic_daily_date ON clash_account_traffic_daily(bucket_date);
