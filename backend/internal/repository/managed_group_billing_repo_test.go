package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 额度组的实际扣费与组内 5h / 7d 用量在同一个落账事务里累加。
func TestApplyUsageBillingEffectsAccumulatesQuotaGroupMemberUsage(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`(?s)UPDATE users\s+SET balance = balance - \$1`).
		WithArgs(1.25, int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(8.75))
	mock.ExpectExec(`(?s)UPDATE group_members SET\s+usage_5h_usd = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start \+ INTERVAL '5 hours' <= NOW\(\) THEN \$1 ELSE usage_5h_usd \+ \$1 END,`+
		`.*window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start \+ INTERVAL '7 days' <= NOW\(\) THEN date_trunc\('day', NOW\(\)\) ELSE window_7d_start END,`+
		`.*WHERE group_id = \$2 AND user_id = \$3`).
		WithArgs(1.25, int64(9), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result := &service.UsageBillingApplyResult{Applied: true}
	err = (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, &service.UsageBillingCommand{
		UserID:          42,
		BalanceCost:     1.25,
		GroupID:         9,
		GroupMemberCost: 1.25,
	}, result)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// 成员已被移出分组时没有可更新的行：余额照常扣，组内用量跳过，不报错。
func TestApplyUsageBillingEffectsIgnoresMissingGroupMember(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectExec(`UPDATE group_members SET`).
		WithArgs(0.5, int64(9), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	err = (&usageBillingRepository{}).applyUsageBillingEffects(ctx, tx, &service.UsageBillingCommand{
		UserID:          42,
		GroupID:         9,
		GroupMemberCost: 0.5,
	}, &service.UsageBillingApplyResult{Applied: true})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

// 认证快照必须带上分组类型：订阅组免扣费、额度组累计用量都依赖它。
func TestAPIKeyRepository_GetByKeyForAuth_CarriesManagedGroupType_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-managed-type@test.com")

	group, err := client.Group.Create().
		SetName("g-auth-managed-type").
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1).
		SetIsExclusive(true).
		SetKind(service.GroupKindManaged).
		SetCategory(service.GroupCategoryTeam).
		SetManagedType(service.ManagedGroupTypeSubscription).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-managed-type",
		Name:    "Managed Type Key",
		GroupID: &group.ID,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.Equal(t, service.GroupKindManaged, got.Group.Kind)
	require.Equal(t, service.ManagedGroupTypeSubscription, got.Group.ManagedType)
	require.True(t, got.Group.IsManagedSubscription())
}

func TestGroupManagementSQLDerivesAllocationFromManagedType(t *testing.T) {
	require.Equal(t, "(CASE WHEN g.kind='managed' THEN (CASE WHEN g.managed_type='quota' THEN 'auto' ELSE 'manual' END) ELSE COALESCE(s.allocation_mode,'auto') END)", effectiveModeSQL)
	require.Regexp(t, regexp.MustCompile(`COALESCE\(g\.managed_type,''\)`), groupListSQL)
}
