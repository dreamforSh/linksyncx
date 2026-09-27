-- Clash 节点流量统计：每个实例按连接采样本地内核的上下行字节，把增量按「节点 + 日期」累加。
-- 累计值由日桶求和得到，不写 clash_nodes，避免与健康检查、出口探测的行更新争锁。
-- 节点被回收时日桶随之级联删除。

CREATE TABLE IF NOT EXISTS clash_node_traffic_daily (
    node_id BIGINT NOT NULL REFERENCES clash_nodes(id) ON DELETE CASCADE,
    bucket_date DATE NOT NULL,
    upload_bytes BIGINT NOT NULL DEFAULT 0,
    download_bytes BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (node_id, bucket_date),
    CONSTRAINT clash_node_traffic_daily_bytes_check
        CHECK (upload_bytes >= 0 AND download_bytes >= 0)
);

CREATE INDEX IF NOT EXISTS idx_clash_node_traffic_daily_date ON clash_node_traffic_daily(bucket_date);
