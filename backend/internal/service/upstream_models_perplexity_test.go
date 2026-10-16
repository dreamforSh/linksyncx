//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
)

const perplexityDynamicModelsFixture = `{"models":[
 {"model":"turbo","label":"Auto","subscription_tier":"free"},
 {"model":"pplx_pro","label":"Web Pro","subscription_tier":"pro"},
 {"model":"future_model","label":"Future Model Label","subscription_tier":"max","capabilities":{"image":true,"search":false},"reasoning":true,"context_window":32000,"input_modalities":["text","image"]}
]}`

func TestPerplexityModels_DiscoveryPreservesMetadataWithoutRegistryRequests(t *testing.T) {
	svc, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, perplexity.ModelsConfigPath, req.URL.Path)
		_, _ = w.Write([]byte(perplexityDynamicModelsFixture))
	})
	repo := &perplexityInfoRepo{}
	svc.accountRepo = repo
	catalog, err := svc.SyncUpstreamModelCatalog(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"turbo", "pplx_pro", "future_model"}, catalog.Models)
	require.Equal(t, 1, upstream.count(), "no models.dev capability enrichment for web model preferences")
	model := catalog.Metadata["future_model"]
	require.Equal(t, "Future Model Label", model.DisplayName)
	require.Equal(t, "max", model.SubscriptionTier)
	require.JSONEq(t, `{"image":true,"search":false}`, string(model.Capabilities))
	require.NotNil(t, model.Reasoning)
	require.True(t, *model.Reasoning)
	require.Equal(t, int64(32000), model.ContextWindow)
	require.Equal(t, []string{"text", "image"}, model.InputModalities)
	require.Nil(t, catalog.Metadata["turbo"].Reasoning, "unknown capability must stay unknown")
	require.Empty(t, catalog.Warnings)
	require.NotNil(t, repo.extra[UpstreamModelMetadataExtraKey])
	require.Equal(t, "perplexity", account.GetUpstreamModelMetadataSnapshot().Source)
}

func TestPerplexityModels_ProjectsAliasesAndNeverInventsMissingTargets(t *testing.T) {
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(perplexityDynamicModelsFixture))
	})
	account.Credentials["model_mapping"] = map[string]any{
		"my-future": "future_model:computer", "my-pro": "sonar-pro", "made-up": "not-present", "also-made-up": "unknown-hyphenated-name",
	}
	models, err := svc.FetchPerplexityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.Equal(t, "my-future", models[0].ID)
	require.Equal(t, "Future Model Label", models[0].DisplayName)
	require.Equal(t, "max", models[0].SubscriptionTier)
	require.JSONEq(t, `{"image":true,"search":false}`, string(models[0].Capabilities))
	require.Equal(t, "my-pro", models[1].ID)
	require.Equal(t, "Web Pro", models[1].DisplayName)
}

func TestPerplexityModels_WildcardsProjectOnlyConcreteDiscoveredNames(t *testing.T) {
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(perplexityDynamicModelsFixture))
	})
	account.Credentials["model_mapping"] = map[string]any{"future_*": "future_model"}
	models, err := svc.FetchPerplexityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Len(t, models, 1)
	require.Equal(t, "future_model", models[0].ID)
}

func TestPerplexityModels_CancellationAndMissingProxyDoNotGoDirect(t *testing.T) {
	svc, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(perplexityDynamicModelsFixture))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := svc.buildPerplexityUpstreamModelsRequest(ctx, account)
	require.NoError(t, err)
	require.ErrorIs(t, req.Context().Err(), context.Canceled)
	require.Empty(t, req.Header.Get("Authorization"))
	require.Contains(t, req.Header.Get("Cookie"), "fake-csrf")

	proxyID := int64(11)
	account.ProxyID = &proxyID
	_, err = svc.FetchPerplexityAccountModels(context.Background(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Zero(t, upstream.count())
}

func TestPerplexityModels_DefaultsContainOnlyPerplexityAliases(t *testing.T) {
	models := DefaultPerplexityAccountModels(nil)
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
		require.Equal(t, "perplexity", model.OwnedBy)
		require.Empty(t, model.SubscriptionTier)
		require.Empty(t, model.Capabilities)
	}
	require.Equal(t, perplexity.DefaultModelIDs(), ids)
	body, err := json.Marshal(models)
	require.NoError(t, err)
	require.NotContains(t, string(body), "claude")
}
