package perplexity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerplexityThinkingPreferenceResolution(t *testing.T) {
	for _, tc := range []struct {
		base    string
		enabled bool
		catalog []string
		want    string
	}{
		{"gpt54", true, nil, "gpt54_thinking"},
		{"claude46sonnet", true, nil, "claude46sonnetthinking"},
		{"gpt54", false, nil, "gpt54"},
		{"gpt54_thinking", true, nil, "gpt54_thinking"},
		{"gpt54_thinking", false, nil, ""},
		{"pplx_reasoning", true, nil, "pplx_reasoning"},
		{"pplx_alpha", false, nil, ""},
		{"future_model", true, []string{"future_model_thinking"}, "future_model_thinking"},
		{"future_model", true, []string{"different_family_thinking"}, ""},
		{"future_model", true, nil, ""},
		{"pplx_pro", true, nil, ""},
	} {
		t.Run(tc.base+map[bool]string{true: "/enabled", false: "/disabled"}[tc.enabled], func(t *testing.T) {
			got, err := ResolveThinkingPreference(tc.base, tc.enabled, tc.catalog)
			if tc.want == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestPerplexityExplicitProbeNeverUsesAutomaticFallback(t *testing.T) {
	for _, model := range []string{"", "gpt-5-without-mapping", "gpt54:unsupported"} {
		_, _, err := ResolveModelPreferenceChecked(model)
		require.Error(t, err)
	}
	pref, mode, err := ResolveModelPreferenceChecked("gpt54:computer")
	require.NoError(t, err)
	require.Equal(t, "gpt54", pref)
	require.Equal(t, ModeComputer, mode)
}
