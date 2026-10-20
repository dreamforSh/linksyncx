package middleware

import "sort"

// AuditRouteTableKeys 按表名导出三张审计路由表的键（已排序），仅供外部测试包使用。
// 路由漂移测试要注册真实路由，必须 import routes 包，而 routes 依赖本包：
// 写在 package middleware 的测试里会成环，只能放到 middleware_test 并经本文件读未导出表。
func AuditRouteTableKeys() map[string][]string {
	return map[string][]string{
		"auditSensitiveReads":    sortedAuditRouteKeys(auditSensitiveReads),
		"auditActionOverrides":   sortedAuditRouteKeys(auditActionOverrides),
		"auditBodyOmittedRoutes": sortedAuditRouteKeys(auditBodyOmittedRoutes),
	}
}

func sortedAuditRouteKeys[V any](table map[string]V) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
