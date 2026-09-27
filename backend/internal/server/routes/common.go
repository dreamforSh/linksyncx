package routes

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine) {
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Claude Code 遥测日志（忽略，直接返回200）。
	// 真实 CLI 2.1.280 的遥测路径带 /v2/（/api/event_logging/v2/batch，skipAuth
	// 默认 false 即带认证上报）；旧路径仅为向后兼容保留。
	// 不上游转发：多账号 relay 场景下把终端用户的设备遥测绑定到任一上游账号
	// 都是隐私与关联性风险；且遥测缺失本身是合法客户端配置（DISABLE_TELEMETRY）。
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	r.POST("/api/event_logging/v2/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
