//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func perplexityCatalogTestAccount(syncedAt time.Time, models ...string) *Account {
	account := &Account{ID: 7, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	entries := make(map[string]UpstreamModelMetadata, len(models))
	for _, id := range models {
		entries[id] = UpstreamModelMetadata{ID: id}
	}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		Source: "perplexity", SyncedAt: syncedAt.UTC().Format(time.RFC3339),
		IdentityFingerprint: perplexityAccountFingerprint(account),
		Models:              entries,
	})
	return account
}

func TestPerplexityDeadModelAutoUpgradeGating(t *testing.T) {
	account := perplexityCatalogTestAccount(time.Now(), "gpt55", "gpt55_thinking", "turbo")
	require.Equal(t, "gpt55", upgradePerplexityDeadModelPreference(account, "gpt54", "gpt54"))
	require.Equal(t, "gpt55_thinking", upgradePerplexityDeadModelPreference(account, "gpt54_thinking", "gpt54_thinking"))
	require.Equal(t, "gpt55", upgradePerplexityDeadModelPreference(account, "gpt55", "gpt55"), "preference still in the catalog is never rewritten")
	require.Equal(t, "claude46sonnet", upgradePerplexityDeadModelPreference(account, "claude46sonnet", "claude46sonnet"), "no same-family candidate in the catalog")
	require.Equal(t, "turbo", upgradePerplexityDeadModelPreference(account, "sonar", "turbo"), "static aliases are never rewritten")
	require.Equal(t, "turbo", upgradePerplexityDeadModelPreference(account, "turbo", "turbo"))
	require.Equal(t, "pplx_pro", upgradePerplexityDeadModelPreference(account, "sonar-pro", "pplx_pro"))

	stale := perplexityCatalogTestAccount(time.Now().Add(-25*time.Hour), "gpt55")
	require.Equal(t, "gpt54", upgradePerplexityDeadModelPreference(stale, "gpt54", "gpt54"), "stale snapshot is not a rewrite source")

	future := perplexityCatalogTestAccount(time.Now().Add(time.Hour), "gpt55")
	require.Equal(t, "gpt54", upgradePerplexityDeadModelPreference(future, "gpt54", "gpt54"), "future-dated snapshot is not a rewrite source")

	foreign := perplexityCatalogTestAccount(time.Now(), "gpt55")
	foreign.Credentials["api_key"] = "rotated"
	require.Equal(t, "gpt54", upgradePerplexityDeadModelPreference(foreign, "gpt54", "gpt54"), "another identity's catalog is not a rewrite source")

	bare := &Account{ID: 9, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "fixture"}}
	require.Equal(t, "gpt54", upgradePerplexityDeadModelPreference(bare, "gpt54", "gpt54"), "missing snapshot is not a rewrite source")
	require.Equal(t, "gpt54", upgradePerplexityDeadModelPreference(nil, "gpt54", "gpt54"))
}

func TestPerplexityForwardAutoUpgradesDeadModelFromCatalog(t *testing.T) {
	svc, upstream, account := perplexityIngressService(t, func(int, *http.Request) *http.Response {
		return perplexityAttachmentResponse(200, perplexityModelAnswer("OK", "gpt55")+"event: end_of_stream\n\n")
	})
	account.Credentials["model_mapping"] = map[string]any{"public": "gpt54"}
	account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
		Source: "perplexity", SyncedAt: time.Now().UTC().Format(time.RFC3339),
		IdentityFingerprint: perplexityAccountFingerprint(account),
		Models:              map[string]UpstreamModelMetadata{"gpt55": {ID: "gpt55"}, "turbo": {ID: "turbo"}},
	})
	body := []byte(`{"model":"public","messages":[{"role":"user","content":"hello"}]}`)
	c, recorder := perplexityIngressContext(t, body)
	result, err := svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 1)
	require.Equal(t, "gpt55", gjson.GetBytes(upstream.bodies[0], "params.model_preference").String())
	require.Equal(t, "gpt55", result.UpstreamModel)
	require.Equal(t, "gpt55", recorder.Header().Get("X-Perplexity-Model-Preference"))
}

func TestPerplexityForwardKeepsCatalogListedAndAliasPreferences(t *testing.T) {
	for _, tc := range []struct {
		name, mapped, want string
	}{
		{"dead versioned preference upgrades", "gpt54", "gpt55"},
		{"alias target never upgrades", "sonar", "turbo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, upstream, account := perplexityIngressService(t, func(int, *http.Request) *http.Response {
				return perplexityAttachmentResponse(200, perplexityModelAnswer("OK", tc.want)+"event: end_of_stream\n\n")
			})
			account.Credentials["model_mapping"] = map[string]any{"public": tc.mapped}
			// The catalog lacks the mapped target on purpose: only same-family
			// upgrades may rewrite; the alias target turbo must pass through.
			account.SetUpstreamModelMetadataSnapshot(UpstreamModelMetadataSnapshot{
				Source: "perplexity", SyncedAt: time.Now().UTC().Format(time.RFC3339),
				IdentityFingerprint: perplexityAccountFingerprint(account),
				Models:              map[string]UpstreamModelMetadata{"gpt55": {ID: "gpt55"}},
			})
			body := []byte(`{"model":"public","messages":[{"role":"user","content":"hello"}]}`)
			c, _ := perplexityIngressContext(t, body)
			_, err := svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
			require.NoError(t, err)
			require.Len(t, upstream.bodies, 1)
			require.Equal(t, tc.want, gjson.GetBytes(upstream.bodies[0], "params.model_preference").String())
		})
	}
}
