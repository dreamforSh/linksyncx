package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SetTicketCheckService 注入打票检测服务。
func (h *AccountHandler) SetTicketCheckService(svc *service.TicketCheckService) {
	h.ticketCheck = svc
}

func ticketCheckAccountID(c *gin.Context) (int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return accountID, true
}

func (h *AccountHandler) requireTicketCheck(c *gin.Context) bool {
	if h != nil && h.ticketCheck != nil {
		return true
	}
	response.BadRequest(c, "Ticket check service is unavailable")
	return false
}

// GetTicketCheck 返回账号的打票检测配置与最近一次检测结果。
func (h *AccountHandler) GetTicketCheck(c *gin.Context) {
	if !h.requireTicketCheck(c) {
		return
	}
	accountID, ok := ticketCheckAccountID(c)
	if !ok {
		return
	}
	status, err := h.ticketCheck.Status(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// UpdateTicketCheck 保存账号级打票检测开关与「降智时临时停止调用」开关。
func (h *AccountHandler) UpdateTicketCheck(c *gin.Context) {
	if !h.requireTicketCheck(c) {
		return
	}
	accountID, ok := ticketCheckAccountID(c)
	if !ok {
		return
	}
	var req service.TicketCheckSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.ticketCheck.UpdateSettings(c.Request.Context(), accountID, req); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	status, err := h.ticketCheck.Status(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, status)
}

// RunTicketCheck 立即执行一次打票检测（不影响 6 小时周期）。
func (h *AccountHandler) RunTicketCheck(c *gin.Context) {
	if !h.requireTicketCheck(c) {
		return
	}
	accountID, ok := ticketCheckAccountID(c)
	if !ok {
		return
	}
	result, err := h.ticketCheck.RunNow(c.Request.Context(), accountID)
	// 探测本身失败（出口连不上、Cloudflare 拦截、缺票据头）时，结果里已经带着
	// 可读原因；这类失败不该伪装成 500，前端要能展示「检测失败 + 原因」。
	if err != nil && (result.Error != "" || result.HTTPStatus != 0) {
		response.Success(c, result)
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GetTicketProxyPool 返回打票代理池中的代理 ID 列表。
func (h *AccountHandler) GetTicketProxyPool(c *gin.Context) {
	if !h.requireTicketCheck(c) {
		return
	}
	ids, err := h.ticketCheck.TicketProxyPool(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if ids == nil {
		ids = []int64{}
	}
	response.Success(c, gin.H{"ids": ids})
}

// UpdateTicketProxyPool 保存打票代理池。
func (h *AccountHandler) UpdateTicketProxyPool(c *gin.Context) {
	if !h.requireTicketCheck(c) {
		return
	}
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.ticketCheck.SetTicketProxyPool(c.Request.Context(), req.IDs); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ids": req.IDs})
}
