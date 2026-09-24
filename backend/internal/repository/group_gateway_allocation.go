package repository

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// gatewayAdmissionSettingsSQL 读取分组的有效管控设置：管理分组即使缺少设置行也强制开启，
// 保证管理分组的准入失败即拒绝（fail-closed）。
const gatewayAdmissionSettingsSQL = `SELECT ` + effectiveEnabledSQL + `, ` + effectiveModeSQL + ` FROM groups g LEFT JOIN group_management_settings s ON s.group_id=g.id WHERE g.id=$1 AND g.deleted_at IS NULL`

func (r *groupManagementRepository) GatewayAdmission(ctx context.Context, userID, groupID int64, admin, consume bool) (service.GatewayAllocation, func(), error) {
	policy := service.GatewayAllocation{GroupID: groupID}
	noop := func() {}
	var enabled bool
	var mode string
	err := r.db.QueryRowContext(ctx, gatewayAdmissionSettingsSQL, groupID).Scan(&enabled, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, noop, nil
	}
	if err != nil || !enabled {
		return policy, noop, err
	}
	policy.Enabled = true
	if admin {
		return policy, noop, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return policy, noop, err
	}
	defer func() { _ = tx.Rollback() }()
	var maxConcurrent int
	var dailyLimit, dailyUsed int64
	err = tx.QueryRowContext(ctx, `SELECT gm.max_concurrent, gm.daily_limit,
		CASE WHEN gm.daily_window_start < CURRENT_DATE THEN 0 ELSE gm.daily_used END
		FROM group_members gm JOIN users u ON u.id=gm.user_id JOIN groups g ON g.id=gm.group_id
		WHERE gm.user_id=$1 AND gm.group_id=$2 AND u.status='active' AND u.deleted_at IS NULL AND g.deleted_at IS NULL FOR UPDATE OF gm`, userID, groupID).
		Scan(&maxConcurrent, &dailyLimit, &dailyUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, noop, service.ErrGroupMemberRequired
	}
	if err != nil {
		return policy, noop, err
	}
	if mode == service.GroupAssignmentModeManual {
		policy.AllowedIDs = make(map[int64]struct{})
		rows, err := tx.QueryContext(ctx, `SELECT gaa.account_id FROM group_account_assignments gaa
			JOIN account_groups ag ON ag.account_id=gaa.account_id AND ag.group_id=gaa.group_id
			JOIN accounts a ON a.id=gaa.account_id
			WHERE gaa.user_id=$1 AND gaa.group_id=$2 AND gaa.status='active' AND a.status='active' AND a.deleted_at IS NULL`, userID, groupID)
		if err != nil {
			return policy, noop, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return policy, noop, err
			}
			policy.AllowedIDs[id] = struct{}{}
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return policy, noop, err
		}
		if consume && len(policy.AllowedIDs) == 0 {
			return policy, noop, service.ErrGroupAccountRequired
		}
	}
	if !consume {
		return policy, noop, nil
	}
	if dailyLimit > 0 && dailyUsed >= dailyLimit {
		return policy, noop, service.ErrGroupQuotaExceeded
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_member_leases WHERE user_id=$1 AND group_id=$2 AND expires_at>NOW()`, userID, groupID).Scan(&active); err != nil {
		return policy, noop, err
	}
	if active >= maxConcurrent {
		return policy, noop, service.ErrGroupConcurrencyExceeded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_members SET daily_used=$3+1, daily_window_start=CURRENT_DATE, updated_at=NOW() WHERE user_id=$1 AND group_id=$2`, userID, groupID, dailyUsed); err != nil {
		return policy, noop, err
	}
	var leaseID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO group_member_leases(user_id,group_id,expires_at) VALUES($1,$2,NOW()+INTERVAL '4 hours') RETURNING id`, userID, groupID).Scan(&leaseID); err != nil {
		return policy, noop, err
	}
	if err := tx.Commit(); err != nil {
		return policy, noop, err
	}
	return policy, func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := r.db.ExecContext(releaseCtx, `DELETE FROM group_member_leases WHERE id=$1`, leaseID); err != nil {
			slog.Warn("group request lease release failed", "lease_id", leaseID, "error", err)
		}
	}, nil
}
