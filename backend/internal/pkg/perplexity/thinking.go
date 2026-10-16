package perplexity

import (
	"fmt"
	"strings"
)

// These pairs are verified web preferences, not generated model-version guesses.
var thinkingPreferences = map[string]string{
	"gpt54":          "gpt54_thinking",
	"claude46sonnet": "claude46sonnetthinking",
}

// ResolveModelPreferenceChecked is used by explicit model tests: an invalid
// public spelling must never be tested as the automatic turbo fallback.
func ResolveModelPreferenceChecked(mappedModel string) (string, string, error) {
	name := strings.ToLower(strings.TrimSpace(mappedModel))
	if index := strings.LastIndex(name, ":"); index >= 0 {
		switch name[index+1:] {
		case ModeConcise, ModeCopilot, ModeComputer:
			name = name[:index]
		default:
			return "", "", fmt.Errorf("unsupported Perplexity model mode")
		}
	}
	if _, known := aliasToPreference[name]; !known && !isRawPreferenceName(name) {
		return "", "", fmt.Errorf("no Perplexity preference is configured for the selected model")
	}
	preference, mode := ResolveModelPreference(mappedModel)
	return preference, mode, nil
}

// IsThinkingPreference identifies an explicitly selected web reasoning variant.
func IsThinkingPreference(preference string) bool {
	return preference == "pplx_reasoning" || preference == "pplx_alpha" || strings.HasSuffix(preference, "thinking")
}

// ResolveThinkingPreference preserves the mapped family. Catalog contains only
// server-discovered preferences belonging to the current credential identity.
// Selecting a variant does not enforce an effort level or a token budget.
func ResolveThinkingPreference(preference string, enabled bool, catalog []string) (string, error) {
	if !enabled {
		if IsThinkingPreference(preference) {
			return "", fmt.Errorf("the mapped Perplexity model already requires thinking; it cannot be disabled by the request")
		}
		return preference, nil
	}
	if IsThinkingPreference(preference) {
		return preference, nil
	}
	if variant, ok := thinkingPreferences[preference]; ok {
		return variant, nil
	}
	for _, candidate := range []string{preference + "_thinking", preference + "thinking"} {
		for _, discovered := range catalog {
			if candidate == discovered {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("no confirmed thinking variant is available for the mapped Perplexity model %q; select a reasoning model explicitly", preference)
}
