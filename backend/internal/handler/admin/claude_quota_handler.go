package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// ClaudeQuotaHandler 处理 Claude OAuth 账号的订阅档位与重置（免费重置券、每周会话重置）。
type ClaudeQuotaHandler struct {
	quotaService     claudeQuotaService
	adminService     service.AdminService
	rateLimitService openAIAccountStateRecoverer
}

type claudeQuotaService interface {
	RefreshQuota(ctx context.Context, accountID int64) (*service.ClaudeQuotaRefreshResult, error)
	ResetCredit(ctx context.Context, accountID int64, program string) (*service.ClaudeQuotaResetResult, error)
}

// claudeQuotaResetPostProcessTimeout 限定领取之后的恢复耗时：重置已经不可退回，
// 整个请求必须留在面板请求超时之内，否则浏览器会中断一个已经成功的操作，诱发重复领取。
const claudeQuotaResetPostProcessTimeout = 8 * time.Second

type claudeQuotaRefreshResponse struct {
	service.ClaudeQuotaRefreshResult
	Account *dto.Account `json:"account,omitempty"`
}

type claudeQuotaResetRequest struct {
	// Program 为 cedar_ember（免费重置券）或 juniper_tide（每周会话重置），空表示自动选择。
	Program string `json:"program"`
}

type claudeQuotaResetResponse struct {
	service.ClaudeQuotaResetResult
	Snapshot              *service.ClaudeResetSnapshot `json:"snapshot,omitempty"`
	Usage                 *service.UsageInfo           `json:"usage,omitempty"`
	Account               *dto.Account                 `json:"account,omitempty"`
	CacheRefreshed        bool                         `json:"cache_refreshed"`
	AccountStateRecovered bool                         `json:"account_state_recovered"`
	WarningCode           string                       `json:"warning_code,omitempty"`
}

// NewClaudeQuotaHandler creates a new Claude quota handler.
func NewClaudeQuotaHandler(quotaService *service.ClaudeQuotaService, adminService service.AdminService, rateLimitService *service.RateLimitService) *ClaudeQuotaHandler {
	handler := &ClaudeQuotaHandler{adminService: adminService}
	// 避免把 typed nil 装进接口
	if quotaService != nil {
		handler.quotaService = quotaService
	}
	if rateLimitService != nil {
		handler.rateLimitService = rateLimitService
	}
	return handler
}

// RefreshQuota 查询重置状态（同一次读取刷新用量）与订阅档位并写入快照。
// POST /api/v1/admin/anthropic/accounts/:id/quota/refresh
//
// 用 POST 而不是带副作用的 GET：它会写账号状态，审计中间件只记录变更类请求。
func (h *ClaudeQuotaHandler) RefreshQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "claude quota service is not enabled")
		return
	}
	result, err := h.quotaService.RefreshQuota(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result == nil {
		response.Error(c, http.StatusInternalServerError, "claude quota refresh returned an empty result")
		return
	}
	refreshResponse := claudeQuotaRefreshResponse{ClaudeQuotaRefreshResult: *result}
	if h.adminService != nil {
		// 档位写在账号 extra 里：带回最新账号行，列表的档位徽章无需整页刷新。
		if account, err := h.adminService.GetAccount(c.Request.Context(), accountID); err == nil && account != nil {
			refreshResponse.Account = dto.AccountFromService(account)
		}
	}
	response.Success(c, refreshResponse)
}

// ResetQuota 领取一次重置，随后解除本地限流并刷新重置快照与用量。
// POST /api/v1/admin/anthropic/accounts/:id/reset-quota
//
// 上游确认未扣卡的结果（未达限额、冷却中等）照常以 200 返回 result，由面板展示原因。
func (h *ClaudeQuotaHandler) ResetQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "claude quota service is not enabled")
		return
	}
	var req claudeQuotaResetRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
	}

	result, err := h.quotaService.ResetCredit(c.Request.Context(), accountID, req.Program)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result == nil {
		response.Error(c, http.StatusInternalServerError, "claude quota reset returned an empty result")
		return
	}

	resetResponse := claudeQuotaResetResponse{ClaudeQuotaResetResult: *result}
	if !result.Consumed() {
		response.Success(c, resetResponse)
		return
	}

	// 重置已在上游生效：后处理脱离客户端连接，操作员关掉页面也要完成本地恢复。
	postCtx, cancelPost := context.WithTimeout(context.WithoutCancel(c.Request.Context()), claudeQuotaResetPostProcessTimeout)
	defer cancelPost()

	var loadAccount func(context.Context, int64) (*service.Account, error)
	if h.adminService != nil {
		loadAccount = h.adminService.GetAccount
	}
	postResult := service.RunClaudeQuotaResetPostProcess(postCtx, accountID, h.quotaService, h.rateLimitService, loadAccount)
	if postResult.Refresh != nil {
		resetResponse.Snapshot = postResult.Refresh.Snapshot
		resetResponse.Usage = postResult.Refresh.Usage
	}
	resetResponse.CacheRefreshed = postResult.CacheRefreshed
	resetResponse.AccountStateRecovered = postResult.AccountStateRecovered
	resetResponse.WarningCode = postResult.WarningCode
	if postResult.Account != nil {
		resetResponse.Account = dto.AccountFromService(postResult.Account)
	}
	response.Success(c, resetResponse)
}
