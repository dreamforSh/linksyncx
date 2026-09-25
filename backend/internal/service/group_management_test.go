package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

type groupManagementRepoStub struct {
	GroupManagementRepository
	allowed   bool
	assignErr error
	poolCalls int
}

func (s *groupManagementRepoStub) CanAccessGroup(context.Context, int64, string, int64) (bool, error) {
	return s.allowed, nil
}
func (s *groupManagementRepoStub) AssignAccounts(context.Context, int64, int64, []int64, string) error {
	return s.assignErr
}
func (s *groupManagementRepoStub) ListAccountPool(context.Context, int64) ([]AssignedAccount, error) {
	s.poolCalls++
	return []AssignedAccount{{ID: 100, GroupID: 42}}, nil
}
func (s *groupManagementRepoStub) ListAccounts(_ context.Context, _ int64, userID *int64) ([]AssignedAccount, error) {
	return []AssignedAccount{{ID: 100, UserID: *userID, GroupID: 42}}, nil
}

func TestGroupManagementRejectsUnauthorizedGroup(t *testing.T) {
	svc := NewGroupManagementService(&groupManagementRepoStub{})
	_, err := svc.Members(context.Background(), 7, RoleGroupManager, 42)
	if !errors.Is(err, ErrGroupManagementForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
}

func TestGroupManagementRejectsAccountOutsideGroup(t *testing.T) {
	repo := &groupManagementRepoStub{allowed: true, assignErr: ErrAccountNotInGroup}
	svc := NewGroupManagementService(repo)
	err := svc.AssignAccounts(context.Background(), 7, RoleGroupManager, 42, 9, []int64{100}, GroupAssignmentModeManual)
	if !errors.Is(err, ErrAccountNotInGroup) {
		t.Fatalf("error = %v, want account-not-in-group", err)
	}
}

func TestGroupManagementMemberWriteRequiresManager(t *testing.T) {
	svc := NewGroupManagementService(&groupManagementRepoStub{allowed: true})
	if _, err := svc.AddMember(context.Background(), 7, RoleUser, 42, 9); !errors.Is(err, ErrGroupManagementForbidden) {
		t.Fatalf("add member error = %v, want forbidden", err)
	}
	if err := svc.RemoveMember(context.Background(), 7, RoleUser, 42, 9); !errors.Is(err, ErrGroupManagementForbidden) {
		t.Fatalf("remove member error = %v, want forbidden", err)
	}
	if _, err := svc.Settings(context.Background(), 7, RoleUser, 42); !errors.Is(err, ErrGroupManagementForbidden) {
		t.Fatalf("settings error = %v, want forbidden", err)
	}
}

func TestGroupManagementRejectsInvalidSettingsAndAutoIDs(t *testing.T) {
	svc := NewGroupManagementService(&groupManagementRepoStub{allowed: true})
	if _, err := svc.UpdateSettings(context.Background(), 7, RoleGroupManager, GroupSettings{GroupID: 42, AllocationMode: GroupAssignmentModeAuto, MaxConcurrent: 0}); !errors.Is(err, ErrGroupManagementBadInput) {
		t.Fatalf("settings error = %v, want bad input", err)
	}
	if err := svc.AssignAccounts(context.Background(), 7, RoleGroupManager, 42, 9, []int64{100}, GroupAssignmentModeAuto); !errors.Is(err, ErrGroupManagementBadInput) {
		t.Fatalf("auto accounts error = %v, want bad input", err)
	}
}
func TestGroupManagementAccountsReadScope(t *testing.T) {
	ctx := context.Background()
	repo := &groupManagementRepoStub{allowed: true}
	svc := NewGroupManagementService(repo)
	items, err := svc.Accounts(ctx, 7, RoleUser, 42)
	if err != nil || len(items) != 1 || items[0].UserID != 7 || repo.poolCalls != 0 {
		t.Fatalf("member accounts = %+v, error = %v, pool calls = %d", items, err, repo.poolCalls)
	}
	items, err = svc.Accounts(ctx, 7, RoleGroupManager, 42)
	if err != nil || len(items) != 1 || items[0].GroupID != 42 || repo.poolCalls != 1 {
		t.Fatalf("manager pool = %+v, error = %v, pool calls = %d", items, err, repo.poolCalls)
	}
}

func TestResetDailyWindow(t *testing.T) {
	now := time.Date(2025, 1, 2, 8, 0, 0, 0, time.UTC)
	member := &GroupMembership{DailyUsed: 11, DailyWindowStart: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	if !ResetDailyWindow(member, now) {
		t.Fatal("expected stale window to reset")
	}
	if member.DailyUsed != 0 || !member.DailyWindowStart.Equal(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("member after reset = %+v", member)
	}
}

type settingsRepoStub struct {
	groupManagementRepoStub
	current *GroupSettings
	updated *GroupSettings
}

func (s *settingsRepoStub) GetSettings(context.Context, int64) (*GroupSettings, error) {
	current := *s.current
	return &current, nil
}

func (s *settingsRepoStub) UpdateSettings(_ context.Context, settings GroupSettings) (*GroupSettings, error) {
	s.updated = &settings
	return &settings, nil
}

func TestGroupManagementManagedGroupKeepsEnforcement(t *testing.T) {
	repo := &settingsRepoStub{current: &GroupSettings{GroupID: 42, Managed: true, Enabled: true, AllocationMode: GroupAssignmentModeManual, MaxConcurrent: 1}}
	svc := NewGroupManagementService(repo)
	ctx := context.Background()

	disable := GroupSettings{GroupID: 42, Enabled: false, AllocationMode: GroupAssignmentModeManual, MaxConcurrent: 1}
	if _, err := svc.UpdateSettings(ctx, 1, RoleAdmin, disable); !errors.Is(err, ErrManagedGroupEnforcement) {
		t.Fatalf("disable managed enforcement error = %v, want managed-enforcement", err)
	}
	if repo.updated != nil {
		t.Fatalf("settings were persisted despite rejection: %+v", repo.updated)
	}

	// 管理分组的分配方式由类型决定：订阅组固定手动分配，额度组固定自动调度
	repo.current.ManagedType = ManagedGroupTypeSubscription
	switchMode := GroupSettings{GroupID: 42, Enabled: true, AllocationMode: GroupAssignmentModeAuto, MaxConcurrent: 2, DefaultLimit5hUSD: 1.5}
	if _, err := svc.UpdateSettings(ctx, 1, RoleAdmin, switchMode); err != nil {
		t.Fatalf("updating managed settings error = %v, want nil", err)
	}
	if repo.updated == nil || repo.updated.AllocationMode != GroupAssignmentModeManual || repo.updated.DefaultLimit5hUSD != 1.5 {
		t.Fatalf("subscription group settings = %+v, want manual allocation and default 5h limit", repo.updated)
	}
	repo.current.ManagedType = ManagedGroupTypeQuota
	switchMode.AllocationMode = GroupAssignmentModeManual
	if _, err := svc.UpdateSettings(ctx, 1, RoleAdmin, switchMode); err != nil || repo.updated.AllocationMode != GroupAssignmentModeAuto {
		t.Fatalf("quota group settings = %+v, error = %v, want auto allocation", repo.updated, err)
	}
	negative := switchMode
	negative.DefaultLimit7dUSD = -1
	if _, err := svc.UpdateSettings(ctx, 1, RoleAdmin, negative); !errors.Is(err, ErrGroupManagementBadInput) {
		t.Fatalf("negative default limit error = %v, want bad input", err)
	}

	repo.current.Managed = false
	repo.updated = nil
	if _, err := svc.UpdateSettings(ctx, 1, RoleAdmin, disable); err != nil {
		t.Fatalf("disable channel group enforcement error = %v, want nil", err)
	}
	if repo.updated == nil || repo.updated.Enabled {
		t.Fatalf("channel group enforcement was not disabled: %+v", repo.updated)
	}
}
