package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

type groupManagementRepository struct{ db *sql.DB }

func NewGroupManagementRepository(db *sql.DB) service.GroupManagementRepository {
	return &groupManagementRepository{db: db}
}

func (r *groupManagementRepository) resetDailyWindows(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `UPDATE group_members SET daily_used = 0, daily_window_start = CURRENT_DATE, updated_at = NOW() WHERE daily_window_start < CURRENT_DATE`)
	return err
}

func (r *groupManagementRepository) Overview(ctx context.Context, userID int64, role string) (*service.GroupManagementOverview, error) {
	groups, err := r.listOverviewGroups(ctx, userID, role)
	if err != nil {
		return nil, err
	}
	members, err := r.ListMembers(ctx, 0, &userID)
	if err != nil {
		return nil, err
	}
	assignments, err := r.ListAccounts(ctx, 0, &userID)
	if err != nil {
		return nil, err
	}
	for i := range groups {
		if !groups[i].Manager {
			groups[i].MemberCount = 0
			groups[i].AccountCount = 0
			groups[i].MaxConcurrent = 0
			groups[i].DailyLimit = 0
		}
	}
	out := &service.GroupManagementOverview{Role: role, Groups: groups, Memberships: members, Assignments: assignments, Accounts: make([]service.AssignedAccount, 0)}
	if err := r.db.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).Scan(&out.Balance); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if role != service.RoleUser {
		for _, group := range groups {
			if !group.Manager {
				continue
			}
			pool, err := r.ListAccountPool(ctx, group.ID)
			if err != nil {
				return nil, err
			}
			out.Accounts = append(out.Accounts, pool...)
		}
	}
	return out, nil
}

// 管理分组（kind=managed）永远开启管控，分配方式由类型决定（额度组自动、订阅组手动）；
// 渠道分组按设置表，缺省为关闭 / 自动。
const (
	effectiveEnabledSQL = `(g.kind='managed' OR COALESCE(s.enabled,false))`
	effectiveModeSQL    = `(CASE WHEN g.kind='managed' THEN (CASE WHEN g.managed_type='quota' THEN 'auto' ELSE 'manual' END) ELSE COALESCE(s.allocation_mode,'auto') END)`
)

const groupListSQL = `SELECT g.id, g.name, g.kind, COALESCE(g.category,''), COALESCE(g.managed_type,''),
	(SELECT COUNT(*) FROM group_members gm JOIN users u ON u.id=gm.user_id WHERE gm.group_id=g.id AND u.deleted_at IS NULL),
	(SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=g.id AND a.deleted_at IS NULL),
	%s, ` + effectiveEnabledSQL + `, ` + effectiveModeSQL + `, COALESCE(s.max_concurrent,1), COALESCE(s.daily_limit,0),
	ARRAY(SELECT x.user_id FROM group_managers x JOIN users u ON u.id=x.user_id WHERE x.group_id=g.id AND u.role='group_manager' AND u.status='active' AND u.deleted_at IS NULL ORDER BY x.user_id)
	FROM groups g LEFT JOIN group_management_settings s ON s.group_id=g.id
	WHERE g.deleted_at IS NULL AND (%s) ORDER BY g.id`

func (r *groupManagementRepository) listOverviewGroups(ctx context.Context, userID int64, role string) ([]service.ManagedGroup, error) {
	predicate := "TRUE"
	manager := "TRUE"
	args := []any{}
	switch role {
	case service.RoleGroupManager:
		predicate = `EXISTS (SELECT 1 FROM group_managers x JOIN users u ON u.id=x.user_id WHERE x.group_id=g.id AND x.user_id=$1 AND u.role='group_manager' AND u.status='active' AND u.deleted_at IS NULL) OR EXISTS (SELECT 1 FROM group_members x WHERE x.group_id=g.id AND x.user_id=$1)`
		manager = `EXISTS (SELECT 1 FROM group_managers x WHERE x.group_id=g.id AND x.user_id=$1)`
		args = append(args, userID)
	case service.RoleUser:
		predicate = `EXISTS (SELECT 1 FROM group_members x JOIN users u ON u.id=x.user_id WHERE x.group_id=g.id AND x.user_id=$1 AND u.status='active' AND u.deleted_at IS NULL)`
		manager = "FALSE"
		args = append(args, userID)
	}
	return r.queryGroups(ctx, fmt.Sprintf(groupListSQL, manager, predicate), args...)
}

func (r *groupManagementRepository) queryGroups(ctx context.Context, query string, args ...any) ([]service.ManagedGroup, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.ManagedGroup, 0)
	for rows.Next() {
		var item service.ManagedGroup
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.Category, &item.ManagedType, &item.MemberCount, &item.AccountCount, &item.Manager, &item.Enabled, &item.AllocationMode, &item.MaxConcurrent, &item.DailyLimit, pq.Array(&item.ManagerUserIDs)); err != nil {
			return nil, err
		}
		if !item.Manager {
			item.ManagerUserIDs = []int64{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupManagementRepository) CanAccessGroup(ctx context.Context, actorID int64, role string, groupID int64) (bool, error) {
	query := `SELECT EXISTS (SELECT 1 FROM group_members m JOIN groups g ON g.id=m.group_id JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.group_id=$2 AND g.deleted_at IS NULL AND u.deleted_at IS NULL AND u.status='active')`
	if role == service.RoleGroupManager {
		query = `SELECT EXISTS (SELECT 1 FROM group_managers m JOIN groups g ON g.id=m.group_id JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND m.group_id=$2 AND g.deleted_at IS NULL AND u.deleted_at IS NULL AND u.status='active' AND u.role='group_manager')`
	}
	var allowed bool
	err := r.db.QueryRowContext(ctx, query, actorID, groupID).Scan(&allowed)
	return allowed, err
}

func (r *groupManagementRepository) ListMembers(ctx context.Context, groupID int64, userID *int64) ([]service.GroupMembership, error) {
	if err := r.resetDailyWindows(ctx); err != nil {
		return nil, err
	}
	// 停用的组用户也要列出来，组管理员才能重新启用；余额只对管理分组返回。
	query := `SELECT gm.user_id, gm.group_id, g.name, u.email, COALESCE(u.username,''), gm.owned, u.status,
		CASE WHEN g.kind='managed' THEN u.balance END,
		CASE WHEN g.kind='managed' THEN GREATEST(LEAST(u.balance, COALESCE((SELECT SUM(CASE WHEN t.direction='grant' THEN t.amount ELSE -t.amount END)
			FROM group_balance_transfers t WHERE t.group_id=gm.group_id AND t.member_id=gm.user_id), 0)), 0) END,
		gm.max_concurrent, gm.daily_limit, gm.daily_used, gm.daily_window_start,
		gm.limit_5h_usd, gm.limit_7d_usd, ` + groupMemberUsage5hSQL + `, ` + groupMemberUsage7dSQL + `,
		CASE WHEN gm.window_5h_start IS NULL OR gm.window_5h_start + INTERVAL '5 hours' <= NOW() THEN NULL ELSE gm.window_5h_start + INTERVAL '5 hours' END,
		CASE WHEN gm.window_7d_start IS NULL OR gm.window_7d_start + INTERVAL '7 days' <= NOW() THEN NULL ELSE gm.window_7d_start + INTERVAL '7 days' END
		FROM group_members gm JOIN groups g ON g.id=gm.group_id JOIN users u ON u.id=gm.user_id
		WHERE g.deleted_at IS NULL AND u.deleted_at IS NULL`
	args := make([]any, 0, 2)
	if groupID > 0 {
		query += " AND gm.group_id=$1"
		args = append(args, groupID)
	}
	if userID != nil {
		query += fmt.Sprintf(" AND gm.user_id=$%d", len(args)+1)
		args = append(args, *userID)
	}
	query += " ORDER BY gm.user_id"
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.GroupMembership, 0)
	for rows.Next() {
		var item service.GroupMembership
		var balance, reclaimable sql.NullFloat64
		var reset5h, reset7d sql.NullTime
		if err := rows.Scan(&item.UserID, &item.GroupID, &item.GroupName, &item.Email, &item.Username, &item.Owned, &item.Status, &balance, &reclaimable,
			&item.MaxConcurrent, &item.DailyLimit, &item.DailyUsed, &item.DailyWindowStart,
			&item.Limit5hUSD, &item.Limit7dUSD, &item.Usage5hUSD, &item.Usage7dUSD, &reset5h, &reset7d); err != nil {
			return nil, err
		}
		if balance.Valid {
			item.Balance = &balance.Float64
		}
		if reclaimable.Valid {
			item.Reclaimable = &reclaimable.Float64
		}
		if reset5h.Valid {
			item.Reset5hAt = &reset5h.Time
		}
		if reset7d.Valid {
			item.Reset7dAt = &reset7d.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupManagementRepository) ListAccounts(ctx context.Context, groupID int64, userID *int64) ([]service.AssignedAccount, error) {
	if err := r.resetDailyWindows(ctx); err != nil {
		return nil, err
	}
	query := `SELECT x.group_id,x.id,x.name,x.platform,x.status,x.user_id,x.assignment_mode,x.remaining_quota FROM (
		SELECT gaa.group_id,a.id,a.name,a.platform,a.status,gaa.user_id,gaa.assignment_mode,
		CASE WHEN gm.daily_limit=0 THEN NULL ELSE GREATEST(gm.daily_limit-gm.daily_used,0) END AS remaining_quota
		FROM group_account_assignments gaa JOIN accounts a ON a.id=gaa.account_id
		JOIN groups g ON g.id=gaa.group_id JOIN users u ON u.id=gaa.user_id
		JOIN group_members gm ON gm.group_id=gaa.group_id AND gm.user_id=gaa.user_id
		LEFT JOIN group_management_settings s ON s.group_id=gaa.group_id
		WHERE gaa.status='active' AND ` + effectiveEnabledSQL + ` AND ` + effectiveModeSQL + `='manual' AND a.deleted_at IS NULL AND g.deleted_at IS NULL AND u.deleted_at IS NULL AND u.status='active'
		UNION ALL
		SELECT ag.group_id,a.id,a.name,a.platform,a.status,gm.user_id,'auto' AS assignment_mode,
		CASE WHEN gm.daily_limit=0 THEN NULL ELSE GREATEST(gm.daily_limit-gm.daily_used,0) END AS remaining_quota
		FROM account_groups ag JOIN accounts a ON a.id=ag.account_id
		JOIN groups g ON g.id=ag.group_id LEFT JOIN group_management_settings s ON s.group_id=ag.group_id
		JOIN group_members gm ON gm.group_id=ag.group_id JOIN users u ON u.id=gm.user_id
		WHERE ` + effectiveEnabledSQL + ` AND ` + effectiveModeSQL + `='auto' AND a.deleted_at IS NULL AND g.deleted_at IS NULL AND u.deleted_at IS NULL AND u.status='active'
	) x WHERE TRUE`
	args := make([]any, 0, 2)
	if groupID > 0 {
		query += " AND x.group_id=$1"
		args = append(args, groupID)
	}
	if userID != nil {
		query += fmt.Sprintf(" AND x.user_id=$%d", len(args)+1)
		args = append(args, *userID)
	}
	query += " ORDER BY x.id, x.user_id"
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.AssignedAccount, 0)
	for rows.Next() {
		var item service.AssignedAccount
		var remaining sql.NullInt64
		if err := rows.Scan(&item.GroupID, &item.ID, &item.Name, &item.Platform, &item.Status, &item.UserID, &item.AssignmentMode, &remaining); err != nil {
			return nil, err
		}
		if remaining.Valid {
			item.RemainingQuota = &remaining.Int64
		}
		item.AssignedUserIDs = []int64{item.UserID}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupManagementRepository) ListAccountPool(ctx context.Context, groupID int64) ([]service.AssignedAccount, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT ag.group_id,a.id,a.name,a.platform,a.status,
		ARRAY(SELECT gaa.user_id FROM group_account_assignments gaa JOIN users u ON u.id=gaa.user_id WHERE gaa.group_id=ag.group_id AND gaa.account_id=ag.account_id AND gaa.status='active' AND u.status='active' AND u.deleted_at IS NULL ORDER BY gaa.user_id),
		`+effectiveModeSQL+`
		FROM account_groups ag JOIN accounts a ON a.id=ag.account_id JOIN groups g ON g.id=ag.group_id
		LEFT JOIN group_management_settings s ON s.group_id=ag.group_id
		WHERE ag.group_id=$1 AND a.deleted_at IS NULL AND g.deleted_at IS NULL ORDER BY a.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.AssignedAccount, 0)
	for rows.Next() {
		var item service.AssignedAccount
		if err := rows.Scan(&item.GroupID, &item.ID, &item.Name, &item.Platform, &item.Status, pq.Array(&item.AssignedUserIDs), &item.AssignmentMode); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *groupManagementRepository) GetSettings(ctx context.Context, groupID int64) (*service.GroupSettings, error) {
	var item service.GroupSettings
	err := r.db.QueryRowContext(ctx, `SELECT g.id, g.kind='managed', COALESCE(g.managed_type,''), `+effectiveEnabledSQL+`, `+effectiveModeSQL+`, COALESCE(s.max_concurrent,1), COALESCE(s.daily_limit,0),
		COALESCE(s.default_limit_5h_usd,0), COALESCE(s.default_limit_7d_usd,0)
		FROM groups g LEFT JOIN group_management_settings s ON s.group_id=g.id WHERE g.id=$1 AND g.deleted_at IS NULL`, groupID).
		Scan(&item.GroupID, &item.Managed, &item.ManagedType, &item.Enabled, &item.AllocationMode, &item.MaxConcurrent, &item.DailyLimit, &item.DefaultLimit5hUSD, &item.DefaultLimit7dUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrGroupManagementBadInput
	}
	return &item, err
}

func (r *groupManagementRepository) UpdateSettings(ctx context.Context, item service.GroupSettings) (*service.GroupSettings, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, item.GroupID); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO group_management_settings(group_id,enabled,allocation_mode,max_concurrent,daily_limit,default_limit_5h_usd,default_limit_7d_usd) VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(group_id) DO UPDATE SET enabled=EXCLUDED.enabled,allocation_mode=EXCLUDED.allocation_mode,max_concurrent=EXCLUDED.max_concurrent,daily_limit=EXCLUDED.daily_limit,
		default_limit_5h_usd=EXCLUDED.default_limit_5h_usd,default_limit_7d_usd=EXCLUDED.default_limit_7d_usd,updated_at=NOW()`,
		item.GroupID, item.Enabled, item.AllocationMode, item.MaxConcurrent, item.DailyLimit, item.DefaultLimit5hUSD, item.DefaultLimit7dUSD)
	if err != nil {
		return nil, err
	}
	if item.AllocationMode == service.GroupAssignmentModeAuto {
		if _, err := tx.ExecContext(ctx, `DELETE FROM group_account_assignments WHERE group_id=$1`, item.GroupID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func lockGroup(ctx context.Context, tx *sql.Tx, groupID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM groups WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, groupID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrGroupManagementBadInput
	}
	return err
}

func (r *groupManagementRepository) AddMember(ctx context.Context, groupID, userID int64) (*service.GroupMembership, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return nil, err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL)`, userID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, service.ErrGroupManagementBadInput
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO group_members(user_id,group_id,max_concurrent,daily_limit,limit_5h_usd,limit_7d_usd)
		SELECT $1,$2,COALESCE(s.max_concurrent,1),COALESCE(s.daily_limit,0),COALESCE(s.default_limit_5h_usd,0),COALESCE(s.default_limit_7d_usd,0)
		FROM groups g LEFT JOIN group_management_settings s ON s.group_id=g.id WHERE g.id=$2 ON CONFLICT(user_id,group_id) DO NOTHING`, userID, groupID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	items, err := r.ListMembers(ctx, groupID, &userID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, service.ErrGroupManagementBadInput
	}
	return &items[0], nil
}

func (r *groupManagementRepository) RemoveMember(ctx context.Context, groupID, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM group_members gm USING users u WHERE gm.group_id=$1 AND gm.user_id=$2 AND u.id=gm.user_id AND u.deleted_at IS NULL`, groupID, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrGroupManagementBadInput
	}
	return tx.Commit()
}

func (r *groupManagementRepository) UpdateMemberLimit(ctx context.Context, groupID, userID int64, input service.GroupMemberLimitInput) (*service.GroupMembership, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return nil, err
	}
	// 美元上限为 NULL 表示不修改
	result, err := tx.ExecContext(ctx, `UPDATE group_members gm SET max_concurrent=$3,daily_limit=$4,
		limit_5h_usd=COALESCE($5::numeric, gm.limit_5h_usd),limit_7d_usd=COALESCE($6::numeric, gm.limit_7d_usd),updated_at=NOW()
		FROM users u WHERE gm.group_id=$1 AND gm.user_id=$2 AND u.id=gm.user_id AND u.status='active' AND u.deleted_at IS NULL`,
		groupID, userID, input.MaxConcurrent, input.DailyLimit, input.Limit5hUSD, input.Limit7dUSD)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, service.ErrGroupManagementBadInput
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	items, err := r.ListMembers(ctx, groupID, &userID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, service.ErrGroupManagementBadInput
	}
	return &items[0], nil
}

func (r *groupManagementRepository) AssignAccounts(ctx context.Context, groupID, userID int64, accountIDs []int64, mode string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM group_members gm JOIN users u ON u.id=gm.user_id WHERE gm.group_id=$1 AND gm.user_id=$2 AND u.status='active' AND u.deleted_at IS NULL)`, groupID, userID).Scan(&active); err != nil {
		return err
	}
	if !active {
		return service.ErrGroupManagementBadInput
	}
	var allocationMode string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT allocation_mode FROM group_management_settings WHERE group_id=$1),'auto')`, groupID).Scan(&allocationMode); err != nil {
		return err
	}
	if mode == service.GroupAssignmentModeManual && allocationMode != mode {
		return service.ErrGroupManagementBadInput
	}
	unique := make(map[int64]struct{}, len(accountIDs))
	clean := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		if _, ok := unique[id]; !ok {
			unique[id] = struct{}{}
			clean = append(clean, id)
		}
	}
	if mode == service.GroupAssignmentModeManual && len(clean) > 0 {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_groups ag JOIN accounts a ON a.id=ag.account_id WHERE ag.group_id=$1 AND ag.account_id=ANY($2) AND a.deleted_at IS NULL`, groupID, pq.Array(clean)).Scan(&count); err != nil {
			return err
		}
		if count != len(clean) {
			return service.ErrAccountNotInGroup
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_account_assignments WHERE group_id=$1 AND user_id=$2`, groupID, userID); err != nil {
		return err
	}
	if mode == service.GroupAssignmentModeManual {
		for _, id := range clean {
			if _, err := tx.ExecContext(ctx, `INSERT INTO group_account_assignments(group_id,account_id,user_id,assignment_mode) VALUES($1,$2,$3,'manual')`, groupID, id, userID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *groupManagementRepository) RevokeAccount(ctx context.Context, groupID, userID, accountID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM group_account_assignments WHERE group_id=$1 AND user_id=$2 AND account_id=$3`, groupID, userID, accountID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrAccountNotInGroup
	}
	return tx.Commit()
}

func (r *groupManagementRepository) AdminListGroups(ctx context.Context) ([]service.ManagedGroup, error) {
	return r.queryGroups(ctx, fmt.Sprintf(groupListSQL, `EXISTS(SELECT 1 FROM group_managers x JOIN users u ON u.id=x.user_id WHERE x.group_id=g.id AND u.role='group_manager' AND u.status='active' AND u.deleted_at IS NULL)`, "TRUE"))
}
func (r *groupManagementRepository) AdminListUsers(ctx context.Context) ([]service.GroupManagementUser, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT u.id,u.email,COALESCE(u.username,''),u.role,
		(SELECT COUNT(*) FROM group_members gm JOIN groups g ON g.id=gm.group_id WHERE gm.user_id=u.id AND g.deleted_at IS NULL),
		(SELECT COUNT(*) FROM group_account_assignments gaa JOIN groups g ON g.id=gaa.group_id JOIN accounts a ON a.id=gaa.account_id WHERE gaa.user_id=u.id AND gaa.status='active' AND g.deleted_at IS NULL AND a.deleted_at IS NULL)
		FROM users u WHERE u.deleted_at IS NULL ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.GroupManagementUser, 0)
	for rows.Next() {
		var x service.GroupManagementUser
		if err := rows.Scan(&x.ID, &x.Email, &x.Username, &x.Role, &x.MemberCount, &x.AssignmentCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (r *groupManagementRepository) AddManager(ctx context.Context, groupID, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO group_managers(user_id,group_id) SELECT id,$2 FROM users WHERE id=$1 AND role='group_manager' AND status='active' AND deleted_at IS NULL ON CONFLICT DO NOTHING`, userID, groupID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM group_managers gm JOIN users u ON u.id=gm.user_id WHERE gm.group_id=$1 AND gm.user_id=$2 AND u.role='group_manager' AND u.status='active' AND u.deleted_at IS NULL)`, groupID, userID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return service.ErrGroupManagementBadInput
		}
	}
	return tx.Commit()
}
func (r *groupManagementRepository) RemoveManager(ctx context.Context, groupID, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockGroup(ctx, tx, groupID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM group_managers gm USING users u WHERE gm.user_id=$1 AND gm.group_id=$2 AND u.id=gm.user_id AND u.deleted_at IS NULL`, userID, groupID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return service.ErrGroupManagementBadInput
	}
	return tx.Commit()
}
