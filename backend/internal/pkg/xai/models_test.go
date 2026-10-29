package xai

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultModelMappingExcludesCrossClientWildcards(t *testing.T) {
	original := RuntimeModelMappingOptions()
	t.Cleanup(func() { SetRuntimeModelMappingOptions(original) })
	SetRuntimeModelMappingOptions(ModelMappingOptions{})
	mapping := DefaultModelMapping()

	require.Equal(t, "grok-4.6", mapping["grok"])
	require.Equal(t, "grok-4.6", mapping["grok-latest"])
	require.Equal(t, "grok-build-0.1", mapping["grok-build"])
	require.Equal(t, "grok-build-0.1", mapping["grok-build-latest"])
	require.Equal(t, DefaultImagineImageQualityModel, mapping["grok-imagine-edit"])
	require.Equal(t, DefaultImagineVideo15Model, mapping["grok-imagine-video-1.5"])
	require.Equal(t, DefaultImagineVideo15Model, mapping["grok-imagine-video-1.5-preview"])
	require.Equal(t, "grok-4.6", mapping["xai/grok"])

	// Cross-vendor wildcards must stay opt-in.
	_, hasGPT := mapping["gpt-*"]
	_, hasClaude := mapping["claude-*"]
	require.False(t, hasGPT)
	require.False(t, hasClaude)
}

func TestModelMappingWithOptionsCrossClient(t *testing.T) {
	t.Parallel()
	mapping := ModelMappingWithOptions(ModelMappingOptions{
		DefaultText:          "grok-4.3",
		EnableCrossClientMap: true,
	})
	require.Equal(t, "grok-4.3", mapping["grok"])
	require.Equal(t, "grok-4.3", mapping["gpt-*"])
	require.Equal(t, "grok-4.3", mapping["claude-*"])
	require.Equal(t, "grok-4.3", mapping["codex-*"])
}

func TestCanonicalImagineVideoModel(t *testing.T) {
	t.Parallel()
	require.Equal(t, DefaultImagineVideoModel, CanonicalImagineVideoModel("grok-imagine-video"))
	require.Equal(t, DefaultImagineVideo15Model, CanonicalImagineVideoModel("grok-imagine-video-1.5"))
	require.Equal(t, DefaultImagineVideo15Model, CanonicalImagineVideoModel("grok-imagine-video-1.5-preview"))
	require.Equal(t, DefaultImagineVideo15Model, CanonicalImagineVideoModel("xai/grok-video-1.5"))
	require.Equal(t, "grok-imagine-video-2", CanonicalImagineVideoModel("grok-imagine-video-2"))
}

func TestIsGrokModelID(t *testing.T) {
	t.Parallel()
	require.True(t, IsGrokModelID("grok-4.5"))
	require.True(t, IsGrokModelID("grok-4.6"))
	require.True(t, IsGrokModelID("x-ai/grok-4.3"))
	require.False(t, IsGrokModelID("gpt-5"))
	require.False(t, IsGrokModelID("claude-sonnet-4"))
}

func TestIsGrokImagineModel(t *testing.T) {
	t.Parallel()
	require.True(t, IsGrokImagineModel("grok-imagine-image"))
	require.True(t, IsGrokImagineModel("grok-imagine-video-1.5-preview"))
	require.True(t, IsGrokImagineModel("xai/grok-imagine-image-quality"))
	require.True(t, IsGrokImagineModel("grok-video-1.5"))
	require.False(t, IsGrokImagineModel("grok-4.6"))
	require.False(t, IsGrokImagineModel("grok-build-0.1"))
}

func TestDefaultModelsIncludesGrok46And47(t *testing.T) {
	t.Parallel()
	ids := DefaultModelIDs()
	require.Contains(t, ids, "grok-4.7")
	require.Equal(t, "grok-4.7", ResolveGrokTextResponsesModelID("grok-4.7"))
	require.Equal(t, "grok-4.7", ResolveGrokTextResponsesModelID("grok-4.7-latest"))

	// Keep the existing assertions for the previous current model as well.
	require.Contains(t, ids, "grok-4.6")
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("grok-4.6"))
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("grok-4.6-latest"))
}

func TestResolveGrokTextResponsesModelID(t *testing.T) {
	t.Parallel()
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID(""))
	require.Equal(t, "grok-4.3", ResolveGrokTextResponsesModelID("grok", "grok-4.3"))
	require.Equal(t, "grok-4.20-multi-agent-0309", ResolveGrokTextResponsesModelID("grok-4.20-multi-agent"))
}

func TestExplicitGrok45DoesNotFollowRuntimeDefault(t *testing.T) {
	require.Equal(t, "grok-4.5", ResolveGrokTextResponsesModelID("grok-4.5", "grok-4.6"))
	require.Equal(t, "grok-4.5", ResolveGrokTextResponsesModelID("grok-4.5-latest", "grok-4.6"))
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("grok", "grok-4.6"))
}

func TestBareGrokAliasesFollowGrok46Default(t *testing.T) {
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("grok"))
	require.Equal(t, "grok-4.6", ResolveGrokTextResponsesModelID("grok-latest"))
	require.Equal(t, "grok-build-0.1", ResolveGrokTextResponsesModelID("grok-build-latest"))
}

func TestGrokBuildFastModelIsRoutedOnlyForOAuth(t *testing.T) {
	original := RuntimeModelMappingOptions()
	t.Cleanup(func() { SetRuntimeModelMappingOptions(original) })
	SetRuntimeModelMappingOptions(ModelMappingOptions{})

	oauth := DefaultOAuthModelMapping()
	for _, requested := range []string{"grok-4.7-fast", "grok-4.7-build-fast", "xai/grok-4.7-fast", "x-ai/grok-4.7-build-fast"} {
		require.Equal(t, GrokBuildFastTextModel, oauth[requested], requested)
	}
	// Everything the API-key mapping routes stays routed for OAuth.
	apiKey := DefaultModelMapping()
	for requested, upstream := range apiKey {
		require.Equal(t, upstream, oauth[requested], requested)
	}
	for _, requested := range []string{"grok-4.7-fast", "grok-4.7-build-fast", "xai/grok-4.7-fast"} {
		_, routed := apiKey[requested]
		require.False(t, routed, "the public API does not serve %s", requested)
	}
}

func TestGrokBuildFastModelIdentity(t *testing.T) {
	require.Equal(t, GrokBuildFastTextModel, ResolveGrokTextResponsesModelID("grok-4.7-build-fast"))
	require.Equal(t, GrokBuildFastTextModel, ResolveGrokTextResponsesModelID("xai/grok-4.7-fast"))
	require.True(t, IsGrokTextResponsesModelID("grok-4.7-fast"))
	require.True(t, IsGrokBuildOnlyTextModel("Grok-4.7-Build-Fast"))
	require.False(t, IsGrokBuildOnlyTextModel("grok-4.7"))

	ids := make([]string, 0)
	for _, model := range BuildOnlyModels() {
		ids = append(ids, model.ID)
	}
	require.Equal(t, []string{GrokBuildFastTextModel}, ids)
	require.NotContains(t, DefaultModelIDs(), GrokBuildFastTextModel)
}
