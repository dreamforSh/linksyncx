package service

import (
	"context"
	"errors"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	GroupAssignmentModeAuto   = "auto"
	GroupAssignmentModeManual = "manual"
	GroupAssignmentActive     = "active"
	GroupAssignmentRevoked    = "revoked"
)

var (
	ErrGroupManagementForbidden = infraerrors.Forbidden("GROUP_MANAGEMENT_FORBIDDEN", "not authorized for this group")
	ErrGroupManagementBadInput  = infraerrors.BadRequest("GROUP_MANAGEMENT_BAD_INPUT", "invalid group management request")
	ErrAccountNotInGroup        = infraerrors.BadRequest("ACCOUNT_NOT_IN_GROUP", "account does not belong to this group")
	ErrCannotDemoteSelf         = infraerrors.BadRequest("CANNOT_DEMOTE_SELF", "cannot demote yourself from admin")
)

type GroupManagementOverview struct {
	Role        string            `json:"role"`
	Groups      []ManagedGroup    `json:"manageable_groups"`
	Memberships []GroupMembership `json:"memberships"`
	Assignments []AssignedAccount `json:"assignments"`
	Accounts    []AssignedAccount `json:"accounts"`
}

type GroupSettings struct {
	GroupID        int64  `json:"group_id"`
	Enabled        bool   `json:"enabled"`
	AllocationMode string `json:"allocation_mode"`
	MaxConcurrent  int    `json:"max_concurrent"`
	DailyLimit     int64  `json:"daily_limit"`
}

type ManagedGroup struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	MemberCount    int64   `json:"member_count"`
	AccountCount   int64   `json:"account_count"`
	Manager        bool    `json:"manager"`
	ManagerUserIDs []int64 `json:"manager_user_ids"`
	Enabled        bool    `json:"enabled"`
	AllocationMode string  `json:"allocation_mode"`
	MaxConcurrent  int     `json:"max_concurrent"`
	DailyLimit     int64   `json:"daily_limit"`
}

type GroupMembership struct {
	UserID           int64     `json:"user_id"`
	GroupID          int64     `json:"group_id"`
	GroupName        string    `json:"group_name"`
	Email            string    `json:"email,omitempty"`
	Username         string    `json:"username,omitempty"`
	MaxConcurrent    int       `json:"max_concurrent"`
	DailyLimit       int64     `json:"daily_limit"`
	DailyUsed        int64     `json:"daily_used"`
	DailyWindowStart time.Time `json:"daily_window_start"`
}

type AssignedAccount struct {
	ID              int64   `json:"id"`
	GroupID         int64   `json:"group_id"`
	Name            string  `json:"name"`
	Platform        string  `json:"platform"`
	Status          string  `json:"status"`
	UserID          int64   `json:"user_id,omitempty"`
	AssignmentMode  string  `json:"assignment_mode,omitempty"`
	RemainingQuota  *int64  `json:"remaining_quota"`
	AssignedUserIDs []int64 `json:"assigned_user_ids"`
}

type GroupManagementUser struct {
	ID              int64  `json:"id"`
	Email           string `json:"email"`
	Username        string `json:"username"`
	Role            string `json:"role"`
	MemberCount     int64  `json:"member_count"`
	AssignmentCount int64  `json:"assignment_count"`
}

type GroupManagementRepository interface {
	GatewayAdmission(context.Context, int64, int64, bool, bool) (GatewayAllocation, func(), error)
	Overview(context.Context, int64, string) (*GroupManagementOverview, error)
	CanAccessGroup(context.Context, int64, string, int64) (bool, error)
	ListMembers(context.Context, int64, *int64) ([]GroupMembership, error)
	ListAccounts(context.Context, int64, *int64) ([]AssignedAccount, error)
	ListAccountPool(context.Context, int64) ([]AssignedAccount, error)
	GetSettings(context.Context, int64) (*GroupSettings, error)
	UpdateSettings(context.Context, GroupSettings) (*GroupSettings, error)
	AddMember(context.Context, int64, int64) (*GroupMembership, error)
	RemoveMember(context.Context, int64, int64) error
	UpdateMemberLimit(context.Context, int64, int64, int, int64) (*GroupMembership, error)
	AssignAccounts(context.Context, int64, int64, []int64, string) error
	RevokeAccount(context.Context, int64, int64, int64) error
	AdminListGroups(context.Context) ([]ManagedGroup, error)
	AdminListUsers(context.Context) ([]GroupManagementUser, error)
	AddManager(context.Context, int64, int64) error
	RemoveManager(context.Context, int64, int64) error
}

type GroupManagementService struct {
	repo GroupManagementRepository
}

func NewGroupManagementService(repo GroupManagementRepository) *GroupManagementService {
	return &GroupManagementService{repo: repo}
}

func (s *GroupManagementService) Overview(ctx context.Context, userID int64, role string) (*GroupManagementOverview, error) {
	if userID <= 0 || !validRole(role) {
		return nil, ErrGroupManagementForbidden
	}
	return s.repo.Overview(ctx, userID, role)
}

func (s *GroupManagementService) Members(ctx context.Context, actorID int64, role string, groupID int64) ([]GroupMembership, error) {
	selfOnly, err := s.authorize(ctx, actorID, role, groupID, false)
	if err != nil {
		return nil, err
	}
	var userID *int64
	if selfOnly {
		userID = &actorID
	}
	return s.repo.ListMembers(ctx, groupID, userID)
}

func (s *GroupManagementService) Accounts(ctx context.Context, actorID int64, role string, groupID int64) ([]AssignedAccount, error) {
	selfOnly, err := s.authorize(ctx, actorID, role, groupID, false)
	if err != nil {
		return nil, err
	}
	if selfOnly {
		return s.repo.ListAccounts(ctx, groupID, &actorID)
	}
	return s.repo.ListAccountPool(ctx, groupID)
}

func (s *GroupManagementService) Settings(ctx context.Context, actorID int64, role string, groupID int64) (*GroupSettings, error) {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	return s.repo.GetSettings(ctx, groupID)
}

func (s *GroupManagementService) UpdateSettings(ctx context.Context, actorID int64, role string, settings GroupSettings) (*GroupSettings, error) {
	if err := s.requireAccess(ctx, actorID, role, settings.GroupID); err != nil {
		return nil, err
	}
	if settings.MaxConcurrent <= 0 || settings.DailyLimit < 0 || (settings.AllocationMode != GroupAssignmentModeAuto && settings.AllocationMode != GroupAssignmentModeManual) {
		return nil, ErrGroupManagementBadInput
	}
	return s.repo.UpdateSettings(ctx, settings)
}

func (s *GroupManagementService) AddMember(ctx context.Context, actorID int64, role string, groupID, userID int64) (*GroupMembership, error) {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	if userID <= 0 {
		return nil, ErrGroupManagementBadInput
	}
	return s.repo.AddMember(ctx, groupID, userID)
}

func (s *GroupManagementService) RemoveMember(ctx context.Context, actorID int64, role string, groupID, userID int64) error {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if userID <= 0 {
		return ErrGroupManagementBadInput
	}
	return s.repo.RemoveMember(ctx, groupID, userID)
}

func (s *GroupManagementService) UpdateMemberLimit(ctx context.Context, actorID int64, role string, groupID, userID int64, maxConcurrent int, dailyLimit int64) (*GroupMembership, error) {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	if userID <= 0 || maxConcurrent <= 0 || dailyLimit < 0 {
		return nil, ErrGroupManagementBadInput
	}
	return s.repo.UpdateMemberLimit(ctx, groupID, userID, maxConcurrent, dailyLimit)
}

func (s *GroupManagementService) AssignAccounts(ctx context.Context, actorID int64, role string, groupID, userID int64, accountIDs []int64, mode string) error {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if userID <= 0 || (mode != GroupAssignmentModeManual && mode != GroupAssignmentModeAuto) || (mode == GroupAssignmentModeAuto && len(accountIDs) != 0) {
		return ErrGroupManagementBadInput
	}
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return ErrGroupManagementBadInput
		}
	}
	return s.repo.AssignAccounts(ctx, groupID, userID, accountIDs, mode)
}

func (s *GroupManagementService) RevokeAccount(ctx context.Context, actorID int64, role string, groupID, userID, accountID int64) error {
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if userID <= 0 || accountID <= 0 {
		return ErrGroupManagementBadInput
	}
	return s.repo.RevokeAccount(ctx, groupID, userID, accountID)
}

func (s *GroupManagementService) AdminListGroups(ctx context.Context) ([]ManagedGroup, error) {
	return s.repo.AdminListGroups(ctx)
}
func (s *GroupManagementService) AdminListUsers(ctx context.Context) ([]GroupManagementUser, error) {
	return s.repo.AdminListUsers(ctx)
}
func (s *GroupManagementService) AdminAddManager(ctx context.Context, groupID, userID int64) error {
	if groupID <= 0 || userID <= 0 {
		return ErrGroupManagementBadInput
	}
	return s.repo.AddManager(ctx, groupID, userID)
}
func (s *GroupManagementService) AdminRemoveManager(ctx context.Context, groupID, userID int64) error {
	if groupID <= 0 || userID <= 0 {
		return ErrGroupManagementBadInput
	}
	return s.repo.RemoveManager(ctx, groupID, userID)
}

func (s *GroupManagementService) authorize(ctx context.Context, actorID int64, role string, groupID int64, write bool) (bool, error) {
	if groupID <= 0 || actorID <= 0 || !validRole(role) {
		return false, ErrGroupManagementForbidden
	}
	if write && !managerRole(role) {
		return false, ErrGroupManagementForbidden
	}
	if role == RoleAdmin {
		return false, nil
	}
	allowed, err := s.repo.CanAccessGroup(ctx, actorID, role, groupID)
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, ErrGroupManagementForbidden
	}
	return role == RoleUser, nil
}

func (s *GroupManagementService) requireAccess(ctx context.Context, actorID int64, role string, groupID int64) error {
	_, err := s.authorize(ctx, actorID, role, groupID, true)
	return err
}

func validRole(role string) bool {
	return role == RoleAdmin || role == RoleGroupManager || role == RoleUser
}
func managerRole(role string) bool { return role == RoleAdmin || role == RoleGroupManager }

// ResetDailyWindow is deliberately independent of persistence so quota-window behavior
// can be tested without Postgres. A zero daily limit remains unlimited in this MVP.
func ResetDailyWindow(member *GroupMembership, now time.Time) bool {
	if member == nil || member.DailyWindowStart.IsZero() {
		return false
	}
	dateOnly := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	windowDate := member.DailyWindowStart.In(now.Location())
	if !windowDate.Before(dateOnly) {
		return false
	}
	member.DailyUsed = 0
	member.DailyWindowStart = dateOnly
	return true
}

func IsGroupManagementError(err error) bool {
	return errors.Is(err, ErrGroupManagementForbidden) || errors.Is(err, ErrGroupManagementBadInput) || errors.Is(err, ErrAccountNotInGroup)
}
