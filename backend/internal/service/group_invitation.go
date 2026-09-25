package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 组管理员看不到用户 ID：加入分组改为「按邮箱邀请已注册用户，对方在组管理页确认后加入」。
// 超管仍可在控制台按用户 ID 直接添加成员。

const (
	GroupInvitationPending  = "pending"
	GroupInvitationAccepted = "accepted"
	GroupInvitationDeclined = "declined"
	GroupInvitationRevoked  = "revoked"

	groupInvitationEmailMaxLen = 255
	// groupInvitationListLimit 分组邀请列表只展示最近的记录
	groupInvitationListLimit = 50
)

var (
	ErrGroupInviteRequired      = infraerrors.Forbidden("GROUP_INVITE_REQUIRED", "group managers add members by email invitation")
	ErrGroupInviteUserNotFound  = infraerrors.NotFound("GROUP_INVITE_USER_NOT_FOUND", "no registered user uses this email")
	ErrGroupInviteNotEligible   = infraerrors.BadRequest("GROUP_INVITE_NOT_ELIGIBLE", "only active regular users can be invited")
	ErrGroupInviteAlreadyMember = infraerrors.Conflict("GROUP_INVITE_ALREADY_MEMBER", "the user is already a member of this group")
	ErrGroupInvitationNotFound  = infraerrors.NotFound("GROUP_INVITATION_NOT_FOUND", "the invitation does not exist or has already been handled")
)

// GroupInvitation 一条分组邀请。组管理员与被邀请人看到的都是邮箱，不含用户 ID。
type GroupInvitation struct {
	ID          int64      `json:"id"`
	GroupID     int64      `json:"group_id"`
	GroupName   string     `json:"group_name"`
	Category    string     `json:"category,omitempty"`
	ManagedType string     `json:"managed_type,omitempty"`
	Email       string     `json:"email"`
	InviterName string     `json:"inviter_name"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	RespondedAt *time.Time `json:"responded_at,omitempty"`

	// 内部字段，不序列化
	UserID    int64 `json:"-"`
	InvitedBy int64 `json:"-"`
	Managed   bool  `json:"-"`
}

// GroupInvitee 按邮箱找到的被邀请用户。
type GroupInvitee struct {
	ID     int64
	Email  string
	Role   string
	Status string
}

// normalizeInvitationEmail 与登录时的邮箱匹配规则一致：去空白、小写。
func normalizeInvitationEmail(email string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" || len(normalized) > groupInvitationEmailMaxLen {
		return "", false
	}
	at := strings.Index(normalized, "@")
	if at <= 0 || at == len(normalized)-1 || strings.ContainsAny(normalized, " \t\r\n") {
		return "", false
	}
	return normalized, true
}

// InviteMember 组管理员 / 超管按邮箱邀请已注册的普通用户加入分组。
// 同一分组对同一用户只保留一条待处理邀请，重复邀请返回已有的那条。
func (s *GroupManagementService) InviteMember(ctx context.Context, actorID int64, role string, groupID int64, email string) (*GroupInvitation, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	normalized, ok := normalizeInvitationEmail(email)
	if !ok {
		return nil, ErrGroupManagementBadInput
	}
	invitee, err := s.groupUsers.FindInviteeByEmail(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if invitee == nil {
		return nil, ErrGroupInviteUserNotFound
	}
	if invitee.ID == actorID || invitee.Role != RoleUser || invitee.Status != StatusActive {
		return nil, ErrGroupInviteNotEligible
	}
	state, err := s.groupUsers.GetMemberState(ctx, groupID, invitee.ID)
	if err != nil {
		return nil, err
	}
	if state != nil {
		return nil, ErrGroupInviteAlreadyMember
	}
	invitation, created, err := s.groupUsers.CreateInvitation(ctx, groupID, invitee.ID, invitee.Email, actorID)
	if err != nil {
		return nil, err
	}
	if created {
		logger.LegacyPrintf("service.group_management", "audit: group invitation sent actor_id=%d role=%s group_id=%d invitation_id=%d",
			actorID, role, groupID, invitation.ID)
	}
	return invitation, nil
}

// GroupInvitations 分组最近的邀请记录（含已接受 / 已拒绝 / 已撤销），供组管理员跟进。
func (s *GroupManagementService) GroupInvitations(ctx context.Context, actorID int64, role string, groupID int64) ([]GroupInvitation, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	return s.groupUsers.ListGroupInvitations(ctx, groupID, groupInvitationListLimit)
}

// RevokeInvitation 撤销一条尚未处理的邀请。
func (s *GroupManagementService) RevokeInvitation(ctx context.Context, actorID int64, role string, groupID, invitationID int64) error {
	if err := s.requireGroupUserDeps(); err != nil {
		return err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return err
	}
	if invitationID <= 0 {
		return ErrGroupManagementBadInput
	}
	revoked, err := s.groupUsers.RevokeInvitation(ctx, groupID, invitationID)
	if err != nil {
		return err
	}
	if !revoked {
		return ErrGroupInvitationNotFound
	}
	logger.LegacyPrintf("service.group_management", "audit: group invitation revoked actor_id=%d group_id=%d invitation_id=%d",
		actorID, groupID, invitationID)
	return nil
}

// MyInvitations 当前用户收到的待处理邀请。
func (s *GroupManagementService) MyInvitations(ctx context.Context, actorID int64, role string) ([]GroupInvitation, error) {
	if actorID <= 0 || !validRole(role) {
		return nil, ErrGroupManagementForbidden
	}
	if s.groupUsers == nil {
		return []GroupInvitation{}, nil
	}
	return s.groupUsers.ListUserInvitations(ctx, actorID)
}

// RespondInvitation 被邀请人接受或拒绝邀请。接受时在同一事务里写入成员行；
// 管理分组同时授予该分组的 Key 绑定资格。
func (s *GroupManagementService) RespondInvitation(ctx context.Context, actorID int64, role string, invitationID int64, accept bool) (*GroupInvitation, error) {
	if err := s.requireGroupUserDeps(); err != nil {
		return nil, err
	}
	if actorID <= 0 || !validRole(role) {
		return nil, ErrGroupManagementForbidden
	}
	if invitationID <= 0 {
		return nil, ErrGroupManagementBadInput
	}
	status := GroupInvitationDeclined
	if accept {
		status = GroupInvitationAccepted
	}
	var invitation *GroupInvitation
	err := s.withTx(ctx, func(txCtx context.Context) error {
		locked, err := s.groupUsers.LockUserInvitation(txCtx, invitationID, actorID)
		if err != nil {
			return err
		}
		if locked == nil {
			return ErrGroupInvitationNotFound
		}
		invitation = locked
		if accept {
			// 邀请只面向普通用户；发出后被改成其他角色的不能再接受
			if role != RoleUser {
				return ErrGroupInviteNotEligible
			}
			if err := s.groupUsers.LockGroup(txCtx, locked.GroupID); err != nil {
				return err
			}
			if err := s.groupUsers.InsertMember(txCtx, locked.GroupID, actorID, false, locked.InvitedBy, nil, nil); err != nil {
				return err
			}
			if locked.Managed {
				if err := s.users.AddGroupToAllowedGroups(txCtx, actorID, locked.GroupID); err != nil {
					return err
				}
			}
		}
		return s.groupUsers.SetInvitationStatus(txCtx, invitationID, status)
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	invitation.Status = status
	invitation.RespondedAt = &now
	if accept {
		s.invalidateAuth(ctx, actorID)
	}
	logger.LegacyPrintf("service.group_management", "audit: group invitation %s user_id=%d group_id=%d invitation_id=%d",
		status, actorID, invitation.GroupID, invitationID)
	return invitation, nil
}
