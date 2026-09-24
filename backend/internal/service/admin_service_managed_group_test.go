//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func testManagedGroup(id int64, platform string) *Group {
	return &Group{
		ID: id, Name: "managed", Platform: platform, Kind: GroupKindManaged, Category: GroupCategoryTeam,
		IsExclusive: true, SubscriptionType: SubscriptionTypeStandard, Status: StatusActive, RateMultiplier: 1,
	}
}

func testChannelGroup(id int64, platform string) *Group {
	return &Group{
		ID: id, Name: "channel", Platform: platform, Kind: GroupKindChannel,
		SubscriptionType: SubscriptionTypeStandard, Status: StatusActive, RateMultiplier: 1,
	}
}

// managedTestGroups 每次返回全新对象：UpdateGroup 会就地修改加载到的分组。
func managedTestGroups() map[int64]*Group {
	return map[int64]*Group{
		5: testManagedGroup(5, PlatformAnthropic),
		6: testChannelGroup(6, PlatformAnthropic),
		7: testChannelGroup(7, PlatformOpenAI),
	}
}

func TestCreateGroupManagedForcesExclusiveAndDefaultsCategory(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}

	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{
		Name: "acme", Platform: PlatformAnthropic, Kind: GroupKindManaged, RateMultiplier: 1,
	})

	require.NoError(t, err)
	require.Equal(t, GroupKindManaged, group.Kind)
	require.Equal(t, GroupCategoryTeam, group.Category)
	require.True(t, repo.created.IsExclusive)
	require.Equal(t, SubscriptionTypeStandard, repo.created.SubscriptionType)
}

func TestCreateGroupManagedRejectsChannelOnlyOptions(t *testing.T) {
	fallbackID := int64(6)
	base := func() *CreateGroupInput {
		return &CreateGroupInput{Name: "acme", Platform: PlatformAnthropic, Kind: GroupKindManaged, RateMultiplier: 1}
	}
	cases := map[string]func(*CreateGroupInput){
		"subscription billing": func(in *CreateGroupInput) { in.SubscriptionType = SubscriptionTypeSubscription },
		"composite platform":   func(in *CreateGroupInput) { in.Platform = PlatformComposite },
		"copy accounts":        func(in *CreateGroupInput) { in.CopyAccountsFromGroupIDs = []int64{6} },
		"fallback group":       func(in *CreateGroupInput) { in.FallbackGroupID = &fallbackID },
		"unknown category":     func(in *CreateGroupInput) { in.Category = "department" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}
			input := base()
			mutate(input)
			_, err := (&adminServiceImpl{groupRepo: repo}).CreateGroup(context.Background(), input)
			require.Error(t, err)
			require.Nil(t, repo.created)
		})
	}
}

func TestCreateGroupChannelRejectsCategoryAndUnknownKind(t *testing.T) {
	repo := &groupRepoStubForAdmin{}
	svc := &adminServiceImpl{groupRepo: repo}

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "c", Platform: PlatformAnthropic, RateMultiplier: 1, Category: GroupCategoryTeam})
	require.ErrorIs(t, err, ErrGroupCategoryChannel)

	_, err = svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "c", Platform: PlatformAnthropic, RateMultiplier: 1, Kind: "org"})
	require.ErrorIs(t, err, ErrInvalidGroupKind)
	require.Nil(t, repo.created)
}

func TestCreateGroupRejectsManagedFallbackAndCopySource(t *testing.T) {
	repo := &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}
	svc := &adminServiceImpl{groupRepo: repo}
	managedID := int64(5)

	_, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "c", Platform: PlatformAnthropic, RateMultiplier: 1, FallbackGroupID: &managedID})
	require.ErrorIs(t, err, ErrManagedGroupAsFallback)

	_, err = svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "c", Platform: PlatformAnthropic, RateMultiplier: 1, CopyAccountsFromGroupIDs: []int64{5}})
	require.ErrorIs(t, err, ErrManagedGroupCopyAccounts)
	require.Nil(t, repo.created)
}

func TestUpdateGroupManagedInvariants(t *testing.T) {
	newSvc := func() (*adminServiceImpl, *groupRepoStubForAdmin) {
		repo := &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}
		return &adminServiceImpl{groupRepo: repo}, repo
	}
	ctx := context.Background()

	svc, repo := newSvc()
	_, err := svc.UpdateGroup(ctx, 5, &UpdateGroupInput{Kind: GroupKindChannel})
	require.ErrorIs(t, err, ErrGroupKindImmutable)
	require.Nil(t, repo.updated)

	svc, repo = newSvc()
	_, err = svc.UpdateGroup(ctx, 5, &UpdateGroupInput{Platform: PlatformOpenAI})
	require.Error(t, err)
	require.Nil(t, repo.updated)

	svc, repo = newSvc()
	notExclusive := false
	_, err = svc.UpdateGroup(ctx, 5, &UpdateGroupInput{IsExclusive: &notExclusive})
	require.Error(t, err)
	require.Nil(t, repo.updated)

	svc, repo = newSvc()
	_, err = svc.UpdateGroup(ctx, 5, &UpdateGroupInput{CopyAccountsFromGroupIDs: []int64{6}})
	require.ErrorIs(t, err, ErrManagedGroupCopyAccounts)
	require.Nil(t, repo.updated)

	svc, repo = newSvc()
	_, err = svc.UpdateGroup(ctx, 6, &UpdateGroupInput{CopyAccountsFromGroupIDs: []int64{5}})
	require.ErrorIs(t, err, ErrManagedGroupCopyAccounts)
	require.Nil(t, repo.updated)

	svc, repo = newSvc()
	enterprise := GroupCategoryEnterprise
	_, err = svc.UpdateGroup(ctx, 5, &UpdateGroupInput{Category: &enterprise})
	require.NoError(t, err)
	require.Equal(t, GroupCategoryEnterprise, repo.updated.Category)

	svc, repo = newSvc()
	_, err = svc.UpdateGroup(ctx, 6, &UpdateGroupInput{Category: &enterprise})
	require.ErrorIs(t, err, ErrGroupCategoryChannel)
	require.Nil(t, repo.updated)
}

func TestDuplicateGroupRejectsManagedGroup(t *testing.T) {
	svc := &adminServiceImpl{groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}
	_, err := svc.DuplicateGroup(context.Background(), 5, "", "")
	require.ErrorIs(t, err, ErrManagedGroupDuplicate)
}

func TestCreateAccountRequiresGroup(t *testing.T) {
	accountRepo := &accountRepoStubForBulkUpdate{createID: 11}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "no-group", Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
	})

	require.ErrorIs(t, err, ErrAccountGroupRequired)
	require.Nil(t, accountRepo.createAccount)
}

func TestCreateAccountManagedGroupExclusiveAndPlatform(t *testing.T) {
	newSvc := func() (*adminServiceImpl, *accountRepoStubForBulkUpdate) {
		accountRepo := &accountRepoStubForBulkUpdate{createID: 11}
		return &adminServiceImpl{accountRepo: accountRepo, groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}, accountRepo
	}
	input := func(platform string, groupIDs ...int64) *CreateAccountInput {
		return &CreateAccountInput{
			Name: "acct", Platform: platform, Type: AccountTypeAPIKey, Concurrency: 1,
			GroupIDs: groupIDs, SkipMixedChannelCheck: true,
		}
	}
	ctx := context.Background()

	svc, accountRepo := newSvc()
	_, err := svc.CreateAccount(ctx, input(PlatformAnthropic, 5, 6))
	require.ErrorIs(t, err, ErrManagedGroupAccountExclusive)
	require.Nil(t, accountRepo.createAccount)

	svc, accountRepo = newSvc()
	_, err = svc.CreateAccount(ctx, input(PlatformOpenAI, 5))
	require.ErrorIs(t, err, ErrManagedGroupPlatformMismatch)
	require.Nil(t, accountRepo.createAccount)

	svc, accountRepo = newSvc()
	_, err = svc.CreateAccount(ctx, input(PlatformAnthropic, 5))
	require.NoError(t, err)
	require.Equal(t, []int64{5}, accountRepo.bindGroupsByAccount[11])

	// 渠道分组之间可以多选
	svc, accountRepo = newSvc()
	_, err = svc.CreateAccount(ctx, input(PlatformAnthropic, 6, 7))
	require.NoError(t, err)
	require.Equal(t, []int64{6, 7}, accountRepo.bindGroupsByAccount[11])
}

func TestUpdateAccountKeepsGroupsAndManagedPlatform(t *testing.T) {
	newSvc := func() (*adminServiceImpl, *accountRepoStubForBulkUpdate) {
		accountRepo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
			21: {ID: 21, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Extra: map[string]any{}},
		}}
		return &adminServiceImpl{accountRepo: accountRepo, groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}, accountRepo
	}
	ctx := context.Background()

	svc, accountRepo := newSvc()
	empty := []int64{}
	_, err := svc.UpdateAccount(ctx, 21, &UpdateAccountInput{GroupIDs: &empty})
	require.ErrorIs(t, err, ErrAccountGroupRequired)
	require.Empty(t, accountRepo.bindGroupsCalls)

	svc, accountRepo = newSvc()
	managed := []int64{5}
	_, err = svc.UpdateAccount(ctx, 21, &UpdateAccountInput{GroupIDs: &managed, SkipMixedChannelCheck: true})
	require.ErrorIs(t, err, ErrManagedGroupPlatformMismatch)
	require.Empty(t, accountRepo.bindGroupsCalls)
}

func TestBulkUpdateAccountsManagedGroupRejectsMismatchedPlatform(t *testing.T) {
	accountRepo := &accountRepoStubForBulkUpdate{getByIDsAccounts: []*Account{
		{ID: 1, Platform: PlatformAnthropic},
		{ID: 2, Platform: PlatformOpenAI},
	}}
	svc := &adminServiceImpl{accountRepo: accountRepo, groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}
	ctx := context.Background()

	managed := []int64{5}
	_, err := svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, GroupIDs: &managed, SkipMixedChannelCheck: true})
	require.ErrorIs(t, err, ErrManagedGroupPlatformMismatch)
	require.Empty(t, accountRepo.bindGroupsCalls)

	empty := []int64{}
	_, err = svc.BulkUpdateAccounts(ctx, &BulkUpdateAccountsInput{AccountIDs: []int64{1}, GroupIDs: &empty})
	require.ErrorIs(t, err, ErrAccountGroupRequired)
}

func TestChannelServiceRejectsManagedGroups(t *testing.T) {
	svc := &ChannelService{groupRepo: &groupRepoStubForAdmin{getByIDByID: managedTestGroups()}}
	require.ErrorIs(t, svc.checkGroupConflicts(context.Background(), 0, []int64{6, 5}), ErrManagedGroupInChannel)
}

func TestCreateCRSAccountInGroupsBindsPlatformTargets(t *testing.T) {
	accountRepo := &accountRepoStubForBulkUpdate{createID: 31}
	svc := &CRSSyncService{accountRepo: accountRepo}
	targets := map[string][]int64{PlatformAnthropic: {6, 8}}
	ctx := context.Background()

	err := svc.createCRSAccountInGroups(ctx, &Account{Name: "gpt", Platform: PlatformOpenAI}, targets)
	require.ErrorContains(t, err, "no target group selected")
	require.Nil(t, accountRepo.createAccount)

	err = svc.createCRSAccountInGroups(ctx, &Account{Name: "claude", Platform: PlatformAnthropic}, targets)
	require.NoError(t, err)
	require.Equal(t, []int64{6, 8}, accountRepo.bindGroupsByAccount[31])
}
