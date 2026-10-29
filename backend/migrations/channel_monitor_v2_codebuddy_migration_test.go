package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorV2CodeBuddyPlatformMigration(t *testing.T) {
	content, err := FS.ReadFile("261_channel_monitor_v2_codebuddy_platform.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		`platforms = platforms || '[{"platform":"codebuddy","enabled":true,"models":[]}]'::jsonb`)
	// Appends only once and never re-enables a codebuddy entry the operator turned off.
	require.Contains(t, sql, `AND NOT platforms @> '[{"platform":"codebuddy"}]'::jsonb`)
	require.Contains(t, sql, "version = version + 1")
}
