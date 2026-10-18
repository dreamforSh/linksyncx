package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashAccountTrafficMigration(t *testing.T) {
	content, err := FS.ReadFile("258_clash_account_traffic.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS clash_account_traffic_daily")
	require.Contains(t, sql, "account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE")
	require.Contains(t, sql, "node_id BIGINT NOT NULL REFERENCES clash_nodes(id) ON DELETE CASCADE")
	require.Contains(t, sql, "PRIMARY KEY (account_id, node_id, bucket_date)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_clash_account_traffic_daily_node_date ON clash_account_traffic_daily(node_id, bucket_date)")
	// Attributed traffic has its own table; flushes never lock accounts or nodes.
	require.NotContains(t, strings.ToUpper(sql), "ALTER TABLE ACCOUNTS")
	require.NotContains(t, strings.ToUpper(sql), "ALTER TABLE CLASH_NODES")
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
