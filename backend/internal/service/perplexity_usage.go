package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PerplexityUsageSection distinguishes a failed refresh from the last successful
// observation. Stale retains data; error means that no last-good data is available.
type PerplexityUsageSection struct {
	Status      string     `json:"status"`
	FetchedAt   *time.Time `json:"fetched_at,omitempty"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// PerplexityUsage is the account's web-session profile, not model-call usage.
// FetchedAt is the last completely successful collection; each section carries
// its own last-good timestamp. AttemptedAt does not imply freshness or success.
type PerplexityUsage struct {
	Snapshot            map[string]any                    `json:"snapshot"`
	Sections            map[string]PerplexityUsageSection `json:"sections"`
	FetchedAt           *time.Time                        `json:"fetched_at,omitempty"`
	AttemptedAt         *time.Time                        `json:"attempted_at,omitempty"`
	SessionValid        *bool                             `json:"session_valid,omitempty"`
	IdentityFingerprint string                            `json:"-"`
	ObservedAtMs        int64                             `json:"-"`
}

type perplexityAccountInfoFetcher interface {
	FetchPerplexityAccountInfo(context.Context, *Account) map[string]any
}

type perplexityUsageCache struct {
	profile     *PerplexityUsage
	fingerprint string
	timestamp   time.Time
}

// SetPerplexityAccountInfoFetcher reuses the configured account-test sender. A
// missing fetcher is reported explicitly; there is no default/direct HTTP client.
func (s *AccountUsageService) SetPerplexityAccountInfoFetcher(fetcher perplexityAccountInfoFetcher) {
	if s != nil {
		s.perplexityInfoFetcher = fetcher
	}
}

var perplexityInfoSections = [...]string{"session", "credits", "rate_limits"}

func perplexityIdentityFingerprint(account *Account, proxyURL string) string {
	if account == nil {
		return ""
	}
	identity, err := json.Marshal([]any{account.ID, account.GetCredential("api_key"), account.GetCredential("csrf_token"), account.GetPerplexityBaseURL(), proxyURL})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(identity)
	return hex.EncodeToString(digest[:])
}

func perplexityAccountFingerprint(account *Account) string {
	if account == nil {
		return ""
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return ""
	}
	return perplexityIdentityFingerprint(account, proxyURL)
}

func perplexityProfileFromSnapshot(raw any) *PerplexityUsage {
	profile := &PerplexityUsage{
		Snapshot: make(map[string]any),
		Sections: make(map[string]PerplexityUsageSection),
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return profile
	}
	var snapshot map[string]json.RawMessage
	if json.Unmarshal(body, &snapshot) != nil {
		return profile
	}
	_ = json.Unmarshal(snapshot["fetched_at"], &profile.FetchedAt)
	_ = json.Unmarshal(snapshot["attempted_at"], &profile.AttemptedAt)
	_ = json.Unmarshal(snapshot["session_valid"], &profile.SessionValid)
	_ = json.Unmarshal(snapshot["sections"], &profile.Sections)
	_ = json.Unmarshal(snapshot["identity_fingerprint"], &profile.IdentityFingerprint)
	_ = json.Unmarshal(snapshot["observed_at_ms"], &profile.ObservedAtMs)
	if profile.Sections == nil {
		profile.Sections = make(map[string]PerplexityUsageSection)
	}
	for _, name := range perplexityInfoSections {
		var data map[string]any
		if json.Unmarshal(snapshot[name], &data) != nil || data == nil {
			continue
		}
		profile.Snapshot[name] = data
		if _, exists := profile.Sections[name]; !exists {
			// Legacy snapshots have only one timestamp. Preserve it as the best
			// known observation time without inventing a new successful refresh.
			profile.Sections[name] = PerplexityUsageSection{Status: "stale", FetchedAt: profile.FetchedAt}
		}
	}
	return profile
}

func (p *PerplexityUsage) persistedSnapshot() map[string]any {
	snapshot := make(map[string]any, len(p.Snapshot)+4)
	for name, data := range p.Snapshot {
		snapshot[name] = data
	}
	snapshot["sections"] = p.Sections
	if p.FetchedAt != nil {
		snapshot["fetched_at"] = p.FetchedAt.UTC().Format(time.RFC3339Nano)
	}
	if p.AttemptedAt != nil {
		snapshot["attempted_at"] = p.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
	if p.SessionValid != nil {
		snapshot["session_valid"] = *p.SessionValid
	}
	if p.IdentityFingerprint != "" {
		snapshot["identity_fingerprint"] = p.IdentityFingerprint
	}
	if p.ObservedAtMs > 0 {
		snapshot["observed_at_ms"] = p.ObservedAtMs
	}
	return snapshot
}

func (p *PerplexityUsage) failSection(name, message string, now time.Time) {
	section := p.Sections[name]
	section.Status = "error"
	if _, ok := p.Snapshot[name]; ok {
		section.Status = "stale"
	}
	section.AttemptedAt = &now
	section.Error = message
	p.Sections[name] = section
}

func perplexityUsageInfo(profile *PerplexityUsage) *UsageInfo {
	// Copy before returning: callers must not mutate shared cached maps.
	profile = perplexityProfileFromSnapshot(profile.persistedSnapshot())
	info := &UsageInfo{Source: "active", UpdatedAt: profile.FetchedAt, Perplexity: profile}
	var failures []string
	for _, name := range perplexityInfoSections {
		if section := profile.Sections[name]; section.Error != "" {
			failures = append(failures, name+": "+section.Error)
		}
	}
	if len(failures) > 0 {
		info.Error = strings.Join(failures, "; ")
		info.ErrorCode = "perplexity_profile_partial_failure"
	}
	if profile.SessionValid != nil && !*profile.SessionValid {
		info.NeedsReauth = true
		info.ErrorCode = "unauthenticated"
	}
	return info
}

func (s *AccountUsageService) getPerplexityUsage(ctx context.Context, account *Account, force bool) (*UsageInfo, error) {
	if account.Type != AccountTypeAPIKey {
		return nil, fmt.Errorf("perplexity usage requires a cookie-backed api-key account")
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, err
	}
	if s.cache == nil {
		return nil, fmt.Errorf("perplexity usage cache is not configured")
	}
	// Credentials and egress changes must not hit a previous identity's cache.
	fingerprint := perplexityIdentityFingerprint(account, proxyURL)
	load := func() *perplexityUsageCache {
		raw, ok := s.cache.perplexityCache.Load(account.ID)
		if !ok {
			return nil
		}
		cached, _ := raw.(*perplexityUsageCache)
		if cached == nil || cached.fingerprint != fingerprint {
			return nil
		}
		return cached
	}
	initial := load()
	fresh := func(cached *perplexityUsageCache) bool {
		// Compare cache entries, not clock ticks: two Windows time.Now calls may
		// have identical timestamps even though a force refresh is a new request.
		if cached == nil || (force && cached == initial) {
			return false
		}
		ttl := apiCacheTTL
		for _, section := range cached.profile.Sections {
			if section.Status != "success" {
				ttl = apiErrorCacheTTL
				break
			}
		}
		return time.Since(cached.timestamp) < ttl
	}
	if cached := load(); fresh(cached) {
		return perplexityUsageInfo(cached.profile), nil
	}
	result, err, _ := s.cache.perplexityFlight.Do(fingerprint, func() (any, error) {
		previous := load()
		if fresh(previous) {
			return previous.profile, nil
		}
		queryAccount := *account
		queryAccount.Extra = make(map[string]any, len(account.Extra)+1)
		for key, value := range account.Extra {
			queryAccount.Extra[key] = value
		}
		if previous != nil {
			queryAccount.Extra[perplexityQuotaSnapshotExtraKey] = previous.profile.persistedSnapshot()
		}
		var profile *PerplexityUsage
		if s.perplexityInfoFetcher == nil {
			profile = perplexityProfileFromSnapshot(queryAccount.Extra[perplexityQuotaSnapshotExtraKey])
			now := time.Now().UTC()
			profile.AttemptedAt = &now
			profile.SessionValid = nil
			for _, name := range perplexityInfoSections {
				profile.failSection(name, "perplexity account info fetcher is not configured", now)
			}
		} else {
			profile = perplexityProfileFromSnapshot(s.perplexityInfoFetcher.FetchPerplexityAccountInfo(ctx, &queryAccount))
		}
		s.cache.perplexityCache.Store(account.ID, &perplexityUsageCache{profile: profile, fingerprint: fingerprint, timestamp: time.Now()})
		return profile, nil
	})
	if err != nil {
		return nil, err
	}
	profile, ok := result.(*PerplexityUsage)
	if !ok || profile == nil {
		return nil, fmt.Errorf("perplexity usage fetch returned no profile")
	}
	return perplexityUsageInfo(profile), nil
}
