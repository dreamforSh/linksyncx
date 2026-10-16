//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
)

func perplexityMaintenanceConfig() *config.Config {
	return &config.Config{Gateway: config.GatewayConfig{Perplexity: config.GatewayPerplexityConfig{
		MaintenanceEnabled: true, SessionKeepaliveHours: 6, QuotaRefreshMinutes: 5,
	}}}
}

func TestPerplexityMaintenanceSectionsHaveIndependentIntervals(t *testing.T) {
	account := perplexityQuotaTestAccount(t, 0, 3)
	now := time.Now()
	profile := perplexityProfileFromSnapshot(account.Extra[perplexityQuotaSnapshotExtraKey])
	for _, name := range perplexityInfoSections {
		profile.Sections[name] = PerplexityUsageSection{Status: "success", FetchedAt: &now, AttemptedAt: &now}
	}
	account.Extra[perplexityQuotaSnapshotExtraKey] = profile.persistedSnapshot()
	svc := NewPerplexityMaintenanceService(nil, nil, nil, perplexityMaintenanceConfig())
	require.Empty(t, svc.dueSections(account, now.Add(time.Minute)))
	require.Equal(t, []string{"credits", "rate_limits"}, svc.dueSections(account, now.Add(6*time.Minute)))
	require.Equal(t, []string{"session", "credits", "rate_limits"}, svc.dueSections(account, now.Add(7*time.Hour)))
	account.Credentials["api_key"] = "new-credential-generation"
	require.Equal(t, []string{"session", "credits", "rate_limits"}, svc.dueSections(account, now.Add(time.Second)))
}

func TestPerplexityMaintenanceQuotaRecoveryDoesNotUnpauseManualAccounts(t *testing.T) {
	for _, manuallyPaused := range []bool{false, true} {
		t.Run(map[bool]string{false: "quota filtered", true: "manual pause"}[manuallyPaused], func(t *testing.T) {
			account := perplexityQuotaTestAccount(t, 0, 3)
			account.Schedulable = !manuallyPaused
			profile := perplexityProfileFromSnapshot(account.Extra[perplexityQuotaSnapshotExtraKey])
			old := time.Now().Add(-6 * time.Minute)
			profile.Sections["rate_limits"] = PerplexityUsageSection{Status: "success", FetchedAt: &old, AttemptedAt: &old}
			account.Extra[perplexityQuotaSnapshotExtraKey] = profile.persistedSnapshot()
			require.Equal(t, "perplexity_quota_remaining_pro", perplexityQuotaPauseReason(account, "paid"))
			tests, upstream, _ := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == perplexity.RateLimitAllPath {
					_, _ = w.Write([]byte(`{"remaining_pro":4,"remaining_research":2}`))
					return
				}
				servePerplexityGoodProfile(w, req)
			})
			repo := newPerplexityStateRepo(account)
			tests.accountRepo = repo
			svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
			svc.runOnce(context.Background())
			current, err := repo.GetByID(context.Background(), account.ID)
			require.NoError(t, err)
			require.Empty(t, perplexityQuotaPauseReason(current, "paid"))
			require.Equal(t, !manuallyPaused, current.Schedulable)
			require.Equal(t, 4, upstream.count(), "3 profile sections + 1 catalog fetch per pass")
			for _, req := range upstream.requests {
				require.NotEqual(t, perplexity.AskPath, req.URL.Path)
			}
		})
	}
}

func TestPerplexityMaintenanceSkipsDisabledAndForeignErrors(t *testing.T) {
	for _, state := range []string{StatusDisabled, "foreign_error", "empty_cookie"} {
		account := perplexityQuotaTestAccount(t, 0, 0)
		switch state {
		case StatusDisabled:
			account.Status = StatusDisabled
		case "foreign_error":
			account.Status, account.ErrorMessage = StatusError, "administrator disabled this account"
		case "empty_cookie":
			account.Credentials["api_key"] = ""
		}
		tests, upstream, _ := newPerplexityInfoTestService(servePerplexityGoodProfile)
		repo := newPerplexityStateRepo(account)
		tests.accountRepo = repo
		svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
		svc.runOnce(context.Background())
		require.Zero(t, upstream.count())
	}
}

func TestPerplexityMaintenanceLockContentionAndErrorsDoNotProbe(t *testing.T) {
	for _, failure := range []bool{false, true} {
		tests, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
		account.Status = StatusActive
		repo := newPerplexityStateRepo(account)
		tests.accountRepo = repo
		locks := &fakeLeaderLockCache{owners: map[string]string{"perplexity:maintenance:72": "peer"}}
		if failure {
			locks.acquireErr = errors.New("cache unavailable")
		}
		svc := NewPerplexityMaintenanceService(repo, tests, locks, perplexityMaintenanceConfig())
		svc.runOnce(context.Background())
		require.Zero(t, upstream.count())
		require.Equal(t, "peer", locks.heldBy("perplexity:maintenance:72"))
	}
}

func TestPerplexityMaintenanceStopCancelsRequestsAndIsIdempotent(t *testing.T) {
	entered := make(chan struct{}, 1)
	tests, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		entered <- struct{}{}
		<-req.Context().Done()
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	account.Status = StatusActive
	repo := newPerplexityStateRepo(account)
	tests.accountRepo = repo
	svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
	t.Cleanup(svc.Stop)
	svc.Start()
	svc.Start()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not start")
	}
	stopped := make(chan struct{})
	go func() { svc.Stop(); svc.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance failed to cancel its upstream request")
	}
	svc.Start()
	require.Equal(t, 1, upstream.count())
}

func TestPerplexityMaintenanceConcurrencyIsBounded(t *testing.T) {
	entered := make(chan struct{}, 64)
	release := make(chan struct{})
	var active, peak atomic.Int32
	tests, upstream, first := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-req.Context().Done():
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	var accounts []*Account
	for id := int64(1); id <= 6; id++ {
		account := copyPerplexityAccount(first)
		account.ID, account.Status = id, StatusActive
		accounts = append(accounts, account)
	}
	repo := newPerplexityStateRepo(accounts...)
	tests.accountRepo = repo
	svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { svc.runOnce(ctx); close(done) }()
	for i := 0; i < perplexityMaintenanceConcurrency; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("maintenance concurrency did not reach the configured bound")
		}
	}
	require.Equal(t, int32(perplexityMaintenanceConcurrency), peak.Load())
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("maintenance did not finish")
	}
	require.LessOrEqual(t, peak.Load(), int32(perplexityMaintenanceConcurrency))
	require.Equal(t, 24, upstream.count(), "6 accounts * (3 profile sections + 1 catalog fetch)")
}

func perplexityCatalogRequestCount(upstream *perplexityInfoHTTP) int {
	count := 0
	for _, req := range upstream.requests {
		if req.URL.Path == perplexity.ModelsConfigPath {
			count++
		}
	}
	return count
}

func TestPerplexityMaintenanceRefreshesModelCatalog(t *testing.T) {
	tests, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == perplexity.ModelsConfigPath {
			_, _ = w.Write([]byte(`{"models":[{"model":"gpt55","label":"GPT-5.5"},{"model":"gpt55_thinking","label":"GPT-5.5 Thinking"},{"model":"turbo","label":"Auto"}]}`))
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	account.Status, account.Schedulable = StatusActive, true
	repo := &perplexityModelRepoStub{perplexityStateRepoStub: newPerplexityStateRepo(account)}
	tests.accountRepo = repo
	svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
	svc.runOnce(context.Background())
	current, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	snapshot := current.GetUpstreamModelMetadataSnapshot()
	require.NotNil(t, snapshot)
	require.Equal(t, "perplexity", snapshot.Source)
	require.Equal(t, perplexityAccountFingerprint(current), snapshot.IdentityFingerprint)
	require.Contains(t, snapshot.Models, "gpt55")
	require.Contains(t, snapshot.Models, "gpt55_thinking")
	require.Contains(t, snapshot.Models, "turbo")
	require.Equal(t, StatusActive, current.Status)
	require.Empty(t, current.ErrorMessage)
	require.Equal(t, 1, perplexityCatalogRequestCount(upstream))

	// A fresh snapshot is not refetched on the next pass.
	svc.runOnce(context.Background())
	require.Equal(t, 1, perplexityCatalogRequestCount(upstream))
}

func TestPerplexityMaintenanceCatalogRefreshFailureIsBestEffort(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			tests, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == perplexity.ModelsConfigPath {
					w.WriteHeader(status)
					return
				}
				servePerplexityGoodProfile(w, req)
			})
			account.Status, account.Schedulable = StatusActive, true
			repo := &perplexityModelRepoStub{perplexityStateRepoStub: newPerplexityStateRepo(account)}
			tests.accountRepo = repo
			svc := NewPerplexityMaintenanceService(repo, tests, nil, perplexityMaintenanceConfig())
			svc.runOnce(context.Background())
			require.Equal(t, 1, perplexityCatalogRequestCount(upstream))
			current, err := repo.GetByID(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, StatusActive, current.Status, "catalog failures must never mark the account")
			require.Empty(t, current.ErrorMessage)
			require.True(t, current.Schedulable)
			require.Nil(t, current.TempUnschedulableUntil)
			require.Nil(t, current.GetUpstreamModelMetadataSnapshot())

			// The per-attempt backoff keeps a failing endpoint from being hit every pass.
			svc.runOnce(context.Background())
			require.Equal(t, 1, perplexityCatalogRequestCount(upstream))
		})
	}
}

func TestPerplexityMaintenanceCatalogRefreshCanBeDisabled(t *testing.T) {
	tests, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	account.Status = StatusActive
	repo := &perplexityModelRepoStub{perplexityStateRepoStub: newPerplexityStateRepo(account)}
	tests.accountRepo = repo
	cfg := perplexityMaintenanceConfig()
	cfg.Gateway.Perplexity.CatalogRefreshDisabled = true
	svc := NewPerplexityMaintenanceService(repo, tests, nil, cfg)
	svc.runOnce(context.Background())
	require.Zero(t, perplexityCatalogRequestCount(upstream))
	require.Equal(t, 3, upstream.count(), "profile sections still refresh when the catalog is disabled")
}
