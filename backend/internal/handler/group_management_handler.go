package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type GroupManagementHandler struct {
	service *service.GroupManagementService
}

func NewGroupManagementHandler(svc *service.GroupManagementService) *GroupManagementHandler {
	return &GroupManagementHandler{service: svc}
}

type groupLimitRequest struct {
	MaxConcurrent int   `json:"max_concurrent"`
	DailyLimit    int64 `json:"daily_limit"`
	// 组内 5h / 7d 美元上限（额度组），省略表示不修改，0 表示不限
	Limit5hUSD *float64 `json:"limit_5h_usd" binding:"omitempty,gte=0"`
	Limit7dUSD *float64 `json:"limit_7d_usd" binding:"omitempty,gte=0"`
}
type groupSettingsRequest struct {
	Enabled        bool   `json:"enabled"`
	AllocationMode string `json:"allocation_mode" binding:"required,oneof=manual auto"`
	MaxConcurrent  int    `json:"max_concurrent"`
	DailyLimit     int64  `json:"daily_limit"`
	// 新成员默认的 5h / 7d 美元上限，省略表示不修改
	DefaultLimit5hUSD *float64 `json:"default_limit_5h_usd" binding:"omitempty,gte=0"`
	DefaultLimit7dUSD *float64 `json:"default_limit_7d_usd" binding:"omitempty,gte=0"`
}
type groupMemberRequest struct {
	UserID int64 `json:"user_id" binding:"required,gt=0"`
}
type groupAccountRequest struct {
	AccountIDs []int64 `json:"account_ids"`
	Mode       string  `json:"mode" binding:"omitempty,oneof=manual auto"`
}

func (h *GroupManagementHandler) actor(c *gin.Context) (int64, string, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "authentication required")
		return 0, "", false
	}
	role, ok := middleware.GetUserRoleFromContext(c)
	if !ok || role == "" {
		response.Unauthorized(c, "authentication required")
		return 0, "", false
	}
	return subject.UserID, role, true
}
func parseGroupID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid group id")
		return 0, false
	}
	return id, true
}
func parseParamID(c *gin.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "invalid id")
		return 0, false
	}
	return id, true
}
func handleGroupManagementError(c *gin.Context, err error) {
	if !response.ErrorFrom(c, err) {
		response.Error(c, http.StatusInternalServerError, "group management request failed")
	}
}

// GET /api/v1/group-management/me/overview
func (h *GroupManagementHandler) Overview(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	out, err := h.service.Overview(c.Request.Context(), uid, role)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// GET /api/v1/group-management/groups/:id/members
func (h *GroupManagementHandler) Members(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.Members(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// GET /api/v1/group-management/groups/:id/accounts
func (h *GroupManagementHandler) Accounts(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.Accounts(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// GET /api/v1/group-management/groups/:id/settings
func (h *GroupManagementHandler) GetSettings(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.Settings(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/groups/:id/members
func (h *GroupManagementHandler) AddMember(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	var req groupMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	out, err := h.service.AddMember(c.Request.Context(), uid, role, gid, req.UserID)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// DELETE /api/v1/group-management/groups/:id/members/:user_id
func (h *GroupManagementHandler) RemoveMember(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	if err := h.service.RemoveMember(c.Request.Context(), uid, role, gid, target); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "removed"})
}

// PUT /api/v1/group-management/groups/:id/settings
func (h *GroupManagementHandler) Settings(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	var req groupSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	settings := service.GroupSettings{GroupID: gid, Enabled: req.Enabled, AllocationMode: req.AllocationMode, MaxConcurrent: req.MaxConcurrent, DailyLimit: req.DailyLimit}
	if req.DefaultLimit5hUSD == nil || req.DefaultLimit7dUSD == nil {
		current, err := h.service.Settings(c.Request.Context(), uid, role, gid)
		if err != nil {
			handleGroupManagementError(c, err)
			return
		}
		settings.DefaultLimit5hUSD, settings.DefaultLimit7dUSD = current.DefaultLimit5hUSD, current.DefaultLimit7dUSD
	}
	if req.DefaultLimit5hUSD != nil {
		settings.DefaultLimit5hUSD = *req.DefaultLimit5hUSD
	}
	if req.DefaultLimit7dUSD != nil {
		settings.DefaultLimit7dUSD = *req.DefaultLimit7dUSD
	}
	out, err := h.service.UpdateSettings(c.Request.Context(), uid, role, settings)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// PUT /api/v1/group-management/groups/:id/members/:user_id/limit
func (h *GroupManagementHandler) MemberLimit(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	var req groupLimitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	out, err := h.service.UpdateMemberLimit(c.Request.Context(), uid, role, gid, target, service.GroupMemberLimitInput{
		MaxConcurrent: req.MaxConcurrent,
		DailyLimit:    req.DailyLimit,
		Limit5hUSD:    req.Limit5hUSD,
		Limit7dUSD:    req.Limit7dUSD,
	})
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// PUT /api/v1/group-management/groups/:id/members/:user_id/accounts
func (h *GroupManagementHandler) MemberAccounts(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	var req groupAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.Mode == "" {
		req.Mode = service.GroupAssignmentModeManual
	}
	if err := h.service.AssignAccounts(c.Request.Context(), uid, role, gid, target, req.AccountIDs, req.Mode); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "updated"})
}

// DELETE /api/v1/group-management/groups/:id/members/:user_id/accounts/:account_id
func (h *GroupManagementHandler) RevokeAccount(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	account, ok := parseParamID(c, "account_id")
	if !ok {
		return
	}
	if err := h.service.RevokeAccount(c.Request.Context(), uid, role, gid, target, account); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "revoked"})
}

// The following endpoints are mounted below the existing admin-only group.
func (h *GroupManagementHandler) AdminGroups(c *gin.Context) {
	out, err := h.service.AdminListGroups(c.Request.Context())
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *GroupManagementHandler) AdminUsers(c *gin.Context) {
	out, err := h.service.AdminListUsers(c.Request.Context())
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}
func (h *GroupManagementHandler) AdminAddManager(c *gin.Context) {
	gid, ok := parseParamID(c, "group_id")
	if !ok {
		return
	}
	uid, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	if err := h.service.AdminAddManager(c.Request.Context(), gid, uid); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "assigned"})
}
func (h *GroupManagementHandler) AdminRemoveManager(c *gin.Context) {
	gid, ok := parseParamID(c, "group_id")
	if !ok {
		return
	}
	uid, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	if err := h.service.AdminRemoveManager(c.Request.Context(), gid, uid); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "revoked"})
}

type groupCategoryRequest struct {
	Category string `json:"category" binding:"required,oneof=enterprise team"`
}

type createGroupUserRequest struct {
	Email         string  `json:"email" binding:"required,email,max=255"`
	Username      string  `json:"username" binding:"max=100"`
	Password      string  `json:"password" binding:"required,min=6,max=72"`
	MaxConcurrent *int    `json:"max_concurrent"`
	DailyLimit    *int64  `json:"daily_limit"`
	InitialAmount float64 `json:"initial_amount" binding:"gte=0"`
}

type groupUserStatusRequest struct {
	Status string `json:"status" binding:"required,oneof=active disabled"`
}

type groupUserPasswordRequest struct {
	Password string `json:"password" binding:"required,min=6,max=72"`
}

type groupTransferRequest struct {
	Direction string  `json:"direction" binding:"required,oneof=grant reclaim"`
	Amount    float64 `json:"amount" binding:"required,gt=0"`
	Notes     string  `json:"notes" binding:"max=500"`
}

// GET /api/v1/group-management/me/summary
func (h *GroupManagementHandler) Summary(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	out, err := h.service.Summary(c.Request.Context(), uid, role)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// PUT /api/v1/group-management/groups/:id/category
func (h *GroupManagementHandler) UpdateCategory(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	var req groupCategoryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if err := h.service.UpdateCategory(c.Request.Context(), uid, role, gid, req.Category); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"category": req.Category})
}

// GET /api/v1/group-management/groups/:id/owned-users
func (h *GroupManagementHandler) OwnedUsers(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.OwnedUserCandidates(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/groups/:id/users
func (h *GroupManagementHandler) CreateGroupUser(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	var req createGroupUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	out, err := h.service.CreateGroupUser(c.Request.Context(), uid, role, gid, service.CreateGroupUserInput{
		Email:         req.Email,
		Username:      req.Username,
		Password:      req.Password,
		MaxConcurrent: req.MaxConcurrent,
		DailyLimit:    req.DailyLimit,
		InitialAmount: req.InitialAmount,
	})
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// PUT /api/v1/group-management/groups/:id/users/:user_id/status
func (h *GroupManagementHandler) GroupUserStatus(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	var req groupUserStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if err := h.service.SetGroupUserStatus(c.Request.Context(), uid, role, gid, target, req.Status); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": req.Status})
}

// PUT /api/v1/group-management/groups/:id/users/:user_id/password
func (h *GroupManagementHandler) GroupUserPassword(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	var req groupUserPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if err := h.service.ResetGroupUserPassword(c.Request.Context(), uid, role, gid, target, req.Password); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": "reset"})
}

// POST /api/v1/group-management/groups/:id/users/:user_id/balance-transfers
func (h *GroupManagementHandler) TransferBalance(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	target, ok := parseParamID(c, "user_id")
	if !ok {
		return
	}
	var req groupTransferRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	out, err := h.service.TransferBalance(c.Request.Context(), uid, role, gid, target, req.Direction, req.Amount, req.Notes)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// GET /api/v1/group-management/groups/:id/balance-transfers?page=&page_size=&user_id=
func (h *GroupManagementHandler) ListTransfers(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	var memberID int64
	if raw := c.Query("user_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "invalid user id")
			return
		}
		memberID = parsed
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	items, total, err := h.service.ListTransfers(c.Request.Context(), uid, role, gid, memberID, page, pageSize)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

// GET /api/v1/admin/group-management/user-groups?user_ids=1,2,3
func (h *GroupManagementHandler) AdminUserGroups(c *gin.Context) {
	ids := make([]int64, 0)
	for _, part := range strings.Split(c.Query("user_ids"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "invalid user ids")
			return
		}
		ids = append(ids, id)
	}
	out, err := h.service.AdminUserGroupSummaries(c.Request.Context(), ids)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

type groupInvitationRequest struct {
	Email string `json:"email" binding:"required,max=255"`
}

// GET /api/v1/group-management/groups/:id/invitations
func (h *GroupManagementHandler) GroupInvitations(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.GroupInvitations(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/groups/:id/invitations
func (h *GroupManagementHandler) InviteMember(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	var req groupInvitationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	out, err := h.service.InviteMember(c.Request.Context(), uid, role, gid, req.Email)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// DELETE /api/v1/group-management/groups/:id/invitations/:invitation_id
func (h *GroupManagementHandler) RevokeInvitation(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	invitationID, ok := parseParamID(c, "invitation_id")
	if !ok {
		return
	}
	if err := h.service.RevokeInvitation(c.Request.Context(), uid, role, gid, invitationID); err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, gin.H{"status": service.GroupInvitationRevoked})
}

// GET /api/v1/group-management/me/invitations
func (h *GroupManagementHandler) MyInvitations(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	out, err := h.service.MyInvitations(c.Request.Context(), uid, role)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/me/invitations/:invitation_id/accept
func (h *GroupManagementHandler) AcceptInvitation(c *gin.Context) {
	h.respondInvitation(c, true)
}

// POST /api/v1/group-management/me/invitations/:invitation_id/decline
func (h *GroupManagementHandler) DeclineInvitation(c *gin.Context) {
	h.respondInvitation(c, false)
}

func (h *GroupManagementHandler) respondInvitation(c *gin.Context, accept bool) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	invitationID, ok := parseParamID(c, "invitation_id")
	if !ok {
		return
	}
	out, err := h.service.RespondInvitation(c.Request.Context(), uid, role, invitationID, accept)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// GET /api/v1/group-management/groups/:id/account-usage
func (h *GroupManagementHandler) AccountUsage(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	out, err := h.service.AccountUsage(c.Request.Context(), uid, role, gid)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/groups/:id/accounts/:account_id/quota-refresh
func (h *GroupManagementHandler) RefreshAccountQuota(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	accountID, ok := parseParamID(c, "account_id")
	if !ok {
		return
	}
	out, err := h.service.RefreshAccountQuota(c.Request.Context(), uid, role, gid, accountID)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}

// POST /api/v1/group-management/groups/:id/accounts/:account_id/reset-credit
func (h *GroupManagementHandler) ResetAccountCredit(c *gin.Context) {
	uid, role, ok := h.actor(c)
	if !ok {
		return
	}
	gid, ok := parseGroupID(c)
	if !ok {
		return
	}
	accountID, ok := parseParamID(c, "account_id")
	if !ok {
		return
	}
	out, err := h.service.ResetAccountCredit(c.Request.Context(), uid, role, gid, accountID)
	if err != nil {
		handleGroupManagementError(c, err)
		return
	}
	response.Success(c, out)
}
