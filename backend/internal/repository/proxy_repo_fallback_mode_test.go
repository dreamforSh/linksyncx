package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 未指定的回退策略按列默认值写成 none（CHECK 约束只允许 none / proxy，ent 显式写列时
// 不套用数据库默认值）；已指定的取值原样写入，非法值交给 CHECK 约束拒绝，不在这里改写。
func TestStoredProxyFallbackMode(t *testing.T) {
	require.Equal(t, service.FallbackModeNone, storedProxyFallbackMode(""))
	require.Equal(t, service.FallbackModeNone, storedProxyFallbackMode(service.FallbackModeNone))
	require.Equal(t, service.FallbackModeProxy, storedProxyFallbackMode(service.FallbackModeProxy))
	require.Equal(t, "direct", storedProxyFallbackMode("direct"), "invalid values must reach the CHECK constraint untouched")
}
