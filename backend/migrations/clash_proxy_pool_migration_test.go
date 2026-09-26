package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashProxyPoolMigration(t *testing.T) {
	content, err := FS.ReadFile("246_clash_proxy_pool.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE proxies ADD COLUMN IF NOT EXISTS source VARCHAR(16) NOT NULL DEFAULT 'manual'")
	require.Contains(t, sql, "CHECK (source IN ('manual', 'clash'))")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS clash_profiles")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS clash_nodes")
	require.Contains(t, sql, "proxy_id BIGINT NOT NULL REFERENCES proxies(id)")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_nodes_listen_port ON clash_nodes(listen_port)")
	require.Contains(t, sql, "CREATE UNIQUE INDEX IF NOT EXISTS uq_clash_nodes_proxy_id ON clash_nodes(proxy_id)")
	require.NotContains(t, strings.ToUpper(sql), "ALTER TABLE ACCOUNTS")
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
