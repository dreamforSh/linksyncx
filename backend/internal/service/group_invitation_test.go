package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func (s *groupUserRepoStub) CountPendingInvitations(context.Context, int64) (int64, error) {
	return s.pendingCount, nil
}

func (s *groupUserRepoStub) FindInviteeByEmail(_ context.Context, email string) (*GroupInvitee, error) {
	return s.invitees[email], nil
}

func (s *groupUserRepoStub) CreateInvitation(_ context.Context, groupID, userID int64, email string, invitedBy int64) (*GroupInvitation, bool, error) {
	for _, item := range s.invitations {
		if item.GroupID == groupID && item.UserID == userID && item.Status == GroupInvitationPending {
			return item, false, nil
		}
	}
	item := &GroupInvitation{
		ID: int64(len(s.invitations) + 1), GroupID: groupID, UserID: userID, Email: email, InvitedBy: invitedBy,
		Status: GroupInvitationPending, CreatedAt: time.Now(), Managed: true,
	}
	s.invitations = append(s.invitations, item)
	return item, true, nil
}

func (s *groupUserRepoStub) ListGroupInvitations(_ context.Context, groupID int64, _ int) ([]GroupInvitation, error) {
	out := make([]GroupInvitation, 0)
	for _, item := range s.invitations {
		if item.GroupID == groupID {
			out = append(out, *item)
		}
	}
	return out, nil
}

func (s *groupUserRepoStub) RevokeInvitation(_ context.Context, groupID, invitationID int64) (bool, error) {
	for _, item := range s.invitations {
		if item.ID == invitationID && item.GroupID == groupID && item.Status == GroupInvitationPending {
			item.Status = GroupInvitationRevoked
			return true, nil
		}
	}
	return false, nil
}

func (s *groupUserRepoStub) ListUserInvitations(_ context.Context, userID int64) ([]GroupInvitation, error) {
	out := make([]GroupInvitation, 0)
	for _, item := range s.invitations {
		if item.UserID == userID && item.Status == GroupInvitationPending {
			out = append(out, *item)
		}
	}
	return out, nil
}

func (s *groupUserRepoStub) LockUserInvitation(_ context.Context, invitationID, userID int64) (*GroupInvitation, error) {
	for _, item := range s.invitations {
		if item.ID == invitationID && item.UserID == userID && item.Status == GroupInvitationPending {
			copied := *item
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *groupUserRepoStub) SetInvitationStatus(_ context.Context, invitationID int64, status string) error {
	for _, item := range s.invitations {
		if item.ID == invitationID {
			item.Status = status
		}
	}
	return nil
}

func TestInviteMemberByEmail(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	f.repo.invitees["bob@example.com"] = &GroupInvitee{ID: 21, Email: "Bob@Example.com", Role: RoleUser, Status: StatusActive}
	f.repo.invitees["root@example.com"] = &GroupInvitee{ID: 1, Email: "root@example.com", Role: RoleAdmin, Status: StatusActive}
	f.repo.invitees["off@example.com"] = &GroupInvitee{ID: 22, Email: "off@example.com", Role: RoleUser, Status: StatusDisabled}
	f.repo.invitees["me@example.com"] = &GroupInvitee{ID: testManagerID, Email: "me@example.com", Role: RoleUser, Status: StatusActive}
	f.repo.invitees["member@example.com"] = &GroupInvitee{ID: 23, Email: "member@example.com", Role: RoleUser, Status: StatusActive}
	f.repo.states[23] = &GroupMemberState{Role: RoleUser, Status: StatusActive}

	_, err := f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "not-an-email")
	require.ErrorIs(t, err, ErrGroupManagementBadInput)
	_, err = f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "nobody@example.com")
	require.ErrorIs(t, err, ErrGroupInviteUserNotFound)
	for _, email := range []string{"root@example.com", "off@example.com", "me@example.com"} {
		_, err = f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, email)
		require.ErrorIs(t, err, ErrGroupInviteNotEligible, email)
	}
	_, err = f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "member@example.com")
	require.ErrorIs(t, err, ErrGroupInviteAlreadyMember)
	// 普通成员不能发邀请
	_, err = f.svc.InviteMember(ctx, 21, RoleUser, testManagedGroupID, "bob@example.com")
	require.ErrorIs(t, err, ErrGroupManagementForbidden)

	// 邮箱按登录规则归一（去空白、小写）；重复邀请返回同一条待处理邀请
	first, err := f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "  BOB@example.com ")
	require.NoError(t, err)
	require.Equal(t, int64(21), first.UserID)
	require.Equal(t, testManagerID, first.InvitedBy)
	again, err := f.svc.InviteMember(ctx, testManagerID, RoleGroupManager, testManagedGroupID, "bob@example.com")
	require.NoError(t, err)
	require.Equal(t, first.ID, again.ID)
	require.Len(t, f.repo.invitations, 1)
}

func TestRespondInvitationAcceptJoinsManagedGroup(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	invitation, _, _ := f.repo.CreateInvitation(ctx, testManagedGroupID, 21, "bob@example.com", testManagerID)

	// 只能处理发给自己的邀请
	_, err := f.svc.RespondInvitation(ctx, 22, RoleUser, invitation.ID, true)
	require.ErrorIs(t, err, ErrGroupInvitationNotFound)

	out, err := f.svc.RespondInvitation(ctx, 21, RoleUser, invitation.ID, true)
	require.NoError(t, err)
	require.Equal(t, GroupInvitationAccepted, out.Status)
	require.NotNil(t, out.RespondedAt)
	require.Len(t, f.repo.inserted, 1)
	require.Equal(t, insertedMember{groupID: testManagedGroupID, userID: 21, owned: false, createdBy: testManagerID}, f.repo.inserted[0])
	require.Equal(t, [][2]int64{{21, testManagedGroupID}}, f.users.allowedAdd)
	require.Equal(t, GroupInvitationAccepted, f.repo.invitations[0].Status)
	require.Contains(t, f.authCache.userIDs, int64(21))

	// 已处理的邀请不能再次接受
	_, err = f.svc.RespondInvitation(ctx, 21, RoleUser, invitation.ID, true)
	require.ErrorIs(t, err, ErrGroupInvitationNotFound)
}

func TestRespondInvitationDeclineAndRoleCheck(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	invitation, _, _ := f.repo.CreateInvitation(ctx, testManagedGroupID, 21, "bob@example.com", testManagerID)

	// 发出后被改成组管理员的用户不能再接受
	_, err := f.svc.RespondInvitation(ctx, 21, RoleGroupManager, invitation.ID, true)
	require.ErrorIs(t, err, ErrGroupInviteNotEligible)
	require.Empty(t, f.repo.inserted)
	require.Equal(t, GroupInvitationPending, f.repo.invitations[0].Status)

	out, err := f.svc.RespondInvitation(ctx, 21, RoleUser, invitation.ID, false)
	require.NoError(t, err)
	require.Equal(t, GroupInvitationDeclined, out.Status)
	require.Empty(t, f.repo.inserted)
	require.Empty(t, f.users.allowedAdd)
}

func TestRevokeInvitationAndListing(t *testing.T) {
	f := newGroupUserFixture(true)
	ctx := context.Background()
	invitation, _, _ := f.repo.CreateInvitation(ctx, testManagedGroupID, 21, "bob@example.com", testManagerID)

	mine, err := f.svc.MyInvitations(ctx, 21, RoleUser)
	require.NoError(t, err)
	require.Len(t, mine, 1)

	require.ErrorIs(t, f.svc.RevokeInvitation(ctx, 21, RoleUser, testManagedGroupID, invitation.ID), ErrGroupManagementForbidden)
	require.NoError(t, f.svc.RevokeInvitation(ctx, testManagerID, RoleGroupManager, testManagedGroupID, invitation.ID))
	require.ErrorIs(t, f.svc.RevokeInvitation(ctx, testManagerID, RoleGroupManager, testManagedGroupID, invitation.ID), ErrGroupInvitationNotFound)

	mine, err = f.svc.MyInvitations(ctx, 21, RoleUser)
	require.NoError(t, err)
	require.Empty(t, mine)
	listed, err := f.svc.GroupInvitations(ctx, testManagerID, RoleGroupManager, testManagedGroupID)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, GroupInvitationRevoked, listed[0].Status)
}

func TestSummaryIncludesPendingInvitations(t *testing.T) {
	f := newGroupUserFixture(true)
	f.repo.pendingCount = 2
	out, err := f.svc.Summary(context.Background(), 21, RoleUser)
	require.NoError(t, err)
	require.Equal(t, int64(2), out.PendingInvitationCount)
	require.Equal(t, int64(3), out.MembershipCount)
}

func TestMembersHidesInvitedMemberBalanceFromManagers(t *testing.T) {
	f := newGroupUserFixture(true)
	ownedBalance, invitedBalance := 5.0, 80.0
	f.mgmt.members = []GroupMembership{
		{UserID: 11, GroupID: testManagedGroupID, Owned: true, Balance: &ownedBalance},
		{UserID: 21, GroupID: testManagedGroupID, Owned: false, Balance: &invitedBalance},
	}
	items, err := f.svc.Members(context.Background(), testManagerID, RoleGroupManager, testManagedGroupID)
	require.NoError(t, err)
	require.NotNil(t, items[0].Balance)
	require.Nil(t, items[1].Balance)

	items, err = f.svc.Members(context.Background(), 1, RoleAdmin, testManagedGroupID)
	require.NoError(t, err)
	require.NotNil(t, items[1].Balance)
}

func TestSubscriptionGroupRejectsBalanceGrants(t *testing.T) {
	f := newGroupUserFixture(true)
	f.mgmt.managedType = ManagedGroupTypeSubscription
	ctx := context.Background()
	f.repo.states[11] = &GroupMemberState{Owned: true, Role: RoleUser, Status: StatusActive}
	f.users.balances[testManagerID] = 20

	_, err := f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 11, GroupTransferGrant, 5, "")
	require.ErrorIs(t, err, ErrGroupTransferNotSupported)

	input := validGroupUserInput()
	input.InitialAmount = 5
	_, err = f.svc.CreateGroupUser(ctx, testManagerID, RoleGroupManager, testManagedGroupID, input)
	require.ErrorIs(t, err, ErrGroupTransferNotSupported)
	require.Empty(t, f.users.created)

	// 回收之前（例如额度组时期）划拨的余额仍然允许
	memberID := int64(11)
	f.repo.transfers = []GroupBalanceTransfer{{MemberID: &memberID, Direction: GroupTransferGrant, Amount: 3}}
	f.users.balances[11] = 3
	_, err = f.svc.TransferBalance(ctx, testManagerID, RoleGroupManager, testManagedGroupID, 11, GroupTransferReclaim, 3, "")
	require.NoError(t, err)
	require.InDelta(t, 23, f.users.balances[testManagerID], 1e-9)
}

func TestManagedGroupTypeNormalization(t *testing.T) {
	require.Equal(t, ManagedGroupTypeQuota, NormalizeManagedGroupType(GroupKindManaged, ""))
	require.Equal(t, ManagedGroupTypeSubscription, NormalizeManagedGroupType(GroupKindManaged, " Subscription "))
	require.Equal(t, "", NormalizeManagedGroupType(GroupKindChannel, ""))

	require.NoError(t, ValidateManagedGroupType(GroupKindManaged, ManagedGroupTypeQuota))
	require.ErrorIs(t, ValidateManagedGroupType(GroupKindManaged, "pool"), ErrInvalidManagedType)
	require.ErrorIs(t, ValidateManagedGroupType(GroupKindChannel, ManagedGroupTypeQuota), ErrManagedTypeChannel)
	require.NoError(t, ValidateManagedGroupType(GroupKindChannel, ""))

	require.Equal(t, GroupAssignmentModeAuto, ManagedGroupAllocationMode(ManagedGroupTypeQuota))
	require.Equal(t, GroupAssignmentModeManual, ManagedGroupAllocationMode(ManagedGroupTypeSubscription))

	quota := &Group{Kind: GroupKindManaged, ManagedType: ManagedGroupTypeQuota}
	subscription := &Group{Kind: GroupKindManaged, ManagedType: ManagedGroupTypeSubscription}
	channel := &Group{Kind: GroupKindChannel}
	require.True(t, quota.IsManagedQuota())
	require.False(t, quota.IsManagedSubscription())
	require.True(t, subscription.IsManagedSubscription())
	require.False(t, channel.IsManagedQuota())
	var nilGroup *Group
	require.False(t, nilGroup.IsManagedSubscription())
	require.False(t, ManagedSubscriptionBilling(&APIKey{}))
	require.True(t, ManagedSubscriptionBilling(&APIKey{Group: subscription}))
}
