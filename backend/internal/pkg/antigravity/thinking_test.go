package antigravity

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitGeminiThinkingVariant(t *testing.T) {
	tests := []struct {
		model     string
		wantOK    bool
		wantBase  string
		wantLevel string
		wantEff   string
	}{
		{"gemini-4-argon-high", true, "gemini-4-argon", "high", "high"},
		{"gemini-4-argon-medium", true, "gemini-4-argon", "medium", "medium"},
		{"gemini-4-argon-low", true, "gemini-4-argon", "low", "low"},
		{"gemini-4-argon-minimal", true, "gemini-4-argon", "minimal", "minimal"},
		{"gemini-4-argon-xhigh", true, "gemini-4-argon", "high", "xhigh"},
		{"gemini-4-argon-max", true, "gemini-4-argon", "high", "max"},
		{"models/gemini-4-argon-high", true, "gemini-4-argon", "high", "high"},
		{"gemini-3.5-flash-extra-low", true, "gemini-3.5-flash", "minimal", "minimal"},
		{"gemini-3.8-flash-tiered", true, "gemini-3.8-flash", "", "tiered"},
		{"gemini-4-argon", false, "", "", ""},
		{"gemini-3.1-flash-lite", false, "", "", ""},
		{"gemini-3.1-flash-image", false, "", "", ""},
		{"gemini-2.5-flash-thinking", false, "", "", ""},
		{"claude-opus-4-6-thinking", false, "", "", ""},
		{"gemini-*", false, "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, ok := SplitGeminiThinkingVariant(tt.model)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				require.Equal(t, tt.wantBase, got.BaseModel)
				require.Equal(t, tt.wantLevel, got.ThinkingLevel)
				require.Equal(t, tt.wantEff, got.Effort)
			}
		})
	}
}

func TestBaseDisplayNameFromThinkingVariant(t *testing.T) {
	require.Equal(t, "Gemini 4 Argon", BaseDisplayNameFromThinkingVariant("Gemini 4 Argon (High)", "gemini-4-argon"))
	require.Equal(t, "Gemini 4 Argon", BaseDisplayNameFromThinkingVariant("Gemini 4 Argon High", "gemini-4-argon"))
	require.Equal(t, "gemini-4-argon", BaseDisplayNameFromThinkingVariant("gemini-4-argon-high", "gemini-4-argon"))
	require.Equal(t, "gemini-4-argon", BaseDisplayNameFromThinkingVariant("", "gemini-4-argon"))
}

func TestIsGeminiReasoningModel_DynamicGemini4(t *testing.T) {
	require.True(t, IsGeminiReasoningModel("gemini-4-argon"))
	require.True(t, IsGeminiReasoningModel("gemini-4-argon-high"))
	require.True(t, IsGeminiReasoningModel("gemini-4-neon-high"))
	require.True(t, IsGeminiReasoningModel("gemini-3.8-flash-low"))
	require.False(t, IsGeminiReasoningModel("gemini-3.1-flash-image"))
	require.False(t, IsGeminiReasoningModel("claude-sonnet-4-6"))
}

func TestBuildGenerationConfigWithOptions_Gemini4ThinkingLevel(t *testing.T) {
	t.Run("gemini-4-argon-high with preferred low level uses thinkingLevel only", func(t *testing.T) {
		req := &ClaudeRequest{
			Model: "gemini-4-argon-high",
			Thinking: &ThinkingConfig{
				Type:         "enabled",
				BudgetTokens: 20480,
			},
		}
		cfg := buildGenerationConfigWithOptions(req, "low")
		require.NotNil(t, cfg.ThinkingConfig)
		require.True(t, cfg.ThinkingConfig.IncludeThoughts)
		require.Equal(t, "low", cfg.ThinkingConfig.ThinkingLevel)
		require.Zero(t, cfg.ThinkingConfig.ThinkingBudget, "thinkingBudget must be omitted when thinkingLevel is set")
	})

	t.Run("gemini-4-argon with output_config.effort medium adapts to thinkingLevel", func(t *testing.T) {
		req := &ClaudeRequest{
			Model:        "gemini-4-argon-high",
			Thinking:     &ThinkingConfig{Type: "adaptive"},
			OutputConfig: &ClaudeOutputConfig{Effort: "medium"},
		}
		cfg := buildGenerationConfigWithOptions(req, "")
		require.NotNil(t, cfg.ThinkingConfig)
		require.True(t, cfg.ThinkingConfig.IncludeThoughts)
		require.Equal(t, "medium", cfg.ThinkingConfig.ThinkingLevel)
		require.Zero(t, cfg.ThinkingConfig.ThinkingBudget)
	})

	t.Run("gemini-4-argon-high with disabled thinking sets minimal without thoughts", func(t *testing.T) {
		req := &ClaudeRequest{
			Model:    "gemini-4-argon-high",
			Thinking: &ThinkingConfig{Type: "disabled"},
		}
		cfg := buildGenerationConfigWithOptions(req, "")
		require.NotNil(t, cfg.ThinkingConfig)
		require.False(t, cfg.ThinkingConfig.IncludeThoughts)
		require.Equal(t, "minimal", cfg.ThinkingConfig.ThinkingLevel)
		require.Zero(t, cfg.ThinkingConfig.ThinkingBudget)
	})

	t.Run("TransformClaudeToGeminiWithOptions emits thinkingLevel JSON without thinkingBudget", func(t *testing.T) {
		body, err := TransformClaudeToGeminiWithOptions(
			&ClaudeRequest{
				Model:        "gemini-4-argon",
				MaxTokens:    4096,
				Thinking:     &ThinkingConfig{Type: "adaptive"},
				OutputConfig: &ClaudeOutputConfig{Effort: "low"},
				Messages: []ClaudeMessage{
					{Role: "user", Content: json.RawMessage(`"hello"`)},
				},
			},
			"proj-1",
			"gemini-4-argon-high",
			TransformOptions{EnableIdentityPatch: true, PreferredThinkingLevel: "low"},
		)
		require.NoError(t, err)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(body, &parsed))
		require.Equal(t, "gemini-4-argon-high", parsed["model"])
		reqObj, ok := parsed["request"].(map[string]any)
		require.True(t, ok)
		genCfg, ok := reqObj["generationConfig"].(map[string]any)
		require.True(t, ok)
		tc, ok := genCfg["thinkingConfig"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "low", tc["thinkingLevel"])
		require.Equal(t, true, tc["includeThoughts"])
		_, hasBudget := tc["thinkingBudget"]
		require.False(t, hasBudget)
	})
}
