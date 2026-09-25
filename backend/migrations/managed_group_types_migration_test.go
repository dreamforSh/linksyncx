package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedGroupTypesAndInvitationsMigration(t *testing.T) {
	content, err := FS.ReadFile("245_managed_group_types_and_invitations.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS managed_type VARCHAR(20)")
	// 存量管理分组按原分配方式推断类型，必须在加约束之前回填
	backfillAt := strings.Index(sql, "SET managed_type = CASE")
	checkAt := strings.Index(sql, "ADD CONSTRAINT groups_managed_type_check")
	require.Positive(t, backfillAt)
	require.Less(t, backfillAt, checkAt)
	require.Contains(t, sql, "= 'auto' THEN 'quota' ELSE 'subscription' END WHERE g.kind = 'managed' AND g.managed_type IS NULL")
	require.Contains(t, sql, "(kind = 'managed' AND managed_type IN ('quota', 'subscription')) OR (kind = 'channel' AND managed_type IS NULL)")

	for _, column := range []string{"limit_5h_usd", "limit_7d_usd", "usage_5h_usd", "usage_7d_usd"} {
		require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS "+column+" DECIMAL(20,8) NOT NULL DEFAULT 0")
	}
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS window_5h_start TIMESTAMPTZ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS window_7d_start TIMESTAMPTZ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS default_limit_5h_usd DECIMAL(20,8) NOT NULL DEFAULT 0")

	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS group_invitations")
	require.Contains(t, sql, "CHECK (status IN ('pending', 'accepted', 'declined', 'revoked'))")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS uq_group_invitations_pending ON group_invitations (group_id, user_id) WHERE status = 'pending'")
}
