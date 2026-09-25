package repository

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGatewayAdmissionManualAtomicReservationAndRelease(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "allocation_mode"}).AddRow(true, "manual"))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gm.max_concurrent").WithArgs(int64(7), int64(3)).
		WillReturnRows(memberAdmissionRows().AddRow(1, 2, 0, 0, 0, 0, 0))
	mock.ExpectQuery("SELECT gaa.account_id FROM group_account_assignments").WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(11))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM group_member_leases").WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE group_members SET daily_used=").WithArgs(int64(7), int64(3), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO group_member_leases").WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectCommit()
	mock.ExpectExec("DELETE FROM group_member_leases WHERE id=").WithArgs(int64(9)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	policy, release, err := (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.NoError(t, err)
	require.True(t, service.GroupAccountAllowed(service.WithGatewayAllocation(context.Background(), policy), &policy.GroupID, 11))
	require.False(t, service.GroupAccountAllowed(service.WithGatewayAllocation(context.Background(), policy), &policy.GroupID, 12))
	release()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGatewayAdmissionManualWithoutAssignmentsDoesNotSpendQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "mode"}).AddRow(true, "manual"))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gm.max_concurrent").WithArgs(int64(7), int64(3)).
		WillReturnRows(memberAdmissionRows().AddRow(1, 2, 0, 0, 0, 0, 0))
	mock.ExpectQuery("SELECT gaa.account_id").WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"account_id"}))
	mock.ExpectRollback()
	_, _, err = (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.ErrorIs(t, err, service.ErrGroupAccountRequired)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGatewayAdmissionExhaustedDailyQuota(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "mode"}).AddRow(true, "auto"))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT gm.max_concurrent").WithArgs(int64(7), int64(3)).
		WillReturnRows(memberAdmissionRows().AddRow(1, 2, 2, 0, 0, 0, 0))
	mock.ExpectRollback()
	_, _, err = (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.ErrorIs(t, err, service.ErrGroupQuotaExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGatewayAdmissionSettingsForceManagedGroups(t *testing.T) {
	// 管理分组即使缺少设置行也必须按管控准入（fail-closed）；分配方式由类型决定，
	// 额度组自动调度，订阅组（以及未知类型）手动分配。
	require.Contains(t, gatewayAdmissionSettingsSQL, "g.kind='managed' OR COALESCE(s.enabled,false)")
	require.Contains(t, gatewayAdmissionSettingsSQL, "CASE WHEN g.kind='managed' THEN (CASE WHEN g.managed_type='quota' THEN 'auto' ELSE 'manual' END) ELSE COALESCE(s.allocation_mode,'auto') END")
	require.Contains(t, gatewayAdmissionSettingsSQL, "FROM groups g LEFT JOIN group_management_settings s")
}

// memberAdmissionRows 成员准入查询的列：并发、日请求上限、日已用、5h / 7d 美元上限与有效用量。
func memberAdmissionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"max_concurrent", "daily_limit", "daily_used", "limit_5h_usd", "limit_7d_usd", "usage_5h_usd", "usage_7d_usd"})
}

func TestGatewayAdmissionQuotaGroupUSDLimits(t *testing.T) {
	cases := []struct {
		name string
		row  []driverValue
		want error
	}{
		{"5h limit reached", []driverValue{1, 0, 0, 5.0, 0.0, 5.0, 5.0}, service.ErrGroupMemberUsage5hExceeded},
		{"7d limit reached", []driverValue{1, 0, 0, 5.0, 20.0, 1.0, 20.5}, service.ErrGroupMemberUsage7dExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "mode"}).AddRow(true, "auto"))
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT gm.max_concurrent")+"(?s).*"+regexp.QuoteMeta(groupMemberUsage5hSQL)).
				WithArgs(int64(7), int64(3)).
				WillReturnRows(memberAdmissionRows().AddRow(tc.row...))
			mock.ExpectRollback()
			_, _, err = (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
			require.ErrorIs(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestGatewayAdmissionQuotaGroupWithinUSDLimits(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "mode"}).AddRow(true, "auto"))
	mock.ExpectBegin()
	// 过期窗口的用量由 SQL 视为 0；这里模拟窗口内用量低于上限
	mock.ExpectQuery("SELECT gm.max_concurrent").WithArgs(int64(7), int64(3)).
		WillReturnRows(memberAdmissionRows().AddRow(1, 0, 0, 5.0, 20.0, 4.99, 19.0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM group_member_leases")).WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec("UPDATE group_members SET daily_used=").WithArgs(int64(7), int64(3), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO group_member_leases").WithArgs(int64(7), int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(9))
	mock.ExpectCommit()
	policy, release, err := (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.NoError(t, err)
	require.Nil(t, policy.AllowedIDs, "quota groups schedule from the whole pool")
	mock.ExpectExec("DELETE FROM group_member_leases WHERE id=").WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
	release()
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGroupMemberUsageWindowSQL(t *testing.T) {
	// 与 API Key 限额一致：窗口过期视为 0
	require.Equal(t, "CASE WHEN gm.window_5h_start IS NOT NULL AND gm.window_5h_start + INTERVAL '5 hours' <= NOW() THEN 0 ELSE gm.usage_5h_usd END", groupMemberUsage5hSQL)
	require.Equal(t, "CASE WHEN gm.window_7d_start IS NOT NULL AND gm.window_7d_start + INTERVAL '7 days' <= NOW() THEN 0 ELSE gm.usage_7d_usd END", groupMemberUsage7dSQL)
}

func TestGatewayAdmissionPassesThroughWhenEnforcementDisabled(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(gatewayAdmissionSettingsSQL)).WithArgs(int64(3)).
		WillReturnRows(sqlmock.NewRows([]string{"enabled", "mode"}).AddRow(false, "auto"))
	policy, release, err := (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.NoError(t, err)
	require.False(t, policy.Enabled)
	release()
	require.NoError(t, mock.ExpectationsWereMet())
}

type driverValue = driver.Value
