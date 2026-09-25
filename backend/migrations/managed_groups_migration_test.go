package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedGroupsMigration(t *testing.T) {
	content, err := FS.ReadFile("242_managed_groups.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS kind VARCHAR(20) NOT NULL DEFAULT 'channel'")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS category VARCHAR(20)")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS groups_kind_check")
	require.Contains(t, sql, "CHECK (kind IN ('channel', 'managed'))")
	require.Contains(t, sql, "(kind = 'managed' AND category IN ('enterprise', 'team')) OR (kind = 'channel' AND category IS NULL)")
	require.Contains(t, sql, "CREATE INDEX IF NOT EXISTS idx_groups_kind")
}
