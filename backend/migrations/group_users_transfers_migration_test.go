package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupUsersAndTransfersMigration(t *testing.T) {
	content, err := FS.ReadFile("243_group_users_and_transfers.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS owned BOOLEAN NOT NULL DEFAULT FALSE")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS created_by BIGINT REFERENCES users(id) ON DELETE SET NULL")
	// 一个用户最多被一个分组拥有
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS uq_group_members_owned_user ON group_members (user_id) WHERE owned")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_balance_transfers")
	require.Contains(t, sql, "CHECK (direction IN ('grant', 'reclaim'))")
	require.Contains(t, sql, "amount DECIMAL(20,8) NOT NULL CHECK (amount > 0)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_group_balance_transfers_group ON group_balance_transfers (group_id, created_at DESC)")
}
