package migrations

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandCodePlatformMigration(t *testing.T) {
	content, err := FS.ReadFile("260_command_code_platform.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql,
		"CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'command_code'))")
	require.Contains(t, sql,
		"CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'perplexity', 'codebuddy', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'command_code'))")
	require.Contains(t, sql,
		"CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok', 'perplexity', 'antigravity', 'kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go', 'command_code'))")
	require.Contains(t, sql, "position('command_code' IN monitor_constraint_def) = 0")
	require.Contains(t, sql, "position('command_code' IN template_constraint_def) = 0")

	// 新约束必须是上一版约束的超集：platform / target_platform 上一版为 259，
	// 渠道监控 provider 上一版为 255（259 未改动 provider 约束）。
	for _, tc := range []struct {
		column   string
		previous string
	}{
		{column: "platform", previous: "259_codebuddy_platform.sql"},
		{column: "target_platform", previous: "259_codebuddy_platform.sql"},
		{column: "provider", previous: "255_perplexity_platform.sql"},
	} {
		previousContent, err := FS.ReadFile(tc.previous)
		require.NoError(t, err)
		previousLists := checkConstraintValueLists(string(previousContent), tc.column)
		require.NotEmpty(t, previousLists, tc.previous)
		currentLists := checkConstraintValueLists(string(content), tc.column)
		require.NotEmpty(t, currentLists, tc.column)
		for _, current := range currentLists {
			require.Contains(t, current, "command_code", tc.column)
			for _, previous := range previousLists {
				require.Subset(t, current, previous, tc.column)
			}
		}
	}
}

// checkConstraintValueLists 提取 SQL 中 `CHECK (<column> IN (...))` 的取值列表。
func checkConstraintValueLists(sql, column string) [][]string {
	pattern := regexp.MustCompile(`CHECK\s*\(\s*` + regexp.QuoteMeta(column) + `\s+IN\s*\(([^)]*)\)\s*\)`)
	var lists [][]string
	for _, match := range pattern.FindAllStringSubmatch(sql, -1) {
		var values []string
		for _, raw := range strings.Split(match[1], ",") {
			values = append(values, strings.Trim(strings.TrimSpace(raw), "'"))
		}
		lists = append(lists, values)
	}
	return lists
}
