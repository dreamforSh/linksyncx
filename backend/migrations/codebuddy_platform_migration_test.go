package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeBuddyPlatformMigration(t *testing.T) {
	content, err := FS.ReadFile("259_codebuddy_platform.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))")
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))")
}
