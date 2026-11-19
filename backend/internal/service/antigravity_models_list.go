package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/util/urlvalidator"
	"golang.org/x/sync/singleflight"
)

const (
	// Align with OpenAI model discovery cache windows:
	// - within 1m (fresh): return immediately without upstream traffic
	// - 1m..5m (optimistic SWR): return cached list and refresh in background
	// - >5m (expired): wait for live upstream refresh via singleflight
	antigravityModelsFreshTTL      = 1 * time.Minute
	antigravityModelsCacheTTL      = 5 * time.Minute
	antigravityModelsRefreshWindow = 15 * time.Second
)

// antigravityModelsLoader, when set, replaces the live upstream fetch.
// Production leaves it nil. Tests use it to avoid calling Google.
type antigravityModelsLoader func(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error)

type antigravityModelsCacheEntry struct {
	accountID  int64
	models     []antigravity.ClaudeModel
	freshUntil time.Time
	expiresAt  time.Time
}

type antigravityModelsCacheStore struct {
	mu           sync.Mutex
	items        map[string]antigravityModelsCacheEntry
	group        singleflight.Group
	refreshGroup singleflight.Group
}

var antigravityModelsCache = &antigravityModelsCacheStore{
	items: map[string]antigravityModelsCacheEntry{},
}

// antigravityRawModelsCache caches raw upstream models per account (independent of model_mapping)
// so mapping edits and gateway discovery can re-project without hitting upstream again.
var antigravityRawModelsCache = &antigravityModelsCacheStore{
	items: map[string]antigravityModelsCacheEntry{},
}

type antigravityCacheLookupState int

const (
	antigravityCacheMiss antigravityCacheLookupState = iota
	antigravityCacheStale
	antigravityCacheFresh
)

func (c *antigravityModelsCacheStore) lookup(key string, now time.Time) ([]antigravity.ClaudeModel, antigravityCacheLookupState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || !now.Before(entry.expiresAt) {
		return nil, antigravityCacheMiss
	}
	models := cloneAntigravityClaudeModels(entry.models)
	if !entry.freshUntil.IsZero() && !now.Before(entry.freshUntil) {
		return models, antigravityCacheStale
	}
	return models, antigravityCacheFresh
}

func (c *antigravityModelsCacheStore) get(key string, now time.Time) ([]antigravity.ClaudeModel, bool) {
	models, state := c.lookup(key, now)
	return models, state != antigravityCacheMiss
}

func (c *antigravityModelsCacheStore) set(key string, accountID int64, models []antigravity.ClaudeModel, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = antigravityModelsCacheEntry{
		accountID:  accountID,
		models:     cloneAntigravityClaudeModels(models),
		freshUntil: now.Add(antigravityModelsFreshTTL),
		expiresAt:  now.Add(antigravityModelsCacheTTL),
	}
}

func (c *antigravityModelsCacheStore) deleteByAccountID(accountID int64) {
	if accountID <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, entry := range c.items {
		if entry.accountID == accountID {
			delete(c.items, k)
		}
	}
}

func cloneAntigravityClaudeModels(models []antigravity.ClaudeModel) []antigravity.ClaudeModel {
	if models == nil {
		return []antigravity.ClaudeModel{}
	}
	out := make([]antigravity.ClaudeModel, len(models))
	copy(out, models)
	return out
}

func antigravityRawModelsCacheKey(account *Account) string {
	if account == nil {
		return ""
	}
	h := sha256.New()
	var proxyID int64
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	_, _ = fmt.Fprintf(h, "%d\n%s\n%s\n%s\n%d\n",
		account.ID,
		account.Type,
		strings.TrimSpace(account.GetCredential("project_id")),
		strings.TrimSpace(account.GetCredential("base_url")),
		proxyID,
	)
	sum := sha256.Sum256([]byte(account.GetCredential("api_key")))
	_, _ = h.Write(sum[:])
	return hex.EncodeToString(h.Sum(nil))
}

// antigravityModelsCacheKey fingerprints the account without the access token.
// A token refresh must not miss the cache; a mapping, restriction mode, base URL, or key change must.
func antigravityModelsCacheKey(account *Account) string {
	h := sha256.New()
	_, _ = h.Write([]byte(antigravityRawModelsCacheKey(account)))
	_, _ = h.Write([]byte{'\n'})
	_, _ = h.Write([]byte(strings.TrimSpace(account.GetCredential(AntigravityModelRestrictionModeKey))))
	// Fingerprint the stored mapping only. GetModelMapping injects the
	// default catalog, which would make an empty mapping look custom.
	mapping := rawAntigravityModelMapping(account)
	keys := make([]string, 0, len(mapping))
	for alias, target := range mapping {
		keys = append(keys, alias+"\x00"+target)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = h.Write([]byte{'\n'})
		_, _ = h.Write([]byte(key))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FetchAntigravityAccountModels returns the account's public model aliases.
// Like OpenAI account discovery, fresh entries return immediately, optimistic stale
// entries trigger a background refresh, and live fetch failures fall back to any
// persisted account catalog snapshot before the static catalog without caching errors.
func (s *AccountTestService) FetchAntigravityAccountModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if s == nil || account == nil {
		return antigravity.DefaultModels(), nil
	}
	load := s.antigravityModelsLoad
	isCustomLoader := load != nil
	if load == nil {
		load = s.loadAntigravityUpstreamModels
	}
	models, err := fetchCachedAntigravityProjectedModels(ctx, account, load, !isCustomLoader)
	if err != nil {
		slog.Warn("antigravity model discovery failed; using fallback catalog", "account_id", account.ID, "error", err)
		if fallback, ok := fallbackAntigravityAccountModels(account); ok {
			return fallback, nil
		}
		return antigravity.DefaultModels(), nil
	}
	return models, nil
}

func fallbackAntigravityAccountModels(account *Account) ([]antigravity.ClaudeModel, bool) {
	if account == nil {
		return nil, false
	}
	if snapshotModels, ok := account.AntigravitySnapshotClaudeModels(); ok {
		return projectAntigravityAccountModels(snapshotModels, account), true
	}
	return nil, false
}

func fetchCachedAntigravityProjectedModels(
	ctx context.Context,
	account *Account,
	load antigravityModelsLoader,
	recordObserved bool,
) ([]antigravity.ClaudeModel, error) {
	if account == nil {
		return nil, errors.New("account is required")
	}
	now := time.Now()
	key := antigravityModelsCacheKey(account)
	if cached, state := antigravityModelsCache.lookup(key, now); state != antigravityCacheMiss {
		if state == antigravityCacheStale {
			accountCopy := *account
			go func() {
				refreshCtx, cancel := context.WithTimeout(context.Background(), antigravityModelsRefreshWindow)
				defer cancel()
				_, _, _ = antigravityModelsCache.refreshGroup.Do(key, func() (any, error) {
					loaded, loadErr := load(refreshCtx, &accountCopy)
					if loadErr != nil {
						return nil, loadErr
					}
					if loaded == nil {
						loaded = []antigravity.ClaudeModel{}
					}
					if recordObserved && accountCopy.ID > 0 {
						recordAntigravityObservedClaudeModels(accountCopy.ID, loaded)
						antigravityRawModelsCache.set(antigravityRawModelsCacheKey(&accountCopy), accountCopy.ID, loaded, time.Now())
					}
					projected := projectAntigravityAccountModels(loaded, &accountCopy)
					antigravityModelsCache.set(key, accountCopy.ID, projected, time.Now())
					return projected, nil
				})
			}()
		}
		return cached, nil
	}

	v, err, _ := antigravityModelsCache.group.Do(key, func() (any, error) {
		if cached, ok := antigravityModelsCache.get(key, time.Now()); ok {
			return cached, nil
		}
		// Reuse unexpired raw cache when only model_mapping changed on the same account.
		if recordObserved {
			if rawCached, ok := antigravityRawModelsCache.get(antigravityRawModelsCacheKey(account), time.Now()); ok {
				projected := projectAntigravityAccountModels(rawCached, account)
				antigravityModelsCache.set(key, account.ID, projected, time.Now())
				return cloneAntigravityClaudeModels(projected), nil
			}
		}
		loaded, loadErr := load(ctx, account)
		if loadErr != nil {
			return nil, loadErr
		}
		if loaded == nil {
			loaded = []antigravity.ClaudeModel{}
		}
		if recordObserved && account.ID > 0 {
			recordAntigravityObservedClaudeModels(account.ID, loaded)
			antigravityRawModelsCache.set(antigravityRawModelsCacheKey(account), account.ID, loaded, time.Now())
		}
		projected := projectAntigravityAccountModels(loaded, account)
		antigravityModelsCache.set(key, account.ID, projected, time.Now())
		return cloneAntigravityClaudeModels(projected), nil
	})
	if err != nil {
		return nil, err
	}
	models, ok := v.([]antigravity.ClaudeModel)
	if !ok || models == nil {
		return []antigravity.ClaudeModel{}, nil
	}
	return cloneAntigravityClaudeModels(models), nil
}

func invalidateAntigravityAccountModelsCache(account *Account, rawModels []antigravity.ClaudeModel) {
	if account == nil {
		return
	}
	if account.ID > 0 {
		antigravityModelsCache.deleteByAccountID(account.ID)
	}
	now := time.Now()
	if rawModels != nil {
		if account.ID > 0 {
			recordAntigravityObservedClaudeModels(account.ID, rawModels)
		}
		antigravityRawModelsCache.set(antigravityRawModelsCacheKey(account), account.ID, rawModels, now)
		projected := projectAntigravityAccountModels(rawModels, account)
		antigravityModelsCache.set(antigravityModelsCacheKey(account), account.ID, projected, now)
	}
}

// InvalidateAntigravityAccountModelsCacheByID evicts cached model projections for accountID
// so account mapping / restriction mode edits take effect immediately.
func InvalidateAntigravityAccountModelsCacheByID(accountID int64) {
	antigravityModelsCache.deleteByAccountID(accountID)
}

func (s *AccountTestService) loadAntigravityUpstreamModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	return loadAntigravityUpstreamModelsCommon(
		ctx,
		account,
		s.antigravityGatewayService,
		s.httpUpstream,
		s.cfg,
		s.validateUpstreamBaseURL,
		func(req *http.Request, proxyURL string, acc *Account) (*http.Response, error) {
			return s.doUpstreamModelsRequest(req, proxyURL, acc)
		},
	)
}

func loadAntigravityUpstreamModelsCommon(
	ctx context.Context,
	account *Account,
	gatewaySvc *AntigravityGatewayService,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
	validateBaseURL func(string) (string, error),
	doRelay func(*http.Request, string, *Account) (*http.Response, error),
) ([]antigravity.ClaudeModel, error) {
	if account == nil {
		return nil, errors.New("account is required")
	}
	// type=upstream keeps a third-party API key. It must use that key against
	// the account's own base URL. Treating it as OAuth sends the key to Google.
	if antigravityUsesRelayModelList(account) {
		return loadAntigravityRelayModelsWithDeps(ctx, account, gatewaySvc, httpUpstream, cfg, validateBaseURL, doRelay)
	}
	return loadAntigravityOAuthModelsWithDeps(ctx, account, gatewaySvc, cfg)
}

func loadAntigravityRelayModelsWithDeps(
	ctx context.Context,
	account *Account,
	gatewaySvc *AntigravityGatewayService,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
	validateBaseURL func(string) (string, error),
	doRelay func(*http.Request, string, *Account) (*http.Response, error),
) ([]antigravity.ClaudeModel, error) {
	if httpUpstream == nil && doRelay == nil {
		return nil, errors.New("upstream HTTP client is not configured")
	}
	req, err := buildAntigravityRelayModelsRequest(ctx, account, validateBaseURL)
	if err != nil {
		return nil, err
	}
	proxyURL, err := upstreamModelsProxyURL(account)
	if err != nil {
		return nil, err
	}
	var resp *http.Response
	if doRelay != nil {
		resp, err = doRelay(req, proxyURL, account)
	} else if tlsProfile := gatewaySvc.resolveTLSProfile(account); tlsProfile != nil {
		resp, err = httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, tlsProfile)
	} else {
		resp, err = httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	bodyLimit := resolveModelsListReadLimit(cfg)
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > bodyLimit {
		return nil, fmt.Errorf("antigravity model list exceeds %d bytes", bodyLimit)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("antigravity model list returned HTTP %d", resp.StatusCode)
	}
	return parseAntigravityAPIKeyModels(body)
}

func loadAntigravityOAuthModelsWithDeps(
	ctx context.Context,
	account *Account,
	gatewaySvc *AntigravityGatewayService,
	cfg *config.Config,
) ([]antigravity.ClaudeModel, error) {
	if gatewaySvc == nil || gatewaySvc.GetTokenProvider() == nil {
		return nil, errors.New("antigravity token provider is not configured")
	}
	accessToken, err := gatewaySvc.GetTokenProvider().GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("no antigravity access token is available")
	}
	proxyURL, err := upstreamModelsProxyURL(account)
	if err != nil {
		return nil, err
	}
	ctx = withAntigravityDevice(ctx, account)
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	client, err := antigravity.NewClientForAccount(proxyURL, accountID, gatewaySvc.resolveTLSProfile(account))
	if err != nil {
		return nil, err
	}
	if accountID <= 0 {
		defer client.CloseIdleConnections()
	}
	projectID := resolveAntigravityAccountProjectID(account)
	modelsURLs, fallbackOnAuthError := resolveAntigravityModelsBaseURLs(account)
	modelsResp, _, err := client.FetchAvailableModelsWithURLs(
		ctx,
		accessToken,
		projectID,
		resolveModelsListReadLimit(cfg),
		modelsURLs,
		fallbackOnAuthError,
	)
	if err != nil {
		return nil, err
	}
	return claudeModelsFromAvailable(modelsResp), nil
}

func (s *AntigravityGatewayService) validateRelayBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty base URL")
	}
	if s != nil && s.settingService != nil && s.settingService.cfg != nil && s.settingService.cfg.Security.URLAllowlist.Enabled {
		return urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
			AllowedHosts:     s.settingService.cfg.Security.URLAllowlist.UpstreamHosts,
			RequireAllowlist: true,
			AllowPrivate:     s.settingService.cfg.Security.URLAllowlist.AllowPrivateHosts,
		})
	}
	if s != nil && s.settingService != nil && s.settingService.cfg != nil && !s.settingService.cfg.Security.URLAllowlist.AllowInsecureHTTP {
		return urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
			RequireAllowlist: false,
			AllowPrivate:     s.settingService.cfg.Security.URLAllowlist.AllowPrivateHosts,
		})
	}
	return urlvalidator.ValidateURLFormat(raw, true)
}

func (s *AntigravityGatewayService) gatewayConfig() *config.Config {
	if s != nil && s.settingService != nil {
		return s.settingService.cfg
	}
	return nil
}

// FetchAccountModels discovers a single Antigravity account's live upstream models
// using the shared fresh/SWR singleflight cache and projects them through the account's mapping.
func (s *AntigravityGatewayService) FetchAccountModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if s == nil {
		return nil, errors.New("antigravity gateway service is not configured")
	}
	if account == nil {
		return nil, errors.New("account is required")
	}
	return fetchCachedAntigravityProjectedModels(ctx, account, func(fetchCtx context.Context, acc *Account) ([]antigravity.ClaudeModel, error) {
		return loadAntigravityUpstreamModelsCommon(
			fetchCtx,
			acc,
			s,
			s.httpUpstream,
			s.gatewayConfig(),
			s.validateRelayBaseURL,
			nil,
		)
	}, true)
}

// FetchGroupModels discovers upstream Antigravity models for a group (or the global pool
// when groupID is nil), mirroring OpenAI's scheduled upstream model discovery:
//   - selects schedulable Antigravity accounts ordered by priority and fails over on error
//   - merges public aliases from any sibling accounts with custom mappings or synced snapshots
//   - falls back to persisted account catalog snapshots when live upstream calls fail
func (s *AntigravityGatewayService) FetchGroupModels(
	ctx context.Context,
	groupID *int64,
	requireMixed bool,
	maxSwitches int,
) ([]antigravity.ClaudeModel, *Account, bool) {
	return s.fetchGroupModels(ctx, groupID, requireMixed, maxSwitches, false)
}

func mergeProjectedAntigravityModels(
	byID map[string]antigravity.ClaudeModel,
	acc *Account,
	items []antigravity.ClaudeModel,
	includeGeminiAliases bool,
) {
	raw := rawAntigravityModelMapping(acc)
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if includeGeminiAliases {
			if bare, ok := bareGeminiModelFromThinkingVariant(id); ok && antigravityAllowsBareGeminiAlias(acc, raw, bare) {
				bareDisplay := antigravity.BaseDisplayNameFromThinkingVariant(item.DisplayName, bare)
				if existing, exists := byID[bare]; !exists {
					byID[bare] = antigravity.ClaudeModel{ID: bare, Type: "model", DisplayName: bareDisplay, CreatedAt: item.CreatedAt}
				} else if (existing.DisplayName == "" || existing.DisplayName == bare) && bareDisplay != "" && bareDisplay != bare {
					existing.DisplayName = bareDisplay
					byID[bare] = existing
				}
			}
		}
		item.ID = id
		if existing, exists := byID[id]; exists {
			if (existing.DisplayName == "" || existing.DisplayName == id) && item.DisplayName != "" && item.DisplayName != id {
				byID[id] = item
			}
			continue
		}
		if strings.TrimSpace(item.DisplayName) == "" {
			item.DisplayName = id
		}
		if strings.TrimSpace(item.Type) == "" {
			item.Type = "model"
		}
		byID[id] = item
	}
}

func finalizeMergedAntigravityModels(byID map[string]antigravity.ClaudeModel) []antigravity.ClaudeModel {
	out := make([]antigravity.ClaudeModel, 0, len(byID))
	for _, model := range byID {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func antigravityClaudeModelsToGemini(claudeModels []antigravity.ClaudeModel) []gemini.Model {
	methods := []string{"generateContent", "streamGenerateContent"}
	byName := make(map[string]gemini.Model)
	addGemini := func(id, display string) {
		id = strings.TrimSpace(strings.TrimPrefix(id, "models/"))
		if !isAntigravityGeminiModel(id) || strings.Contains(id, "*") {
			return
		}
		name := "models/" + id
		if _, exists := byName[name]; exists {
			return
		}
		byName[name] = gemini.Model{
			Name:                       name,
			DisplayName:                strings.TrimSpace(display),
			SupportedGenerationMethods: methods,
		}
	}
	for _, m := range claudeModels {
		addGemini(m.ID, m.DisplayName)
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]gemini.Model, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

// FetchPinnedAntigravityModelsList fetches Antigravity models using only the
// accounts pinned on group.CodexModelsManifestConfig, ignoring scheduler
// priorities, load factors, and transient rate-limit/overload windows. All
// usable accounts are fetched concurrently; results are merged in config order.
func (s *AntigravityGatewayService) FetchPinnedAntigravityModelsList(
	ctx context.Context,
	group *Group,
	maxSwitches int,
	includeGeminiAliases bool,
) ([]antigravity.ClaudeModel, *Account, error) {
	if s == nil || s.accountRepo == nil || group == nil {
		return nil, nil, ErrNoPinnedCodexModelsAccounts
	}
	cfg := group.CodexModelsManifestConfig
	if group.Platform != PlatformAntigravity || !cfg.Enabled || len(cfg.AccountIDs) == 0 {
		return nil, nil, ErrNoPinnedCodexModelsAccounts
	}

	fallbackToScheduler := func(cause error) ([]antigravity.ClaudeModel, *Account, error) {
		if ctx.Err() == nil && cfg.FallbackToScheduler {
			if models, selected, ok := s.fetchGroupModels(ctx, &group.ID, false, maxSwitches, includeGeminiAliases); ok {
				return models, selected, nil
			}
		}
		return nil, nil, cause
	}

	members, err := s.accountRepo.ListByGroup(ctx, group.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("load pinned antigravity models accounts: %w", err)
	}
	memberByID := make(map[int64]Account, len(members))
	for _, member := range members {
		memberByID[member.ID] = member
	}

	usable := make([]Account, 0, len(cfg.AccountIDs))
	for _, id := range cfg.AccountIDs {
		member, ok := memberByID[id]
		if !ok || member.Platform != PlatformAntigravity {
			continue
		}
		if !isPinnedCodexModelsAccountUsable(&member) {
			continue
		}
		usable = append(usable, member)
	}
	if len(usable) == 0 {
		return fallbackToScheduler(ErrNoPinnedCodexModelsAccounts)
	}

	results := make([][]antigravity.ClaudeModel, len(usable))
	fetchErrs := make([]error, len(usable))
	var fetchGroup sync.WaitGroup
	for i := range usable {
		fetchGroup.Add(1)
		go func(idx int) {
			defer fetchGroup.Done()
			models, fetchErr := s.FetchAccountModels(ctx, &usable[idx])
			results[idx] = models
			fetchErrs[idx] = fetchErr
		}(i)
	}
	fetchGroup.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	byID := make(map[string]antigravity.ClaudeModel)
	var firstSuccess *Account
	failedIDs := make([]int64, 0)
	var lastErr error
	for i := range usable {
		if fetchErrs[i] != nil {
			failedIDs = append(failedIDs, usable[i].ID)
			lastErr = fetchErrs[i]
			continue
		}
		if firstSuccess == nil {
			firstSuccess = &usable[i]
		}
		mergeProjectedAntigravityModels(byID, &usable[i], results[i], includeGeminiAliases)
	}
	if firstSuccess == nil {
		if lastErr == nil || infraerrors.Code(lastErr) == http.StatusInternalServerError {
			lastErr = infraerrors.New(http.StatusBadGateway, "ANTIGRAVITY_MODELS_UPSTREAM_FAILED", "pinned model discovery accounts all failed")
		}
		return fallbackToScheduler(lastErr)
	}
	if len(failedIDs) > 0 {
		slog.Warn("antigravity_models_pinned_partial_failure", "group_id", group.ID, "failed_account_ids", failedIDs)
	}
	return finalizeMergedAntigravityModels(byID), firstSuccess, nil
}

// FetchPinnedAntigravityGeminiModels discovers Gemini-family models from
// pinned Antigravity accounts on the group.
func (s *AntigravityGatewayService) FetchPinnedAntigravityGeminiModels(
	ctx context.Context,
	group *Group,
	maxSwitches int,
) ([]gemini.Model, *Account, error) {
	claudeModels, selected, err := s.FetchPinnedAntigravityModelsList(ctx, group, maxSwitches, true)
	if err != nil {
		return nil, nil, err
	}
	return antigravityClaudeModelsToGemini(claudeModels), selected, nil
}

func (s *AntigravityGatewayService) fetchGroupModels(
	ctx context.Context,
	groupID *int64,
	requireMixed bool,
	maxSwitches int,
	includeGeminiAliases bool,
) ([]antigravity.ClaudeModel, *Account, bool) {
	if s == nil || s.accountRepo == nil {
		return nil, nil, false
	}
	var (
		accounts []Account
		err      error
	)
	if groupID != nil {
		accounts, err = s.accountRepo.ListSchedulableByGroupID(ctx, *groupID)
	} else {
		accounts, err = s.accountRepo.ListSchedulable(ctx)
	}
	if err != nil {
		return nil, nil, false
	}

	filterCandidates := func(list []Account) []Account {
		out := make([]Account, 0, len(list))
		for _, acc := range list {
			if acc.Platform != PlatformAntigravity {
				continue
			}
			if requireMixed && !acc.IsMixedSchedulingEnabled() {
				continue
			}
			out = append(out, acc)
		}
		return out
	}

	candidates := filterCandidates(accounts)
	if len(candidates) == 0 && groupID != nil {
		// 分组内账号全部处于临时限流/过载时，仍从活跃成员获取模型目录，避免退回静态列表。
		if members, listErr := s.accountRepo.ListByGroup(ctx, *groupID); listErr == nil {
			usable := make([]Account, 0, len(members))
			for _, member := range members {
				if isPinnedCodexModelsAccountUsable(&member) {
					usable = append(usable, member)
				}
			}
			candidates = filterCandidates(usable)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].ID < candidates[j].ID
	})

	if maxSwitches <= 0 {
		maxSwitches = 3
	}

	byID := make(map[string]antigravity.ClaudeModel)
	addModels := func(acc *Account, items []antigravity.ClaudeModel) {
		mergeProjectedAntigravityModels(byID, acc, items, includeGeminiAliases)
	}

	var primaryAccount *Account
	probedPaidTier := false
	probedUnrestricted := false
	attempts := 0
	for i := range candidates {
		if ctx.Err() != nil {
			break
		}
		acc := &candidates[i]
		// If primary already succeeded, normally rely on warm cache, persisted snapshots,
		// or explicit custom mappings to avoid fanout storms. However, if primary was a
		// non-paid or restricted account and a paid-tier / unrestricted sibling has no
		// warm cache yet, probe one such sibling (within maxSwitches) so newer Pro/Ultra
		// models (e.g. Gemini 4 series) are not hidden behind a lower-tier priority-1 account.
		if primaryAccount != nil {
			if cached, ok := antigravityModelsCache.get(antigravityModelsCacheKey(acc), time.Now()); ok {
				addModels(acc, cached)
				if accountHasAntigravityPaidTier(acc) {
					probedPaidTier = true
				}
				if len(rawAntigravityModelMapping(acc)) == 0 {
					probedUnrestricted = true
				}
				continue
			}
			needsSupplementalProbe := attempts < maxSwitches &&
				((!probedPaidTier && accountHasAntigravityPaidTier(acc)) ||
					(!probedUnrestricted && len(rawAntigravityModelMapping(acc)) == 0))
			if needsSupplementalProbe {
				attempts++
				if models, fetchErr := s.FetchAccountModels(ctx, acc); fetchErr == nil {
					addModels(acc, models)
					if accountHasAntigravityPaidTier(acc) {
						probedPaidTier = true
					}
					if len(rawAntigravityModelMapping(acc)) == 0 {
						probedUnrestricted = true
					}
					continue
				} else {
					slog.Warn("antigravity supplemental group model discovery failed", "account_id", acc.ID, "error", fetchErr)
				}
			}
			if snapModels, ok := fallbackAntigravityAccountModels(acc); ok {
				addModels(acc, snapModels)
				continue
			}
			for alias := range rawAntigravityModelMapping(acc) {
				alias = strings.TrimSpace(alias)
				if alias != "" && !strings.Contains(alias, "*") {
					addModels(acc, []antigravity.ClaudeModel{{ID: alias, Type: "model", DisplayName: alias}})
				}
			}
			continue
		}

		if attempts > maxSwitches {
			break
		}
		attempts++
		models, fetchErr := s.FetchAccountModels(ctx, acc)
		if fetchErr != nil {
			slog.Warn("antigravity group model discovery failover", "account_id", acc.ID, "error", fetchErr)
			continue
		}
		primaryAccount = acc
		if accountHasAntigravityPaidTier(acc) {
			probedPaidTier = true
		}
		if len(rawAntigravityModelMapping(acc)) == 0 {
			probedUnrestricted = true
		}
		addModels(acc, models)
	}

	if primaryAccount == nil {
		// All live upstream attempts failed: try persisted account snapshots or explicit custom mappings.
		hadSnapshot := false
		for i := range candidates {
			acc := &candidates[i]
			if snapModels, ok := fallbackAntigravityAccountModels(acc); ok {
				addModels(acc, snapModels)
				hadSnapshot = true
				continue
			}
			raw := rawAntigravityModelMapping(acc)
			if len(raw) > 0 {
				hadSnapshot = true
				for alias := range raw {
					alias = strings.TrimSpace(alias)
					if alias != "" && !strings.Contains(alias, "*") {
						addModels(acc, []antigravity.ClaudeModel{{ID: alias, Type: "model", DisplayName: alias}})
					}
				}
			}
		}
		if !hadSnapshot {
			return nil, nil, false
		}
	}

	return finalizeMergedAntigravityModels(byID), primaryAccount, true
}

// FetchGroupGeminiModels discovers Gemini-family models served by Antigravity accounts
// for /antigravity/v1beta/models and mixed Gemini /v1beta/models endpoints.
func (s *AntigravityGatewayService) FetchGroupGeminiModels(
	ctx context.Context,
	groupID *int64,
	requireMixed bool,
	maxSwitches int,
) ([]gemini.Model, bool) {
	claudeModels, _, ok := s.fetchGroupModels(ctx, groupID, requireMixed, maxSwitches, true)
	if !ok {
		return nil, false
	}
	return antigravityClaudeModelsToGemini(claudeModels), true
}

func parseAntigravityAPIKeyModels(body []byte) ([]antigravity.ClaudeModel, error) {
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Name        string `json:"name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Name        string `json:"name"`
			CreatedAt   string `json:"created_at"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	entries := payload.Data
	if len(entries) == 0 {
		entries = payload.Models
	}
	out := make([]antigravity.ClaudeModel, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.Name)
		}
		id = strings.TrimPrefix(id, "models/")
		if id == "" {
			continue
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          id,
			Type:        "model",
			DisplayName: display,
			CreatedAt:   entry.CreatedAt,
		})
	}
	return out, nil
}

func claudeModelsFromAvailable(resp *antigravity.FetchAvailableModelsResponse) []antigravity.ClaudeModel {
	if resp == nil || len(resp.Models) == 0 {
		return []antigravity.ClaudeModel{}
	}
	ids := make([]string, 0, len(resp.Models))
	for id := range resp.Models {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]antigravity.ClaudeModel, 0, len(ids))
	for _, id := range ids {
		info := resp.Models[id]
		display := strings.TrimSpace(info.DisplayName)
		if display == "" {
			display = strings.TrimSpace(id)
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          strings.TrimSpace(id),
			Type:        "model",
			DisplayName: display,
		})
	}
	return out
}

// projectAntigravityAccountModels publishes an account's public model list:
//   - Empty stored mapping: returns all upstream models plus split bare Gemini names
//     derived from thinking-suffixed upstream IDs (e.g. gemini-4-argon-high -> also
//     publishes gemini-4-argon so clients can select the model and thinking effort independently).
//   - Strict whitelist mode (model_restriction_mode="whitelist"): only publishes models
//     matched by raw model_mapping, resolving Gemini bare/thinking-tier targets against
//     available upstream thinking variants.
//   - Explicit mapping mode (model_restriction_mode="mapping"): publishes configured
//     aliases (whose targets exist upstream) alongside all live upstream models and their
//     split bare Gemini names so newly released models (such as Gemini 4) are never hidden.
//   - Legacy stored mapping without model_restriction_mode: respects custom restrictions,
//     while allowing newly released upstream Gemini models through when the legacy mapping
//     already covers Gemini models (consistent with Account.resolveModelMapping after
//     migration 254 removed default gemini-4-argon* keys from stored mappings).
func projectAntigravityAccountModels(upstream []antigravity.ClaudeModel, account *Account) []antigravity.ClaudeModel {
	if account == nil {
		return []antigravity.ClaudeModel{}
	}
	byID := make(map[string]antigravity.ClaudeModel, len(upstream))
	upstreamIDs := make([]string, 0, len(upstream))
	for _, model := range upstream {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := byID[id]; ok {
			continue
		}
		model.ID = id
		if strings.TrimSpace(model.Type) == "" {
			model.Type = "model"
		}
		if strings.TrimSpace(model.DisplayName) == "" {
			model.DisplayName = id
		}
		byID[id] = model
		upstreamIDs = append(upstreamIDs, id)
	}
	sort.Strings(upstreamIDs)

	raw := rawAntigravityModelMapping(account)
	mode := strings.TrimSpace(account.GetCredential(AntigravityModelRestrictionModeKey))
	if len(raw) == 0 {
		out := make([]antigravity.ClaudeModel, 0, len(upstreamIDs))
		seen := make(map[string]struct{}, len(upstreamIDs))
		for _, id := range upstreamIDs {
			seen[id] = struct{}{}
			out = append(out, byID[id])
		}
		out = appendSplitBareGeminiModels(out, seen, func(string) bool { return true })
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}

	strictWhitelist := account.antigravityWhitelistEnabled()
	isMappingOverlay := !strictWhitelist && mode == "mapping"
	legacyAllowsUpstreamGemini := !strictWhitelist && mode == "" && antigravityMappingHasGeminiEntry(raw)

	effectiveMapping := raw
	if isMappingOverlay || legacyAllowsUpstreamGemini {
		effectiveMapping = make(map[string]string, len(raw)+len(upstreamIDs))
		for k, v := range raw {
			effectiveMapping[k] = v
		}
		for _, id := range upstreamIDs {
			if !isMappingOverlay && !isAntigravityGeminiModel(id) {
				continue
			}
			if _, exists := effectiveMapping[id]; !exists && !mappingHasWildcardForModel(effectiveMapping, id) {
				effectiveMapping[id] = id
			}
		}
	}

	aliases := make([]string, 0, len(effectiveMapping))
	for alias := range effectiveMapping {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	candidates := make([]string, 0, len(upstreamIDs)+len(aliases))
	candidates = append(candidates, upstreamIDs...)
	candidates = append(candidates, aliases...)

	out := make([]antigravity.ClaudeModel, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		target, matched := resolveRequestedModelInMapping(effectiveMapping, id)
		target = strings.TrimSpace(target)
		if !matched {
			continue
		}
		src, matchedUpstreamID, available := findAntigravityUpstreamModelForTarget(byID, target)
		if !available {
			continue
		}
		seen[id] = struct{}{}
		display := strings.TrimSpace(src.DisplayName)
		if id != matchedUpstreamID {
			if bare, ok := bareGeminiModelFromThinkingVariant(matchedUpstreamID); ok && strings.EqualFold(bare, id) {
				display = antigravity.BaseDisplayNameFromThinkingVariant(src.DisplayName, id)
			} else {
				display = id
			}
		}
		if display == "" {
			display = id
		}
		modelType := src.Type
		if modelType == "" {
			modelType = "model"
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          id,
			Type:        modelType,
			DisplayName: display,
			CreatedAt:   src.CreatedAt,
		})
	}

	out = appendSplitBareGeminiModels(out, seen, func(bare string) bool {
		return antigravityAllowsBareGeminiAlias(account, effectiveMapping, bare)
	})

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func appendSplitBareGeminiModels(
	models []antigravity.ClaudeModel,
	seen map[string]struct{},
	allow func(bare string) bool,
) []antigravity.ClaudeModel {
	if len(models) == 0 {
		return models
	}
	extras := make([]antigravity.ClaudeModel, 0)
	for _, m := range models {
		bare, ok := bareGeminiModelFromThinkingVariant(m.ID)
		if !ok {
			continue
		}
		if _, exists := seen[bare]; exists {
			continue
		}
		if allow != nil && !allow(bare) {
			continue
		}
		seen[bare] = struct{}{}
		modelType := m.Type
		if modelType == "" {
			modelType = "model"
		}
		extras = append(extras, antigravity.ClaudeModel{
			ID:          bare,
			Type:        modelType,
			DisplayName: antigravity.BaseDisplayNameFromThinkingVariant(m.DisplayName, bare),
			CreatedAt:   m.CreatedAt,
		})
	}
	return append(models, extras...)
}

func findAntigravityUpstreamModelForTarget(
	byID map[string]antigravity.ClaudeModel,
	target string,
) (antigravity.ClaudeModel, string, bool) {
	target = strings.TrimSpace(strings.TrimPrefix(target, "models/"))
	if target == "" {
		return antigravity.ClaudeModel{}, "", false
	}
	if src, ok := byID[target]; ok {
		return src, target, true
	}
	if !isAntigravityGeminiModel(target) {
		return antigravity.ClaudeModel{}, "", false
	}
	base := target
	preferred := "high"
	if variant, ok := antigravity.SplitGeminiThinkingVariant(target); ok {
		base = variant.BaseModel
		if variant.ThinkingLevel != "" {
			preferred = variant.ThinkingLevel
		}
	}
	for _, level := range geminiThinkingVariantOrder(preferred) {
		candidate := base + "-" + level
		if src, ok := byID[candidate]; ok {
			return src, candidate, true
		}
	}
	if src, ok := byID[base]; ok {
		return src, base, true
	}
	return antigravity.ClaudeModel{}, "", false
}

func antigravityMappingHasGeminiEntry(mapping map[string]string) bool {
	for alias, target := range mapping {
		if isAntigravityGeminiModel(alias) || isAntigravityGeminiModel(target) {
			return true
		}
	}
	return false
}

func antigravityAllowsBareGeminiAlias(account *Account, mapping map[string]string, bare string) bool {
	if strings.TrimSpace(bare) == "" {
		return false
	}
	if len(mapping) == 0 {
		return true
	}
	if account != nil && account.antigravityWhitelistEnabled() {
		return mappingSupportsRequestedModel(mapping, bare)
	}
	if mappingSupportsRequestedModel(mapping, bare) {
		return true
	}
	// Non-whitelist account that already exposes at least one thinking variant for this base model.
	for alias := range mapping {
		if variantBase, ok := bareGeminiModelFromThinkingVariant(alias); ok && strings.EqualFold(variantBase, bare) {
			return true
		}
	}
	return false
}

func rawAntigravityModelMapping(account *Account) map[string]string {
	if account == nil || account.Credentials == nil {
		return nil
	}
	return stringMappingFromRaw(account.Credentials["model_mapping"])
}
