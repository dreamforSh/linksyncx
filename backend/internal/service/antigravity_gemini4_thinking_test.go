//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProjectAntigravityAccountModels_Gemini4SplitAndRestrictionModes(t *testing.T) {
	upstream := []antigravity.ClaudeModel{
		{ID: "claude-sonnet-4-6", Type: "model", DisplayName: "Claude Sonnet 4.6"},
		{ID: "gemini-3-flash", Type: "model", DisplayName: "Gemini 3 Flash"},
		{ID: "gemini-4-argon-high", Type: "model", DisplayName: "Gemini 4 Argon (High)"},
	}

	toMap := func(models []antigravity.ClaudeModel) map[string]string {
		m := make(map[string]string, len(models))
		for _, item := range models {
			m[item.ID] = item.DisplayName
		}
		return m
	}

	t.Run("empty mapping exposes both gemini-4-argon-high and split bare gemini-4-argon", func(t *testing.T) {
		acc := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
		got := toMap(projectAntigravityAccountModels(upstream, acc))
		require.Equal(t, "Gemini 4 Argon (High)", got["gemini-4-argon-high"])
		require.Equal(t, "Gemini 4 Argon", got["gemini-4-argon"])
		require.Equal(t, "Claude Sonnet 4.6", got["claude-sonnet-4-6"])
	})

	t.Run("legacy stored mapping without gemini-4-argon (post migration 254) still exposes Gemini 4", func(t *testing.T) {
		acc := &Account{
			ID:       2,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"model_mapping": map[string]any{
					"claude-sonnet-4-6": "claude-sonnet-4-6",
					"claude-haiku-4-5":  "claude-sonnet-4-6",
					"gemini-3-flash":    "gemini-3-flash",
				},
			},
		}
		got := toMap(projectAntigravityAccountModels(upstream, acc))
		require.Equal(t, "Gemini 4 Argon (High)", got["gemini-4-argon-high"])
		require.Equal(t, "Gemini 4 Argon", got["gemini-4-argon"])
		require.Equal(t, "claude-haiku-4-5", got["claude-haiku-4-5"])
	})

	t.Run("explicit mapping mode keeps custom aliases and exposes live Gemini 4", func(t *testing.T) {
		acc := &Account{
			ID:       3,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				AntigravityModelRestrictionModeKey: "mapping",
				"model_mapping": map[string]any{
					"claude-haiku-4-5": "claude-sonnet-4-6",
				},
			},
		}
		got := toMap(projectAntigravityAccountModels(upstream, acc))
		require.Equal(t, "Gemini 4 Argon (High)", got["gemini-4-argon-high"])
		require.Equal(t, "Gemini 4 Argon", got["gemini-4-argon"])
		require.Equal(t, "claude-haiku-4-5", got["claude-haiku-4-5"])
	})

	t.Run("strict whitelist hides unwhitelisted models but matches bare gemini-4-argon via upstream high variant", func(t *testing.T) {
		accOnlyClaude := &Account{
			ID:       4,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				AntigravityModelRestrictionModeKey: "whitelist",
				"model_mapping": map[string]any{
					"claude-sonnet-4-6": "claude-sonnet-4-6",
				},
			},
		}
		gotClaude := toMap(projectAntigravityAccountModels(upstream, accOnlyClaude))
		require.Contains(t, gotClaude, "claude-sonnet-4-6")
		require.NotContains(t, gotClaude, "gemini-4-argon-high")
		require.NotContains(t, gotClaude, "gemini-4-argon")

		accBareArgon := &Account{
			ID:       5,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				AntigravityModelRestrictionModeKey: "whitelist",
				"model_mapping": map[string]any{
					"gemini-4-argon": "gemini-4-argon",
				},
			},
		}
		gotBare := toMap(projectAntigravityAccountModels(upstream, accBareArgon))
		require.Equal(t, "Gemini 4 Argon", gotBare["gemini-4-argon"])
		require.NotContains(t, gotBare, "gemini-4-argon-high")
	})

	t.Run("cache key differentiates model_restriction_mode", func(t *testing.T) {
		mapping := map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}
		accWhitelist := &Account{
			ID:       6,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				AntigravityModelRestrictionModeKey: "whitelist",
				"model_mapping":                    mapping,
			},
		}
		accMapping := &Account{
			ID:       6,
			Platform: PlatformAntigravity,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				AntigravityModelRestrictionModeKey: "mapping",
				"model_mapping":                    mapping,
			},
		}
		require.NotEqual(t, antigravityModelsCacheKey(accWhitelist), antigravityModelsCacheKey(accMapping))
	})
}

func TestAntigravityGatewayService_FetchGroupModels_ProbesPaidSiblingForGemini4(t *testing.T) {
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	groupID := int64(81)
	repo := &antigravityCatalogPersistRepoStub{
		byGroup: map[int64][]Account{
			groupID: {
				{
					ID:          11,
					Platform:    PlatformAntigravity,
					Type:        AccountTypeUpstream,
					Priority:    1,
					Status:      StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"base_url": "https://free.example", "api_key": "k1", "plan_type": "free"},
				},
				{
					ID:          12,
					Platform:    PlatformAntigravity,
					Type:        AccountTypeUpstream,
					Priority:    2,
					Status:      StatusActive,
					Schedulable: true,
					Credentials: map[string]any{"base_url": "https://pro.example", "api_key": "k2", "plan_type": "pro"},
				},
			},
		},
	}
	upstream := &antigravityPerAccountUpstream{
		responses: map[int64]*antigravityStaticJSONUpstream{
			11: {status: http.StatusOK, body: `{"data":[{"id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6"}]}`},
			12: {status: http.StatusOK, body: `{"data":[{"id":"claude-sonnet-4-6","display_name":"Claude Sonnet 4.6"},{"id":"gemini-4-argon-high","display_name":"Gemini 4 Argon (High)"}]}`},
		},
	}
	gw := &AntigravityGatewayService{accountRepo: repo, httpUpstream: upstream}

	models, selected, ok := gw.FetchGroupModels(context.Background(), &groupID, false, 3)
	require.True(t, ok)
	require.NotNil(t, selected)
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	require.Contains(t, ids, "gemini-4-argon-high")
	require.Contains(t, ids, "gemini-4-argon")
	require.Contains(t, ids, "claude-sonnet-4-6")
}

func TestAntigravityGemini4ThinkingSplitAndAdaptationAcrossProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	newObservedArgonAccount := func(id int64) *Account {
		acc := &Account{
			ID:          id,
			Name:        "ag-gemini4",
			Platform:    PlatformAntigravity,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Concurrency: 1,
			Credentials: map[string]any{
				"access_token": "tok",
				"project_id":   "proj-argon",
			},
		}
		recordAntigravityObservedModelIDs(id, []string{"gemini-4-argon-high", "claude-sonnet-4-6"})
		return acc
	}

	t.Run("chat completions bare gemini-4-argon with reasoning_effort=low routes to gemini-4-argon-high and sets thinkingLevel=low", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"model":"gemini-4-argon","reasoning_effort":"low","messages":[{"role":"user","content":"hi"}]}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

		res, err := svc.ForwardAsChatCompletions(context.Background(), c, newObservedArgonAccount(7101), body, nil)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, upstream.requestBodies, 1)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "low", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.True(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool())
		require.False(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingBudget").Exists())
	})

	t.Run("responses bare gemini-4-argon with reasoning.effort=medium routes to gemini-4-argon-high and sets thinkingLevel=medium", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"model":"gemini-4-argon","reasoning":{"effort":"medium"},"input":"hi"}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/responses", body)

		res, err := svc.ForwardAsResponses(context.Background(), c, newObservedArgonAccount(7102), body, nil)
		require.NoError(t, err)
		require.NotNil(t, res)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, upstream.requestBodies, 1)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "medium", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.True(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool())
	})

	t.Run("chat completions gemini-4-argon with reasoning_effort=none sets minimal without thoughts", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"model":"gemini-4-argon","reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

		_, err := svc.ForwardAsChatCompletions(context.Background(), c, newObservedArgonAccount(7103), body, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "minimal", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.False(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool())
	})

	t.Run("chat completions gemini-4-argon-low falls back to observed gemini-4-argon-high with thinkingLevel=low", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"model":"gemini-4-argon-low","messages":[{"role":"user","content":"hi"}]}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/chat/completions", body)

		_, err := svc.ForwardAsChatCompletions(context.Background(), c, newObservedArgonAccount(7104), body, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "low", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.True(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool())
	})

	t.Run("claude messages gemini-4-argon with output_config.effort=medium adapts thinkingLevel on gemini-4-argon-high", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"model":"gemini-4-argon","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"messages":[{"role":"user","content":"hi"}]}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1/messages", body)

		_, err := svc.Forward(context.Background(), c, newObservedArgonAccount(7105), body, false)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "medium", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.True(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool())
		require.False(t, gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingBudget").Exists())
	})

	t.Run("native v1beta gemini-4-argon-low redirected to observed gemini-4-argon-high injects thinkingLevel=low", func(t *testing.T) {
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{antigravityCompatSuccessResponse()}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
		c, rec := newAntigravityCompatContext(http.MethodPost, "/v1beta/models/gemini-4-argon-low:generateContent", body)

		_, err := svc.ForwardGemini(context.Background(), c, newObservedArgonAccount(7106), "gemini-4-argon-low", "generateContent", false, body, false)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "low", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
	})

	t.Run("TestConnectionWithOptions adapts thinkingLevel and splits thinking from answer text", func(t *testing.T) {
		sse := "data: " + `{"response":{"candidates":[{"content":{"parts":[{"text":"step 1 reasoning","thought":true},{"text":"Hello!"}]}}]}}` + "\n\n"
		upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(sse)),
		}}}
		svc := newAntigravityCompatService(config.GatewayConfig{MaxLineSize: defaultMaxLineSize}, upstream)

		res, err := svc.TestConnectionWithOptions(context.Background(), newObservedArgonAccount(7107), "gemini-4-argon", "hi", "low")
		require.NoError(t, err)
		require.Equal(t, "gemini-4-argon-high", res.MappedModel)
		require.Equal(t, "step 1 reasoning", res.Thinking)
		require.Equal(t, "Hello!", res.Text)
		reqBody := upstream.requestBodies[0]
		require.Equal(t, "gemini-4-argon-high", gjson.GetBytes(reqBody, "model").String())
		require.Equal(t, "low", gjson.GetBytes(reqBody, "request.generationConfig.thinkingConfig.thinkingLevel").String())
		require.Equal(t, int64(antigravityThinkingTestMaxOutputTokens), gjson.GetBytes(reqBody, "request.generationConfig.maxOutputTokens").Int())
	})
}

func TestGemini4PricingAliasAndCodexManifestDescriptor(t *testing.T) {
	require.Equal(t, "gemini-4-argon", normalizeGeminiThinkingTierAlias("gemini-4-argon-high"))
	require.Equal(t, "gemini-4-argon", normalizeGeminiThinkingTierAlias("gemini-4-argon-low"))
	require.Equal(t, "gemini-4-argon", normalizeGeminiThinkingTierAlias("gemini-4-argon-medium"))

	desc := newConfiguredCodexModelDescriptor("gemini-4-argon")
	require.NotNil(t, desc.DefaultReasoningLevel)
	require.Equal(t, "high", *desc.DefaultReasoningLevel)
	efforts := make([]string, 0, len(desc.SupportedReasoningLevels))
	for _, lvl := range desc.SupportedReasoningLevels {
		efforts = append(efforts, lvl.Effort)
	}
	require.Equal(t, []string{"minimal", "low", "medium", "high"}, efforts)

	lowDesc := newConfiguredCodexModelDescriptor("gemini-4-argon-low")
	require.NotNil(t, lowDesc.DefaultReasoningLevel)
	require.Equal(t, "low", *lowDesc.DefaultReasoningLevel)

	raw, err := json.Marshal(desc)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"default_reasoning_level":"high"`)
}
