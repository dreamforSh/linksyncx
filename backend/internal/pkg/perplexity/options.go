package perplexity

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/language"
)

// RequestOptions contains validated search settings, not model selection. The
// gateway resolves the model and mode through the account's model mapping.
type RequestOptions struct {
	Sources   []string
	Language  string
	Timezone  string
	Incognito bool
	Local     LocalOptions

	// Internal must only be populated by trusted conversation/upload helpers.
	// Public request parsing uses an explicit allowlist, never JSON unmarshalling
	// into this struct, so clients cannot supply thread handles or attachments.
	Internal          InternalOptions `json:"-"`
	IgnoredParameters []string        `json:"-"`
}

// LocalOptions controls gateway presentation and unsupported-parameter policy.
// These fields are never forwarded to the upstream search endpoint.
type LocalOptions struct {
	IncludeSources        bool
	IncludeSearchProgress bool
	NativeFollowups       bool
	StrictParameters      bool
}

// InternalOptions carries server-generated continuation and upload references.
// Attachments are references returned by the server-side upload helper, not URLs
// or caller-supplied file IDs.
type InternalOptions struct {
	SessionScope        string
	LastBackendUUID     string
	FrontendContextUUID string
	Attachments         []string
}

// DefaultRequestOptions preserves ordinary cross-protocol clients. In
// particular, Anthropic requires max_tokens even though Perplexity cannot apply
// that token limit; strict parameter rejection is therefore opt-in.
func DefaultRequestOptions() RequestOptions {
	return RequestOptions{
		Sources:  []string{"web"},
		Language: "en-US",
		Timezone: "UTC",
		Local: LocalOptions{
			IncludeSources:        true,
			IncludeSearchProgress: true,
			NativeFollowups:       true,
		},
	}
}

// Normalize validates and canonicalizes upstream settings without changing local
// booleans. Use DefaultRequestOptions before applying partial public overrides.
func (o *RequestOptions) Normalize() error {
	if len(o.Sources) == 0 {
		return fmt.Errorf("perplexity.sources must be a non-empty array of web, scholar, or social")
	}
	seen := make(map[string]bool, len(o.Sources))
	sources := make([]string, 0, len(o.Sources))
	for _, source := range o.Sources {
		switch source {
		case "web", "scholar", "social":
		default:
			return fmt.Errorf("perplexity.sources contains unsupported source %q", source)
		}
		if !seen[source] {
			seen[source] = true
			sources = append(sources, source)
		}
	}
	o.Sources = sources

	locale := strings.TrimSpace(o.Language)
	if locale == "" || locale != o.Language || strings.Contains(locale, "_") {
		return fmt.Errorf("perplexity.language must be a valid BCP 47 language tag")
	}
	tag, err := language.Parse(locale)
	if err != nil || tag == language.Und {
		return fmt.Errorf("perplexity.language must be a valid BCP 47 language tag")
	}
	o.Language = tag.String()

	if o.Timezone == "" || strings.TrimSpace(o.Timezone) != o.Timezone || o.Timezone == "Local" {
		return fmt.Errorf("perplexity.timezone must be a valid IANA timezone")
	}
	if _, err := time.LoadLocation(o.Timezone); err != nil {
		return fmt.Errorf("perplexity.timezone must be a valid IANA timezone")
	}
	return nil
}

// BuildAskBodyWithOptions builds a search request from validated settings. There
// is deliberately no public mode override in RequestOptions.
func BuildAskBodyWithOptions(query, modelPref, mode string, opts RequestOptions) ([]byte, error) {
	if err := opts.Normalize(); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = ModeConcise
	}
	contextUUID := opts.Internal.FrontendContextUUID
	if contextUUID == "" {
		contextUUID = uuid.NewString()
	}
	attachments := opts.Internal.Attachments
	if attachments == nil {
		attachments = []string{}
	}
	var lastBackendUUID any
	if opts.Internal.LastBackendUUID != "" {
		lastBackendUUID = opts.Internal.LastBackendUUID
	}
	payload := map[string]any{
		"query_str": query,
		"params": map[string]any{
			"attachments":           attachments,
			"frontend_context_uuid": contextUUID,
			"frontend_uuid":         uuid.NewString(),
			"is_incognito":          opts.Incognito,
			"language":              opts.Language,
			"timezone":              opts.Timezone,
			"last_backend_uuid":     lastBackendUUID,
			"mode":                  mode,
			"model_preference":      modelPref,
			"source":                "default",
			"sources":               opts.Sources,
			"search_focus":          "internet",
			"version":               askVersion,
		},
	}
	return json.Marshal(payload)
}
