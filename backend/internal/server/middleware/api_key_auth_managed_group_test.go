//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 管理分组的订阅组不扣余额：余额为 0 的组用户也能通过鉴权；额度组照常要求余额。
func TestAPIKeyAuthManagedGroupBalanceGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9)

	run := func(managedType string) *httptest.ResponseRecorder {
		user := &service.User{
			ID: 10, Role: service.RoleUser, Status: service.StatusActive, Balance: 0, Concurrency: 3,
			AllowedGroups: []int64{groupID},
		}
		group := &service.Group{
			ID: groupID, Name: "acme", Platform: service.PlatformAnthropic, Status: service.StatusActive,
			IsExclusive: true, SubscriptionType: service.SubscriptionTypeStandard, Hydrated: true,
			Kind: service.GroupKindManaged, ManagedType: managedType, RateMultiplier: 1,
		}
		apiKey := &service.APIKey{
			ID: 105, UserID: user.ID, Key: "managed-" + managedType, Status: service.StatusActive,
			GroupID: &groupID, Group: group, User: user,
		}
		apiKeyRepo := &stubApiKeyRepo{
			getByKey: func(ctx context.Context, key string) (*service.APIKey, error) {
				if key != apiKey.Key {
					return nil, service.ErrAPIKeyNotFound
				}
				clone := *apiKey
				userClone := *user
				groupClone := *group
				clone.User = &userClone
				clone.Group = &groupClone
				return &clone, nil
			},
		}
		cfg := &config.Config{RunMode: config.RunModeStandard}
		apiKeyService := service.NewAPIKeyService(apiKeyRepo, nil, nil, nil, nil, nil, cfg)
		router := newAuthTestRouter(apiKeyService, nil, cfg)

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/t", nil)
		req.Header.Set("x-api-key", apiKey.Key)
		router.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusOK, run(service.ManagedGroupTypeSubscription).Code)

	w := run(service.ManagedGroupTypeQuota)
	require.Equal(t, http.StatusForbidden, w.Code)
	requireAPIKeyAuthError(t, w, "INSUFFICIENT_BALANCE", "Insufficient account balance")
}
