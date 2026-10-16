//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPerplexityThinkingIntentAndStrengthHonesty(t *testing.T) {
	for _, body := range []string{
		`{"thinking":true}`, `{"thinking":{"type":"enabled"}}`, `{"reasoning_effort":"high"}`,
		`{"reasoning":{"effort":"low"}}`, `{"output_config":{"effort":"max"}}`,
	} {
		opts := perplexity.DefaultRequestOptions()
		require.NoError(t, validatePerplexityRequest([]byte(body), "chat_completions", &opts))
		require.NotNil(t, opts.Internal.ThinkingEnabled)
		require.True(t, *opts.Internal.ThinkingEnabled)
	}
	for _, body := range []string{
		`{"thinking":true,"reasoning_effort":"none"}`, `{"thinking":"enabled"}`,
		`{"reasoning_effort":"invented"}`, `{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		`{"perplexity":{"max_query_chars":1000.5}}`,
	} {
		opts, err := resolvePerplexityRequestOptions(nil, []byte(body))
		if err == nil {
			err = validatePerplexityRequest([]byte(body), "chat_completions", &opts)
		}
		require.Error(t, err, body)
	}
	opts := perplexity.DefaultRequestOptions()
	opts.Local.StrictParameters = true
	require.NoError(t, validatePerplexityRequest([]byte(`{"thinking":true}`), "chat_completions", &opts))
	require.Error(t, validatePerplexityRequest([]byte(`{"reasoning_effort":"high"}`), "chat_completions", &opts))
}

func TestPerplexityThinkingCatalogIsBoundToCredentialGeneration(t *testing.T) {
	account := &Account{ID: 7, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		Source: "perplexity", SyncedAt: time.Now().UTC().Format(time.RFC3339), IdentityFingerprint: perplexityAccountFingerprint(account),
		Models: map[string]UpstreamModelMetadata{"future_model_thinking": {ID: "future_model_thinking"}},
	})
	enabled := true
	opts := perplexity.DefaultRequestOptions()
	opts.Internal.ThinkingEnabled = &enabled
	selected, err := resolvePerplexityThinkingPreference(account, "future_model", opts)
	require.NoError(t, err)
	require.Equal(t, "future_model_thinking", selected)
	account.Credentials["api_key"] = "replacement"
	_, err = resolvePerplexityThinkingPreference(account, "future_model", opts)
	require.Error(t, err)
}

func TestPerplexityThinkingSelectionSurvivesAllIngressAdapters(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			svc, upstream, account := perplexityIngressService(t, func(int, *http.Request) *http.Response {
				return perplexityAttachmentResponse(200, perplexityModelAnswer("OK", "gpt54")+"event: end_of_stream\n\n")
			})
			account.Credentials["model_mapping"] = map[string]any{"public": "gpt54"}
			request := map[string]any{"model": "public", "thinking": true}
			if protocol == "responses" {
				request["input"] = "hello"
				delete(request, "thinking")
				request["reasoning"] = map[string]any{"effort": "high"}
			} else {
				request["messages"] = []any{map[string]any{"role": "user", "content": "hello"}}
				if protocol == "messages" {
					request["max_tokens"] = 64
					request["thinking"] = map[string]any{"type": "enabled"}
				}
			}
			body, err := json.Marshal(request)
			require.NoError(t, err)
			c, rec := perplexityIngressContext(t, body)
			var result *OpenAIForwardResult
			switch protocol {
			case "chat":
				result, err = svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
			case "responses":
				result, err = svc.forwardPerplexityResponses(context.Background(), c, account, body, "public", false, time.Now())
			case "messages":
				result, err = svc.forwardPerplexityMessages(context.Background(), c, account, body, "")
			}
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, "gpt54_thinking", gjson.GetBytes(upstream.bodies[0], "params.model_preference").String())
			require.Equal(t, "gpt54_thinking", result.UpstreamModel)
			require.Equal(t, "enabled", rec.Header().Get("X-Perplexity-Thinking-Selection"))
			if protocol == "responses" {
				require.Contains(t, rec.Header().Get("X-Perplexity-Ignored-Parameters"), "reasoning.effort.strength")
			}
		})
	}
}
