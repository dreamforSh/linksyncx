package service

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// fetchPerplexity bypasses the account-ID-only monitor cache. AccountUsageService
// already caches/singleflights by credential and egress identity; an outer cache
// would hide a changed Cookie or a now-unavailable assigned proxy.
func (f *ChannelMonitorQuotaFetcher) fetchPerplexity(ctx context.Context, accountID int64) *domain.MonitorQuotaSnapshot {
	now := time.Now()
	ctx, cancel := context.WithTimeout(ctx, monitorQuotaFetchTimeout)
	defer cancel()
	account, err := f.LoadAccount(ctx, accountID)
	if err != nil || account == nil {
		return quotaErrorSnapshot("perplexity", "linked account not found", now)
	}
	if account.Platform != PlatformPerplexity || monitorAccountQuotaCapability(account) != nil {
		return quotaErrorSnapshot("perplexity", "linked account cannot provide perplexity quota", now)
	}
	if _, err := account.ProxyURLForOutbound(); err != nil {
		return quotaErrorSnapshot("perplexity", "perplexity assigned proxy is unavailable", now)
	}
	return f.fetchUsage(ctx, account, now)
}

func perplexityMonitorQuota(usage *UsageInfo, now time.Time) *domain.MonitorQuotaSnapshot {
	snapshot := quotaErrorSnapshot("perplexity", "perplexity quota is unavailable or incomplete", now)
	if usage == nil || usage.Perplexity == nil {
		return snapshot
	}
	profile := usage.Perplexity
	if profile.SessionValid != nil && !*profile.SessionValid {
		snapshot.CredentialInvalid = true
		snapshot.Error = "perplexity session is unauthenticated or expired"
		return snapshot
	}
	if profile.SessionValid == nil || usage.Error != "" || usage.ErrorCode != "" {
		return snapshot
	}
	// Last-good data is not current evidence. Preserve the actual observation
	// time instead of stamping cached/stale values with the current attempt time.
	for _, name := range perplexityInfoSections {
		section, ok := profile.Sections[name]
		if !ok || section.Status != "success" || section.Error != "" || section.FetchedAt == nil || now.Before(*section.FetchedAt) || now.Sub(*section.FetchedAt) >= perplexityQuotaMaxAge {
			return snapshot
		}
		if section.FetchedAt.Before(snapshot.FetchedAt) {
			snapshot.FetchedAt = *section.FetchedAt
		}
	}
	rateLimits, _ := profile.Snapshot["rate_limits"].(map[string]any)
	for _, bucket := range []struct{ key, label string }{
		{"remaining_pro", "pro"}, {"remaining_research", "research"},
	} {
		if remaining, ok := perplexityRemainingCount(rateLimits[bucket.key]); ok {
			snapshot.Remaining = append(snapshot.Remaining, domain.MonitorQuotaRemaining{Label: bucket.label, Remaining: remaining})
		}
	}
	if len(snapshot.Remaining) == 0 {
		snapshot.Error = "perplexity quota layout is unknown; no verified remaining counters"
		return snapshot
	}
	if session, ok := profile.Snapshot["session"].(map[string]any); ok {
		tier, _ := session["subscription_tier"].(string)
		switch strings.ToLower(strings.TrimSpace(tier)) {
		case "free", "pro", "max", "enterprise":
			snapshot.PlanLevel = strings.ToLower(strings.TrimSpace(tier))
		}
	}
	snapshot.Success, snapshot.Error = true, ""
	return snapshot
}

func perplexityRemainingCount(value any) (float64, bool) {
	var count float64
	switch value := value.(type) {
	case int:
		count = float64(value)
	case int64:
		count = float64(value)
	case float64:
		count = value
	case json.Number:
		var err error
		count, err = value.Float64()
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return count, !math.IsNaN(count) && !math.IsInf(count, 0) && count >= 0 && math.Trunc(count) == count
}
