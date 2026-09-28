package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashProfileLinksSourceMigration(t *testing.T) {
	content, err := FS.ReadFile("250_clash_profile_links_source.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	// Re-runnable: the constraint from 248 is replaced, not added twice.
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS clash_profiles_source_type_check")
	require.Contains(t, sql, "CHECK (source_type IN ('url', 'file', 'links'))")
	require.NotContains(t, strings.ToUpper(sql), "CONCURRENTLY")
}
