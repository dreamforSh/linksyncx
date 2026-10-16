package service

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
)

const perplexityQuotaMaxAge = 15 * time.Minute

// perplexityExhaustedQuotaBucket only trusts the explicit counters verified in
// the web rate-limit contract. Unknown credit layouts, negative/unlimited
// values, stale data and failed refreshes are not evidence of exhaustion.
func perplexityExhaustedQuotaBucket(account *Account, preference, mode string, now time.Time) string {
	if account == nil || !account.IsPerplexity() || account.Type != AccountTypeAPIKey || mode == perplexity.ModeComputer {
		return ""
	}
	if preference == "turbo" && mode == perplexity.ModeConcise {
		return ""
	}
	bucket := "remaining_pro"
	if preference == "pplx_alpha" {
		bucket = "remaining_research"
	}
	snapshot, ok := account.Extra[perplexityQuotaSnapshotExtraKey].(map[string]any)
	if !ok {
		return ""
	}
	fingerprint, _ := snapshot["identity_fingerprint"].(string)
	if fingerprint == "" || fingerprint != perplexityAccountFingerprint(account) {
		return ""
	}
	var status string
	var fetchedAt *time.Time
	switch sections := snapshot["sections"].(type) {
	case map[string]PerplexityUsageSection:
		section := sections["rate_limits"]
		status, fetchedAt = section.Status, section.FetchedAt
	case map[string]any:
		section, _ := sections["rate_limits"].(map[string]any)
		status, _ = section["status"].(string)
		if value, ok := section["fetched_at"].(string); ok {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				fetchedAt = &parsed
			}
		}
	}
	if status != "success" || fetchedAt == nil || now.Before(*fetchedAt) || now.Sub(*fetchedAt) >= perplexityQuotaMaxAge {
		return ""
	}
	rateLimits, _ := snapshot["rate_limits"].(map[string]any)
	if perplexityQuotaIsZero(rateLimits[bucket]) {
		return bucket
	}
	return ""
}

func perplexityQuotaIsZero(value any) bool {
	switch value := value.(type) {
	case int:
		return value == 0
	case int64:
		return value == 0
	case float64:
		return value == 0
	case json.Number:
		count, err := value.Int64()
		return err == nil && count == 0
	default:
		return false
	}
}

func perplexityQuotaPauseReason(account *Account, requestedModel string) string {
	if account == nil || !account.IsPerplexity() || requestedModel == "" {
		return ""
	}
	preference, mode := perplexity.ResolveModelPreference(account.GetMappedModel(requestedModel))
	if bucket := perplexityExhaustedQuotaBucket(account, preference, mode, time.Now()); bucket != "" {
		return "perplexity_quota_" + bucket
	}
	return ""
}

func perplexityQuotaFailover(bucket string) *UpstreamFailoverError {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type": "rate_limit_error", "code": "insufficient_quota",
		"message": "perplexity quota is exhausted for " + bucket,
	}})
	return &UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests, ResponseBody: body,
		ResponseHeaders:        http.Header{"X-Perplexity-Quota-Bucket": []string{bucket}},
		RetryableOnSameAccount: false,
	}
}
