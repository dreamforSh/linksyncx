//go:build unit

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// grokOfficialCards is the xAI text price table (USD per token below 200k
// prompt tokens) with the multiplier its long-context row applies to every
// token at or above 200k. Source: https://docs.x.ai/developers/pricing
var grokOfficialCards = []struct {
	model                 string
	input, cached, output float64
	longContextMultiplier float64
}{
	{"grok-4.7", 2e-6, 0.5e-6, 6e-6, 2},
	{"grok-4.7-fast", 4e-6, 1e-6, 12e-6, 1.5},
	{"grok-4.6", 2e-6, 0.5e-6, 6e-6, 2},
	{"grok-4.5", 2e-6, 0.3e-6, 6e-6, 2},
	{"grok-4.3", 1.25e-6, 0.2e-6, 2.5e-6, 2},
	{"grok-4.20-0309-reasoning", 1.25e-6, 0.2e-6, 2.5e-6, 2},
	{"grok-4.20-0309-non-reasoning", 1.25e-6, 0.2e-6, 2.5e-6, 2},
	{"grok-4.20-multi-agent-0309", 1.25e-6, 0.2e-6, 2.5e-6, 2},
	{"grok-build-0.1", 1e-6, 0.2e-6, 2e-6, 2},
}

func newBundledCatalogBillingService(t *testing.T) *BillingService {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	pricingSvc := &PricingService{}
	pricingData, err := pricingSvc.parsePricingData(data)
	require.NoError(t, err)
	pricingSvc.pricingData = pricingData
	return NewBillingService(&config.Config{}, pricingSvc)
}

func TestGrokOfficialCardsAgreeAcrossCatalogAndFallback(t *testing.T) {
	sources := map[string]*BillingService{
		"bundled catalog": newBundledCatalogBillingService(t),
		"fallback":        newTestBillingService(),
	}
	for source, svc := range sources {
		for _, card := range grokOfficialCards {
			pricing, err := svc.GetModelPricing(card.model)
			require.NoError(t, err, "%s %s", source, card.model)
			require.InDelta(t, card.input, pricing.InputPricePerToken, 1e-15, "%s %s input", source, card.model)
			require.InDelta(t, card.cached, pricing.CacheReadPricePerToken, 1e-15, "%s %s cached", source, card.model)
			require.InDelta(t, card.output, pricing.OutputPricePerToken, 1e-15, "%s %s output", source, card.model)
			require.Equal(t, 200000, pricing.LongContextInputThreshold, "%s %s", source, card.model)
			require.True(t, pricing.LongContextThresholdInclusive, "%s %s", source, card.model)
			require.InDelta(t, card.longContextMultiplier, pricing.LongContextInputMultiplier, 1e-12, "%s %s", source, card.model)
			require.InDelta(t, card.longContextMultiplier, pricing.LongContextOutputMultiplier, 1e-12, "%s %s", source, card.model)
		}
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	for _, card := range grokOfficialCards {
		require.Equal(t, "xai", gjson.GetBytes(data, gjson.Escape(card.model)+".litellm_provider").String(), card.model)
	}
}

func TestGrok47FastLadderBillsFastRates(t *testing.T) {
	for source, svc := range map[string]*BillingService{
		"bundled catalog": newBundledCatalogBillingService(t),
		"fallback":        newTestBillingService(),
	} {
		resolver := NewModelPricingResolver(nil, svc)
		group := &Group{LongContextPricingEnabled: true}
		cost := func(model string, tokens UsageTokens) *CostBreakdown {
			t.Helper()
			breakdown, err := svc.CalculateCostUnified(CostInput{Model: model, Group: group, Tokens: tokens, RateMultiplier: 1, Resolver: resolver})
			require.NoError(t, err, "%s %s", source, model)
			return breakdown
		}

		short := UsageTokens{InputTokens: 100000, CacheReadTokens: 50000, OutputTokens: 1000}
		fast := cost("grok-4.7-fast", short)
		standard := cost("grok-4.7", short)
		require.False(t, fast.LongContextBillingApplied, source)
		// $4 input, $1 cached, $12 output per MTok.
		require.InDelta(t, 0.4+0.05+0.012, fast.ActualCost, 1e-9, source)
		require.InDelta(t, 2*standard.ActualCost, fast.ActualCost, 1e-9, source)

		// The prompt reaches 200k with the cached tokens, which bills the whole
		// request at $6 / $1.50 / $18: 1.5x the standard long-context card.
		long := UsageTokens{InputTokens: 150000, CacheReadTokens: 50000, OutputTokens: 1000}
		fastLong := cost("grok-4.7-fast", long)
		standardLong := cost("grok-4.7", long)
		require.True(t, fastLong.LongContextBillingApplied, source)
		require.True(t, standardLong.LongContextBillingApplied, source)
		require.InDelta(t, 0.9+0.075+0.018, fastLong.ActualCost, 1e-9, source)
		require.InDelta(t, 1.5*standardLong.ActualCost, fastLong.ActualCost, 1e-9, source)
	}
}

func TestGrokFastAliasesAndPrefixesUseTheFastCard(t *testing.T) {
	svc := newTestBillingService()
	for _, model := range []string{"grok-4.7-fast", "grok-4.7-build-fast", "xai/grok-4.7-fast", "x-ai/GROK-4.7-FAST", "grok/grok-4.7-build-fast"} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.InDelta(t, 4e-6, pricing.InputPricePerToken, 1e-15, model)
		require.InDelta(t, 12e-6, pricing.OutputPricePerToken, 1e-15, model)
		require.InDelta(t, 1.5, pricing.LongContextInputMultiplier, 1e-12, model)
	}
}

func TestGrokRetiredSlugsBillTheirRedirectTarget(t *testing.T) {
	svc := newTestBillingService()
	for target, models := range map[string][]string{
		"grok-4.3": {
			"grok-4-1-fast-reasoning", "grok-4-1-fast-non-reasoning", "grok-4-1-fast",
			"grok-4-fast-reasoning", "grok-4-fast-non-reasoning", "grok-4-fast",
			"grok-4-0709", "grok-4", "grok-4-latest", "grok-3", "grok-3-latest",
			"xai/grok-4-1-fast-reasoning",
		},
		"grok-build-0.1": {"grok-code-fast-1", "grok-code-fast", "x-ai/grok-code-fast-1"},
	} {
		want, err := svc.GetModelPricing(target)
		require.NoError(t, err)
		for _, model := range models {
			pricing, err := svc.GetModelPricing(model)
			require.NoError(t, err, model)
			require.Equal(t, *want, *pricing, "%s bills as %s", model, target)
		}
	}
}

func TestGrokAliasesKeepTheirOwnCard(t *testing.T) {
	svc := newTestBillingService()
	grok43, err := svc.GetModelPricing("grok-4.3")
	require.NoError(t, err)
	for _, model := range []string{"grok-4.3-latest", "xai/grok-4.3", "grok-4.20-multi-agent", "grok-4.20-multi-agent-latest"} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.Equal(t, *grok43, *pricing, model)
	}
}

func TestGrokBareAliasBillsTheRuntimeDefaultModel(t *testing.T) {
	original := xai.RuntimeModelMappingOptions()
	t.Cleanup(func() { xai.SetRuntimeModelMappingOptions(original) })

	svc := newTestBillingService()
	xai.SetRuntimeModelMappingOptions(xai.ModelMappingOptions{DefaultText: "grok-4.3"})
	for _, model := range []string{"grok", "grok-latest", "xai/grok"} {
		pricing, err := svc.GetModelPricing(model)
		require.NoError(t, err, model)
		require.InDelta(t, 1.25e-6, pricing.InputPricePerToken, 1e-15, model)
	}

	// A default outside the price table keeps the grok-4.6 card.
	xai.SetRuntimeModelMappingOptions(xai.ModelMappingOptions{DefaultText: "grok-9-preview"})
	pricing, err := svc.GetModelPricing("grok")
	require.NoError(t, err)
	require.InDelta(t, 2e-6, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 0.5e-6, pricing.CacheReadPricePerToken, 1e-15)
}

func TestResolveGrokServiceTierBilling(t *testing.T) {
	grok := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
	for _, tc := range []struct {
		name, requested, observed, billing string
		downgraded                         bool
	}{
		{name: "untiered", requested: "", observed: "default", billing: ""},
		{name: "default", requested: "default", observed: "default", billing: "default"},
		{name: "confirmed priority", requested: "priority", observed: "priority", billing: "priority"},
		{name: "confirmed fast", requested: "fast", observed: "priority", billing: "fast"},
		{name: "priority served at default", requested: "priority", observed: "default", billing: "default", downgraded: true},
		{name: "priority without a declaration", requested: "priority", observed: "", billing: "default", downgraded: true},
		{name: "flex has no xAI tier", requested: "flex", observed: "", billing: "default"},
		{name: "ultrafast has no xAI tier", requested: "ultrafast", observed: "priority", billing: "default", downgraded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolution := ResolveOpenAIServiceTierBilling(grok, tc.requested, tc.observed)
			require.Equal(t, tc.billing, resolution.Billing)
			require.Equal(t, tc.downgraded, resolution.Downgraded)
		})
	}

	flex := "flex"
	result := &OpenAIForwardResult{ServiceTier: &flex}
	ApplyOpenAIServiceTierBillingResolution(grok, result)
	require.NotNil(t, result.ServiceTier)
	require.Equal(t, "default", *result.ServiceTier, "an unsupported cheaper tier must not bill below standard")

	priority := "priority"
	confirmed := &OpenAIForwardResult{ServiceTier: &priority, UpstreamResponseServiceTier: "priority"}
	ApplyOpenAIServiceTierBillingResolution(grok, confirmed)
	require.Equal(t, "priority", *confirmed.ServiceTier)
}

func TestGrokPriorityProcessingDoublesEveryTier(t *testing.T) {
	svc := newTestBillingService()
	resolver := NewModelPricingResolver(nil, svc)
	group := &Group{LongContextPricingEnabled: true}
	for _, tokens := range []UsageTokens{
		{InputTokens: 100000, CacheReadTokens: 20000, OutputTokens: 1000},
		{InputTokens: 230000, CacheReadTokens: 20000, OutputTokens: 1000},
	} {
		standard, err := svc.CalculateCostUnified(CostInput{Model: "grok-4.7", Group: group, Tokens: tokens, RateMultiplier: 1, Resolver: resolver})
		require.NoError(t, err)
		priority, err := svc.CalculateCostUnified(CostInput{Model: "grok-4.7", Group: group, Tokens: tokens, RateMultiplier: 1, Resolver: resolver, ServiceTier: "priority"})
		require.NoError(t, err)
		require.Equal(t, standard.LongContextBillingApplied, priority.LongContextBillingApplied)
		require.InDelta(t, 2*standard.ActualCost, priority.ActualCost, 1e-9)
	}
}

func TestNormalizeGrokResponsesServiceTier(t *testing.T) {
	for raw, want := range map[string]string{
		`{"model":"grok-4.7"}`:                            `{"model":"grok-4.7"}`,
		`{"model":"grok-4.7","service_tier":"priority"}`:  `{"model":"grok-4.7","service_tier":"priority"}`,
		`{"model":"grok-4.7","service_tier":"fast"}`:      `{"model":"grok-4.7","service_tier":"priority"}`,
		`{"model":"grok-4.7","service_tier":" Fast "}`:    `{"model":"grok-4.7","service_tier":"priority"}`,
		`{"model":"grok-4.7","service_tier":"default"}`:   `{"model":"grok-4.7","service_tier":"default"}`,
		`{"model":"grok-4.7","service_tier":"flex"}`:      `{"model":"grok-4.7"}`,
		`{"model":"grok-4.7","service_tier":"ultrafast"}`: `{"model":"grok-4.7"}`,
		`{"model":"grok-4.7","service_tier":null}`:        `{"model":"grok-4.7"}`,
		`{"model":"grok-4.7","service_tier":1}`:           `{"model":"grok-4.7"}`,
	} {
		out, err := normalizeGrokResponsesServiceTier([]byte(raw))
		require.NoError(t, err, raw)
		require.JSONEq(t, want, string(out), raw)
	}
}

func TestGrokOAuthAccountsRouteTheFastModel(t *testing.T) {
	oauth := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "token"}}
	apiKey := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "key"}}

	require.True(t, oauth.IsModelSupported("grok-4.7-fast"))
	require.True(t, oauth.IsModelSupported("grok-4.7-build-fast"))
	require.Equal(t, "grok-4.7-fast", oauth.GetMappedModel("grok-4.7-build-fast"))
	require.True(t, oauth.IsModelSupported("grok-4.7"))

	require.False(t, apiKey.IsModelSupported("grok-4.7-fast"), "the public API does not serve Grok 4.7 Fast")
	require.True(t, apiKey.IsModelSupported("grok-4.7"))
}

func TestKnownFreeGrokAccountsDoNotRouteTheFastModel(t *testing.T) {
	grokOAuth := func(credentials map[string]any) *Account {
		credentials["access_token"] = "token"
		return &Account{Platform: PlatformGrok, Type: AccountTypeOAuth, Credentials: credentials}
	}

	free := grokOAuth(map[string]any{"subscription_tier": "free"})
	require.False(t, free.IsModelSupported("grok-4.7-fast"), "Grok Build's free tier does not include Grok 4.7 Fast")
	require.False(t, free.IsModelSupported("grok-4.7-build-fast"))
	require.False(t, free.IsModelSupported("xai/grok-4.7-fast"))
	require.True(t, free.IsModelSupported("grok-4.7"))
	require.True(t, free.IsModelSupported("grok-4.6"))

	// A custom route that lands on the Fast model is excluded as well.
	customFree := grokOAuth(map[string]any{
		"subscription_tier": "x_basic",
		"model_mapping":     map[string]any{"speedy": "grok-4.7-fast", "grok-4.6": "grok-4.6"},
	})
	require.False(t, customFree.IsModelSupported("speedy"))
	require.True(t, customFree.IsModelSupported("grok-4.6"))

	paid := grokOAuth(map[string]any{"subscription_tier": "supergrok"})
	require.True(t, paid.IsModelSupported("grok-4.7-fast"))

	// An unobserved plan is not proof of the free tier.
	unobserved := grokOAuth(map[string]any{})
	require.True(t, unobserved.IsModelSupported("grok-4.7-fast"))
}

func TestGrokAccountsOnThePublicAPIDoNotRouteTheFastModel(t *testing.T) {
	whitelist := func() map[string]any {
		return map[string]any{"grok-4.7": "grok-4.7", "grok-4.7-fast": "grok-4.7-fast"}
	}

	// The account form prefills the whole Grok catalog as a whitelist, so an
	// API-key account can list the Fast model; api.x.ai still never serves it.
	apiKey := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "key", "model_mapping": whitelist()}}
	require.False(t, apiKey.IsModelSupported("grok-4.7-fast"))
	require.True(t, apiKey.IsModelSupported("grok-4.7"))

	regional := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "key", "base_url": xai.DefaultEUWest1BaseURL, "model_mapping": whitelist()}}
	require.False(t, regional.IsModelSupported("grok-4.7-fast"))

	// A relay decides for itself what it serves.
	relay := &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "key", "base_url": "https://grok-relay.example.test/v1", "model_mapping": whitelist()}}
	require.True(t, relay.IsModelSupported("grok-4.7-fast"))

	// An OAuth account pinned to the public API cannot reach it either.
	pinnedOAuth := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "token", "base_url": xai.DefaultBaseURL}}
	require.False(t, pinnedOAuth.IsModelSupported("grok-4.7-fast"))
	require.True(t, pinnedOAuth.IsModelSupported("grok-4.7"))
}
