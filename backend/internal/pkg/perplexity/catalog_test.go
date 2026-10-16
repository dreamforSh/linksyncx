package perplexity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewestFamilyPreference(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pref    string
		catalog []string
		want    string
		ok      bool
	}{
		{"upgrade to newest version", "gpt54", []string{"gpt54", "gpt55"}, "gpt55", true},
		{"highest of several newer", "gpt54", []string{"gpt53", "gpt56", "gpt55"}, "gpt56", true},
		{"older versions are not upgrades", "gpt54", []string{"gpt53", "gpt52"}, "", false},
		{"same version is not an upgrade", "gpt54", []string{"gpt54"}, "", false},
		{"thinking variant groups with base family", "gpt54_thinking", []string{"gpt55", "gpt55_thinking"}, "gpt55_thinking", true},
		{"base preference prefers base candidate", "gpt54", []string{"gpt55", "gpt55_thinking"}, "gpt55", true},
		{"thinking-only successor still upgrades", "gpt54", []string{"gpt55_thinking"}, "gpt55_thinking", true},
		{"joined thinking suffix family", "claude46sonnetthinking", []string{"claude47sonnetthinking"}, "claude47sonnetthinking", true},
		{"embedded version family", "claude46sonnet", []string{"claude47sonnet"}, "claude47sonnet", true},
		{"mixed family catalog", "gpt54", []string{"turbo", "pplx_pro", "gpt55", "claude47sonnet"}, "gpt55", true},
		{"different family ignored", "gpt54", []string{"claude47sonnet"}, "", false},
		{"empty catalog", "gpt54", nil, "", false},
		{"unversioned pref never upgrades", "turbo", []string{"turbo2", "gpt55"}, "", false},
		{"unversioned pplx pref never upgrades", "pplx_pro", []string{"gpt55", "pplx_pro"}, "", false},
		{"empty pref", "", []string{"gpt55"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NewestFamilyPreference(tc.pref, tc.catalog)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestIsStaticAliasPreference(t *testing.T) {
	for _, name := range []string{"sonar", "sonar-pro", "sonar-reasoning", "Sonar-Deep-Research", "turbo", "sonar-computer", "sonar:computer", " sonar-pro "} {
		require.True(t, IsStaticAliasPreference(name), name)
	}
	for _, name := range []string{"gpt54", "gpt54_thinking", "claude55opus", "gpt54:computer", "", "gpt-5", "pplx_pro"} {
		require.False(t, IsStaticAliasPreference(name), name)
	}
}
