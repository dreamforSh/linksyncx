package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	testManagedGroupID = int64(42)
	testManagerID      = int64(7)
)

type groupUserMgmtRepoStub struct {
	GroupManagementRepository
	managed       bool
	managedType   string
	allowed       bool
	legacyAdded   []int64
	legacyRemoved []int64
	members       []GroupMembership
}

func (s *groupUserMgmtRepoStub) CanAccessGroup(context.Context, int64, string, int64) (bool, error) {
	return s.allowed, nil
}

func (s *groupUserMgmtRepoStub) GetSettings(_ context.Context, groupID int64) (*GroupSettings, error) {
	return &GroupSettings{GroupID: groupID, Managed: s.managed, ManagedType: s.managedType, Enabled: true, AllocationMode: GroupAssignmentModeManual, MaxConcurrent: 1}, nil
}

func (s *groupUserMgmtRepoStub) ListMembers(_ context.Context, groupID int64, userID *int64) ([]GroupMembership, error) {
	if userID == nil {
		return append([]GroupMembership(nil), s.members...), nil
	}
	return []GroupMembership{{GroupID: groupID, UserID: *userID, Owned: true, Status: StatusActive}}, nil
}

func (s *groupUserMgmtRepoStub) AddMember(_ context.Context, _ int64, userID int64) (*GroupMembership, error) {
	s.legacyAdded = append(s.legacyAdded, userID)
	return &GroupMembership{UserID: userID}, nil
}

func (s *groupUserMgmtRepoStub) RemoveMember(_ context.Context, _ int64, userID int64) error {
	s.legacyRemoved = append(s.legacyRemoved, userID)
	return nil
}

type insertedMember struct {
	groupID, userID int64
	owned           bool
	createdBy       int64
	maxConcurrent   *int
	dailyLimit      *int64
}

type groupUserRepoStub struct {
	GroupUserRepository
	states          map[int64]*GroupMemberState
	ownedByManager  map[int64]bool
	remaining       int64
	inserted        []insertedMember
	deleted         []int64
	disabledKeysFor []int64
	statusChanges   map[int64]string
	transfers       []GroupBalanceTransfer
	records         []RedeemCode
	categories      map[int64]string
	lockedUsers     [][]int64
	// 邮箱邀请
	invitees     map[string]*GroupInvitee
	invitations  []*GroupInvitation
	pendingCount int64
}

func newGroupUserRepoStub() *groupUserRepoStub {
	return &groupUserRepoStub{
		states:         map[int64]*GroupMemberState{},
		ownedByManager: map[int64]bool{},
		statusChanges:  map[int64]string{},
		categories:     map[int64]string{},
		invitees:       map[string]*GroupInvitee{},
	}
}

func (s *groupUserRepoStub) Summary(context.Context, int64) (int64, int64, error) { return 2, 3, nil }
func (s *groupUserRepoStub) LockGroup(context.Context, int64) error               { return nil }
func (s *groupUserRepoStub) UpdateCategory(_ context.Context, groupID int64, category string) (bool, error) {
	s.categories[groupID] = category
	return true, nil
}
func (s *groupUserRepoStub) InsertMember(_ context.Context, groupID, userID int64, owned bool, createdBy int64, maxConcurrent *int, dailyLimit *int64) error {
	s.inserted = append(s.inserted, insertedMember{groupID, userID, owned, createdBy, maxConcurrent, dailyLimit})
	s.states[userID] = &GroupMemberState{Owned: owned, Role: RoleUser, Status: StatusActive}
	return nil
}
func (s *groupUserRepoStub) GetMemberState(_ context.Context, _ int64, userID int64) (*GroupMemberState, error) {
	return s.states[userID], nil
}
func (s *groupUserRepoStub) IsOwnedByManager(_ context.Context, userID, _ int64) (bool, error) {
	return s.ownedByManager[userID], nil
}
func (s *groupUserRepoStub) ListOwnedUsersForManager(context.Context, int64, int64) ([]GroupOwnedUser, error) {
	return []GroupOwnedUser{{UserID: 11}}, nil
}
func (s *groupUserRepoStub) DeleteMember(_ context.Context, _ int64, userID int64) (bool, error) {
	s.deleted = append(s.deleted, userID)
	return true, nil
}
func (s *groupUserRepoStub) CountMemberships(context.Context, int64) (int64, error) {
	return s.remaining, nil
}
func (s *groupUserRepoStub) DisableGroupAPIKeys(_ context.Context, userID, _ int64) (int64, error) {
	s.disabledKeysFor = append(s.disabledKeysFor, userID)
	return 1, nil
}
func (s *groupUserRepoStub) SetUserStatus(_ context.Context, userID int64, status string) (bool, error) {
	s.statusChanges[userID] = status
	return true, nil
}
func (s *groupUserRepoStub) LockUsers(_ context.Context, userIDs ...int64) error {
	s.lockedUsers = append(s.lockedUsers, userIDs)
	return nil
}
func (s *groupUserRepoStub) NetGranted(_ context.Context, _ int64, memberID int64) (float64, error) {
	var net float64
	for _, transfer := range s.transfers {
		if transfer.MemberID == nil || *transfer.MemberID != memberID {
			continue
		}
		if transfer.Direction == GroupTransferGrant {
			net += transfer.Amount
		} else {
			net -= transfer.Amount
		}
	}
	return net, nil
}
func (s *groupUserRepoStub) InsertTransfer(_ context.Context, transfer *GroupBalanceTransfer) error {
	transfer.ID = int64(len(s.transfers) + 1)
	s.transfers = append(s.transfers, *transfer)
	return nil
}
func (s *groupUserRepoStub) InsertAdjustmentRecord(_ context.Context, record *RedeemCode) error {
	s.records = append(s.records, *record)
	return nil
}
func (s *groupUserRepoStub) ListTransfers(context.Context, int64, int64, int, int) ([]GroupBalanceTransfer, int64, error) {
	return s.transfers, int64(len(s.transfers)), nil
}
func (s *groupUserRepoStub) UserGroupSummaries(_ context.Context, userIDs []int64) ([]UserGroupSummary, error) {
	out := make([]UserGroupSummary, 0, len(userIDs))
	for _, id := range userIDs {
		out = append(out, UserGroupSummary{UserID: id, ManagedGroups: []GroupRef{}})
	}
	return out, nil
}

type groupUserStoreStub struct {
	nextID        int64
	created       []*User
	users         map[int64]*User
	balances      map[int64]float64
	allowedAdd    [][2]int64
	allowedRemove [][2]int64
	updates       []UserUpdateFields
}

func newGroupUserStoreStub() *groupUserStoreStub {
	return &groupUserStoreStub{nextID: 100, users: map[int64]*User{}, balances: map[int64]float64{}}
}

func (s *groupUserStoreStub) Create(_ context.Context, user *User) error {
	s.nextID++
	user.ID = s.nextID
	s.created = append(s.created, user)
	s.users[user.ID] = user
	return nil
}
func (s *groupUserStoreStub) GetByID(_ context.Context, id int64) (*User, error) {
	if user, ok := s.users[id]; ok {
		return user, nil
	}
	return nil, ErrUserNotFound
}
func (s *groupUserStoreStub) Update(_ context.Context, _ *User, fields UserUpdateFields) error {
	s.updates = append(s.updates, fields)
	return nil
}
func (s *groupUserStoreStub) AdjustBalance(_ context.Context, id int64, delta float64) (BalanceChange, error) {
	old := s.balances[id]
	if old+delta < 0 {
		return BalanceChange{Old: old, New: old + delta}, ErrBalanceNegative
	}
	s.balances[id] = old + delta
	return BalanceChange{Old: old, New: old + delta}, nil
}
func (s *groupUserStoreStub) AddGroupToAllowedGroups(_ context.Context, userID, groupID int64) error {
	s.allowedAdd = append(s.allowedAdd, [2]int64{userID, groupID})
	return nil
}
func (s *groupUserStoreStub) RemoveGroupFromUserAllowedGroups(_ context.Context, userID, groupID int64) error {
	s.allowedRemove = append(s.allowedRemove, [2]int64{userID, groupID})
	return nil
}

type authCacheSpy struct{ userIDs []int64 }

func (s *authCacheSpy) InvalidateAuthCacheByKey(context.Context, string) {}
func (s *authCacheSpy) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}
func (s *authCacheSpy) InvalidateAuthCacheByGroupID(context.Context, int64) {}

type balanceCacheSpy struct{ userIDs []int64 }

func (s *balanceCacheSpy) InvalidateUserBalance(_ context.Context, userID int64) error {
	s.userIDs = append(s.userIDs, userID)
	return nil
}

type defaultConcurrencyStub int

func (d defaultConcurrencyStub) GetDefaultConcurrency(context.Context) int { return int(d) }

type groupUserFixture struct {
	svc          *GroupManagementService
	mgmt         *groupUserMgmtRepoStub
	repo         *groupUserRepoStub
	users        *groupUserStoreStub
	authCache    *authCacheSpy
	balanceCache *balanceCacheSpy
}

func newGroupUserFixture(managed bool) *groupUserFixture {
	f := &groupUserFixture{
		mgmt:         &groupUserMgmtRepoStub{managed: managed, allowed: true},
		repo:         newGroupUserRepoStub(),
		users:        newGroupUserStoreStub(),
		authCache:    &authCacheSpy{},
		balanceCache: &balanceCacheSpy{},
	}
	f.svc = NewGroupManagementService(f.mgmt).WithGroupUserDependencies(GroupUserDependencies{
		GroupUsers:   f.repo,
		Users:        f.users,
		AuthCache:    f.authCache,
		BalanceCache: f.balanceCache,
		Defaults:     defaultConcurrencyStub(5),
	})
	return f
}

func validGroupUserInput() CreateGroupUserInput {
	return CreateGroupUserInput{Email: "alice@example.com", Username: " alice ", Password: "secret123"}
}

func TestCreateGroupUserRequiresManagedGroup(t *testing.T) {
	f := newGroupUserFixture(false)
	_, err := f.svc.CreateGroupUser(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, validGroupUserInput())
	require.ErrorIs(t, err, ErrGroupNotManaged)
	require.Empty(t, f.users.created)
}

func TestCreateGroupUserBuildsRestrictedOwnedMember(t *testing.T) {
	f := newGroupUserFixture(true)
	concurrency := 3
	input := validGroupUserInput()
	input.MaxConcurrent = &concurrency

	member, err := f.svc.CreateGroupUser(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, input)
	require.NoError(t, err)

	require.Len(t, f.users.created, 1)
	user := f.users.created[0]
	require.Equal(t, RoleUser, user.Role)
	require.Equal(t, StatusActive, user.Status)
	require.Equal(t, "alice", user.Username)
	require.Zero(t, user.Balance)
	require.Equal(t, 5, user.Concurrency)
	require.True(t, user.RestrictPublicGroups)
	require.Equal(t, []int64{testManagedGroupID}, user.AllowedGroups)
	require.True(t, user.CheckPassword("secret123"))

	require.Len(t, f.repo.inserted, 1)
	inserted := f.repo.inserted[0]
	require.True(t, inserted.owned)
	require.Equal(t, testManagerID, inserted.createdBy)
	require.Equal(t, &concurrency, inserted.maxConcurrent)
	require.Nil(t, inserted.dailyLimit)
	require.Equal(t, user.ID, member.UserID)
	require.Empty(t, f.repo.transfers)
}

func TestCreateGroupUserInitialAmountTransfersFromManager(t *testing.T) {
	f := newGroupUserFixture(true)
	f.users.balances[testManagerID] = 20
	input := validGroupUserInput()
	input.InitialAmount = 5

	member, err := f.svc.CreateGroupUser(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, input)
	require.NoError(t, err)

	require.InDelta(t, 15, f.users.balances[testManagerID], 1e-9)
	require.InDelta(t, 5, f.users.balances[member.UserID], 1e-9)
	require.Len(t, f.repo.transfers, 1)
	require.Equal(t, GroupTransferGrant, f.repo.transfers[0].Direction)
	require.Len(t, f.repo.records, 2)
	require.Equal(t, AdjustmentTypeGroupTransfer, f.repo.records[0].Type)
	require.InDelta(t, -5, f.repo.records[0].Value, 1e-9)
	require.Equal(t, testManagerID, *f.repo.records[0].UsedBy)
	require.InDelta(t, 5, f.repo.records[1].Value, 1e-9)
	require.ElementsMatch(t, []int64{testManagerID, member.UserID}, f.balanceCache.userIDs)
}

func TestCreateGroupUserInitialAmountFailsWithoutBalance(t *testing.T) {
	f := newGroupUserFixture(true)
	input := validGroupUserInput()
	input.InitialAmount = 5

	_, err := f.svc.CreateGroupUser(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, input)
	require.ErrorIs(t, err, ErrGroupTransferInsufficient)
	require.Empty(t, f.repo.transfers)
}

func TestCreateGroupUserRejectsInvalidInput(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()

	short := validGroupUserInput()
	short.Password = "12345"
	_, err := f.svc.CreateGroupUser(ctx, testManagerID, RoleGroupManager, testManagedGroupID, short)
	require.ErrorIs(t, err, ErrGroupUserPasswordInvalid)

	noEmail := validGroupUserInput()
	noEmail.Email = "not-an-email"
	_, err = f.svc.CreateGroupUser(ctx, testManagerID, RoleGroupManager, testManagedGroupID, noEmail)
	require.ErrorIs(t, err, ErrGroupManagementBadInput)

	adminAmount := validGroupUserInput()
	adminAmount.InitialAmount = 1
	_, err = f.svc.CreateGroupUser(ctx, 1, RoleAdmin, testManagedGroupID, adminAmount)
	require.ErrorIs(t, err, ErrGroupTransferManagerOnly)

	_, err = f.svc.CreateGroupUser(ctx, 9, RoleUser, testManagedGroupID, validGroupUserInput())
	require.ErrorIs(t, err, ErrGroupManagementForbidden)
	require.Empty(t, f.users.created)
}

func TestTransferBalanceGrantAndReclaim(t *testing.T) {
	f := newGroupUserFixture(true)
	member := int64(11)
	f.repo.states[member] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}
	f.users.balances[testManagerID] = 10
	ctx := context.Background()

	transfer, err := f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferGrant, 4.123456789, " monthly ")
	require.NoError(t, err)
	require.InDelta(t, 4.12345679, transfer.Amount, 1e-12)
	require.Equal(t, "monthly", transfer.Notes)
	require.InDelta(t, 10-4.12345679, f.users.balances[testManagerID], 1e-9)
	require.InDelta(t, 4.12345679, f.users.balances[member], 1e-9)
	require.Equal(t, [][]int64{{testManagerID, member}}, f.repo.lockedUsers)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferReclaim, 1, "")
	require.NoError(t, err)
	require.InDelta(t, 3.12345679, f.users.balances[member], 1e-9)
	require.Len(t, f.repo.transfers, 2)
	require.Equal(t, GroupTransferReclaim, f.repo.transfers[1].Direction)
}

func TestTransferBalanceReclaimLimitedToNetGranted(t *testing.T) {
	f := newGroupUserFixture(true)
	member := int64(11)
	f.repo.states[member] = &GroupMemberState{Owned: false, Role: RoleUser, Status: StatusActive}
	// 成员自己充值的余额不能被组管理员回收
	f.users.balances[member] = 10
	f.users.balances[testManagerID] = 5
	ctx := context.Background()

	_, err := f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferReclaim, 1, "")
	require.ErrorIs(t, err, ErrGroupTransferExceedsGranted)
	require.InDelta(t, 10, f.users.balances[member], 1e-9)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferGrant, 3, "")
	require.NoError(t, err)
	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferReclaim, 3.5, "")
	require.ErrorIs(t, err, ErrGroupTransferExceedsGranted)
	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferReclaim, 3, "")
	require.NoError(t, err)
	require.InDelta(t, 10, f.users.balances[member], 1e-9)
	require.InDelta(t, 5, f.users.balances[testManagerID], 1e-9)
}

func TestTransferBalanceGuards(t *testing.T) {
	f := newGroupUserFixture(true)
	member := int64(11)
	f.repo.states[member] = &GroupMemberState{Owned: false, Role: RoleUser, Status: StatusActive}
	ctx := context.Background()

	_, err := f.svc.TransferBalance(ctx, 1, RoleAdmin, testManagedGroupID, member, GroupTransferGrant, 1, "")
	require.ErrorIs(t, err, ErrGroupTransferManagerOnly)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferGrant, 1, "")
	require.ErrorIs(t, err, ErrGroupTransferInsufficient)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 99, GroupTransferGrant, 1, "")
	require.ErrorIs(t, err, ErrGroupMemberNotFound)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, testManagerID, GroupTransferGrant, 1, "")
	require.ErrorIs(t, err, ErrGroupTransferSelf)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, "gift", 1, "")
	require.ErrorIs(t, err, ErrGroupManagementBadInput)

	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferGrant, 0, "")
	require.ErrorIs(t, err, ErrGroupManagementBadInput)
	require.Empty(t, f.repo.transfers)

	channel := newGroupUserFixture(false)
	_, err = channel.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, member, GroupTransferGrant, 1, "")
	require.ErrorIs(t, err, ErrGroupNotManaged)
}

func TestSetGroupUserStatusOnlyForOwnedUsers(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	f.repo.states[11] = &GroupMemberState{Owned: false, Role: RoleUser, Status: StatusActive}
	f.repo.states[12] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}
	f.repo.states[13] = &GroupMemberState{Owned: true, Role: RoleGroupManager, Status: StatusActive}

	require.ErrorIs(t, f.svc.SetGroupUserStatus(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 11, StatusDisabled), ErrGroupUserNotOwned)
	require.ErrorIs(t, f.svc.SetGroupUserStatus(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 13, StatusDisabled), ErrGroupUserNotOwned)
	require.ErrorIs(t, f.svc.SetGroupUserStatus(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12, "banned"), ErrGroupManagementBadInput)
	require.ErrorIs(t, f.svc.SetGroupUserStatus(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 99, StatusDisabled), ErrGroupMemberNotFound)

	require.NoError(t, f.svc.SetGroupUserStatus(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12, StatusDisabled))
	require.Equal(t, map[int64]string{12: StatusDisabled}, f.repo.statusChanges)
	require.Equal(t, []int64{12}, f.authCache.userIDs)
}

func TestResetGroupUserPasswordWritesOnlyPasswordHash(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	f.repo.states[12] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}
	user := &User{ID: 12}
	require.NoError(t, user.SetPassword("old-password"))
	f.users.users[12] = user

	require.ErrorIs(t, f.svc.ResetGroupUserPassword(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12, "short"), ErrGroupUserPasswordInvalid)
	require.NoError(t, f.svc.ResetGroupUserPassword(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12, "new-password"))
	require.True(t, user.CheckPassword("new-password"))
	require.Equal(t, []UserUpdateFields{{PasswordHash: true}}, f.users.updates)
}

func TestAddMemberManagedGroupSyncsAllowedGroups(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()

	_, err := f.svc.AddMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 11)
	require.ErrorIs(t, err, ErrGroupMemberNotEligible)
	require.Empty(t, f.repo.inserted)

	f.repo.ownedByManager[11] = true
	_, err = f.svc.AddMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 11)
	require.NoError(t, err)
	require.Equal(t, [][2]int64{{11, testManagedGroupID}}, f.users.allowedAdd)
	require.False(t, f.repo.inserted[0].owned)

	// 超管可以按 ID 加任意用户
	_, err = f.svc.AddMember(ctx, 1, RoleAdmin, testManagedGroupID, 12)
	require.NoError(t, err)
	require.Len(t, f.repo.inserted, 2)
	require.Empty(t, f.mgmt.legacyAdded)
}

func TestAddMemberChannelGroupKeepsLegacyBehavior(t *testing.T) {
	f := newGroupUserFixture(false)
	// 组管理员看不到用户 ID：渠道分组也只能按邮箱邀请
	_, err := f.svc.AddMember(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, 11)
	require.ErrorIs(t, err, ErrGroupInviteRequired)
	require.Empty(t, f.mgmt.legacyAdded)

	_, err = f.svc.AddMember(context.Background(), 1, RoleAdmin, testManagedGroupID, 11)
	require.NoError(t, err)
	require.Equal(t, []int64{11}, f.mgmt.legacyAdded)
	require.Empty(t, f.repo.inserted)
	require.Empty(t, f.users.allowedAdd)
}

func TestRemoveManagedMemberRevokesAccessAndDisablesOrphanedOwnedUser(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	f.repo.states[12] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}

	require.NoError(t, f.svc.RemoveMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12))
	require.Equal(t, []int64{12}, f.repo.deleted)
	require.Equal(t, [][2]int64{{12, testManagedGroupID}}, f.users.allowedRemove)
	require.Equal(t, []int64{12}, f.repo.disabledKeysFor)
	require.Equal(t, map[int64]string{12: StatusDisabled}, f.repo.statusChanges)
	require.Equal(t, []int64{12}, f.authCache.userIDs)
}

func TestRemoveManagedMemberKeepsUsersWithOtherGroups(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	f.repo.remaining = 1
	f.repo.states[12] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}
	f.repo.states[13] = &GroupMemberState{Owned: false, Role: RoleUser, Status: StatusActive}

	require.NoError(t, f.svc.RemoveMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 12))
	require.NoError(t, f.svc.RemoveMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 13))
	require.Empty(t, f.repo.statusChanges)
	require.ErrorIs(t, f.svc.RemoveMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 99), ErrGroupMemberNotFound)
}

func TestRemoveMemberChannelGroupKeepsLegacyBehavior(t *testing.T) {
	f := newGroupUserFixture(false)
	require.NoError(t, f.svc.RemoveMember(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, 12))
	require.Equal(t, []int64{12}, f.mgmt.legacyRemoved)
	require.Empty(t, f.users.allowedRemove)
}

func TestUpdateCategory(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()

	require.ErrorIs(t, f.svc.UpdateCategory(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "startup"), ErrInvalidGroupCategory)
	require.ErrorIs(t, f.svc.UpdateCategory(ctx, testManagerID, RoleGroupManager, testManagedGroupID, ""), ErrInvalidGroupCategory)
	require.NoError(t, f.svc.UpdateCategory(ctx, testManagerID, RoleGroupManager, testManagedGroupID, " Enterprise "))
	require.Equal(t, GroupCategoryEnterprise, f.repo.categories[testManagedGroupID])
	require.ErrorIs(t, f.svc.UpdateCategory(ctx, 9, RoleUser, testManagedGroupID, GroupCategoryTeam), ErrGroupManagementForbidden)

	channel := newGroupUserFixture(false)
	require.ErrorIs(t, channel.svc.UpdateCategory(ctx, testManagerID, RoleGroupManager, testManagedGroupID, GroupCategoryTeam), ErrGroupNotManaged)
}

func TestOwnedUserCandidatesOnlyForManagers(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()

	admin, err := f.svc.OwnedUserCandidates(ctx, 1, RoleAdmin, testManagedGroupID)
	require.NoError(t, err)
	require.Empty(t, admin)

	manager, err := f.svc.OwnedUserCandidates(ctx, testManagerID, RoleGroupManager, testManagedGroupID)
	require.NoError(t, err)
	require.Len(t, manager, 1)
}

func TestAdminUserGroupSummariesSanitizesIDs(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()

	out, err := f.svc.AdminUserGroupSummaries(ctx, []int64{3, 3, -1, 0, 5})
	require.NoError(t, err)
	require.Len(t, out, 2)

	tooMany := make([]int64, maxUserGroupSummaryIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	_, err = f.svc.AdminUserGroupSummaries(ctx, tooMany)
	require.ErrorIs(t, err, ErrGroupManagementBadInput)
}

func TestSummaryAndMissingDependencies(t *testing.T) {
	f := newGroupUserFixture(true)
	summary, err := f.svc.Summary(context.Background(), testManagerID, RoleGroupManager)
	require.NoError(t, err)
	require.Equal(t, &GroupManagementSummary{Role: RoleGroupManager, ManagedGroupCount: 2, MembershipCount: 3}, summary)

	bare := NewGroupManagementService(&groupUserMgmtRepoStub{managed: true, allowed: true})
	summary, err = bare.Summary(context.Background(), testManagerID, RoleGroupManager)
	require.NoError(t, err)
	require.Zero(t, summary.MembershipCount)
	_, err = bare.CreateGroupUser(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID, validGroupUserInput())
	require.True(t, errors.Is(err, errGroupUserDepsMissing))
}

func TestNormalizeTransferAmount(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want float64
		ok   bool
	}{
		{1.5, 1.5, true},
		{0.000000004, 0, false},
		{0.123456785, 0.12345679, true},
		{-1, 0, false},
		{maxGroupTransferAmount + 1, 0, false},
	} {
		got, ok := normalizeTransferAmount(tc.in)
		require.Equal(t, tc.ok, ok, "amount %v", tc.in)
		if ok {
			require.InDelta(t, tc.want, got, 1e-12, "amount %v", tc.in)
		}
	}
}
