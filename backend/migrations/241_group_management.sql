-- 241_group_management.sql
-- Existing groups remain unenrolled until a manager enables enforcement.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_role_check;
ALTER TABLE users ADD CONSTRAINT users_role_check CHECK (role IN ('admin', 'group_manager', 'user'));

CREATE TABLE IF NOT EXISTS group_managers (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_group_managers_group_id ON group_managers(group_id);

-- No existing group is enrolled until explicitly enabled.
CREATE TABLE IF NOT EXISTS group_management_settings (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    allocation_mode VARCHAR(10) NOT NULL DEFAULT 'auto' CHECK (allocation_mode IN ('auto', 'manual')),
    max_concurrent INT NOT NULL DEFAULT 1 CHECK (max_concurrent > 0),
    daily_limit BIGINT NOT NULL DEFAULT 0 CHECK (daily_limit >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS group_members (
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    max_concurrent INT NOT NULL DEFAULT 1 CHECK (max_concurrent > 0),
    daily_limit BIGINT NOT NULL DEFAULT 0 CHECK (daily_limit >= 0),
    daily_used BIGINT NOT NULL DEFAULT 0 CHECK (daily_used >= 0),
    daily_window_start DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_group_id ON group_members(group_id);
CREATE INDEX IF NOT EXISTS idx_group_members_daily_window ON group_members(daily_window_start);

CREATE TABLE IF NOT EXISTS group_account_assignments (
    group_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    FOREIGN KEY (account_id, group_id) REFERENCES account_groups(account_id, group_id) ON DELETE CASCADE,
    assignment_mode VARCHAR(10) NOT NULL DEFAULT 'manual' CHECK (assignment_mode IN ('auto', 'manual')),
    FOREIGN KEY (user_id, group_id) REFERENCES group_members(user_id, group_id) ON DELETE CASCADE,
    status VARCHAR(10) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, account_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_group_account_assignments_user ON group_account_assignments(user_id, group_id);
CREATE INDEX IF NOT EXISTS idx_group_account_assignments_account ON group_account_assignments(account_id, group_id);
CREATE INDEX IF NOT EXISTS idx_group_account_assignments_active ON group_account_assignments(group_id, status);


-- Short-lived in-flight reservations; expiration recovers slots after a process crash.
CREATE TABLE IF NOT EXISTS group_member_leases (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (user_id, group_id) REFERENCES group_members(user_id, group_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_group_member_leases_member ON group_member_leases(user_id, group_id, expires_at);
-- Serialize the final-admin check across all role/status/soft-delete paths.
CREATE OR REPLACE FUNCTION protect_last_active_admin() RETURNS trigger AS $$
DECLARE
    removing_admin BOOLEAN := FALSE;
BEGIN
    IF OLD.role = 'admin' AND OLD.status = 'active' AND OLD.deleted_at IS NULL THEN
        IF TG_OP = 'DELETE' THEN
            removing_admin := TRUE;
        ELSE
            removing_admin := NEW.role <> 'admin' OR NEW.status <> 'active' OR NEW.deleted_at IS NOT NULL;
        END IF;
        IF removing_admin THEN
            PERFORM pg_advisory_xact_lock(241, 1);
            IF (SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active' AND deleted_at IS NULL) <= 1 THEN
                RAISE EXCEPTION 'cannot remove the last active admin' USING ERRCODE = '23514';
            END IF;
        END IF;
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS trg_protect_last_active_admin ON users;
CREATE TRIGGER trg_protect_last_active_admin BEFORE UPDATE OF role, status, deleted_at OR DELETE ON users
    FOR EACH ROW EXECUTE FUNCTION protect_last_active_admin();
