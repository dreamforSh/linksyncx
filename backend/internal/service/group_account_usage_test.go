package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type accountUsageMgmtRepoStub struct {
	groupManagementRepoStub
	managedType string
	pool        []AssignedAccount
	assigned    []AssignedAccount
}

func (s *accountUsageMgmtRepoStub) GetSettings(_ context.Context, groupID int64) (*GroupSettings, error) {
	return &GroupSettings{GroupID: groupID, Managed: s.managedType != "", ManagedType: s.managedType, Enabled: true, MaxConcurrent: 1}, nil
}
func (s *accountUsageMgmtRepoStub) ListAccountPool(context.Context, int64) ([]AssignedAccount, error) {
	return s.pool, nil
}
func (s *accountUsageMgmtRepoStub) ListAccounts(_ context.Context, _ int64, userID *int64) ([]AssignedAccount, error) {
	out := make([]AssignedAccount, 0)
	for _, item := range s.assigned {
		if item.UserID == *userID {
			out = append(out, item)
		}
	}
	return out, nil
}

type groupAccountLoaderStub struct{ accounts map[int64]*Account }

func (s *groupAccountLoaderStub) GetByID(_ context.Context, id int64) (*Account, error) {
	if account, ok := s.accounts[id]; ok {
		return account, nil
	}
	return nil, ErrAccountNotFound
}
func (s *groupAccountLoaderStub) GetByIDs(_ context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if account, ok := s.accounts[id]; ok {
			out = append(out, account)
		}
	}
	return out, nil
}

type groupAccountUsageReaderStub struct{ usage map[int64]*UsageInfo }

func (s *groupAccountUsageReaderStub) GetUsageBatch(_ context.Context, ids []int64, _ bool) (map[int64]*UsageInfo, map[int64]string, error) {
	out := make(map[int64]*UsageInfo, len(ids))
	for _, id := range ids {
		if usage, ok := s.usage[id]; ok {
			out[id] = usage
		}
	}
	return out, map[int64]string{}, nil
}

type groupAccountQuotaStub struct {
	resets        []int64
	queries       []int64
	cachedCredits []int64
	postSnapshots []int64
}

func (s *groupAccountQuotaStub) QueryUsage(_ context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	s.queries = append(s.queries, accountID)
	return &OpenAIQuotaUsage{RateLimitResetCredits: &OpenAIRateLimitResetCredits{AvailableCount: 1, Credits: []OpenAIRateLimitResetCreditDetail{{ExpiresAt: "2999-01-01T00:00:00Z"}}}}, nil
}
func (s *groupAccountQuotaStub) CachePostResetSnapshot(_ context.Context, accountID int64, _ *OpenAIQuotaUsage) error {
	s.postSnapshots = append(s.postSnapshots, accountID)
	return nil
}
func (s *groupAccountQuotaStub) ResetCredit(_ context.Context, accountID int64) (*OpenAIQuotaResetResult, error) {
	s.resets = append(s.resets, accountID)
	return &OpenAIQuotaResetResult{Code: "reset", WindowsReset: 2}, nil
}
func (s *groupAccountQuotaStub) CacheCreditsSnapshot(context.Context, int64, *OpenAIQuotaUsage) error {
	return nil
}
func (s *groupAccountQuotaStub) CacheResetCreditsSnapshot(_ context.Context, accountID int64, _ *OpenAIRateLimitResetCredits) error {
	s.cachedCredits = append(s.cachedCredits, accountID)
	return nil
}

type groupAccountRecovererStub struct{ recovered []int64 }

func (s *groupAccountRecovererStub) RecoverAccountState(_ context.Context, accountID int64, _ AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	s.recovered = append(s.recovered, accountID)
	return &SuccessfulTestRecoveryResult{}, nil
}

type groupAccountFixture struct {
	svc       *GroupManagementService
	repo      *accountUsageMgmtRepoStub
	quota     *groupAccountQuotaStub
	recoverer *groupAccountRecovererStub
}

func newGroupAccountFixture(managedType string) *groupAccountFixture {
	resetAt := time.Now().Add(2 * time.Hour)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	accounts := map[int64]*Account{
		100: {ID: 100, Name: "codex-a", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
			Extra: map[string]any{openaiQuotaResetCreditsKey: map[string]any{
				"available_count": float64(2),
				"credits":         []any{map[string]any{"expires_at": future}, map[string]any{"expires_at": past}},
			}}},
		101: {ID: 101, Name: "claude-a", Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive},
	}
	f := &groupAccountFixture{
		repo: &accountUsageMgmtRepoStub{
			groupManagementRepoStub: groupManagementRepoStub{allowed: true},
			managedType:             managedType,
			pool: []AssignedAccount{
				{ID: 100, GroupID: 42, AssignedUserIDs: []int64{7, 8}},
				{ID: 101, GroupID: 42, AssignedUserIDs: []int64{}},
			},
			assigned: []AssignedAccount{{ID: 101, GroupID: 42, UserID: 7}},
		},
		quota:     &groupAccountQuotaStub{},
		recoverer: &groupAccountRecovererStub{},
	}
	f.svc = NewGroupManagementService(f.repo).WithGroupAccountDependencies(GroupAccountDependencies{
		Accounts: &groupAccountLoaderStub{accounts: accounts},
		Usage: &groupAccountUsageReaderStub{usage: map[int64]*UsageInfo{
			100: {FiveHour: &UsageProgress{Utilization: 42, ResetsAt: &resetAt, RemainingSeconds: 1}, SevenDay: &UsageProgress{Utilization: 10}},
			101: {Error: "upstream unavailable"},
		}},
		Quota:     f.quota,
		Recoverer: f.recoverer,
	})
	return f
}

func TestAccountUsageOnlyForSubscriptionGroups(t *testing.T) {
	f := newGroupAccountFixture(ManagedGroupTypeQuota)
	_, err := f.svc.AccountUsage(context.Background(), 7, RoleGroupManager, 42)
	require.ErrorIs(t, err, ErrGroupNotSubscription)

	f = newGroupAccountFixture("")
	_, err = f.svc.AccountUsage(context.Background(), 7, RoleGroupManager, 42)
	require.ErrorIs(t, err, ErrGroupNotSubscription)
}

func TestAccountUsageManagerSeesPoolWithResetCredits(t *testing.T) {
	f := newGroupAccountFixture(ManagedGroupTypeSubscription)
	items, err := f.svc.AccountUsage(context.Background(), 7, RoleGroupManager, 42)
	require.NoError(t, err)
	require.Len(t, items, 2)

	codex := items[0]
	require.Equal(t, int64(100), codex.AccountID)
	require.True(t, codex.SupportsReset)
	require.True(t, codex.CanReset)
	require.Equal(t, 2, codex.AssignedUserCount)
	require.NotNil(t, codex.FiveHour)
	require.InDelta(t, 42, codex.FiveHour.Utilization, 1e-9)
	require.Greater(t, codex.FiveHour.RemainingSeconds, 3600, "remaining seconds are recomputed from resets_at")
	// 过期的卡不计入
	require.NotNil(t, codex.ResetCredits)
	require.Equal(t, 1, codex.ResetCredits.AvailableCount)
	require.Len(t, codex.ResetCredits.ExpiresAt, 1)

	claude := items[1]
	require.False(t, claude.SupportsReset)
	require.False(t, claude.CanReset)
	require.True(t, claude.UsageUnavailable)
}

func TestAccountUsageMemberSeesAssignedAccountsReadOnly(t *testing.T) {
	f := newGroupAccountFixture(ManagedGroupTypeSubscription)
	items, err := f.svc.AccountUsage(context.Background(), 7, RoleUser, 42)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, int64(101), items[0].AccountID)
	require.False(t, items[0].CanReset)
	require.Zero(t, items[0].AssignedUserCount)
}

func TestResetAccountCreditRequiresManagerAndSupportedAccount(t *testing.T) {
	f := newGroupAccountFixture(ManagedGroupTypeSubscription)
	ctx := context.Background()

	_, err := f.svc.ResetAccountCredit(ctx, 7, RoleUser, 42, 100)
	require.ErrorIs(t, err, ErrGroupManagementForbidden)
	_, err = f.svc.ResetAccountCredit(ctx, 7, RoleGroupManager, 42, 999)
	require.ErrorIs(t, err, ErrAccountNotInGroup)
	_, err = f.svc.ResetAccountCredit(ctx, 7, RoleGroupManager, 42, 101)
	require.ErrorIs(t, err, ErrGroupAccountResetUnsupported)
	require.Empty(t, f.quota.resets)

	out, err := f.svc.ResetAccountCredit(ctx, 7, RoleGroupManager, 42, 100)
	require.NoError(t, err)
	require.Equal(t, "reset", out.Code)
	require.Equal(t, 2, out.WindowsReset)
	require.Empty(t, out.WarningCode)
	require.Equal(t, []int64{100}, f.quota.resets)
	require.Equal(t, []int64{100}, f.recoverer.recovered)
	require.Equal(t, []int64{100}, f.quota.postSnapshots)
	require.NotNil(t, out.Account)
	require.Equal(t, int64(100), out.Account.AccountID)
}

func TestRefreshAccountQuotaCachesResetCredits(t *testing.T) {
	f := newGroupAccountFixture(ManagedGroupTypeSubscription)
	out, err := f.svc.RefreshAccountQuota(context.Background(), 7, RoleAdmin, 42, 100)
	require.NoError(t, err)
	require.Equal(t, int64(100), out.AccountID)
	require.Equal(t, []int64{100}, f.quota.queries)
	require.Equal(t, []int64{100}, f.quota.cachedCredits)
}

func TestReadGroupAccountResetCredits(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	require.Nil(t, readGroupAccountResetCredits(nil, now))

	// 进程内合并的结构体快照
	credits := readGroupAccountResetCredits(map[string]any{openaiQuotaResetCreditsKey: &OpenAIRateLimitResetCredits{
		AvailableCount: 3,
		Credits:        []OpenAIRateLimitResetCreditDetail{{ExpiresAt: "2026-10-01T00:00:00Z"}, {ExpiresAt: "not-a-time"}},
	}}, now)
	require.NotNil(t, credits)
	require.Equal(t, 2, credits.AvailableCount, "count is clamped to the credits that remain")

	// 声称有卡但全部过期：视为未知
	require.Nil(t, readGroupAccountResetCredits(map[string]any{openaiQuotaResetCreditsKey: map[string]any{
		"available_count": float64(1), "credits": []any{map[string]any{"expires_at": "2026-09-01T00:00:00Z"}},
	}}, now))

	// 没有卡
	empty := readGroupAccountResetCredits(map[string]any{openaiQuotaResetCreditsKey: map[string]any{"available_count": float64(0)}}, now)
	require.NotNil(t, empty)
	require.Zero(t, empty.AvailableCount)
}
