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
)

func perplexityQuotaTestAccount(t *testing.T, pro, research any) *Account {
	t.Helper()
	account := &Account{ID: 35, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true,
		Credentials: map[string]any{"api_key": "fake-session", "model_mapping": map[string]any{"paid": "gpt54", "research": "pplx_alpha", "free": "turbo", "computer": "gpt54:computer"}}}
	now := time.Now().Add(-time.Second)
	profile := perplexityProfileFromSnapshot(nil)
	profile.IdentityFingerprint = perplexityAccountFingerprint(account)
	profile.Snapshot["rate_limits"] = map[string]any{"remaining_pro": pro, "remaining_research": research}
	profile.Sections["rate_limits"] = PerplexityUsageSection{Status: "success", FetchedAt: &now, AttemptedAt: &now}
	account.Extra = map[string]any{perplexityQuotaSnapshotExtraKey: profile.persistedSnapshot()}
	return account
}

func TestPerplexityQuotaSeparatesModelsAndSchedulerPaths(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pro, research any
		model, reason string
	}{
		{"paid exhausted", 0, 9, "paid", "perplexity_quota_remaining_pro"},
		{"research still available", 0, 9, "research", ""},
		{"research exhausted", 9, 0, "research", "perplexity_quota_remaining_research"},
		{"paid still available", 9, 0, "paid", ""},
		{"free not charged to pro", 0, 0, "free", ""},
		{"computer not charged to search", 0, 0, "computer", ""},
		{"missing pro counter", nil, 0, "paid", ""},
		{"unlimited sentinel", -1, 0, "paid", ""},
		{"numeric string is unknown", "0", 0, "paid", ""},
		{"boolean is unknown", false, 0, "paid", ""},
		{"fraction is not zero", 0.5, 0, "paid", ""},
		{"JSON numeric zero", json.Number("0"), 0, "paid", "perplexity_quota_remaining_pro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := perplexityQuotaTestAccount(t, tc.pro, tc.research)
			require.Equal(t, tc.reason, perplexityQuotaPauseReason(account, tc.model))
			legacy := openAICompatibleAccountEligibilityFailureReason(context.Background(), account, PlatformPerplexity, tc.model, false, "")
			require.Equal(t, tc.reason, legacy)
			scheduler := &defaultOpenAIAccountScheduler{}
			eligible, reason := scheduler.isAccountRequestCompatibleReason(context.Background(), account, OpenAIAccountScheduleRequest{RequestedModel: tc.model, Platform: PlatformPerplexity})
			require.Equal(t, tc.reason == "", eligible)
			require.Equal(t, tc.reason, reason)
			// Exercise Ent/Redis's JSON-shaped sections as well as in-process maps.
			raw, err := json.Marshal(account.Extra)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &account.Extra))
			require.Equal(t, tc.reason, perplexityQuotaPauseReason(account, tc.model))
		})
	}
}

func TestPerplexityQuotaDoesNotTrustStaleOrForeignSnapshots(t *testing.T) {
	for _, change := range []string{"stale", "failed", "future", "missing_time", "credentials", "csrf", "base", "proxy", "legacy", "other_platform"} {
		t.Run(change, func(t *testing.T) {
			account := perplexityQuotaTestAccount(t, 0, 0)
			profile := perplexityProfileFromSnapshot(account.Extra[perplexityQuotaSnapshotExtraKey])
			section := profile.Sections["rate_limits"]
			switch change {
			case "stale":
				old := time.Now().Add(-perplexityQuotaMaxAge - time.Second)
				section.FetchedAt = &old
			case "failed":
				section.Status, section.Error = "stale", "upstream unavailable"
			case "future":
				future := time.Now().Add(time.Hour)
				section.FetchedAt = &future
			case "missing_time":
				section.FetchedAt = nil
			case "credentials":
				account.Credentials["api_key"] = "replaced"
			case "csrf":
				account.Credentials["csrf_token"] = "changed"
			case "base":
				account.Credentials["base_url"] = "https://another.example"
			case "proxy":
				id := int64(8)
				account.ProxyID = &id
			case "legacy":
				profile.IdentityFingerprint = ""
			case "other_platform":
				account.Platform = PlatformOpenAI
			}
			profile.Sections["rate_limits"] = section
			account.Extra[perplexityQuotaSnapshotExtraKey] = profile.persistedSnapshot()
			require.Empty(t, perplexityQuotaPauseReason(account, "paid"))
		})
	}
}

func TestPerplexityQuotaPreflightFailsOverWithoutUploadingOrAsking(t *testing.T) {
	svc, upstream, _ := perplexityIngressService(t, func(int, *http.Request) *http.Response {
		t.Fatal("exhausted account must not make an upstream request")
		return nil
	})
	account := perplexityQuotaTestAccount(t, 0, 2)
	body := []byte(`{"model":"paid","messages":[{"role":"user","content":"question"}]}`)
	c, _ := perplexityIngressContext(t, body)
	resp, err := svc.sendPerplexityCCUpstreamRequest(context.Background(), c, account, body, false, "gpt54", perplexity.DefaultRequestOptions())
	require.Nil(t, resp)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, http.StatusTooManyRequests, failover.StatusCode)
	require.Equal(t, "remaining_pro", failover.ResponseHeaders.Get("X-Perplexity-Quota-Bucket"))
	require.False(t, failover.RetryableOnSameAccount)
	require.Empty(t, upstream.requests)
	require.Nil(t, account.TempUnschedulableUntil, "model-scoped quota must not pause the entire account")
	require.Empty(t, perplexityQuotaPauseReason(account, "research"))
}
