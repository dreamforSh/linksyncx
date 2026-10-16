//go:build unit

package service

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
)

func TestPerplexityUsage_QueriesCookieAPIKeyAndCachesWithTTL(t *testing.T) {
	fetcher, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	svc := &AccountUsageService{cache: NewUsageCache()}
	svc.SetPerplexityAccountInfoFetcher(fetcher)

	first, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, first.Perplexity)
	require.Empty(t, first.Error)
	require.Equal(t, 3, upstream.count())
	second, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 3, upstream.count())
	require.Equal(t, first.Perplexity.FetchedAt, second.Perplexity.FetchedAt)

	second.Perplexity.Snapshot["credits"].(map[string]any)["balance"] = -1
	third, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, float64(4000), third.Perplexity.Snapshot["credits"].(map[string]any)["balance"])

	cached, ok := svc.cache.perplexityCache.Load(account.ID)
	require.True(t, ok)
	cached.(*perplexityUsageCache).timestamp = time.Now().Add(-apiCacheTTL - time.Second)
	_, err = svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 6, upstream.count())
	_, err = svc.GetUsageForAccount(context.Background(), account, true)
	require.NoError(t, err)
	require.Equal(t, 9, upstream.count(), "force refresh bypasses the in-memory TTL")
}

func TestPerplexityUsage_SingleflightCombinesConcurrentQueries(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	fetcher, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		once.Do(func() {
			close(entered)
			<-release
		})
		servePerplexityGoodProfile(w, req)
	})
	svc := &AccountUsageService{cache: NewUsageCache()}
	svc.SetPerplexityAccountInfoFetcher(fetcher)

	const callers = 24
	var wg sync.WaitGroup
	errors := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.GetUsageForAccount(context.Background(), account)
			errors <- err
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 3, upstream.count(), "one collection, not one per UI reader")
}

func TestPerplexityUsage_PartialFailureUsesShortTTLAndRetainsLastGood(t *testing.T) {
	var fail atomic.Bool
	fetcher, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if fail.Load() && req.URL.Path == perplexity.CreditsBalancePath {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	svc := &AccountUsageService{cache: NewUsageCache()}
	svc.SetPerplexityAccountInfoFetcher(fetcher)
	first, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	fail.Store(true)
	partial, err := svc.GetUsageForAccount(context.Background(), account, true)
	require.NoError(t, err)
	require.Equal(t, "stale", partial.Perplexity.Sections["credits"].Status)
	require.Equal(t, first.Perplexity.Sections["credits"].FetchedAt, partial.Perplexity.Sections["credits"].FetchedAt)
	require.Equal(t, first.Perplexity.FetchedAt, partial.Perplexity.FetchedAt)
	require.Contains(t, partial.Error, "503")

	_, err = svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 6, upstream.count(), "negative TTL prevents immediate retries")
	cached, _ := svc.cache.perplexityCache.Load(account.ID)
	cached.(*perplexityUsageCache).timestamp = time.Now().Add(-apiErrorCacheTTL - time.Second)
	_, err = svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 9, upstream.count(), "partial-failure TTL is shorter than success TTL")
}

func TestPerplexityUsage_MissingTransportIsExplicitAndKeepsSnapshot(t *testing.T) {
	account := &Account{
		ID: 77, Platform: PlatformPerplexity, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fake-cookie"},
		Extra: map[string]any{perplexityQuotaSnapshotExtraKey: map[string]any{
			"fetched_at": "2026-01-01T00:00:00Z", "credits": map[string]any{"balance": 90},
		}},
	}
	svc := &AccountUsageService{cache: NewUsageCache()}
	info, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Contains(t, info.Error, "not configured")
	require.Equal(t, "stale", info.Perplexity.Sections["credits"].Status)
	require.Equal(t, "error", info.Perplexity.Sections["session"].Status)
	require.Equal(t, "2026-01-01T00:00:00Z", info.Perplexity.Sections["credits"].FetchedAt.Format(time.RFC3339))
	require.Nil(t, info.Perplexity.SessionValid)
}

func TestPerplexityUsage_ChangedCredentialsInvalidateCacheAndMissingProxyFailsClosed(t *testing.T) {
	fetcher, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	svc := &AccountUsageService{cache: NewUsageCache()}
	svc.SetPerplexityAccountInfoFetcher(fetcher)
	_, err := svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	account.Credentials["csrf_token"] = "changed-fake-csrf"
	_, err = svc.GetUsageForAccount(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 6, upstream.count())
	require.Contains(t, upstream.requests[3].Header.Get("Cookie"), "changed-fake-csrf")

	proxyID := int64(21)
	account.ProxyID = &proxyID
	_, err = svc.GetUsageForAccount(context.Background(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Equal(t, 6, upstream.count())
}
