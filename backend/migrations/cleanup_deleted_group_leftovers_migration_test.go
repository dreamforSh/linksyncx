package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCleanupDeletedGroupLeftoversMigration(t *testing.T) {
	content, err := FS.ReadFile("244_cleanup_deleted_group_leftovers.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, table := range []string{
		"group_managers",
		"group_members",
		"group_management_settings",
		"channel_groups",
		"user_group_rate_multipliers",
		"user_allowed_groups",
		"account_groups",
	} {
		require.Contains(t, sql, "DELETE FROM "+table+" x USING groups g WHERE g.id = x.group_id AND g.deleted_at IS NOT NULL")
	}
	require.Contains(t, sql, "SET fallback_group_id = NULL")
	require.Contains(t, sql, "SET fallback_group_id_on_invalid_request = NULL")

	// 停用孤立组用户依赖 owned 标记，必须在删除成员行之前执行
	disableAt := strings.Index(sql, "SET status = 'disabled'")
	deleteMembersAt := strings.Index(sql, "DELETE FROM group_members")
	require.Positive(t, disableAt)
	require.Less(t, disableAt, deleteMembersAt)
}
