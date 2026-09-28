package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClashNodeHiddenMigration(t *testing.T) {
	content, err := FS.ReadFile("249_clash_node_hidden.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE clash_nodes ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT FALSE")
	// Hiding is a flag next to the status, not a new status value, so the
	// existing status check and transitions stay untouched.
	require.NotContains(t, sql, "status_check")
}
