package repository

import (
	"context"
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
		WillReturnRows(sqlmock.NewRows([]string{"max_concurrent", "daily_limit", "daily_used"}).AddRow(1, 2, 0))
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
		WillReturnRows(sqlmock.NewRows([]string{"max", "limit", "used"}).AddRow(1, 2, 0))
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
		WillReturnRows(sqlmock.NewRows([]string{"max", "limit", "used"}).AddRow(1, 2, 2))
	mock.ExpectRollback()
	_, _, err = (&groupManagementRepository{db: db}).GatewayAdmission(context.Background(), 7, 3, false, true)
	require.ErrorIs(t, err, service.ErrGroupQuotaExceeded)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGatewayAdmissionSettingsForceManagedGroups(t *testing.T) {
	// 管理分组即使缺少设置行也必须按管控准入（fail-closed），默认手动分配。
	require.Contains(t, gatewayAdmissionSettingsSQL, "g.kind='managed' OR COALESCE(s.enabled,false)")
	require.Contains(t, gatewayAdmissionSettingsSQL, "CASE WHEN g.kind='managed' THEN 'manual' ELSE 'auto' END")
	require.Contains(t, gatewayAdmissionSettingsSQL, "FROM groups g LEFT JOIN group_management_settings s")
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
