-- 243: 组用户归属与余额划拨流水。
--
-- group_members.owned 标记「由组管理员在管理分组里创建」的组用户；一个用户最多被
-- 一个分组拥有，拥有方的组管理员才能重置其密码、停用 / 启用。
-- group_balance_transfers 记录组管理员与组用户之间的余额划拨（grant）和回收（reclaim），
-- 与两侧用户余额变更在同一事务内写入，供组管理员审计。
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS owned BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE group_members
    ADD COLUMN IF NOT EXISTS created_by BIGINT REFERENCES users(id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS uq_group_members_owned_user
    ON group_members (user_id) WHERE owned;

CREATE TABLE IF NOT EXISTS group_balance_transfers (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    -- 用户被物理删除时保留流水，只断开关联
    manager_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    member_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    direction VARCHAR(10) NOT NULL CHECK (direction IN ('grant', 'reclaim')),
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_group_balance_transfers_group
    ON group_balance_transfers (group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_group_balance_transfers_member
    ON group_balance_transfers (member_id, created_at DESC);

COMMENT ON COLUMN group_members.owned IS
    'True when the member was created by a group manager inside this managed group; a user is owned by at most one group';
COMMENT ON TABLE group_balance_transfers IS
    'Balance grants (manager -> member) and reclaims (member -> manager) inside managed groups';
