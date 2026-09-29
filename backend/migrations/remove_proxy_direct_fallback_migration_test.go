package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 251 号迁移移除"代理过期改投直连"：存量 direct（及非法取值）改为 none，已被改投成直连的账号
// 切回仍存在的原代理并通知调度缓存，最后用 CHECK 约束禁止再写入 none / proxy 以外的取值。
// 真实 Postgres 语义（含幂等）另用 PGlite 验证过。
func TestRemoveProxyDirectFallbackMigration(t *testing.T) {
	content, err := FS.ReadFile("251_remove_proxy_direct_fallback.sql")
	require.NoError(t, err)

	var lines []string
	for _, line := range strings.Split(string(content), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			lines = append(lines, trimmed)
		}
	}
	sql := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")

	require.Contains(t, sql, "UPDATE proxies SET fallback_mode = 'none', updated_at = NOW() WHERE fallback_mode NOT IN ('none', 'proxy');")

	// Restore accounts that the old sweep moved to a direct connection, only onto a proxy that still exists.
	require.Contains(t, sql, "proxy_id = a.proxy_fallback_origin_id, proxy_fallback_origin_id = NULL")
	require.Contains(t, sql, "WHERE a.proxy_id IS NULL AND a.proxy_fallback_origin_id IS NOT NULL AND a.deleted_at IS NULL")
	require.Contains(t, sql, "WHERE p.id = a.proxy_fallback_origin_id AND p.deleted_at IS NULL")
	// Same side effect as the admin "revert proxy" action: the billing probe snapshot follows the network identity.
	require.Contains(t, sql, "extra = CASE WHEN a.type = 'apikey' THEN a.extra - 'upstream_billing_probe' ELSE a.extra END")
	// Restored accounts must reach the scheduler cache immediately, or they keep going direct from cache.
	require.Contains(t, sql, "INSERT INTO scheduler_outbox (event_type, account_id) SELECT 'account_changed', id FROM restored;")

	require.Contains(t, sql, "ALTER TABLE proxies DROP CONSTRAINT IF EXISTS proxies_fallback_mode_check;")
	require.Contains(t, sql, "ALTER TABLE proxies ADD CONSTRAINT proxies_fallback_mode_check CHECK (fallback_mode IN ('none', 'proxy'));")
	require.NotContains(t, sql, "'direct'", "no statement may keep or write the removed direct mode")
}
