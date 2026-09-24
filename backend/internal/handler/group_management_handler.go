package handler

import (
	"net/http"
	"strconv"

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
}
type groupSettingsRequest struct {
	Enabled        bool   `json:"enabled"`
	AllocationMode string `json:"allocation_mode" binding:"required,oneof=manual auto"`
	MaxConcurrent  int    `json:"max_concurrent"`
	DailyLimit     int64  `json:"daily_limit"`
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
	out, err := h.service.UpdateSettings(c.Request.Context(), uid, role, service.GroupSettings{GroupID: gid, Enabled: req.Enabled, AllocationMode: req.AllocationMode, MaxConcurrent: req.MaxConcurrent, DailyLimit: req.DailyLimit})
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
	out, err := h.service.UpdateMemberLimit(c.Request.Context(), uid, role, gid, target, req.MaxConcurrent, req.DailyLimit)
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
