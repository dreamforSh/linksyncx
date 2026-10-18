package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const (
	codeBuddyCatalogExtraKey      = "codebuddy_catalog"
	codeBuddyCreditsExtraKey      = "codebuddy_credits"
	codeBuddyCreditsUsageExtraKey = "codebuddy_credits_usage"
	codeBuddyRequestUsageExtraKey = "codebuddy_request_usage"
	codeBuddyCreditPolicyKey      = "credit_policy"
	// codeBuddyCreditsSnapshotTTL is how long a billing probe stays
	// trustworthy; past it the cached balance is only a hint.
	codeBuddyCreditsSnapshotTTL = 10 * time.Minute
)

// IsCodeBuddy 标识腾讯 CodeBuddy / WorkBuddy 订阅账号。
func (a *Account) IsCodeBuddy() bool {
	return a != nil && a.Platform == PlatformCodeBuddy
}

func (a *Account) IsCodeBuddyOAuth() bool {
	return a.IsCodeBuddy() && a.Type == AccountTypeOAuth
}

func (a *Account) GetCodeBuddyAccessToken() string {
	if !a.IsCodeBuddy() {
		return ""
	}
	return strings.TrimSpace(a.GetCredential(codebuddy.KeyAccessToken))
}

func (a *Account) GetCodeBuddyRefreshToken() string {
	if !a.IsCodeBuddyOAuth() {
		return ""
	}
	return strings.TrimSpace(a.GetCredential(codebuddy.KeyRefreshToken))
}

// CodeBuddyCredentials parses the stored credential document.
func (a *Account) CodeBuddyCredentials() (codebuddy.Credentials, error) {
	if !a.IsCodeBuddy() {
		return codebuddy.Credentials{}, fmt.Errorf("not a codebuddy account")
	}
	return codebuddy.ParseCredentials(a.Credentials)
}

// CodeBuddyProfile returns the stored product profile (cn-cli / cn-work /
// intl-cli / intl-work), resolving it from the token when missing.
func (a *Account) CodeBuddyProfile() string {
	if !a.IsCodeBuddy() {
		return ""
	}
	if profile := a.GetCredential(codebuddy.KeyProfile); codebuddy.ValidProfile(profile) {
		return profile
	}
	if creds, err := a.CodeBuddyCredentials(); err == nil {
		return creds.Profile
	}
	return ""
}

func (a *Account) CodeBuddyCreditPolicy() string {
	if a == nil {
		return codebuddy.CreditPolicyAll
	}
	return codebuddy.NormalizeCreditPolicy(a.GetCredential(codeBuddyCreditPolicyKey))
}

// CodeBuddyCatalogSnapshot is the synced /v3/config catalog stored in Extra.
type CodeBuddyCatalogSnapshot struct {
	SyncedAt string                   `json:"synced_at"`
	Models   []codebuddy.CatalogModel `json:"models"`
}

func (a *Account) GetCodeBuddyCatalog() []codebuddy.CatalogModel {
	var snapshot CodeBuddyCatalogSnapshot
	if decodeCodeBuddyExtra(a, codeBuddyCatalogExtraKey, &snapshot) && len(snapshot.Models) > 0 {
		return snapshot.Models
	}
	return nil
}

func (a *Account) GetCodeBuddyCredits() *codebuddy.CreditsSnapshot {
	var snapshot codebuddy.CreditsSnapshot
	if !decodeCodeBuddyExtra(a, codeBuddyCreditsExtraKey, &snapshot) {
		return nil
	}
	return &snapshot
}

func (a *Account) GetCodeBuddyCreditsUsage() codebuddy.CreditsUsage {
	var usage codebuddy.CreditsUsage
	decodeCodeBuddyExtra(a, codeBuddyCreditsUsageExtraKey, &usage)
	return usage
}

func (a *Account) GetCodeBuddyRequestUsage() *codebuddy.RequestUsage {
	var usage codebuddy.RequestUsage
	if !decodeCodeBuddyExtra(a, codeBuddyRequestUsageExtraKey, &usage) {
		return nil
	}
	return &usage
}

// CodeBuddyCreditsStale reports whether the cached balance aged past the TTL.
func (a *Account) CodeBuddyCreditsStale() bool {
	snapshot := a.GetCodeBuddyCredits()
	if snapshot == nil || snapshot.FetchedAt <= 0 {
		return true
	}
	return time.Since(time.Unix(snapshot.FetchedAt, 0)) > codeBuddyCreditsSnapshotTTL
}

// decodeCodeBuddyExtra decodes an Extra entry that may be a typed struct
// (same-request write) or a generic map (JSONB read).
func decodeCodeBuddyExtra(account *Account, key string, out any) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	raw, ok := account.Extra[key]
	if !ok || raw == nil {
		return false
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return false
	}
	return json.Unmarshal(body, out) == nil
}

// CodeBuddyAvailableModels resolves the models an account advertises: the
// manual whitelist when set (never widened by the catalog), else the synced
// catalog, else the static defaults. The credit policy can only remove
// models, consistent with codeBuddyCreditPolicyDenial on the request path.
func (a *Account) CodeBuddyAvailableModels() []codebuddy.CatalogModel {
	catalog := a.GetCodeBuddyCatalog()
	models := codeBuddyWhitelistedModels(a, catalog)
	if models == nil {
		models = make([]codebuddy.CatalogModel, 0, len(catalog))
		for _, model := range catalog {
			model.ID = codebuddy.PublicModel(model.ID)
			models = append(models, model)
		}
		if len(models) == 0 {
			for _, id := range codebuddy.DefaultModelIDs {
				models = append(models, codebuddy.CatalogModel{ID: id, Name: id})
			}
			return models
		}
	}
	return codebuddy.FilterCatalog(models, a.CodeBuddyCreditPolicy())
}

func codeBuddyWhitelistedModels(a *Account, catalog []codebuddy.CatalogModel) []codebuddy.CatalogModel {
	mapping := a.GetModelMapping()
	if len(mapping) == 0 {
		return nil
	}
	names := make([]string, 0, len(mapping))
	for from := range mapping {
		from = strings.TrimSpace(from)
		if from == "" || strings.Contains(from, "*") {
			continue
		}
		names = append(names, from)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	profile := a.CodeBuddyProfile()
	out := make([]codebuddy.CatalogModel, 0, len(names))
	for _, name := range names {
		target := strings.TrimSpace(mapping[name])
		if target == "" {
			target = name
		}
		entry := codebuddy.CatalogModel{ID: name, Name: name}
		if model, ok := codebuddy.FindModel(catalog, codebuddy.UpstreamModel(target, profile)); ok {
			if model.Name != "" {
				entry.Name = model.Name
			}
			entry.Credits = model.Credits
			entry.CreditsValue = model.CreditsValue
		}
		out = append(out, entry)
	}
	return out
}

// CodeBuddyAvailableOpenAIModels renders the account's models for /v1/models.
func CodeBuddyAvailableOpenAIModels(account *Account) []openai.Model {
	if account == nil {
		return nil
	}
	return codeBuddyModelsAsOpenAI(account.CodeBuddyAvailableModels())
}

func codeBuddyModelsAsOpenAI(models []codebuddy.CatalogModel) []openai.Model {
	out := make([]openai.Model, 0, len(models))
	for _, model := range models {
		display := model.ID
		if model.Credits != "" {
			display = fmt.Sprintf("%s (%s)", model.ID, model.Credits)
		}
		out = append(out, openai.Model{
			ID:          model.ID,
			Object:      "model",
			Created:     1704067200,
			OwnedBy:     "codebuddy",
			Type:        "model",
			DisplayName: display,
		})
	}
	return out
}

// codeBuddyRequestCredits resolves what one upstream call costs. CodeBuddy
// bills per request against the catalog multiplier, not per token.
func codeBuddyRequestCredits(account *Account, upstreamModel string) (float64, bool) {
	if !account.IsCodeBuddy() {
		return 0, false
	}
	return codebuddy.ModelMultiplier(account.GetCodeBuddyCatalog(), upstreamModel)
}

// codeBuddyCreditPolicyDenial explains why a zero-credit-only account must
// not serve upstreamModel, or returns "". A model missing from a synced
// catalog fails closed: its cost is unknown.
func codeBuddyCreditPolicyDenial(account *Account, upstreamModel string) string {
	if !account.IsCodeBuddy() || account.CodeBuddyCreditPolicy() != codebuddy.CreditPolicyZeroOnly {
		return ""
	}
	catalog := account.GetCodeBuddyCatalog()
	if len(catalog) == 0 {
		return ""
	}
	model, found := codebuddy.FindModel(catalog, upstreamModel)
	if !found {
		return fmt.Sprintf("model %s is missing from this CodeBuddy account's synced catalog, so its credit cost is unknown and the zero-credit policy cannot admit it; sync the account's models or allow all models", codebuddy.PublicModel(upstreamModel))
	}
	if codebuddy.IsZeroCredit(model.Credits) {
		return ""
	}
	cost := strings.TrimSpace(model.Credits)
	if cost == "" {
		cost = "an unreported amount of"
	}
	return fmt.Sprintf("model %s costs %s credits and this CodeBuddy account is restricted to zero-credit models", codebuddy.PublicModel(upstreamModel), cost)
}

// CodeBuddyTokenCacheKey is shared by the token provider, refresher and cache
// invalidator.
func CodeBuddyTokenCacheKey(account *Account) string {
	if account == nil {
		return "codebuddy:account:0"
	}
	return fmt.Sprintf("codebuddy:account:%d", account.ID)
}

// codeBuddyTokenRefreshSkew: refresh within 10 minutes of expiry, matching
// the official clients' refresh margin.
const codeBuddyTokenRefreshSkew = 10 * time.Minute

func CodeBuddyProviderRefreshPolicy() ProviderRefreshPolicy {
	return ProviderRefreshPolicy{
		OnRefreshError: ProviderRefreshErrorReturn,
		OnLockHeld:     ProviderLockHeldWaitForCache,
		FailureTTL:     0,
	}
}
