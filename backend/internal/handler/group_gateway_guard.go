package handler

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// GatewayGuard is installed after API-key authentication for both gateway protocols.
func (h *GroupManagementHandler) GatewayGuard(writeError middleware.GatewayErrorWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		key, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || key.GroupID == nil {
			c.Next()
			return
		}
		path := c.Request.URL.Path
		if key.User == nil {
			writeError(c, http.StatusUnauthorized, "Authentication required")
			c.Abort()
			return
		}
		if h == nil || h.service == nil {
			writeError(c, http.StatusServiceUnavailable, "Group management is unavailable")
			c.Abort()
			return
		}
		consume := c.Request.Method == http.MethodPost &&
			(path == "/v1/messages" || path == "/v1/responses" || path == "/v1/chat/completions")
		policy, release, err := h.service.GatewayAdmission(c.Request.Context(), key.User.ID, *key.GroupID, key.User.Role == service.RoleAdmin, consume)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, service.ErrGroupMemberRequired) || errors.Is(err, service.ErrGroupAccountRequired) {
				status = http.StatusForbidden
			} else if errors.Is(err, service.ErrGroupQuotaExceeded) || errors.Is(err, service.ErrGroupConcurrencyExceeded) {
				status = http.StatusTooManyRequests
			}
			writeError(c, status, err.Error())
			c.Abort()
			return
		}
		if policy.Enabled && !consume && !(path == "/v1/usage" && c.Request.Method == http.MethodGet) {
			writeError(c, http.StatusForbidden, "This endpoint is not supported for managed groups")
			c.Abort()
			return
		}
		defer release()
		c.Request = c.Request.WithContext(service.WithGatewayAllocation(c.Request.Context(), policy))
		c.Next()
	}
}
