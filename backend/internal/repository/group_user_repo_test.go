package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func newGroupUserRepoTest(t *testing.T) (*dbent.Client, sqlmock.Sqlmock, service.GroupUserRepository) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return client, mock, NewGroupUserRepository(client)
}

func TestGroupUserRepoInsertMemberUsesGroupDefaults(t *testing.T) {
	_, mock, repo := newGroupUserRepoTest(t)
	concurrency := 3
	// 未指定的额度（日请求数、5h / 7d 美元上限）套用分组设置里的默认值
	mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO group_members (user_id, group_id, max_concurrent, daily_limit, owned, created_by, limit_5h_usd, limit_7d_usd)`)+
		`(?s).*COALESCE\(s\.default_limit_5h_usd, 0\), COALESCE\(s\.default_limit_7d_usd, 0\)`).
		WithArgs(int64(11), int64(42), 3, nil, true, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))

	require.NoError(t, repo.InsertMember(context.Background(), 42, 11, true, 7, &concurrency, nil))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupUserRepoInsertMemberConflictHandling(t *testing.T) {
	ctx := context.Background()
	insert := regexp.QuoteMeta(`INSERT INTO group_members`)
	exists := regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id=$1 AND user_id=$2)`)

	// 已是成员：普通加入幂等成功
	_, mock, repo := newGroupUserRepoTest(t)
	mock.ExpectExec(insert).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(exists).WithArgs(int64(42), int64(11)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	require.NoError(t, repo.InsertMember(ctx, 42, 11, false, 0, nil, nil))
	require.NoError(t, mock.ExpectationsWereMet())

	// 用户不存在 / 未启用
	_, mock, repo = newGroupUserRepoTest(t)
	mock.ExpectExec(insert).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(exists).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	require.ErrorIs(t, repo.InsertMember(ctx, 42, 11, false, 0, nil, nil), service.ErrGroupManagementBadInput)
	require.NoError(t, mock.ExpectationsWereMet())

	// 新建的组用户不可能已是成员，冲突一律视为失败
	_, mock, repo = newGroupUserRepoTest(t)
	mock.ExpectExec(insert).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(exists).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	require.ErrorIs(t, repo.InsertMember(ctx, 42, 11, true, 7, nil, nil), service.ErrGroupManagementBadInput)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupUserRepoRunsInsideEntTransaction(t *testing.T) {
	client, mock, repo := newGroupUserRepoTest(t)
	ctx := context.Background()
	managerID, memberID := int64(7), int64(11)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`)).
		WithArgs(pq.Array([]int64{7, 11})).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7).AddRow(11))
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO group_balance_transfers (group_id, manager_id, member_id, direction, amount, notes)`)).
		WithArgs(int64(42), managerID, memberID, service.GroupTransferGrant, 2.5, "note").
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow(int64(9), created))
	mock.ExpectCommit()

	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	txCtx := dbent.NewTxContext(ctx, tx)
	require.NoError(t, repo.LockUsers(txCtx, memberID, managerID))
	transfer := &service.GroupBalanceTransfer{GroupID: 42, ManagerID: &managerID, MemberID: &memberID, Direction: service.GroupTransferGrant, Amount: 2.5, Notes: "note"}
	require.NoError(t, repo.InsertTransfer(txCtx, transfer))
	require.NoError(t, tx.Commit())

	require.Equal(t, int64(9), transfer.ID)
	require.Equal(t, created, transfer.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupUserRepoListTransfersPaginatesAndFiltersMember(t *testing.T) {
	_, mock, repo := newGroupUserRepoTest(t)
	created := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM group_balance_transfers t WHERE t.group_id=$1 AND t.member_id=$2`)).
		WithArgs(int64(42), int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(25)))
	mock.ExpectQuery(`(?s)FROM group_balance_transfers t.*LIMIT \$3 OFFSET \$4`).
		WithArgs(int64(42), int64(11), 10, 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "group_id", "manager_id", "manager_name", "member_id", "member_name", "direction", "amount", "notes", "created_at"}).
			AddRow(int64(3), int64(42), nil, "", int64(11), "alice", "reclaim", 1.25, "", created))

	items, total, err := repo.ListTransfers(context.Background(), 42, 11, 2, 10)
	require.NoError(t, err)
	require.Equal(t, int64(25), total)
	require.Len(t, items, 1)
	require.Nil(t, items[0].ManagerID)
	require.Equal(t, int64(11), *items[0].MemberID)
	require.Equal(t, "alice", items[0].MemberName)
	require.Equal(t, service.GroupTransferReclaim, items[0].Direction)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupUserRepoUserGroupSummariesMergesOwnedAndManaged(t *testing.T) {
	_, mock, repo := newGroupUserRepoTest(t)
	ids := []int64{3, 5}

	mock.ExpectQuery(regexp.QuoteMeta(`FROM group_members gm JOIN groups g ON g.id=gm.group_id AND g.deleted_at IS NULL`)).
		WithArgs(pq.Array(ids)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "id", "name", "category"}).AddRow(int64(5), int64(42), "acme", "team"))
	mock.ExpectQuery(regexp.QuoteMeta(`FROM group_managers mg JOIN groups g ON g.id=mg.group_id AND g.deleted_at IS NULL`)).
		WithArgs(pq.Array(ids)).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "id", "name", "category"}).
			AddRow(int64(3), int64(42), "acme", "team").
			AddRow(int64(3), int64(43), "globex", "enterprise"))

	out, err := repo.UserGroupSummaries(context.Background(), ids)
	require.NoError(t, err)
	require.Equal(t, []service.UserGroupSummary{
		{UserID: 3, ManagedGroups: []service.GroupRef{{ID: 42, Name: "acme", Category: "team"}, {ID: 43, Name: "globex", Category: "enterprise"}}},
		{UserID: 5, OwnedGroup: &service.GroupRef{ID: 42, Name: "acme", Category: "team"}, ManagedGroups: []service.GroupRef{}},
	}, out)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupUserRepoSetUserStatusOnlyTouchesPlainUsers(t *testing.T) {
	_, mock, repo := newGroupUserRepoTest(t)
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE users SET status=$2, updated_at=NOW() WHERE id=$1 AND role='user' AND deleted_at IS NULL`)).
		WithArgs(int64(12), service.StatusDisabled).
		WillReturnResult(sqlmock.NewResult(0, 0))

	updated, err := repo.SetUserStatus(context.Background(), 12, service.StatusDisabled)
	require.NoError(t, err)
	require.False(t, updated)
	require.NoError(t, mock.ExpectationsWereMet())
}
