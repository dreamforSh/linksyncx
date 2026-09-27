package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashNodeTrafficMigration(t *testing.T) {
	content, err := FS.ReadFile("247_clash_node_traffic.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS clash_node_traffic_daily")
	require.Contains(t, sql, "node_id BIGINT NOT NULL REFERENCES clash_nodes(id) ON DELETE CASCADE")
	require.Contains(t, sql, "PRIMARY KEY (node_id, bucket_date)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_clash_node_traffic_daily_date ON clash_node_traffic_daily(bucket_date)")
	// Traffic lives in its own table so flushes never lock clash_nodes rows.
	require.NotContains(t, strings.ToUpper(sql), "ALTER TABLE CLASH_NODES")
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
