package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerplexityPlatformMigration(t *testing.T) {
	content, err := FS.ReadFile("255_perplexity_platform.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))")
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))")
	require.Contains(t, sql,
		"CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'perplexity', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'))")
}
