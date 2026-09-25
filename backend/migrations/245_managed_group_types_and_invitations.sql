-- 245: 管理分组类型（额度组 / 订阅组）、组用户 5h / 7d 美元上限、邮箱邀请。
--
-- managed_type 只对管理分组有效：
--   quota        额度组：组管理员分配余额与 5h / 7d 美元上限，组用户使用分组全部账号，按用量扣自己的余额；
--   subscription 订阅组：组管理员把账号分配给组用户，组用户只能使用被分配账号的订阅额度，不扣余额。
-- 分配方式由类型决定（额度组自动、订阅组手动）。存量管理分组按原分配方式推断类型。迁移可重复执行。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS managed_type VARCHAR(20);

UPDATE groups g
SET managed_type = CASE
        WHEN (SELECT s.allocation_mode FROM group_management_settings s WHERE s.group_id = g.id) = 'auto' THEN 'quota'
        ELSE 'subscription'
    END
WHERE g.kind = 'managed' AND g.managed_type IS NULL;

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_managed_type_check;
ALTER TABLE groups
    ADD CONSTRAINT groups_managed_type_check CHECK (
        (kind = 'managed' AND managed_type IN ('quota', 'subscription'))
        OR (kind = 'channel' AND managed_type IS NULL)
    );

COMMENT ON COLUMN groups.managed_type IS
    'Managed group type: quota (members share the pool, billed from their balance within 5h/7d USD caps) or subscription (members use assigned accounts only, not billed); NULL for channel groups';

-- 组用户在额度组内的 5h / 7d 美元上限与窗口用量（0 表示不限）。
-- 窗口规则与 API Key 限额一致：5h 窗口从首笔用量开始，7d 窗口从当天零点开始，到期后下一笔用量重开。
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS limit_5h_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS limit_7d_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS usage_5h_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS usage_7d_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS window_5h_start TIMESTAMPTZ;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS window_7d_start TIMESTAMPTZ;

ALTER TABLE group_members DROP CONSTRAINT IF EXISTS group_members_usd_limits_check;
ALTER TABLE group_members
    ADD CONSTRAINT group_members_usd_limits_check CHECK (limit_5h_usd >= 0 AND limit_7d_usd >= 0);

-- 新成员套用的默认上限
ALTER TABLE group_management_settings
    ADD COLUMN IF NOT EXISTS default_limit_5h_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE group_management_settings
    ADD COLUMN IF NOT EXISTS default_limit_7d_usd DECIMAL(20,8) NOT NULL DEFAULT 0;

ALTER TABLE group_management_settings DROP CONSTRAINT IF EXISTS group_management_settings_usd_limits_check;
ALTER TABLE group_management_settings
    ADD CONSTRAINT group_management_settings_usd_limits_check CHECK (default_limit_5h_usd >= 0 AND default_limit_7d_usd >= 0);

-- 组管理员按邮箱邀请已注册用户，对方确认后才加入。email 为发出邀请时的邮箱快照。
CREATE TABLE IF NOT EXISTS group_invitations (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email VARCHAR(255) NOT NULL,
    invited_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'accepted', 'declined', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    responded_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 同一分组对同一用户最多一条待处理邀请
CREATE UNIQUE INDEX IF NOT EXISTS uq_group_invitations_pending
    ON group_invitations (group_id, user_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_group_invitations_user
    ON group_invitations (user_id, status);
CREATE INDEX IF NOT EXISTS idx_group_invitations_group
    ON group_invitations (group_id, status, created_at DESC);

COMMENT ON TABLE group_invitations IS
    'Email invitations to join managed groups; the invitee must accept before becoming a member';
