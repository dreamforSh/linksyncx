package repository

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// groupUserRepository 组用户与余额划拨的持久化。所有语句都经 clientFromContext 执行，
// 与服务层开启的 ent 事务（用户创建、余额调整）处在同一个事务里。
type groupUserRepository struct {
	client *dbent.Client
}

func NewGroupUserRepository(client *dbent.Client) service.GroupUserRepository {
	return &groupUserRepository{client: client}
}

func (r *groupUserRepository) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return clientFromContext(ctx, r.client).ExecContext(ctx, query, args...)
}

func (r *groupUserRepository) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return clientFromContext(ctx, r.client).QueryContext(ctx, query, args...)
}

// queryRow 执行单行查询；没有结果时返回 sql.ErrNoRows。
func (r *groupUserRepository) queryRow(ctx context.Context, query string, args []any, dest ...any) error {
	rows, err := r.query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if err := rows.Scan(dest...); err != nil {
		return err
	}
	return rows.Err()
}

func (r *groupUserRepository) Summary(ctx context.Context, userID int64) (int64, int64, error) {
	var managed, memberships int64
	err := r.queryRow(ctx, `SELECT
		(SELECT COUNT(*) FROM group_managers mg JOIN groups g ON g.id=mg.group_id WHERE mg.user_id=$1 AND g.deleted_at IS NULL),
		(SELECT COUNT(*) FROM group_members gm JOIN groups g ON g.id=gm.group_id WHERE gm.user_id=$1 AND g.deleted_at IS NULL)`,
		[]any{userID}, &managed, &memberships)
	return managed, memberships, err
}

func (r *groupUserRepository) LockGroup(ctx context.Context, groupID int64) error {
	var id int64
	err := r.queryRow(ctx, `SELECT id FROM groups WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, []any{groupID}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrGroupManagementBadInput
	}
	return err
}

func (r *groupUserRepository) UpdateCategory(ctx context.Context, groupID int64, category string) (bool, error) {
	result, err := r.exec(ctx, `UPDATE groups SET category=$2, updated_at=NOW() WHERE id=$1 AND kind='managed' AND deleted_at IS NULL`, groupID, category)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (r *groupUserRepository) InsertMember(ctx context.Context, groupID, userID int64, owned bool, createdBy int64, maxConcurrent *int, dailyLimit *int64) error {
	var creator any
	if createdBy > 0 {
		creator = createdBy
	}
	var concurrency, daily any
	if maxConcurrent != nil {
		concurrency = *maxConcurrent
	}
	if dailyLimit != nil {
		daily = *dailyLimit
	}
	result, err := r.exec(ctx, `INSERT INTO group_members (user_id, group_id, max_concurrent, daily_limit, owned, created_by)
		SELECT u.id, g.id, COALESCE($3::int, s.max_concurrent, 1), COALESCE($4::bigint, s.daily_limit, 0), $5, $6
		FROM users u
		JOIN groups g ON g.id=$2 AND g.deleted_at IS NULL
		LEFT JOIN group_management_settings s ON s.group_id=g.id
		WHERE u.id=$1 AND u.deleted_at IS NULL AND u.status='active'
		ON CONFLICT (user_id, group_id) DO NOTHING`,
		userID, groupID, concurrency, daily, owned, creator)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	// 0 行：成员已存在（幂等成功），或用户不存在 / 未启用
	var exists bool
	if err := r.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_members WHERE group_id=$1 AND user_id=$2)`, []any{groupID, userID}, &exists); err != nil {
		return err
	}
	if !exists || owned {
		return service.ErrGroupManagementBadInput
	}
	return nil
}

func (r *groupUserRepository) GetMemberState(ctx context.Context, groupID, userID int64) (*service.GroupMemberState, error) {
	var state service.GroupMemberState
	err := r.queryRow(ctx, `SELECT gm.owned, u.role, u.status FROM group_members gm
		JOIN users u ON u.id=gm.user_id AND u.deleted_at IS NULL
		WHERE gm.group_id=$1 AND gm.user_id=$2`, []any{groupID, userID}, &state.Owned, &state.Role, &state.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *groupUserRepository) IsOwnedByManager(ctx context.Context, userID, managerID int64) (bool, error) {
	var owned bool
	err := r.queryRow(ctx, `SELECT EXISTS(SELECT 1 FROM group_members gm
		JOIN group_managers mg ON mg.group_id=gm.group_id AND mg.user_id=$2
		JOIN groups g ON g.id=gm.group_id AND g.deleted_at IS NULL
		WHERE gm.user_id=$1 AND gm.owned)`, []any{userID, managerID}, &owned)
	return owned, err
}

func (r *groupUserRepository) ListOwnedUsersForManager(ctx context.Context, managerID, excludeGroupID int64) ([]service.GroupOwnedUser, error) {
	rows, err := r.query(ctx, `SELECT u.id, u.email, COALESCE(u.username,''), u.status, g.id, g.name
		FROM group_members gm
		JOIN group_managers mg ON mg.group_id=gm.group_id AND mg.user_id=$1
		JOIN groups g ON g.id=gm.group_id AND g.deleted_at IS NULL
		JOIN users u ON u.id=gm.user_id AND u.deleted_at IS NULL
		WHERE gm.owned AND NOT EXISTS (SELECT 1 FROM group_members x WHERE x.group_id=$2 AND x.user_id=gm.user_id)
		ORDER BY g.id, u.id`, managerID, excludeGroupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.GroupOwnedUser, 0)
	for rows.Next() {
		var item service.GroupOwnedUser
		if err := rows.Scan(&item.UserID, &item.Email, &item.Username, &item.Status, &item.GroupID, &item.GroupName); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupUserRepository) DeleteMember(ctx context.Context, groupID, userID int64) (bool, error) {
	result, err := r.exec(ctx, `DELETE FROM group_members WHERE group_id=$1 AND user_id=$2`, groupID, userID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (r *groupUserRepository) CountMemberships(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.queryRow(ctx, `SELECT COUNT(*) FROM group_members gm JOIN groups g ON g.id=gm.group_id AND g.deleted_at IS NULL WHERE gm.user_id=$1`, []any{userID}, &count)
	return count, err
}

func (r *groupUserRepository) DisableGroupAPIKeys(ctx context.Context, userID, groupID int64) (int64, error) {
	result, err := r.exec(ctx, `UPDATE api_keys SET status='disabled', updated_at=NOW()
		WHERE user_id=$1 AND group_id=$2 AND deleted_at IS NULL AND status='active'`, userID, groupID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r *groupUserRepository) SetUserStatus(ctx context.Context, userID int64, status string) (bool, error) {
	result, err := r.exec(ctx, `UPDATE users SET status=$2, updated_at=NOW() WHERE id=$1 AND role='user' AND deleted_at IS NULL`, userID, status)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

func (r *groupUserRepository) LockUsers(ctx context.Context, userIDs ...int64) error {
	ids := append([]int64(nil), userIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows, err := r.query(ctx, `SELECT id FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(ids))
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *groupUserRepository) NetGranted(ctx context.Context, groupID, memberID int64) (float64, error) {
	var net float64
	err := r.queryRow(ctx, `SELECT COALESCE(SUM(CASE WHEN direction='grant' THEN amount ELSE -amount END), 0)
		FROM group_balance_transfers WHERE group_id=$1 AND member_id=$2`, []any{groupID, memberID}, &net)
	return net, err
}

func (r *groupUserRepository) InsertTransfer(ctx context.Context, transfer *service.GroupBalanceTransfer) error {
	return r.queryRow(ctx, `INSERT INTO group_balance_transfers (group_id, manager_id, member_id, direction, amount, notes)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		[]any{transfer.GroupID, transfer.ManagerID, transfer.MemberID, transfer.Direction, transfer.Amount, transfer.Notes},
		&transfer.ID, &transfer.CreatedAt)
}

func (r *groupUserRepository) InsertAdjustmentRecord(ctx context.Context, record *service.RedeemCode) error {
	created, err := clientFromContext(ctx, r.client).RedeemCode.Create().
		SetCode(record.Code).
		SetType(record.Type).
		SetValue(record.Value).
		SetStatus(record.Status).
		SetNotes(record.Notes).
		SetNillableUsedBy(record.UsedBy).
		SetNillableUsedAt(record.UsedAt).
		Save(ctx)
	if err != nil {
		return err
	}
	record.ID = created.ID
	record.CreatedAt = created.CreatedAt
	return nil
}

func (r *groupUserRepository) ListTransfers(ctx context.Context, groupID, memberID int64, page, pageSize int) ([]service.GroupBalanceTransfer, int64, error) {
	where := `t.group_id=$1`
	args := []any{groupID}
	if memberID > 0 {
		where += ` AND t.member_id=$2`
		args = append(args, memberID)
	}
	var total int64
	if err := r.queryRow(ctx, `SELECT COUNT(*) FROM group_balance_transfers t WHERE `+where, args, &total); err != nil {
		return nil, 0, err
	}
	limitArg := len(args) + 1
	listArgs := append(append([]any(nil), args...), pageSize, (page-1)*pageSize)
	rows, err := r.query(ctx, `SELECT t.id, t.group_id, t.manager_id, COALESCE(NULLIF(mu.username,''), mu.email, ''),
		t.member_id, COALESCE(NULLIF(uu.username,''), uu.email, ''), t.direction, t.amount, t.notes, t.created_at
		FROM group_balance_transfers t
		LEFT JOIN users mu ON mu.id=t.manager_id
		LEFT JOIN users uu ON uu.id=t.member_id
		WHERE `+where+`
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT $`+strconv.Itoa(limitArg)+` OFFSET $`+strconv.Itoa(limitArg+1), listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.GroupBalanceTransfer, 0)
	for rows.Next() {
		var item service.GroupBalanceTransfer
		var managerID, memberID sql.NullInt64
		if err := rows.Scan(&item.ID, &item.GroupID, &managerID, &item.ManagerName, &memberID, &item.MemberName,
			&item.Direction, &item.Amount, &item.Notes, &item.CreatedAt); err != nil {
			return nil, 0, err
		}
		if managerID.Valid {
			item.ManagerID = &managerID.Int64
		}
		if memberID.Valid {
			item.MemberID = &memberID.Int64
		}
		out = append(out, item)
	}
	return out, total, rows.Err()
}

func (r *groupUserRepository) UserGroupSummaries(ctx context.Context, userIDs []int64) ([]service.UserGroupSummary, error) {
	byUser := make(map[int64]*service.UserGroupSummary, len(userIDs))
	for _, id := range userIDs {
		byUser[id] = &service.UserGroupSummary{UserID: id, ManagedGroups: []service.GroupRef{}}
	}

	owned, err := r.query(ctx, `SELECT gm.user_id, g.id, g.name, COALESCE(g.category,'')
		FROM group_members gm JOIN groups g ON g.id=gm.group_id AND g.deleted_at IS NULL
		WHERE gm.owned AND gm.user_id = ANY($1)`, pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	for owned.Next() {
		var userID int64
		var ref service.GroupRef
		if err := owned.Scan(&userID, &ref.ID, &ref.Name, &ref.Category); err != nil {
			_ = owned.Close()
			return nil, err
		}
		if item, ok := byUser[userID]; ok {
			item.OwnedGroup = &ref
		}
	}
	if err := owned.Err(); err != nil {
		_ = owned.Close()
		return nil, err
	}
	_ = owned.Close()

	managed, err := r.query(ctx, `SELECT mg.user_id, g.id, g.name, COALESCE(g.category,'')
		FROM group_managers mg JOIN groups g ON g.id=mg.group_id AND g.deleted_at IS NULL
		WHERE mg.user_id = ANY($1) ORDER BY g.id`, pq.Array(userIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = managed.Close() }()
	for managed.Next() {
		var userID int64
		var ref service.GroupRef
		if err := managed.Scan(&userID, &ref.ID, &ref.Name, &ref.Category); err != nil {
			return nil, err
		}
		if item, ok := byUser[userID]; ok {
			item.ManagedGroups = append(item.ManagedGroups, ref)
		}
	}
	if err := managed.Err(); err != nil {
		return nil, err
	}

	out := make([]service.UserGroupSummary, 0, len(userIDs))
	for _, id := range userIDs {
		out = append(out, *byUser[id])
	}
	return out, nil
}
