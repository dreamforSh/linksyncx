package antigravity

import "strings"

const (
	geminiThinkingBudgetLowMax    = 1024
	geminiThinkingBudgetMediumMax = 8192
)

// GeminiThinkingVariant describes a Gemini model ID whose thinking tier is encoded
// in its suffix (for example, "gemini-4-argon-high" -> BaseModel="gemini-4-argon",
// Suffix="-high", ThinkingLevel="high", Effort="high").
type GeminiThinkingVariant struct {
	BaseModel     string
	Suffix        string
	ThinkingLevel string
	Effort        string
}

type geminiThinkingSuffixSpec struct {
	suffix        string
	thinkingLevel string
	effort        string
}

// Ordered longest-first so compound suffixes like "-extra-low" and "-xhigh"
// match before "-low" and "-high".
var geminiThinkingSuffixSpecs = []geminiThinkingSuffixSpec{
	{suffix: "-extra-low", thinkingLevel: "minimal", effort: "minimal"},
	{suffix: "-minimal", thinkingLevel: "minimal", effort: "minimal"},
	{suffix: "-xhigh", thinkingLevel: "high", effort: "xhigh"},
	{suffix: "-high", thinkingLevel: "high", effort: "high"},
	{suffix: "-medium", thinkingLevel: "medium", effort: "medium"},
	{suffix: "-low", thinkingLevel: "low", effort: "low"},
	{suffix: "-max", thinkingLevel: "high", effort: "max"},
	{suffix: "-tiered", thinkingLevel: "", effort: "tiered"},
}

// GeminiThinkingVariantSuffixes returns all recognized Gemini thinking-tier suffixes.
func GeminiThinkingVariantSuffixes() []string {
	out := make([]string, len(geminiThinkingSuffixSpecs))
	for i, spec := range geminiThinkingSuffixSpecs {
		out[i] = spec.suffix
	}
	return out
}

// SplitGeminiThinkingVariant splits a suffixed Antigravity Gemini thinking variant ID
// (e.g. "gemini-4-argon-high", "gemini-3.8-flash-medium", "gemini-3.5-flash-extra-low")
// into its base model ID and thinking metadata.
func SplitGeminiThinkingVariant(modelID string) (GeminiThinkingVariant, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(modelID), "models/"))
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "gemini-") || strings.Contains(lower, "*") {
		return GeminiThinkingVariant{}, false
	}
	for _, spec := range geminiThinkingSuffixSpecs {
		if strings.HasSuffix(lower, spec.suffix) {
			bare := trimmed[:len(trimmed)-len(spec.suffix)]
			bareLower := strings.ToLower(bare)
			if bareLower == "gemini" || !strings.HasPrefix(bareLower, "gemini-") {
				return GeminiThinkingVariant{}, false
			}
			// Image-generation models do not have thinking tiers.
			if strings.Contains(bareLower, "-image") {
				return GeminiThinkingVariant{}, false
			}
			return GeminiThinkingVariant{
				BaseModel:     bare,
				Suffix:        spec.suffix,
				ThinkingLevel: spec.thinkingLevel,
				Effort:        spec.effort,
			}, true
		}
	}
	return GeminiThinkingVariant{}, false
}

// HasGeminiThinkingVariantSuffix reports whether modelID carries a Gemini thinking-tier suffix.
func HasGeminiThinkingVariantSuffix(modelID string) bool {
	_, ok := SplitGeminiThinkingVariant(modelID)
	return ok
}

var displayNameThinkingSuffixes = []string{
	" (extra low)",
	" (minimal)",
	" (xhigh)",
	" (high)",
	" (medium)",
	" (low)",
	" (max)",
	" (tiered)",
	" extra low",
	" minimal",
	" xhigh",
	" high",
	" medium",
	" low",
	" max",
	" tiered",
}

// BaseDisplayNameFromThinkingVariant strips trailing thinking-tier annotations
// (e.g. "Gemini 4 Argon (High)" -> "Gemini 4 Argon") when deriving a bare model entry.
func BaseDisplayNameFromThinkingVariant(displayName, bareID string) string {
	trimmed := strings.TrimSpace(displayName)
	bareID = strings.TrimSpace(bareID)
	if trimmed == "" {
		return bareID
	}
	if _, ok := SplitGeminiThinkingVariant(trimmed); ok {
		if bareID != "" {
			return bareID
		}
	}
	lower := strings.ToLower(trimmed)
	for _, suffix := range displayNameThinkingSuffixes {
		if strings.HasSuffix(lower, suffix) {
			candidate := strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)])
			if candidate != "" {
				return candidate
			}
		}
	}
	return trimmed
}

// IsGemini4Model reports whether modelID belongs to the Gemini 4 series (e.g. gemini-4-argon-high).
func IsGemini4Model(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(modelID), "models/")))
	return strings.HasPrefix(lower, "gemini-4-") || strings.HasPrefix(lower, "gemini-4.")
}

// IsGeminiThinkingLevelModel reports whether modelID supports Gemini's thinkingLevel
// field ("minimal" | "low" | "medium" | "high") in generationConfig.thinkingConfig.
func IsGeminiThinkingLevelModel(modelID string) bool {
	lower := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(modelID), "models/")))
	if !strings.HasPrefix(lower, "gemini-") || strings.Contains(lower, "-image") {
		return false
	}
	return IsGemini4Model(lower) ||
		strings.HasPrefix(lower, "gemini-3-") ||
		strings.HasPrefix(lower, "gemini-3.")
}

// NormalizeGeminiThinkingLevel normalizes OpenAI/Claude/Gemini effort strings into
// Gemini's canonical thinkingLevel ("minimal" | "low" | "medium" | "high") or disabled=true.
func NormalizeGeminiThinkingLevel(raw string) (level string, disabled bool, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none", "off", "disabled":
		return "", true, true
	case "minimal", "min", "extra-low", "extra_low", "extralow":
		return "minimal", false, true
	case "low":
		return "low", false, true
	case "medium", "med":
		return "medium", false, true
	case "high", "xhigh", "max", "ultra":
		return "high", false, true
	default:
		return "", false, false
	}
}

// GeminiThinkingLevelFromBudget maps a thinking token budget onto a canonical
// Gemini thinkingLevel. Negative values indicate dynamic thinking ("high").
func GeminiThinkingLevelFromBudget(budget int) string {
	switch {
	case budget < 0:
		return "high"
	case budget == 0:
		return "minimal"
	case budget <= geminiThinkingBudgetLowMax:
		return "low"
	case budget <= geminiThinkingBudgetMediumMax:
		return "medium"
	default:
		return "high"
	}
}

// BuildGeminiThinkingConfig builds a GeminiThinkingConfig for modelID while keeping
// thinkingLevel and thinkingBudget strictly mutually exclusive (Google rejects requests
// that set both fields in the same thinkingConfig).
func BuildGeminiThinkingConfig(modelID, level string, disabled bool, budget *int, includeThoughts bool) *GeminiThinkingConfig {
	if IsGeminiThinkingLevelModel(modelID) {
		if disabled {
			return &GeminiThinkingConfig{
				IncludeThoughts: false,
				ThinkingLevel:   "minimal",
			}
		}
		normalizedLevel, levelDisabled, ok := NormalizeGeminiThinkingLevel(level)
		if levelDisabled {
			return &GeminiThinkingConfig{
				IncludeThoughts: false,
				ThinkingLevel:   "minimal",
			}
		}
		if !ok {
			if budget != nil {
				if *budget == 0 {
					return &GeminiThinkingConfig{
						IncludeThoughts: false,
						ThinkingLevel:   "minimal",
					}
				}
				normalizedLevel = GeminiThinkingLevelFromBudget(*budget)
			} else if variant, hasVariant := SplitGeminiThinkingVariant(modelID); hasVariant && variant.ThinkingLevel != "" {
				normalizedLevel = variant.ThinkingLevel
			} else {
				normalizedLevel = "high"
			}
		}
		return &GeminiThinkingConfig{
			IncludeThoughts: includeThoughts,
			ThinkingLevel:   normalizedLevel,
		}
	}

	// Legacy Gemini 2.5 budget-based thinking models.
	if disabled {
		return &GeminiThinkingConfig{
			IncludeThoughts: false,
			ThinkingBudget:  0,
		}
	}
	resolvedBudget := -1
	if budget != nil {
		resolvedBudget = *budget
	} else if normalizedLevel, levelDisabled, ok := NormalizeGeminiThinkingLevel(level); levelDisabled {
		return &GeminiThinkingConfig{IncludeThoughts: false, ThinkingBudget: 0}
	} else if ok {
		switch normalizedLevel {
		case "minimal":
			resolvedBudget = 512
		case "low":
			resolvedBudget = 1024
		case "medium":
			resolvedBudget = 4096
		case "high":
			resolvedBudget = 16384
		}
	}
	if strings.Contains(strings.ToLower(modelID), "gemini-2.5-flash") && resolvedBudget > Gemini25FlashThinkingBudgetLimit {
		resolvedBudget = Gemini25FlashThinkingBudgetLimit
	}
	return &GeminiThinkingConfig{
		IncludeThoughts: includeThoughts,
		ThinkingBudget:  resolvedBudget,
	}
}

// BuildGeminiThinkingConfigMap converts BuildGeminiThinkingConfig into a JSON-ready map
// for raw map-based Gemini request pipelines.
func BuildGeminiThinkingConfigMap(modelID, level string, disabled bool, budget *int, includeThoughts bool) map[string]any {
	cfg := BuildGeminiThinkingConfig(modelID, level, disabled, budget, includeThoughts)
	if cfg == nil {
		return nil
	}
	out := map[string]any{
		"includeThoughts": cfg.IncludeThoughts,
	}
	if cfg.ThinkingLevel != "" {
		out["thinkingLevel"] = cfg.ThinkingLevel
	} else if cfg.ThinkingBudget != 0 || disabled {
		out["thinkingBudget"] = cfg.ThinkingBudget
	}
	return out
}
