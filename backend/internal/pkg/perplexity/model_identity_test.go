package perplexity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerplexityReportedModelParsing(t *testing.T) {
	ev, ok := ParseEvent([]byte(`{"display_model":"gpt54","user_selected_model":"gpt54_thinking"}`))
	require.True(t, ok)
	require.Equal(t, "gpt54", ev.DisplayModel)
	require.Equal(t, "gpt54_thinking", ev.UserSelectedModel)
	for _, payload := range []string{
		`{"display_model":42,"user_selected_model":true}`,
		`{"display_model":"bad model","user_selected_model":"bad\nmodel"}`,
		`{"display_model":"<script>"}`,
	} {
		ev, ok := ParseEvent([]byte(payload))
		require.True(t, ok)
		require.Empty(t, ev.DisplayModel)
		require.Empty(t, ev.UserSelectedModel)
	}
	require.Empty(t, NormalizeReportedModel(strings.Repeat("a", 201)))
}

func TestPerplexityModelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, requested, reported, status string
	}{
		{"matched", "gpt54", "gpt54", "matched"},
		{"case insensitive", "gpt54", "GPT54", "matched"},
		{"verified auto display alias", "pplx_pro", "turbo", "matched"},
		{"thinking display alias", "claude46sonnetthinking", "claude46sonnet", "matched"},
		{"underscore thinking display alias", "gpt54_thinking", "gpt54", "matched"},
		{"substituted", "gpt54", "claude46sonnet", "substituted"},
		{"missing declaration", "gpt54", "", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewModelIdentityTracker(tc.requested)
			tracker.Observe(Event{DisplayModel: tc.reported, UserSelectedModel: tc.requested}, "answer")
			identity := tracker.Identity()
			require.Equal(t, tc.status, identity.Status)
			require.Equal(t, tc.reported, identity.ActualModel)
			require.False(t, identity.Conflict)
			if tc.status == "substituted" {
				require.Contains(t, identity.Notice(), tc.reported)
				require.Contains(t, identity.Notice(), tc.requested)
			} else {
				require.Empty(t, identity.Notice())
			}
		})
	}
}

func TestPerplexityModelIdentityDistinguishesAnswerAndAuxiliaryDeclarations(t *testing.T) {
	tracker := NewModelIdentityTracker("gpt54")
	tracker.Observe(Event{DisplayModel: "search-worker"}, "")
	tracker.Observe(Event{DisplayModel: "gpt54"}, "answer")
	tracker.Observe(Event{DisplayModel: "turbo", EndOfStream: true}, "")
	require.Equal(t, "gpt54", tracker.Identity().ActualModel)
	require.Equal(t, "matched", tracker.Identity().Status)
	tracker.Observe(Event{DisplayModel: "turbo"}, "changed answer")
	identity := tracker.Identity()
	require.Equal(t, "mixed", identity.Status)
	require.True(t, identity.Conflict)
	require.Empty(t, identity.ActualModel, "multiple answerers must not be advertised as one actual model")
	require.Contains(t, identity.Notice(), "多个不同模型")
	identity.ObservedModels[0] = "changed-copy"
	require.Equal(t, "gpt54", tracker.Identity().ObservedModels[0])
}

func TestPerplexityModelIdentityLateDeclarationAndKnownAliases(t *testing.T) {
	tracker := NewModelIdentityTracker("gpt54")
	tracker.Observe(Event{UserSelectedModel: "gpt54"}, "answer")
	require.Equal(t, "unknown", tracker.Identity().Status)
	tracker.Observe(Event{DisplayModel: "gpt54", EndOfStream: true}, "")
	require.Equal(t, "gpt54", tracker.Identity().ActualModel)

	tracker = NewModelIdentityTracker("pplx_pro")
	tracker.Observe(Event{DisplayModel: "pplx_pro"}, "one")
	tracker.Observe(Event{DisplayModel: "turbo"}, "two")
	require.False(t, tracker.Identity().Conflict)
	require.Equal(t, "matched", tracker.Identity().Status)

	tracker = NewModelIdentityTracker("gpt54")
	tracker.Observe(Event{DisplayModel: "gpt54"}, "one")
	tracker.Observe(Event{DisplayModel: "gpt54thinking"}, "two")
	require.Equal(t, "mixed", tracker.Identity().Status)
}
