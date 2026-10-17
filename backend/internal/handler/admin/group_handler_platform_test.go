//go:build unit

package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 回归分组平台枚举：所有已支持平台（含 Perplexity）必须通过 Create/Update 校验，
// 非法值仍须被拒，避免前端可选平台与后端请求白名单不一致。
func bindGroupPlatformJSON(t *testing.T, target any, body string) error {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c.ShouldBindJSON(target)
}

func TestGroupPlatformBinding_AllowedPlatforms(t *testing.T) {
	allowed := []string{
		"anthropic", "openai", "gemini", "antigravity", "grok", "perplexity",
		"kimi", "zhipu", "deepseek", "minimax", "opencode_go", "composite",
	}
	for _, platform := range allowed {
		t.Run("create_"+platform, func(t *testing.T) {
			var req CreateGroupRequest
			body := fmt.Sprintf(`{"name":"g","platform":%q}`, platform)
			require.NoError(t, bindGroupPlatformJSON(t, &req, body),
				"platform %q 应通过 CreateGroupRequest 校验", platform)
			require.Equal(t, platform, req.Platform)
		})
		t.Run("update_"+platform, func(t *testing.T) {
			var req UpdateGroupRequest
			body := fmt.Sprintf(`{"platform":%q}`, platform)
			require.NoError(t, bindGroupPlatformJSON(t, &req, body),
				"platform %q 应通过 UpdateGroupRequest 校验", platform)
			require.Equal(t, platform, req.Platform)
		})
	}
}

func TestGroupPlatformBinding_RejectsInvalidPlatforms(t *testing.T) {
	invalid := []string{
		"moonshot",   // 厂商别名,不是平台标识
		"Kimi",       // 大小写敏感
		"openai ",    // 尾随空格
		"Perplexity", // 大小写敏感
		"perplexity ",
		"glm",
		"bogus",
	}
	for _, platform := range invalid {
		t.Run("create_"+platform, func(t *testing.T) {
			var req CreateGroupRequest
			body := fmt.Sprintf(`{"name":"g","platform":%q}`, platform)
			require.Error(t, bindGroupPlatformJSON(t, &req, body),
				"platform %q 应被 CreateGroupRequest 拒绝", platform)
		})
		t.Run("update_"+platform, func(t *testing.T) {
			var req UpdateGroupRequest
			body := fmt.Sprintf(`{"platform":%q}`, platform)
			require.Error(t, bindGroupPlatformJSON(t, &req, body),
				"platform %q 应被 UpdateGroupRequest 拒绝", platform)
		})
	}
}

func TestGroupHandlerPerplexity_CreateAndUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{config.RunModeStandard, config.RunModeSimple} {
		for _, kind := range []string{service.GroupKindChannel, service.GroupKindManaged} {
			t.Run(mode+"_"+kind, func(t *testing.T) {
				svc := newStubAdminService()
				handler := NewGroupHandlerWithConfig(svc, nil, nil, &config.Config{RunMode: mode})
				router := gin.New()
				router.POST("/groups", handler.Create)
				router.PUT("/groups/:id", handler.Update)

				category := ""
				if kind == service.GroupKindManaged {
					category = "team"
				}
				body := fmt.Sprintf(`{"name":"Perplexity group","platform":"perplexity","kind":%q,"category":%q,"rate_multiplier":1}`, kind, category)
				req := httptest.NewRequest(http.MethodPost, "/groups", bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				res := httptest.NewRecorder()
				router.ServeHTTP(res, req)

				require.Equal(t, http.StatusOK, res.Code, res.Body.String())
				require.Len(t, svc.createdGroups, 1)
				require.Equal(t, service.PlatformPerplexity, svc.createdGroups[0].Platform)
				require.Equal(t, kind, svc.createdGroups[0].Kind)

				req = httptest.NewRequest(http.MethodPut, "/groups/200", bytes.NewBufferString(`{"name":"Renamed Perplexity group","platform":"perplexity"}`))
				req.Header.Set("Content-Type", "application/json")
				res = httptest.NewRecorder()
				router.ServeHTTP(res, req)

				require.Equal(t, http.StatusOK, res.Code, res.Body.String())
				require.Len(t, svc.updatedGroups, 1)
				require.Equal(t, "Renamed Perplexity group", svc.updatedGroups[0].Name)
				if mode == config.RunModeSimple {
					require.Empty(t, svc.updatedGroups[0].Platform, "simple mode preserves the stored platform")
				} else {
					require.Equal(t, service.PlatformPerplexity, svc.updatedGroups[0].Platform)
				}
			})
		}
	}
}

func TestCompositeRouteTargetPlatform_AllowsConcreteProviders(t *testing.T) {
	for _, platform := range []string{
		"anthropic", "openai", "gemini", "antigravity", "grok", "perplexity",
		"kimi", "zhipu", "deepseek", "minimax", "opencode_go",
	} {
		var req CompositeRouteRequest
		body := fmt.Sprintf(`{"public_model":"m","target_platform":%q}`, platform)
		require.NoError(t, bindGroupPlatformJSON(t, &req, body))
		require.Equal(t, platform, req.TargetPlatform)
	}
}

func TestCompositeRouteTargetPlatform_RejectsComposite(t *testing.T) {
	var req CompositeRouteRequest
	require.Error(t, bindGroupPlatformJSON(t, &req, `{"public_model":"m","target_platform":"composite"}`))
}
