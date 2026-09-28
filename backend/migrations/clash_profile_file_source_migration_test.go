package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashProfileFileSourceMigration(t *testing.T) {
	content, err := FS.ReadFile("248_clash_profile_file_source.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	// Existing subscriptions become url sources; every column has a default so
	// the ALTER never rewrites rows with NULLs.
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS source_type VARCHAR(16) NOT NULL DEFAULT 'url'")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS source_name VARCHAR(255) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS source_size BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS content_encrypted TEXT NOT NULL DEFAULT ''")
	// Re-runnable: the check constraint is dropped before it is added again.
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS clash_profiles_source_type_check")
	require.Contains(t, sql, "CHECK (source_type IN ('url', 'file'))")
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
