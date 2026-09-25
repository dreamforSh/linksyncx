package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayAdmissionRepoStub struct {
	service.GroupManagementRepository
	err error
}

func (s *gatewayAdmissionRepoStub) GatewayAdmission(_ context.Context, _, groupID int64, _, _ bool) (service.GatewayAllocation, func(), error) {
	return service.GatewayAllocation{GroupID: groupID, Enabled: true}, func() {}, s.err
}

// 额度组成员的 5h / 7d 美元上限用尽时返回 429，与日请求数上限一致。
func TestGatewayGuardRejectsExhaustedGroupUSDLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, err := range []error{service.ErrGroupMemberUsage5hExceeded, service.ErrGroupMemberUsage7dExceeded, service.ErrGroupQuotaExceeded} {
		h := NewGroupManagementHandler(service.NewGroupManagementService(&gatewayAdmissionRepoStub{err: err}))
		router := gin.New()
		groupID := int64(9)
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, GroupID: &groupID, User: &service.User{ID: 7, Role: service.RoleUser}})
			c.Next()
		})
		router.POST("/v1/messages", h.GatewayGuard(middleware.AnthropicErrorWriter), func(c *gin.Context) { c.Status(http.StatusOK) })

		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		require.Equal(t, http.StatusTooManyRequests, w.Code, err.Error())
		require.Contains(t, w.Body.String(), err.Error())
	}
}
