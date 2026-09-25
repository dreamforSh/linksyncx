package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 组用户与余额划拨：组管理员在管理分组里自建组用户、在自己与组用户之间划拨 / 回收余额。
// 这些能力只对管理分组（kind=managed）开放；渠道分组的旧组管理行为保持不变。

const (
	GroupTransferGrant   = "grant"
	GroupTransferReclaim = "reclaim"

	groupUserPasswordMinLength = 6
	// bcrypt 只接受 72 字节以内的密码
	groupUserPasswordMaxBytes = 72
	groupTransferNotesMaxLen  = 500
	maxGroupTransferAmount    = 1_000_000_000
	maxUserGroupSummaryIDs    = 200
)

var (
	ErrGroupNotManaged           = infraerrors.BadRequest("GROUP_NOT_MANAGED", "this operation is only available for managed groups")
	ErrGroupTransferManagerOnly  = infraerrors.Forbidden("GROUP_TRANSFER_MANAGER_ONLY", "only group managers can transfer balance; admins adjust user balance in user management")
	ErrGroupTransferInsufficient = infraerrors.BadRequest("GROUP_TRANSFER_INSUFFICIENT_BALANCE", "insufficient balance for this transfer")
	ErrGroupTransferSelf         = infraerrors.BadRequest("GROUP_TRANSFER_SELF", "cannot transfer balance to yourself")
	// 回收只能收回本分组划拨给该成员的净额，不能动成员自己充值的余额
	ErrGroupTransferExceedsGranted = infraerrors.BadRequest("GROUP_TRANSFER_EXCEEDS_GRANTED", "cannot reclaim more than the group has granted to this member")
	ErrGroupUserNotOwned           = infraerrors.Forbidden("GROUP_USER_NOT_OWNED", "the user was not created in this group")
	ErrGroupMemberNotFound         = infraerrors.NotFound("GROUP_MEMBER_NOT_FOUND", "the user is not a member of this group")
	ErrGroupMemberNotEligible      = infraerrors.Forbidden("GROUP_MEMBER_NOT_ELIGIBLE", "group managers can only add group users created in groups they manage")
	ErrGroupUserPasswordInvalid    = infraerrors.BadRequest("GROUP_USER_PASSWORD_INVALID", "password must be 6-72 bytes long")
	// 订阅组不扣余额，划拨没有意义；回收仍允许，便于类型变更后收回之前划拨的余额
	ErrGroupTransferNotSupported = infraerrors.BadRequest("GROUP_TRANSFER_NOT_SUPPORTED", "subscription groups do not bill balance, so balance cannot be granted")

	errGroupUserDepsMissing = errors.New("group user management is not configured")
)

// GroupMemberState 成员行与用户的最小状态，用于写路径的权限判断。
type GroupMemberState struct {
	Owned  bool
	Role   string
	Status string
}

// GroupOwnedUser 组管理员名下（由其管理的分组拥有）的组用户，供「添加已有组用户」选择。
type GroupOwnedUser struct {
	UserID    int64  `json:"user_id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	Status    string `json:"status"`
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
}

// GroupBalanceTransfer 一次余额划拨（grant：组管理员 → 组用户）或回收（reclaim：组用户 → 组管理员）。
type GroupBalanceTransfer struct {
	ID          int64     `json:"id"`
	GroupID     int64     `json:"group_id"`
	ManagerID   *int64    `json:"manager_id"`
	ManagerName string    `json:"manager_name"`
	MemberID    *int64    `json:"member_id"`
	MemberName  string    `json:"member_name"`
	Direction   string    `json:"direction"`
	Amount      float64   `json:"amount"`
	Notes       string    `json:"notes"`
	CreatedAt   time.Time `json:"created_at"`
}

// GroupManagementSummary 侧栏用：当前用户管理的分组数、所属分组数与待处理的邀请数。
type GroupManagementSummary struct {
	Role                   string `json:"role"`
	ManagedGroupCount      int64  `json:"managed_group_count"`
	MembershipCount        int64  `json:"membership_count"`
	PendingInvitationCount int64  `json:"pending_invitation_count"`
}

// GroupRef 分组的最小引用。
type GroupRef struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
}

// UserGroupSummary 超管用户列表用：用户管理的分组与拥有它的管理分组。
type UserGroupSummary struct {
	UserID        int64      `json:"user_id"`
	OwnedGroup    *GroupRef  `json:"owned_group,omitempty"`
	ManagedGroups []GroupRef `json:"managed_groups"`
}

// CreateGroupUserInput 组管理员新建组用户的输入。
type CreateGroupUserInput struct {
	Email         string
	Username      string
	Password      string
	MaxConcurrent *int
	DailyLimit    *int64
	// InitialAmount 建号后立即从组管理员余额划拨给新用户的金额，0 表示不划拨
	InitialAmount float64
}

// GroupUserRepository 组用户与余额划拨的持久化。所有方法都必须感知 ctx 中的 ent 事务。
type GroupUserRepository interface {
	Summary(ctx context.Context, userID int64) (managedGroups int64, memberships int64, err error)
	LockGroup(ctx context.Context, groupID int64) error
	UpdateCategory(ctx context.Context, groupID int64, category string) (bool, error)
	// InsertMember 插入成员行（未指定额度时套用分组默认值）；成员已存在时幂等返回 nil，
	// 用户不存在或未启用时返回 ErrGroupManagementBadInput。
	InsertMember(ctx context.Context, groupID, userID int64, owned bool, createdBy int64, maxConcurrent *int, dailyLimit *int64) error
	GetMemberState(ctx context.Context, groupID, userID int64) (*GroupMemberState, error)
	IsOwnedByManager(ctx context.Context, userID, managerID int64) (bool, error)
	ListOwnedUsersForManager(ctx context.Context, managerID, excludeGroupID int64) ([]GroupOwnedUser, error)
	DeleteMember(ctx context.Context, groupID, userID int64) (bool, error)
	CountMemberships(ctx context.Context, userID int64) (int64, error)
	DisableGroupAPIKeys(ctx context.Context, userID, groupID int64) (int64, error)
	// SetUserStatus 只改普通用户（role=user）的状态。
	SetUserStatus(ctx context.Context, userID int64, status string) (bool, error)
	// LockUsers 按 ID 升序锁定用户行，避免相向划拨互相等待。
	LockUsers(ctx context.Context, userIDs ...int64) error
	// NetGranted 本分组累计划拨给成员的净额（grant 减 reclaim）。
	NetGranted(ctx context.Context, groupID, memberID int64) (float64, error)
	InsertTransfer(ctx context.Context, transfer *GroupBalanceTransfer) error
	InsertAdjustmentRecord(ctx context.Context, record *RedeemCode) error
	ListTransfers(ctx context.Context, groupID, memberID int64, page, pageSize int) ([]GroupBalanceTransfer, int64, error)
	UserGroupSummaries(ctx context.Context, userIDs []int64) ([]UserGroupSummary, error)

	// 邮箱邀请
	CountPendingInvitations(ctx context.Context, userID int64) (int64, error)
	// FindInviteeByEmail 按规范化邮箱查找未删除的用户，找不到返回 nil。
	FindInviteeByEmail(ctx context.Context, email string) (*GroupInvitee, error)
	// CreateInvitation 创建待处理邀请；已有待处理邀请时返回那条且 created=false。
	CreateInvitation(ctx context.Context, groupID, userID int64, email string, invitedBy int64) (*GroupInvitation, bool, error)
	ListGroupInvitations(ctx context.Context, groupID int64, limit int) ([]GroupInvitation, error)
	RevokeInvitation(ctx context.Context, groupID, invitationID int64) (bool, error)
	ListUserInvitations(ctx context.Context, userID int64) ([]GroupInvitation, error)
	// LockUserInvitation 锁定属于该用户、仍待处理且分组未删除的邀请，找不到返回 nil。
	LockUserInvitation(ctx context.Context, invitationID, userID int64) (*GroupInvitation, error)
	SetInvitationStatus(ctx context.Context, invitationID int64, status string) error
}

// groupUserAccountStore 组用户写路径需要的用户仓储能力（UserRepository 的子集）。
type groupUserAccountStore interface {
	Create(ctx context.Context, user *User) error
	GetByID(ctx context.Context, id int64) (*User, error)
	Update(ctx context.Context, user *User, fields UserUpdateFields) error
	AdjustBalance(ctx context.Context, id int64, delta float64) (BalanceChange, error)
	AddGroupToAllowedGroups(ctx context.Context, userID int64, groupID int64) error
	RemoveGroupFromUserAllowedGroups(ctx context.Context, userID int64, groupID int64) error
}

type userBalanceCacheInvalidator interface {
	InvalidateUserBalance(ctx context.Context, userID int64) error
}

type defaultUserConcurrencyProvider interface {
	GetDefaultConcurrency(ctx context.Context) int
}

// GroupUserDependencies 组用户与余额划拨需要的依赖；缺省时这些写路径返回错误，
// 其余组管理能力照常可用。
type GroupUserDependencies struct {
	GroupUsers   GroupUserRepository
	Users        groupUserAccountStore
	EntClient    *dbent.Client
	AuthCache    APIKeyAuthCacheInvalidator
	BalanceCache userBalanceCacheInvalidator
	Defaults     defaultUserConcurrencyProvider
}

// ProvideGroupManagementService 注入组用户、账号限额与重置卡依赖的构造函数（wire 使用）。
func ProvideGroupManagementService(
	repo GroupManagementRepository,
	groupUsers GroupUserRepository,
	userRepo UserRepository,
	entClient *dbent.Client,
	authCache APIKeyAuthCacheInvalidator,
	billingCache *BillingCacheService,
	settingService *SettingService,
	accountRepo AccountRepository,
	accountUsage *AccountUsageService,
	openAIQuota *OpenAIQuotaService,
	rateLimit *RateLimitService,
) *GroupManagementService {
	deps := GroupUserDependencies{GroupUsers: groupUsers, EntClient: entClient, AuthCache: authCache}
	if userRepo != nil {
		deps.Users = userRepo
	}
	if billingCache != nil {
		deps.BalanceCache = billingCache
	}
	if settingService != nil {
		deps.Defaults = settingService
	}
	accountDeps := GroupAccountDependencies{}
	if accountRepo != nil {
		accountDeps.Accounts = accountRepo
	}
	if accountUsage != nil {
		accountDeps.Usage = accountUsage
	}
	if openAIQuota != nil {
		accountDeps.Quota = openAIQuota
	}
	if rateLimit != nil {
		accountDeps.Recoverer = rateLimit
	}
	return NewGroupManagementService(repo).WithGroupUserDependencies(deps).WithGroupAccountDependencies(accountDeps)
}

// WithGroupUserDependencies 挂载组用户相关依赖，返回同一个服务便于链式构造。
func (s *GroupManagementService) WithGroupUserDependencies(deps GroupUserDependencies) *GroupManagementService {
	s.groupUsers = deps.GroupUsers
	s.users = deps.Users
	s.entClient = deps.EntClient
	s.authCache = deps.AuthCache
	s.balanceCache = deps.BalanceCache
	s.defaults = deps.Defaults
	return s
}

func (s *GroupManagementService) requireGroupUserDeps() error {
	if s.groupUsers == nil || s.users == nil {
		return errGroupUserDepsMissing
	}
	return nil
}

// withTx 在 ent 事务里执行 fn；没有 ent client（单测）时直接执行。
func (s *GroupManagementService) withTx(ctx context.Context, fn func(context.Context) error) error {
	if s.entClient == nil {
		return fn(ctx)
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(dbent.NewTxContext(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *GroupManagementService) isManagedGroup(ctx context.Context, groupID int64) (bool, error) {
	settings, err := s.repo.GetSettings(ctx, groupID)
	if err != nil {
		return false, err
	}
	return settings != nil && settings.Managed, nil
}

func (s *GroupManagementService) requireManagedGroup(ctx context.Context, groupID int64) error {
	_, err := s.managedGroupSettings(ctx, groupID)
	return err
}

// managedGroupSettings 返回管理分组的有效设置（含类型）；不是管理分组时返回 ErrGroupNotManaged。
func (s *GroupManagementService) managedGroupSettings(ctx context.Context, groupID int64) (*GroupSettings, error) {
	settings, err := s.repo.GetSettings(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if settings == nil || !settings.Managed {
		return nil, ErrGroupNotManaged
	}
	return settings, nil
}

func (s *GroupManagementService) invalidateAuth(ctx context.Context, userIDs ...int64) {
	if s.authCache == nil {
		return
	}
	for _, id := range userIDs {
		s.authCache.InvalidateAuthCacheByUserID(ctx, id)
	}
}

func (s *GroupManagementService) invalidateBalances(ctx context.Context, userIDs ...int64) {
	s.invalidateAuth(ctx, userIDs...)
	if s.balanceCache == nil {
		return
	}
	for _, id := range userIDs {
		if err := s.balanceCache.InvalidateUserBalance(ctx, id); err != nil {
			logger.LegacyPrintf("service.group_management", "invalidate user balance cache failed: user_id=%d err=%v", id, err)
		}
	}
}

func (s *GroupManagementService) memberView(ctx context.Context, groupID, userID int64) (*GroupMembership, error) {
	items, err := s.repo.ListMembers(ctx, groupID, &userID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrGroupMemberNotFound
	}
	return &items[0], nil
}

func validGroupUserPassword(password string) bool {
	return len(password) >= groupUserPasswordMinLength && len(password) <= groupUserPasswordMaxBytes
}

// normalizeTransferAmount 把金额规整到余额列的 8 位小数，并拒绝非法值。
func normalizeTransferAmount(amount float64) (float64, bool) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || amount > maxGroupTransferAmount {
		return 0, false
	}
	rounded := math.Round(amount*1e8) / 1e8
	return rounded, rounded > 0
}

// Summary 返回侧栏需要的组管理概况。
func (s *GroupManagementService) Summary(ctx context.Context, actorID int64, role string) (*GroupManagementSummary, error) {
	if actorID <= 0 || !validRole(role) {
		return nil, ErrGroupManagementForbidden
	}
	out := &GroupManagementSummary{Role: role}
	if s.groupUsers == nil {
		return out, nil
	}
	managed, memberships, err := s.groupUsers.Summary(ctx, actorID)
	if err != nil {
		return nil, err
	}
	out.ManagedGroupCount = managed
	out.MembershipCount = memberships
	pending, err := s.groupUsers.CountPendingInvitations(ctx, actorID)
	if err != nil {
		return nil, err
	}
	out.PendingInvitationCount = pending
	return out, nil
}

// UpdateCategory 修改管理分组的分类（企业 / Team），组管理员只能改自己管理的分组。
func (s *GroupManagementService) UpdateCategory(ctx context.Context, actorID int64, role string, groupID int64, category string) error {
	if err := s.requireGroupUserDeps(); err != nil {
		return err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	normalized := strings.ToLower(strings.TrimSpace(category))
	if err := ValidateGroupCategory(GroupKindManaged, normalized); err != nil || normalized == "" {
		return ErrInvalidGroupCategory
	}
	if err := s.requireManagedGroup(ctx, groupID); err != nil {
		return err
	}
	updated, err := s.groupUsers.UpdateCategory(ctx, groupID, normalized)
	if err != nil {
		return err
	}
	if !updated {
		return ErrGroupNotManaged
	}
	return nil
}

// CreateGroupUser 在管理分组里新建组用户：普通用户、只能看到被授权的分组、初始余额为 0，
// 并成为本分组拥有的成员。可选地立即从组管理员余额划拨初始额度。
func (s *GroupManagementService) CreateGroupUser(ctx context.Context, actorID int64, role string, groupID int64, input CreateGroupUserInput) (*GroupMembership, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	settings, err := s.managedGroupSettings(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if input.InitialAmount != 0 && settings.ManagedType == ManagedGroupTypeSubscription {
		return nil, ErrGroupTransferNotSupported
	}
	email := strings.TrimSpace(input.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, ErrGroupManagementBadInput
	}
	if !validGroupUserPassword(input.Password) {
		return nil, ErrGroupUserPasswordInvalid
	}
	if (input.MaxConcurrent != nil && *input.MaxConcurrent <= 0) || (input.DailyLimit != nil && *input.DailyLimit < 0) {
		return nil, ErrGroupManagementBadInput
	}
	var amount float64
	if input.InitialAmount != 0 {
		if role != RoleGroupManager {
			return nil, ErrGroupTransferManagerOnly
		}
		normalized, ok := normalizeTransferAmount(input.InitialAmount)
		if !ok {
			return nil, ErrGroupManagementBadInput
		}
		amount = normalized
	}

	user := &User{
		Email:                email,
		Username:             strings.TrimSpace(input.Username),
		Role:                 RoleUser,
		Status:               StatusActive,
		Balance:              0,
		Concurrency:          s.defaultUserConcurrency(ctx),
		AllowedGroups:        []int64{groupID},
		RestrictPublicGroups: true,
	}
	if err := user.SetPassword(input.Password); err != nil {
		return nil, ErrGroupUserPasswordInvalid
	}

	err = s.withTx(ctx, func(txCtx context.Context) error {
		if err := s.groupUsers.LockGroup(txCtx, groupID); err != nil {
			return err
		}
		if err := s.users.Create(txCtx, user); err != nil {
			return err
		}
		if err := s.groupUsers.InsertMember(txCtx, groupID, user.ID, true, actorID, input.MaxConcurrent, input.DailyLimit); err != nil {
			return err
		}
		if amount > 0 {
			_, err := s.transferInTx(txCtx, groupID, actorID, user.ID, GroupTransferGrant, amount, "")
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if amount > 0 {
		s.invalidateBalances(ctx, actorID, user.ID)
	}
	logger.LegacyPrintf("service.group_management", "audit: group user created actor_id=%d role=%s group_id=%d user_id=%d initial_amount=%.8f",
		actorID, role, groupID, user.ID, amount)
	return s.memberView(ctx, groupID, user.ID)
}

func (s *GroupManagementService) defaultUserConcurrency(ctx context.Context) int {
	if s.defaults != nil {
		if value := s.defaults.GetDefaultConcurrency(ctx); value > 0 {
			return value
		}
	}
	return 1
}

// SetGroupUserStatus 停用 / 启用本分组拥有的组用户。
func (s *GroupManagementService) SetGroupUserStatus(ctx context.Context, actorID int64, role string, groupID, userID int64, status string) error {
	if err := s.requireGroupUserDeps(); err != nil {
		return err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if status != StatusActive && status != StatusDisabled {
		return ErrGroupManagementBadInput
	}
	if userID <= 0 || userID == actorID {
		return ErrGroupManagementBadInput
	}
	if err := s.requireManagedGroup(ctx, groupID); err != nil {
		return err
	}
	if err := s.requireOwnedMember(ctx, groupID, userID); err != nil {
		return err
	}
	updated, err := s.groupUsers.SetUserStatus(ctx, userID, status)
	if err != nil {
		return err
	}
	if !updated {
		return ErrGroupUserNotOwned
	}
	s.invalidateAuth(ctx, userID)
	logger.LegacyPrintf("service.group_management", "audit: group user status changed actor_id=%d group_id=%d user_id=%d status=%s",
		actorID, groupID, userID, status)
	return nil
}

// ResetGroupUserPassword 重置本分组拥有的组用户的登录密码；密码哈希变化会让旧登录态失效。
func (s *GroupManagementService) ResetGroupUserPassword(ctx context.Context, actorID int64, role string, groupID, userID int64, password string) error {
	if err := s.requireGroupUserDeps(); err != nil {
		return err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if userID <= 0 || userID == actorID {
		return ErrGroupManagementBadInput
	}
	if !validGroupUserPassword(password) {
		return ErrGroupUserPasswordInvalid
	}
	if err := s.requireManagedGroup(ctx, groupID); err != nil {
		return err
	}
	if err := s.requireOwnedMember(ctx, groupID, userID); err != nil {
		return err
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := user.SetPassword(password); err != nil {
		return ErrGroupUserPasswordInvalid
	}
	if err := s.users.Update(ctx, user, UserUpdateFields{PasswordHash: true}); err != nil {
		return err
	}
	logger.LegacyPrintf("service.group_management", "audit: group user password reset actor_id=%d group_id=%d user_id=%d",
		actorID, groupID, userID)
	return nil
}

func (s *GroupManagementService) requireOwnedMember(ctx context.Context, groupID, userID int64) error {
	state, err := s.groupUsers.GetMemberState(ctx, groupID, userID)
	if err != nil {
		return err
	}
	if state == nil {
		return ErrGroupMemberNotFound
	}
	// 组用户固定为普通用户；被超管改成其他角色后不再归组管理员管理
	if !state.Owned || state.Role != RoleUser {
		return ErrGroupUserNotOwned
	}
	return nil
}

// TransferBalance 组管理员在自己与组用户之间划拨（grant）或回收（reclaim）余额。
// 两侧余额、划拨流水与余额记录在同一事务内写入；余额不足时整体回滚。
func (s *GroupManagementService) TransferBalance(ctx context.Context, actorID int64, role string, groupID, memberID int64, direction string, amount float64, notes string) (*GroupBalanceTransfer, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	if role != RoleGroupManager {
		return nil, ErrGroupTransferManagerOnly
	}
	if direction != GroupTransferGrant && direction != GroupTransferReclaim {
		return nil, ErrGroupManagementBadInput
	}
	normalized, ok := normalizeTransferAmount(amount)
	if !ok || memberID <= 0 {
		return nil, ErrGroupManagementBadInput
	}
	if memberID == actorID {
		return nil, ErrGroupTransferSelf
	}
	notes = strings.TrimSpace(notes)
	if len([]rune(notes)) > groupTransferNotesMaxLen {
		return nil, ErrGroupManagementBadInput
	}
	settings, err := s.managedGroupSettings(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if direction == GroupTransferGrant && settings.ManagedType == ManagedGroupTypeSubscription {
		return nil, ErrGroupTransferNotSupported
	}

	var transfer *GroupBalanceTransfer
	err = s.withTx(ctx, func(txCtx context.Context) error {
		state, err := s.groupUsers.GetMemberState(txCtx, groupID, memberID)
		if err != nil {
			return err
		}
		if state == nil {
			return ErrGroupMemberNotFound
		}
		transfer, err = s.transferInTx(txCtx, groupID, actorID, memberID, direction, normalized, notes)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.invalidateBalances(ctx, actorID, memberID)
	logger.LegacyPrintf("service.group_management", "audit: group balance transfer actor_id=%d group_id=%d member_id=%d direction=%s amount=%.8f",
		actorID, groupID, memberID, direction, normalized)
	return transfer, nil
}

func (s *GroupManagementService) transferInTx(ctx context.Context, groupID, managerID, memberID int64, direction string, amount float64, notes string) (*GroupBalanceTransfer, error) {
	if err := s.groupUsers.LockUsers(ctx, managerID, memberID); err != nil {
		return nil, err
	}
	from, to := managerID, memberID
	if direction == GroupTransferReclaim {
		from, to = memberID, managerID
		// 成员行已加锁，净额在本事务内不会被并发回收改变
		granted, err := s.groupUsers.NetGranted(ctx, groupID, memberID)
		if err != nil {
			return nil, err
		}
		if amount-granted > 1e-9 {
			return nil, ErrGroupTransferExceedsGranted
		}
	}
	if _, err := s.users.AdjustBalance(ctx, from, -amount); err != nil {
		if errors.Is(err, ErrBalanceNegative) {
			return nil, ErrGroupTransferInsufficient
		}
		return nil, err
	}
	if _, err := s.users.AdjustBalance(ctx, to, amount); err != nil {
		return nil, err
	}
	transfer := &GroupBalanceTransfer{
		GroupID:   groupID,
		ManagerID: &managerID,
		MemberID:  &memberID,
		Direction: direction,
		Amount:    amount,
		Notes:     notes,
	}
	if err := s.groupUsers.InsertTransfer(ctx, transfer); err != nil {
		return nil, err
	}
	// 两侧各记一条余额记录，用户在「余额记录」里能看到划入 / 划出
	for _, entry := range []struct {
		userID int64
		value  float64
	}{{from, -amount}, {to, amount}} {
		code, err := GenerateRedeemCode()
		if err != nil {
			return nil, err
		}
		userID := entry.userID
		usedAt := time.Now()
		if err := s.groupUsers.InsertAdjustmentRecord(ctx, &RedeemCode{
			Code:   code,
			Type:   AdjustmentTypeGroupTransfer,
			Value:  entry.value,
			Status: StatusUsed,
			UsedBy: &userID,
			UsedAt: &usedAt,
			Notes:  notes,
		}); err != nil {
			return nil, err
		}
	}
	return transfer, nil
}

// ListTransfers 分页列出分组的划拨流水，可按组用户过滤。
func (s *GroupManagementService) ListTransfers(ctx context.Context, actorID int64, role string, groupID, memberID int64, page, pageSize int) ([]GroupBalanceTransfer, int64, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, 0, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, 0, err
	}
	if memberID < 0 {
		return nil, 0, ErrGroupManagementBadInput
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	return s.groupUsers.ListTransfers(ctx, groupID, memberID, page, pageSize)
}

// OwnedUserCandidates 组管理员可加入本分组的已有组用户：由其管理的其他分组拥有、尚未加入本分组。
// 超管按用户 ID 添加，返回空列表。
func (s *GroupManagementService) OwnedUserCandidates(ctx context.Context, actorID int64, role string, groupID int64) ([]GroupOwnedUser, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	if err := s.requireManagedGroup(ctx, groupID); err != nil {
		return nil, err
	}
	if role != RoleGroupManager {
		return []GroupOwnedUser{}, nil
	}
	return s.groupUsers.ListOwnedUsersForManager(ctx, actorID, groupID)
}

// AdminUserGroupSummaries 超管用户列表展示「管理的分组 / 所属管理分组」。
func (s *GroupManagementService) AdminUserGroupSummaries(ctx context.Context, userIDs []int64) ([]UserGroupSummary, error) {
	if s.groupUsers == nil {
		return []UserGroupSummary{}, nil
	}
	seen := make(map[int64]struct{}, len(userIDs))
	clean := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		clean = append(clean, id)
	}
	if len(clean) > maxUserGroupSummaryIDs {
		return nil, ErrGroupManagementBadInput
	}
	if len(clean) == 0 {
		return []UserGroupSummary{}, nil
	}
	return s.groupUsers.UserGroupSummaries(ctx, clean)
}

// addManagedMember 管理分组的加成员：组管理员只能加自己名下的组用户；成员资格与
// 「允许绑定的分组」同步写入。
func (s *GroupManagementService) addManagedMember(ctx context.Context, actorID int64, role string, groupID, userID int64) (*GroupMembership, error) {
	if role == RoleGroupManager {
		owned, err := s.groupUsers.IsOwnedByManager(ctx, userID, actorID)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, ErrGroupMemberNotEligible
		}
	}
	err := s.withTx(ctx, func(txCtx context.Context) error {
		if err := s.groupUsers.LockGroup(txCtx, groupID); err != nil {
			return err
		}
		if err := s.groupUsers.InsertMember(txCtx, groupID, userID, false, actorID, nil, nil); err != nil {
			return err
		}
		return s.users.AddGroupToAllowedGroups(txCtx, userID, groupID)
	})
	if err != nil {
		return nil, err
	}
	s.invalidateAuth(ctx, userID)
	return s.memberView(ctx, groupID, userID)
}

// removeManagedMember 管理分组的移除成员：同时收回绑定资格、停用该用户绑定在本分组的 Key；
// 移除的是本分组拥有的组用户且他不再属于任何分组时，停用该用户。
func (s *GroupManagementService) removeManagedMember(ctx context.Context, actorID int64, groupID, userID int64) error {
	var disabledUser bool
	err := s.withTx(ctx, func(txCtx context.Context) error {
		if err := s.groupUsers.LockGroup(txCtx, groupID); err != nil {
			return err
		}
		state, err := s.groupUsers.GetMemberState(txCtx, groupID, userID)
		if err != nil {
			return err
		}
		if state == nil {
			return ErrGroupMemberNotFound
		}
		if _, err := s.groupUsers.DeleteMember(txCtx, groupID, userID); err != nil {
			return err
		}
		if err := s.users.RemoveGroupFromUserAllowedGroups(txCtx, userID, groupID); err != nil {
			return err
		}
		if _, err := s.groupUsers.DisableGroupAPIKeys(txCtx, userID, groupID); err != nil {
			return err
		}
		if !state.Owned || state.Role != RoleUser {
			return nil
		}
		remaining, err := s.groupUsers.CountMemberships(txCtx, userID)
		if err != nil {
			return err
		}
		if remaining > 0 {
			return nil
		}
		disabledUser, err = s.groupUsers.SetUserStatus(txCtx, userID, StatusDisabled)
		return err
	})
	if err != nil {
		return err
	}
	s.invalidateAuth(ctx, userID)
	logger.LegacyPrintf("service.group_management", "audit: group member removed actor_id=%d group_id=%d user_id=%d user_disabled=%t",
		actorID, groupID, userID, disabledUser)
	return nil
}
