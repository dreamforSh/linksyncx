package perplexity

import (
	"fmt"
	"strings"
)

// ModelIdentity describes upstream declarations, not a model entitlement probe.
// ActualModel stays empty when the stream never reports a display_model.
type ModelIdentity struct {
	RequestedPreference string   `json:"requested_preference"`
	UserSelectedModel   string   `json:"user_selected_model,omitempty"`
	ActualModel         string   `json:"actual_model,omitempty"`
	ObservedModels      []string `json:"observed_models,omitempty"`
	Status              string   `json:"status"`
	Conflict            bool     `json:"conflict"`
}

// ModelIdentityTracker selects declarations associated with answer text rather
// than mistaking a search worker or a text-free terminal frame for the answerer.
type ModelIdentityTracker struct {
	requested string
	selected  string
	pending   string
	models    []string
	hasAnswer bool
}

func NewModelIdentityTracker(requested string) *ModelIdentityTracker {
	return &ModelIdentityTracker{requested: NormalizeReportedModel(requested)}
}

// NormalizeReportedModel bounds provider-controlled identifiers before they can
// become log fields, response metadata, or a visible Markdown notice.
func NormalizeReportedModel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 {
		return ""
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/", r) {
			continue
		}
		return ""
	}
	return value
}

// ModelsMatch only recognizes observed aliases; it never derives model versions.
func ModelsMatch(requested, actual string) bool {
	requested = strings.ToLower(NormalizeReportedModel(requested))
	actual = strings.ToLower(NormalizeReportedModel(actual))
	if requested == "" || actual == "" {
		return false
	}
	if requested == actual || requested == "pplx_pro" && actual == "turbo" {
		return true
	}
	if !strings.HasSuffix(requested, "thinking") {
		return false
	}
	base := strings.TrimSuffix(strings.TrimSuffix(requested, "thinking"), "_")
	return base == actual
}

// Observe must receive the actual new answer text after snapshot reconciliation.
// A DONE snapshot repeating an existing answer contributes no new text.
func (t *ModelIdentityTracker) Observe(ev Event, newText string) {
	if selected := NormalizeReportedModel(ev.UserSelectedModel); selected != "" {
		t.selected = selected
	}
	if model := NormalizeReportedModel(ev.DisplayModel); model != "" {
		t.pending = model
	}
	t.hasAnswer = t.hasAnswer || strings.TrimSpace(newText) != ""
	if newText == "" || t.pending == "" {
		return
	}
	for _, model := range t.models {
		if strings.EqualFold(model, t.pending) {
			return
		}
	}
	// Bound memory even when a malformed provider changes its declaration on
	// every delta. More than one distinct answer model is already a conflict.
	if len(t.models) < 8 {
		t.models = append(t.models, t.pending)
	}
}

// ResetAnswer discards model contributions to a buffered answer replaced by an
// authoritative final snapshot, retaining any current upstream declaration.
func (t *ModelIdentityTracker) ResetAnswer() {
	t.models = nil
	t.hasAnswer = false
}

func (t *ModelIdentityTracker) Identity() ModelIdentity {
	models := t.models
	if len(models) == 0 && t.hasAnswer && t.pending != "" {
		models = []string{t.pending}
	}
	identity := ModelIdentity{
		RequestedPreference: t.requested,
		UserSelectedModel:   t.selected,
		Status:              "unknown",
		ObservedModels:      append([]string(nil), models...),
	}
	if len(models) == 0 {
		return identity
	}
	identity.ActualModel = models[0]
	identity.Status = "matched"
	firstMatches := ModelsMatch(t.requested, identity.ActualModel)
	for _, model := range models {
		matchesRequested := ModelsMatch(t.requested, model)
		if !matchesRequested {
			identity.Status = "substituted"
		}
		if !strings.EqualFold(identity.ActualModel, model) && (!firstMatches || !matchesRequested) {
			identity.Conflict = true
		}
	}
	if identity.Conflict {
		identity.Status = "mixed"
		identity.ActualModel = ""
	}
	return identity
}

// Notice is appended uniformly across protocols and is not billable model text.
func (i ModelIdentity) Notice() string {
	switch i.Status {
	case "substituted":
		return fmt.Sprintf("\n\n> Perplexity 上游报告使用了 `%s`，与请求的 `%s` 不同。", i.ActualModel, i.RequestedPreference)
	case "mixed":
		return "\n\n> Perplexity 上游在本次回答中报告了多个不同模型，无法确认单一实际执行模型。"
	default:
		return ""
	}
}
