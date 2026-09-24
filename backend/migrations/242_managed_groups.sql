-- 242: 分组类型与管理分组分类。
--
-- kind=channel 为渠道分组（原有全部分组，公共账号池，可挂渠道）；
-- kind=managed 为管理分组（不挂渠道、强制专属，组账号独占于该分组，
-- 由组管理员分配给组成员）。category 仅管理分组使用：enterprise / team。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS kind VARCHAR(20) NOT NULL DEFAULT 'channel';
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS category VARCHAR(20);

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_kind_check;
ALTER TABLE groups
    ADD CONSTRAINT groups_kind_check CHECK (kind IN ('channel', 'managed'));

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_category_check;
ALTER TABLE groups
    ADD CONSTRAINT groups_category_check CHECK (
        (kind = 'managed' AND category IN ('enterprise', 'team'))
        OR (kind = 'channel' AND category IS NULL)
    );

CREATE INDEX IF NOT EXISTS idx_groups_kind ON groups (kind);

COMMENT ON COLUMN groups.kind IS
    'Group kind: channel (shared routing pool, may join channels) or managed (non-channel, exclusive accounts allocated by group managers); immutable after creation';
COMMENT ON COLUMN groups.category IS
    'Managed group category: enterprise or team; NULL for channel groups';
