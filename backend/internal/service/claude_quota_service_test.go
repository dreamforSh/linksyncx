package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type claudeQuotaTestRepo struct {
	stubOpenAIAccountRepo
	mu           sync.Mutex
	extraUpdates []map[string]any
}

func (r *claudeQuotaTestRepo) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make(map[string]any, len(updates))
	for k, v := range updates {
		copied[k] = v
	}
	r.extraUpdates = append(r.extraUpdates, copied)
	for i := range r.accounts {
		if r.accounts[i].ID != id {
			continue
		}
		if r.accounts[i].Extra == nil {
			r.accounts[i].Extra = map[string]any{}
		}
		for k, v := range updates {
			r.accounts[i].Extra[k] = v
		}
	}
	return nil
}

func (r *claudeQuotaTestRepo) UpdateSessionWindowEnd(context.Context, int64, time.Time) error {
	return nil
}

func (r *claudeQuotaTestRepo) updatedKeys() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	merged := map[string]any{}
	for _, update := range r.extraUpdates {
		for k, v := range update {
			merged[k] = v
		}
	}
	return merged
}

type claudeQuotaUsageLogRepo struct {
	UsageLogRepository
}

func (claudeQuotaUsageLogRepo) GetAccountWindowStats(context.Context, int64, time.Time) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{}, nil
}

type claudeQuotaFakeFetcher struct {
	mu      sync.Mutex
	queries []string
	body    string
	err     error
}

func (f *claudeQuotaFakeFetcher) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*ClaudeUsageResponse, error) {
	return f.FetchUsageWithOptions(ctx, &ClaudeUsageFetchOptions{AccessToken: accessToken, ProxyURL: proxyURL})
}

func (f *claudeQuotaFakeFetcher) FetchUsageWithOptions(_ context.Context, opts *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	f.mu.Lock()
	f.queries = append(f.queries, opts.Query)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	var resp ClaudeUsageResponse
	if err := json.Unmarshal([]byte(f.body), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type claudeQuotaFakeAPI struct {
	mu           sync.Mutex
	profileBody  string
	profileErr   error
	profileCalls int
	profileSeen  chan struct{}
	claims       []ClaudeRateLimitResetRequest
	claimOrgs    []string
	claimBody    string
	claimErr     error
}

func (a *claudeQuotaFakeAPI) FetchProfile(context.Context, *ClaudeUsageFetchOptions) ([]byte, error) {
	a.mu.Lock()
	a.profileCalls++
	a.mu.Unlock()
	if a.profileSeen != nil {
		a.profileSeen <- struct{}{}
	}
	if a.profileErr != nil {
		return nil, a.profileErr
	}
	return []byte(a.profileBody), nil
}

func (a *claudeQuotaFakeAPI) ClaimRateLimitReset(_ context.Context, _ *ClaudeUsageFetchOptions, orgUUID string, claim *ClaudeRateLimitResetRequest) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.claims = append(a.claims, *claim)
	a.claimOrgs = append(a.claimOrgs, orgUUID)
	if a.claimErr != nil {
		return nil, a.claimErr
	}
	return []byte(a.claimBody), nil
}

type staticClaudeToken string

func (t staticClaudeToken) GetAccessToken(context.Context, *Account) (string, error) {
	return string(t), nil
}

const claudeMax20xProfile = `{"account":{"uuid":"acc-1"},"organization":{"uuid":"org-from-profile","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`

func claudeQuotaTestAccount(mutate func(*Account)) Account {
	account := Account{
		ID:       41,
		Name:     "claude-max",
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Extra:    map[string]any{"org_uuid": "org-1"},
	}
	if mutate != nil {
		mutate(&account)
	}
	return account
}

func newClaudeQuotaTestService(account Account, fetcher *claudeQuotaFakeFetcher, api *claudeQuotaFakeAPI) (*ClaudeQuotaService, *claudeQuotaTestRepo) {
	repo := &claudeQuotaTestRepo{stubOpenAIAccountRepo: stubOpenAIAccountRepo{accounts: []Account{account}}}
	usage := &AccountUsageService{
		accountRepo:  repo,
		usageLogRepo: claudeQuotaUsageLogRepo{},
		usageFetcher: fetcher,
		cache:        NewUsageCache(),
	}
	svc := NewClaudeQuotaService(repo, usage, api, nil)
	svc.tokenProvider = staticClaudeToken("token-1")
	return svc, repo
}

func claudeUsageBody(cedarEmber, juniperTide string) string {
	resetAt := time.Now().Add(3 * 24 * time.Hour).Unix()
	body := `{
		"five_hour": {"utilization": 35, "resets_at": "` + time.Now().Add(2*time.Hour).UTC().Format(time.RFC3339) + `"},
		"seven_day": {"utilization": 61, "resets_at": "` + time.Now().Add(4*24*time.Hour).UTC().Format(time.RFC3339) + `"},
		"limits": [{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":42,"resets_at":` + jsonInt(resetAt) + `}]`
	if cedarEmber != "" {
		body += `, "cedar_ember": ` + cedarEmber
	}
	if juniperTide != "" {
		body += `, "juniper_tide": ` + juniperTide
	}
	return body + `}`
}

func cedarEmberBlock(grant string) string {
	return `{"eligible": true, "at_limit": false, "exhausted": [], "next_grant_id": "grant_1", "grants": [` + grant + `]}`
}

func usableCedarGrant(resetsLeft int) string {
	return `{"id":"grant_1","label":"launch","resets_total":1,"resets_left":` + jsonInt(int64(resetsLeft)) +
		`,"ends_at":"` + time.Now().Add(20*24*time.Hour).UTC().Format(time.RFC3339) +
		`","clears":["five_hour","seven_day"],"usable_now":true,"use_requires_limit":false}`
}

const claimableJuniper = `{"eligible": true, "in_experiment": true, "arm": "reset", "available": true, "resets_per_week": 1}`

func TestClaudeQuotaRefresh_PersistsSnapshotSubscriptionAndUsage(t *testing.T) {
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), "")}
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile}
	svc, repo := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), fetcher, api)

	result, err := svc.RefreshQuota(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, []string{claudeUsageQueryCedarEmber}, fetcher.queries, "accounts below the limit only read free resets")
	require.True(t, result.CachePersisted)

	require.NotNil(t, result.Snapshot)
	require.False(t, result.Snapshot.AtWall)
	require.Equal(t, 1, result.Snapshot.AvailableCount)
	require.Len(t, result.Snapshot.CedarEmber.Grants, 1)
	require.True(t, result.Snapshot.CedarEmber.Grants[0].Next)
	require.Nil(t, result.Snapshot.JuniperTide)

	require.NotNil(t, result.Subscription)
	require.Equal(t, ClaudePlanMax20x, result.Subscription.PlanType)
	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.SevenDayFable)
	require.InDelta(t, 42, result.Usage.SevenDayFable.Utilization, 1e-9)
	require.Equal(t, "Max 20x", result.Usage.SubscriptionTier)

	updated := repo.updatedKeys()
	require.Contains(t, updated, claudeResetSnapshotExtraKey)
	require.Contains(t, updated, claudeSubscriptionExtraKey)
	require.InDelta(t, 0.42, updated["passive_usage_7d_oi_utilization"], 1e-9, "the fable window is synced to the passive cache")
	require.NotContains(t, updated, "org_uuid", "a known organization is left alone")

	// grant id 不落盘
	snapshotJSON, err := json.Marshal(updated[claudeResetSnapshotExtraKey])
	require.NoError(t, err)
	require.NotContains(t, string(snapshotJSON), "grant_1")
}

func TestClaudeQuotaRefresh_AtSessionWallReadsWeeklyReset(t *testing.T) {
	rateLimitedUntil := time.Now().Add(90 * time.Minute)
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), claimableJuniper)}
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile}
	svc, _ := newClaudeQuotaTestService(claudeQuotaTestAccount(func(a *Account) {
		a.RateLimitResetAt = &rateLimitedUntil
	}), fetcher, api)

	result, err := svc.RefreshQuota(context.Background(), 41)
	require.NoError(t, err)
	require.Equal(t, []string{claudeUsageQueryAtWall}, fetcher.queries)
	require.True(t, result.Snapshot.AtWall)
	require.NotNil(t, result.Snapshot.JuniperTide)
	require.True(t, result.Snapshot.JuniperTide.claimable())
	require.Equal(t, 2, result.Snapshot.AvailableCount)
}

func TestClaudeQuotaRefresh_ProfileFailureKeepsResetStatus(t *testing.T) {
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), "")}
	api := &claudeQuotaFakeAPI{profileErr: errors.New("profile down")}
	svc, repo := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), fetcher, api)
	svc.usage.SetClaudeSubscriptionRefresher(nil)

	result, err := svc.RefreshQuota(context.Background(), 41)
	require.NoError(t, err)
	require.Nil(t, result.Subscription)
	require.Equal(t, 1, result.Snapshot.AvailableCount)
	require.Contains(t, repo.updatedKeys(), claudeResetSnapshotExtraKey)
}

func TestClaudeQuotaResetCredit_CedarEmberSendsGrantAndStableRequestID(t *testing.T) {
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), "")}
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile, claimBody: `{"result":"reset","resets_left":0,"cleared":["five_hour","seven_day"]}`}
	svc, repo := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), fetcher, api)

	svc.usage.cacheClaudeUsageResponse(41, &ClaudeUsageResponse{})
	result, err := svc.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.NoError(t, err)
	require.True(t, result.Consumed())
	require.Equal(t, []string{"five_hour", "seven_day"}, result.Cleared)
	require.NotNil(t, result.ResetsLeft)
	require.Zero(t, *result.ResetsLeft)

	require.Len(t, api.claims, 1)
	claim := api.claims[0]
	require.Equal(t, ClaudeResetProgramCedarEmber, claim.Program)
	require.Equal(t, "grant_1", claim.GrantID)
	require.Equal(t, claudeResetRequestID(41, "grant_1", 1), claim.RequestID)
	require.Equal(t, []string{"org-1"}, api.claimOrgs)
	require.Contains(t, repo.updatedKeys(), claudeResetSnapshotExtraKey, "the pre-claim status is cached")

	_, cached := svc.usage.cache.apiCache.Load(int64(41))
	require.False(t, cached, "a consumed reset drops the stale usage cache")

	// 超时后重试：上游状态未变时复用同一个 request_id，由上游去重
	_, err = svc.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.NoError(t, err)
	require.Len(t, api.claims, 2)
	require.Equal(t, claim.RequestID, api.claims[1].RequestID)
}

func TestClaudeQuotaResetCredit_AutoPrefersWeeklyResetAtWall(t *testing.T) {
	windowEnd := time.Now().Add(time.Hour)
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), claimableJuniper)}
	api := &claudeQuotaFakeAPI{claimBody: `{"result":"reset","next_available_at":"2026-10-04T00:00:00Z"}`}
	svc, _ := newClaudeQuotaTestService(claudeQuotaTestAccount(func(a *Account) {
		a.SessionWindowEnd = &windowEnd
		a.SessionWindowStatus = "rejected"
	}), fetcher, api)

	result, err := svc.ResetCredit(context.Background(), 41, "")
	require.NoError(t, err)
	require.Equal(t, ClaudeResetProgramJuniperTide, result.Program)
	require.Equal(t, "2026-10-04T00:00:00Z", result.NextAvailableAt)
	require.Equal(t, []string{claudeUsageQueryAtWall}, fetcher.queries)
	require.Len(t, api.claims, 1)
	require.Equal(t, ClaudeRateLimitResetRequest{Program: ClaudeResetProgramJuniperTide}, api.claims[0])
}

func TestClaudeQuotaResetCredit_RefusesUnusableResetsWithoutClaiming(t *testing.T) {
	requiresLimit := `{"id":"grant_1","resets_left":1,"clears":["five_hour"],"usable_now":false,"use_requires_limit":true}`
	cases := []struct {
		name    string
		body    string
		program string
		want    error
	}{
		{"requires limit", claudeUsageBody(cedarEmberBlock(requiresLimit), ""), ClaudeResetProgramCedarEmber, ErrClaudeResetRequiresLimit},
		{"requires limit via auto", claudeUsageBody(cedarEmberBlock(requiresLimit), ""), "", ErrClaudeResetRequiresLimit},
		{"no grants", claudeUsageBody(`{"eligible":true,"grants":[]}`, ""), ClaudeResetProgramCedarEmber, ErrClaudeResetUnavailable},
		{"ineligible", claudeUsageBody(`{"eligible":false,"ineligible_reason":"surface"}`, ""), "", ErrClaudeResetUnavailable},
		{"weekly reset not offered", claudeUsageBody("", ""), ClaudeResetProgramJuniperTide, ErrClaudeResetUnavailable},
		{"unknown program", claudeUsageBody("", ""), "bogus", ErrClaudeResetInvalidProgram},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &claudeQuotaFakeAPI{claimBody: `{"result":"reset"}`}
			svc, _ := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), &claudeQuotaFakeFetcher{body: tc.body}, api)
			_, err := svc.ResetCredit(context.Background(), 41, tc.program)
			require.ErrorIs(t, err, tc.want)
			require.Empty(t, api.claims)
		})
	}
}

func TestClaudeQuotaResetCredit_ReturnsNotAppliedResults(t *testing.T) {
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), "")}
	api := &claudeQuotaFakeAPI{claimBody: `{"result":"not_limited","reason":"not_limited"}`}
	svc, _ := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), fetcher, api)

	result, err := svc.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.NoError(t, err)
	require.Equal(t, ClaudeResetResultNotLimited, result.Result)
	require.False(t, result.Consumed())

	api.claimBody = `{"unexpected":true}`
	_, err = svc.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.Error(t, err)

	notApplied := ClaudeResetNotAppliedError(&ClaudeQuotaResetResult{Result: "cooldown", Reason: "cooldown"})
	require.Contains(t, notApplied.Error(), "cooldown")
}

func TestClaudeQuotaResetCredit_LooksUpMissingOrganization(t *testing.T) {
	fetcher := &claudeQuotaFakeFetcher{body: claudeUsageBody(cedarEmberBlock(usableCedarGrant(1)), "")}
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile, claimBody: `{"result":"reset"}`}
	svc, repo := newClaudeQuotaTestService(claudeQuotaTestAccount(func(a *Account) { a.Extra = nil }), fetcher, api)

	_, err := svc.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.NoError(t, err)
	require.Equal(t, []string{"org-from-profile"}, api.claimOrgs)
	require.Equal(t, "org-from-profile", repo.updatedKeys()["org_uuid"])

	api.profileBody = `{"organization":{}}`
	svc2, _ := newClaudeQuotaTestService(claudeQuotaTestAccount(func(a *Account) { a.Extra = nil }), fetcher, api)
	_, err = svc2.ResetCredit(context.Background(), 41, ClaudeResetProgramCedarEmber)
	require.ErrorIs(t, err, ErrClaudeQuotaNoOrganization)
}

func TestClaudeQuotaService_RejectsUnsupportedAccounts(t *testing.T) {
	for name, mutate := range map[string]func(*Account){
		"setup token": func(a *Account) { a.Type = AccountTypeSetupToken },
		"openai":      func(a *Account) { a.Platform = PlatformOpenAI },
		"synthetic":   func(a *Account) { a.Extra = map[string]any{"synthetic_ui_test": true} },
	} {
		t.Run(name, func(t *testing.T) {
			account := claudeQuotaTestAccount(mutate)
			api := &claudeQuotaFakeAPI{}
			svc, _ := newClaudeQuotaTestService(account, &claudeQuotaFakeFetcher{body: claudeUsageBody("", "")}, api)
			_, err := svc.RefreshQuota(context.Background(), 41)
			require.ErrorIs(t, err, ErrClaudeQuotaUnsupported)
			_, err = svc.ResetCredit(context.Background(), 41, "")
			require.ErrorIs(t, err, ErrClaudeQuotaUnsupported)
			require.Empty(t, api.claims)
		})
	}
}

func TestClaudeQuotaRefreshSubscriptionIfStale_ThrottlesBackgroundFetch(t *testing.T) {
	api := &claudeQuotaFakeAPI{profileBody: claudeMax20xProfile, profileSeen: make(chan struct{}, 4)}
	svc, repo := newClaudeQuotaTestService(claudeQuotaTestAccount(nil), &claudeQuotaFakeFetcher{body: claudeUsageBody("", "")}, api)

	account := claudeQuotaTestAccount(nil)
	svc.RefreshSubscriptionIfStale(&account)
	select {
	case <-api.profileSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("expected a background profile fetch")
	}
	require.Eventually(t, func() bool {
		_, ok := repo.updatedKeys()[claudeSubscriptionExtraKey]
		return ok
	}, 5*time.Second, 10*time.Millisecond)

	// 同一账号 30 分钟内不再重复拉取
	svc.RefreshSubscriptionIfStale(&account)
	select {
	case <-api.profileSeen:
		t.Fatal("background fetch must be throttled per account")
	case <-time.After(100 * time.Millisecond):
	}

	fresh := claudeQuotaTestAccount(func(a *Account) {
		a.ID = 99
		a.Extra = map[string]any{claudeSubscriptionExtraKey: &ClaudeSubscriptionInfo{PlanType: ClaudePlanPro, UpdatedAt: time.Now().UTC().Format(time.RFC3339)}}
	})
	svc.RefreshSubscriptionIfStale(&fresh)
	setupToken := claudeQuotaTestAccount(func(a *Account) { a.ID = 98; a.Type = AccountTypeSetupToken })
	svc.RefreshSubscriptionIfStale(&setupToken)
	select {
	case <-api.profileSeen:
		t.Fatal("fresh or unsupported accounts must not trigger a fetch")
	case <-time.After(100 * time.Millisecond):
	}
}

type claudeQuotaWorkflowStub struct {
	calls   *[]string
	refresh *ClaudeQuotaRefreshResult
	err     error
}

func (s claudeQuotaWorkflowStub) RefreshQuota(context.Context, int64) (*ClaudeQuotaRefreshResult, error) {
	*s.calls = append(*s.calls, "refresh")
	return s.refresh, s.err
}

type claudeQuotaRecovererStub struct {
	calls   *[]string
	options []AccountRecoveryOptions
	err     error
}

func (s *claudeQuotaRecovererStub) RecoverAccountState(_ context.Context, _ int64, options AccountRecoveryOptions) (*SuccessfulTestRecoveryResult, error) {
	*s.calls = append(*s.calls, "recover")
	s.options = append(s.options, options)
	return &SuccessfulTestRecoveryResult{}, s.err
}

func TestRunClaudeQuotaResetPostProcess(t *testing.T) {
	var calls []string
	recoverer := &claudeQuotaRecovererStub{calls: &calls}
	refresh := &ClaudeQuotaRefreshResult{Snapshot: &ClaudeResetSnapshot{}, CachePersisted: true}
	loadAccount := func(context.Context, int64) (*Account, error) {
		calls = append(calls, "load")
		return &Account{ID: 41}, nil
	}

	result := RunClaudeQuotaResetPostProcess(context.Background(), 41, claudeQuotaWorkflowStub{calls: &calls, refresh: refresh}, recoverer, loadAccount)
	require.Equal(t, []string{"recover", "refresh", "load"}, calls)
	require.True(t, result.AccountStateRecovered)
	require.True(t, result.CacheRefreshed)
	require.Same(t, refresh, result.Refresh)
	require.Empty(t, result.WarningCode)
	require.True(t, recoverer.options[0].InvalidateToken)

	calls = nil
	result = RunClaudeQuotaResetPostProcess(context.Background(), 41, claudeQuotaWorkflowStub{calls: &calls, err: errors.New("down")}, recoverer, loadAccount)
	require.Equal(t, OpenAIQuotaResetWarningCacheRefreshFailed, result.WarningCode)
	require.NotNil(t, result.Account, "the account row is still refreshed")

	calls = nil
	failing := &claudeQuotaRecovererStub{calls: &calls, err: errors.New("db down")}
	result = RunClaudeQuotaResetPostProcess(context.Background(), 41, claudeQuotaWorkflowStub{calls: &calls, refresh: refresh}, failing, loadAccount)
	require.Equal(t, []string{"recover"}, calls, "nothing else runs when recovery fails")
	require.Equal(t, OpenAIQuotaResetWarningAccountRecoveryFailed, result.WarningCode)

	calls = nil
	result = RunClaudeQuotaResetPostProcess(context.Background(), 41, claudeQuotaWorkflowStub{calls: &calls, refresh: refresh}, recoverer, func(context.Context, int64) (*Account, error) {
		return nil, errors.New("gone")
	})
	require.Equal(t, OpenAIQuotaResetWarningAccountRefreshFailed, result.WarningCode)
}
