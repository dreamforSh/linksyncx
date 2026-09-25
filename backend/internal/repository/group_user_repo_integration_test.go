//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 覆盖迁移 243 与组用户 / 余额划拨的完整事务：建组用户 → 初始划拨 → 回收 → 余额不足回滚 →
// 回收上限 → 停用 → 移除成员。依赖真实 Postgres（testcontainers）。
func TestGroupUserLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()

	manager := mustCreateUser(t, client, &service.User{
		Email:   fmt.Sprintf("gm-p3-manager-%d@example.com", suffix),
		Role:    service.RoleGroupManager,
		Balance: 20,
	})
	group := mustCreateGroup(t, client, &service.Group{
		Name:           fmt.Sprintf("gm-p3-group-%d", suffix),
		RateMultiplier: 1,
		IsExclusive:    true,
	})
	_, err := integrationDB.ExecContext(ctx, `UPDATE groups SET kind='managed', category='team' WHERE id=$1`, group.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO group_managers(user_id, group_id) VALUES ($1, $2)`, manager.ID, group.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO group_management_settings(group_id, enabled, allocation_mode, max_concurrent, daily_limit) VALUES ($1, TRUE, 'manual', 2, 50)`, group.ID)
	require.NoError(t, err)

	svc := service.NewGroupManagementService(NewGroupManagementRepository(integrationDB)).
		WithGroupUserDependencies(service.GroupUserDependencies{
			GroupUsers: NewGroupUserRepository(client),
			Users:      NewUserRepository(client, integrationDB),
			EntClient:  client,
		})

	balanceOf := func(userID int64) float64 {
		var balance float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance FROM users WHERE id=$1`, userID).Scan(&balance))
		return balance
	}
	countRows := func(query string, args ...any) int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, query, args...).Scan(&n))
		return n
	}

	member, err := svc.CreateGroupUser(ctx, manager.ID, service.RoleGroupManager, group.ID, service.CreateGroupUserInput{
		Email:         fmt.Sprintf("gm-p3-member-%d@example.com", suffix),
		Username:      "member",
		Password:      "secret-123",
		InitialAmount: 5,
	})
	require.NoError(t, err)
	require.True(t, member.Owned)
	require.Equal(t, 2, member.MaxConcurrent)
	require.Equal(t, int64(50), member.DailyLimit)
	require.NotNil(t, member.Balance)
	require.InDelta(t, 5, *member.Balance, 1e-9)
	require.InDelta(t, 15, balanceOf(manager.ID), 1e-9)

	var role string
	var restricted bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT role, restrict_public_groups FROM users WHERE id=$1`, member.UserID).Scan(&role, &restricted))
	require.Equal(t, service.RoleUser, role)
	require.True(t, restricted)
	require.Equal(t, 1, countRows(`SELECT COUNT(*) FROM user_allowed_groups WHERE user_id=$1 AND group_id=$2`, member.UserID, group.ID))
	require.Equal(t, 2, countRows(`SELECT COUNT(*) FROM redeem_codes WHERE type='group_transfer' AND used_by IN ($1, $2)`, manager.ID, member.UserID))

	_, err = svc.TransferBalance(ctx, manager.ID, service.RoleGroupManager, group.ID, member.UserID, service.GroupTransferReclaim, 2, "back")
	require.NoError(t, err)
	require.InDelta(t, 17, balanceOf(manager.ID), 1e-9)
	require.InDelta(t, 3, balanceOf(member.UserID), 1e-9)

	// 余额不足：两侧余额与流水整体回滚
	_, err = svc.TransferBalance(ctx, manager.ID, service.RoleGroupManager, group.ID, member.UserID, service.GroupTransferGrant, 100, "")
	require.ErrorIs(t, err, service.ErrGroupTransferInsufficient)
	require.InDelta(t, 17, balanceOf(manager.ID), 1e-9)
	require.InDelta(t, 3, balanceOf(member.UserID), 1e-9)

	// 成员自己充值的部分不能被回收：净划拨额只剩 3
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET balance = balance + 10 WHERE id=$1`, member.UserID)
	require.NoError(t, err)
	_, err = svc.TransferBalance(ctx, manager.ID, service.RoleGroupManager, group.ID, member.UserID, service.GroupTransferReclaim, 4, "")
	require.ErrorIs(t, err, service.ErrGroupTransferExceedsGranted)
	members, err := svc.Members(ctx, manager.ID, service.RoleGroupManager, group.ID)
	require.NoError(t, err)
	require.NotNil(t, members[0].Reclaimable)
	require.InDelta(t, 3, *members[0].Reclaimable, 1e-9)

	transfers, total, err := svc.ListTransfers(ctx, manager.ID, service.RoleGroupManager, group.ID, 0, 1, 20)
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Equal(t, service.GroupTransferReclaim, transfers[0].Direction)
	require.Equal(t, "member", transfers[0].MemberName)

	require.NoError(t, svc.SetGroupUserStatus(ctx, manager.ID, service.RoleGroupManager, group.ID, member.UserID, service.StatusDisabled))
	members, err = svc.Members(ctx, manager.ID, service.RoleGroupManager, group.ID)
	require.NoError(t, err)
	require.Len(t, members, 1)
	require.Equal(t, service.StatusDisabled, members[0].Status)

	require.NoError(t, svc.RemoveMember(ctx, manager.ID, service.RoleGroupManager, group.ID, member.UserID))
	require.Equal(t, 0, countRows(`SELECT COUNT(*) FROM group_members WHERE user_id=$1`, member.UserID))
	require.Equal(t, 0, countRows(`SELECT COUNT(*) FROM user_allowed_groups WHERE user_id=$1 AND group_id=$2`, member.UserID, group.ID))

	summary, err := svc.Summary(ctx, manager.ID, service.RoleGroupManager)
	require.NoError(t, err)
	require.Equal(t, int64(1), summary.ManagedGroupCount)
}

// 删除管理分组：软删除不会触发外键级联，组管理授权、成员、设置、专属倍率与兜底引用都要被清理，
// 只属于该分组的组用户被停用。
func TestDeleteManagedGroupCleansGroupManagementIntegration(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()

	manager := mustCreateUser(t, client, &service.User{
		Email: fmt.Sprintf("gm-del-manager-%d@example.com", suffix),
		Role:  service.RoleGroupManager,
	})
	group := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("gm-del-group-%d", suffix), RateMultiplier: 1, IsExclusive: true})
	other := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("gm-del-other-%d", suffix), RateMultiplier: 1})
	referrer := mustCreateGroup(t, client, &service.Group{Name: fmt.Sprintf("gm-del-referrer-%d", suffix), RateMultiplier: 1})
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{`UPDATE groups SET kind='managed', category='enterprise' WHERE id=$1`, []any{group.ID}},
		{`INSERT INTO group_managers(user_id, group_id) VALUES ($1, $2)`, []any{manager.ID, group.ID}},
		{`INSERT INTO group_management_settings(group_id, enabled, allocation_mode) VALUES ($1, TRUE, 'manual')`, []any{group.ID}},
		{`UPDATE groups SET fallback_group_id_on_invalid_request=$1 WHERE id=$2`, []any{group.ID, referrer.ID}},
	} {
		_, err := integrationDB.ExecContext(ctx, stmt.query, stmt.args...)
		require.NoError(t, err)
	}

	svc := service.NewGroupManagementService(NewGroupManagementRepository(integrationDB)).
		WithGroupUserDependencies(service.GroupUserDependencies{
			GroupUsers: NewGroupUserRepository(client),
			Users:      NewUserRepository(client, integrationDB),
			EntClient:  client,
		})
	orphan, err := svc.CreateGroupUser(ctx, manager.ID, service.RoleGroupManager, group.ID, service.CreateGroupUserInput{
		Email: fmt.Sprintf("gm-del-orphan-%d@example.com", suffix), Password: "secret-123",
	})
	require.NoError(t, err)
	shared, err := svc.CreateGroupUser(ctx, manager.ID, service.RoleGroupManager, group.ID, service.CreateGroupUserInput{
		Email: fmt.Sprintf("gm-del-shared-%d@example.com", suffix), Password: "secret-123",
	})
	require.NoError(t, err)
	// shared 同时属于另一个分组，删除后应保持启用
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO group_members(user_id, group_id) VALUES ($1, $2)`, shared.UserID, other.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO user_group_rate_multipliers(user_id, group_id, rate_multiplier) VALUES ($1, $2, 0.5)`, shared.UserID, group.ID)
	require.NoError(t, err)

	affected, err := NewGroupRepository(client, integrationDB).DeleteCascade(ctx, group.ID)
	require.NoError(t, err)
	require.Equal(t, []int64{orphan.UserID}, affected)

	countRows := func(query string, args ...any) int {
		var n int
		require.NoError(t, integrationDB.QueryRowContext(ctx, query, args...).Scan(&n))
		return n
	}
	for _, table := range []string{"group_managers", "group_members", "group_management_settings", "user_group_rate_multipliers", "user_allowed_groups"} {
		require.Zero(t, countRows(`SELECT COUNT(*) FROM `+table+` WHERE group_id=$1`, group.ID), table)
	}
	statusOf := func(userID int64) string {
		var status string
		require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM users WHERE id=$1`, userID).Scan(&status))
		return status
	}
	require.Equal(t, service.StatusDisabled, statusOf(orphan.UserID))
	require.Equal(t, service.StatusActive, statusOf(shared.UserID))
	require.Zero(t, countRows(`SELECT COUNT(*) FROM groups WHERE id=$1 AND fallback_group_id_on_invalid_request IS NOT NULL`, referrer.ID))

	// 组管理控制台、侧栏概况都不再出现已删除的分组
	overview, err := svc.Overview(ctx, manager.ID, service.RoleGroupManager)
	require.NoError(t, err)
	require.Empty(t, overview.Groups)
	summary, err := svc.Summary(ctx, manager.ID, service.RoleGroupManager)
	require.NoError(t, err)
	require.Zero(t, summary.ManagedGroupCount)
}
