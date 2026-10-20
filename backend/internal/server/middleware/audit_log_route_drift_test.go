package middleware_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/routes"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 审计表的键是手写的 "METHOD FullPath" 字符串：写错或路由改名后既不编译失败也不报错，
// 只会静默永不命中（data-management/s3/config 就这样让一个敏感读取从未入审计）。
// 这里注册真实路由逐键核对，把这类漂移变成测试失败。
func TestAuditRouteTablesOnlyNameRegisteredAuditedRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router, probeAuditKey := newAuditDriftRouter()

	registered := make(map[string]struct{})
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = struct{}{}
	}

	// 中间件只对 GET 查 auditSensitiveReads，只对变更类请求查另外两张表（GET 的 body 本就不捕获），
	// 方法类别不对的键即使路由存在也同样是死键。
	mutating := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}
	allowedMethods := map[string][]string{
		"auditSensitiveReads":    {http.MethodGet},
		"auditActionOverrides":   mutating,
		"auditBodyOmittedRoutes": mutating,
	}

	tables := middleware.AuditRouteTableKeys()
	require.Len(t, tables, len(allowedMethods))
	for table, keys := range tables {
		t.Run(table, func(t *testing.T) {
			require.NotEmpty(t, keys)
			var problems []string
			for _, key := range keys {
				method, path, ok := strings.Cut(key, " ")
				if !ok || !strings.HasPrefix(path, "/") {
					problems = append(problems, fmt.Sprintf("%s: 不是 \"METHOD /path\" 格式", key))
					continue
				}
				if !slices.Contains(allowedMethods[table], method) {
					problems = append(problems, fmt.Sprintf("%s: 中间件不会对 %s 请求查这张表", key, method))
					continue
				}
				if _, exists := registered[key]; !exists {
					problems = append(problems, fmt.Sprintf("%s: 没有对应的已注册路由（路由改名/删除后未同步？）", key))
					continue
				}
				// 路由存在还不够：它还必须挂在审计中间件之后，且中间件看到的 FullPath 与键逐字相同。
				if got := probeAuditKey(method, path); got != key {
					problems = append(problems, fmt.Sprintf("%s: 请求未经过审计中间件（实际命中 %q）", key, got))
				}
			}
			require.Emptyf(t, problems, "%s 存在永不生效的键", table)
		})
	}
}

// newAuditDriftRouter 按生产方式注册挂了审计中间件的全部路由组（auth / user / admin / admin-payment），
// 返回路由器和一个探测函数：对给定路由发真实请求，返回审计中间件计算出的 routeKey（未经过审计则为空）。
func newAuditDriftRouter() (*gin.Engine, func(method, path string) string) {
	router := gin.New()
	v1 := router.Group("/api/v1")
	// handler 全部留 nil：注册时只取方法值、不解引用；挂了审计的路由在审计桩处就 Abort，执行不到 handler。
	h := &handler.Handlers{Admin: &handler.AdminHandlers{
		// registerClashRoutes 遇到 nil handler 会整组跳过，不给桩会把 clash 相关键误判为漂移。
		Clash: &admin.ClashHandler{},
	}}

	var hit string
	// 审计桩记录的正是生产中间件拿去查表的 routeKey；随后 Abort，
	// 避免继续执行 nil handler 和依赖 Redis 的路由级限流器。
	auditLog := middleware.AuditLogMiddleware(func(c *gin.Context) {
		hit = c.Request.Method + " " + c.FullPath()
		c.AbortWithStatus(http.StatusNoContent)
	})
	pass := func(c *gin.Context) { c.Next() }
	jwtAuth := middleware.JWTAuthMiddleware(pass)
	adminAuth := middleware.AdminAuthMiddleware(pass)
	stepUp := middleware.StepUpAuthMiddleware(pass)

	// 与 server.registerRoutes 中接收 auditLog 的注册调用一一对应；其余路由组不挂审计，不可能命中审计表。
	routes.RegisterAuthRoutes(v1, h, jwtAuth, auditLog, nil, nil, nil)
	routes.RegisterUserRoutes(v1, h, jwtAuth, auditLog, nil, nil)
	routes.RegisterAdminRoutes(v1, h, adminAuth, auditLog, stepUp, nil, nil)
	routes.RegisterPaymentRoutes(v1, h.Payment, h.PaymentWebhook, h.Admin.Payment, jwtAuth, adminAuth, auditLog, nil, nil)

	probe := func(method, path string) (key string) {
		hit = ""
		// 没挂审计的路由会一路执行到 nil handler 并 panic——这本身就是要报告的漂移；
		// 吞掉 panic 按“审计未命中”返回，让测试列出问题键而不是整体崩溃。
		defer func() {
			_ = recover()
			key = hit
		}()
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, concreteRoutePath(path), nil))
		return hit
	}
	return router, probe
}

// concreteRoutePath 把路由模板里的 :param / *catchAll 段替换成具体值，用于发探测请求。
// 选一个不会与任何静态段撞名的值，保证请求落到模板本身而不是同级的静态路由。
func concreteRoutePath(template string) string {
	segments := strings.Split(template, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") || strings.HasPrefix(segment, "*") {
			segments[i] = "audit-drift-probe"
		}
	}
	return strings.Join(segments, "/")
}
