//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func goodMonitorPerplexityUsage(now time.Time) *UsageInfo {
	valid := true
	profile := &PerplexityUsage{
		SessionValid: &valid, FetchedAt: &now,
		Snapshot: map[string]any{
			"session":     map[string]any{"subscription_tier": "pro", "email": "private@example.invalid"},
			"credits":     map[string]any{"balance": 4000},
			"rate_limits": map[string]any{"remaining_pro": float64(40), "remaining_research": float64(5)},
		},
		Sections: make(map[string]PerplexityUsageSection),
	}
	for _, name := range perplexityInfoSections {
		profile.Sections[name] = PerplexityUsageSection{Status: "success", FetchedAt: &now}
	}
	return &UsageInfo{Perplexity: profile}
}

func TestPerplexityMonitorQuotaHonestCountersAndTime(t *testing.T) {
	now := time.Now()
	observed := now.Add(-time.Minute)
	usage := goodMonitorPerplexityUsage(observed)
	snapshot := perplexityMonitorQuota(usage, now)
	require.True(t, snapshot.Success)
	require.Equal(t, observed, snapshot.FetchedAt)
	require.Equal(t, "pro", snapshot.PlanLevel)
	require.Len(t, snapshot.Remaining, 2)
	require.Equal(t, float64(40), snapshot.Remaining[0].Remaining)
	require.Empty(t, snapshot.Tiers, "unknown totals must not invent utilization")
	require.Nil(t, snapshot.Balance, "web credits are not a currency balance contract")
	body, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(body), "private@example.invalid")
	require.NotContains(t, string(body), "4000")
	require.Equal(t, MonitorStatusOperational, deriveQuotaCheckResult(snapshot, "quota", now).Status)
	usage.Perplexity.Snapshot["rate_limits"].(map[string]any)["remaining_pro"] = float64(0)
	require.Equal(t, MonitorStatusDegraded, deriveQuotaCheckResult(perplexityMonitorQuota(usage, now), "quota", now).Status)
}

func TestPerplexityMonitorQuotaRejectsUnknownStaleAndPartialEvidence(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		mutate     func(*UsageInfo)
		credential bool
	}{
		{"unknown layout", func(u *UsageInfo) {
			u.Perplexity.Snapshot["rate_limits"] = map[string]any{"search": map[string]any{"remaining": 5}}
		}, false},
		{"unknown session", func(u *UsageInfo) { u.Perplexity.SessionValid = nil }, false},
		{"expired session", func(u *UsageInfo) { valid := false; u.Perplexity.SessionValid = &valid }, true},
		{"403", func(u *UsageInfo) { u.Error = "session forbidden HTTP 403" }, false},
		{"value channel error", func(u *UsageInfo) { u.ErrorCode = "perplexity_profile_partial_failure" }, false},
		{"missing section", func(u *UsageInfo) { delete(u.Perplexity.Sections, "credits") }, false},
		{"stale", func(u *UsageInfo) {
			s := u.Perplexity.Sections["rate_limits"]
			s.Status = "stale"
			u.Perplexity.Sections["rate_limits"] = s
		}, false},
		{"old", func(u *UsageInfo) {
			old := now.Add(-perplexityQuotaMaxAge)
			s := u.Perplexity.Sections["rate_limits"]
			s.FetchedAt = &old
			u.Perplexity.Sections["rate_limits"] = s
		}, false},
		{"future", func(u *UsageInfo) {
			future := now.Add(time.Hour)
			s := u.Perplexity.Sections["rate_limits"]
			s.FetchedAt = &future
			u.Perplexity.Sections["rate_limits"] = s
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usage := goodMonitorPerplexityUsage(now)
			tc.mutate(usage)
			snapshot := perplexityMonitorQuota(usage, now)
			require.False(t, snapshot.Success)
			require.Empty(t, snapshot.Remaining)
			require.Equal(t, tc.credential, snapshot.CredentialInvalid)
		})
	}
	for _, unknown := range []any{-1, "0", true, nil, math.NaN(), math.Inf(1), 0.5, json.Number("invalid")} {
		_, ok := perplexityRemainingCount(unknown)
		require.False(t, ok, "unknown counter %v", unknown)
	}
}

func TestPerplexityMonitorQuotaBypassesIdentityBlindCache(t *testing.T) {
	f, source, _, _, accounts := newQuotaFetcherTestSetup(t)
	account := &Account{ID: 72, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "cookie-one"}}
	accounts.accounts[72] = account
	source.usage = goodMonitorPerplexityUsage(time.Now())
	f.storeSnapshot(72, quotaErrorSnapshot("usage", "cached-old-identity", time.Now()), time.Now().Add(time.Hour))
	require.True(t, f.fetchPerplexity(context.Background(), 72).Success)
	account.Credentials["api_key"] = "cookie-two"
	require.True(t, f.fetchPerplexity(context.Background(), 72).Success)
	require.Equal(t, 2, source.getCalls(), "delegate every read to identity-aware usage caching")
	proxyID := int64(4)
	account.ProxyID = &proxyID
	snapshot := f.fetchPerplexity(context.Background(), 72)
	require.False(t, snapshot.Success)
	require.Contains(t, snapshot.Error, "proxy")
	require.Equal(t, 2, source.getCalls())
	accounts.err = errors.New("missing")
	require.False(t, f.fetchPerplexity(context.Background(), 72).Success)
}
