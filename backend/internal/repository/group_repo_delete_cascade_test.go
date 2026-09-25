package repository

import (
	"context"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// 分组是软删除，外键级联不会触发：删除流程必须在同一事务里显式清理组管理授权、成员、
// 管控设置、渠道关联、专属倍率与兜底引用，并停用不再属于任何分组的组用户。
func TestGroupDeleteCascadeCleansGroupScopedPermissions(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newGroupRepositoryWithSQL(client, nil)

	const groupID = int64(42)
	exec := func(stmt string) {
		mock.ExpectExec(regexp.QuoteMeta(stmt)).WithArgs(groupID).WillReturnResult(sqlmock.NewResult(0, 1))
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, subscription_type FROM groups WHERE id = $1 AND deleted_at IS NULL FOR UPDATE")).
		WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "subscription_type"}).AddRow(groupID, "standard"))
	exec("DELETE FROM user_allowed_groups WHERE group_id = $1")
	exec("DELETE FROM account_groups WHERE group_id = $1")
	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_id FROM group_members WHERE group_id = $1 AND owned")).
		WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(int64(7)).AddRow(int64(8)))
	exec("DELETE FROM group_managers WHERE group_id = $1")
	exec("DELETE FROM group_members WHERE group_id = $1")
	exec("DELETE FROM group_management_settings WHERE group_id = $1")
	exec("DELETE FROM group_invitations WHERE group_id = $1")
	exec("DELETE FROM channel_groups WHERE group_id = $1")
	exec("DELETE FROM user_group_rate_multipliers WHERE group_id = $1")
	// 8 号还属于其他分组，只有 7 号被停用
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE users u SET status = 'disabled'")).
		WithArgs(pq.Array([]int64{7, 8})).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(7)))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE groups SET fallback_group_id = NULL")).
		WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(43)))
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE groups SET fallback_group_id_on_invalid_request = NULL")).
		WithArgs(groupID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE composite_model_routes SET deleted_at = NOW()")).
		WithArgs(groupID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// 软删除：ent 把 DELETE 改写成 UPDATE groups SET deleted_at
	mock.ExpectExec(`UPDATE "groups" SET`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	affected, err := repo.DeleteCascade(context.Background(), groupID)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppendUniqueInt64s(t *testing.T) {
	require.Equal(t, []int64{1, 2, 3}, appendUniqueInt64s([]int64{1, 2}, 2, 3, 3))
	require.Equal(t, []int64{5}, appendUniqueInt64s(nil, 5, 5))
}
