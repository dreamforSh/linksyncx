//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

// The fake transport never opens a socket or consults environment proxies.
type perplexityInfoHTTP struct {
	mu       sync.Mutex
	handler  http.HandlerFunc
	requests []*http.Request
	proxies  []string
	profiles []*tlsfingerprint.Profile
}

func (u *perplexityInfoHTTP) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, errors.New("unexpected request without TLS profile")
}

func (u *perplexityInfoHTTP) DoWithTLS(req *http.Request, proxy string, _ int64, _ int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.mu.Lock()
	u.requests = append(u.requests, req)
	u.proxies = append(u.proxies, proxy)
	u.profiles = append(u.profiles, profile)
	u.mu.Unlock()
	recorder := httptest.NewRecorder()
	u.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

func (u *perplexityInfoHTTP) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

type perplexityInfoRepo struct {
	AccountRepository
	errorID int64
	message string
	cleared int64
	extra   map[string]any
}

func (r *perplexityInfoRepo) SetError(_ context.Context, id int64, message string) error {
	r.errorID, r.message = id, message
	return nil
}

func (r *perplexityInfoRepo) ClearError(_ context.Context, id int64) error {
	r.cleared = id
	return nil
}

func (r *perplexityInfoRepo) UpdateExtra(_ context.Context, _ int64, extra map[string]any) error {
	r.extra = extra
	return nil
}

func newPerplexityInfoTestService(handler http.HandlerFunc) (*AccountTestService, *perplexityInfoHTTP, *Account) {
	upstream := &perplexityInfoHTTP{handler: handler}
	cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
	account := &Account{
		ID: 72, Platform: PlatformPerplexity, Type: AccountTypeAPIKey, Concurrency: 2,
		Credentials: map[string]any{"api_key": "fake-session", "csrf_token": "fake-csrf|fake-hash", "base_url": "https://fake.perplexity.example"},
	}
	return &AccountTestService{httpUpstream: upstream, cfg: cfg}, upstream, account
}

func servePerplexityGoodProfile(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch req.URL.Path {
	case perplexity.SessionPath:
		_, _ = w.Write([]byte(`{"user":{"email":"fake@example.invalid","subscription_state":"pro"}}`))
	case perplexity.CreditsBalancePath:
		_, _ = w.Write([]byte(`{"balance":4000,"currency":"credits"}`))
	case perplexity.RateLimitAllPath:
		_, _ = w.Write([]byte(`{"search":{"remaining":4}}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestPerplexityAccountInfo_CookieCSRFProxyAndTLS(t *testing.T) {
	svc, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	proxyID := int64(19)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "socks5", Host: "127.0.0.1", Port: 7891, Status: StatusActive}

	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
	require.NotNil(t, profile.SessionValid)
	require.True(t, *profile.SessionValid)
	require.Len(t, upstream.requests, 3)
	for i, req := range upstream.requests {
		require.Equal(t, "fake.perplexity.example", req.URL.Host)
		require.Equal(t, "__Secure-next-auth.session-token=fake-session; next-auth.csrf-token=fake-csrf|fake-hash", req.Header.Get("Cookie"))
		require.Empty(t, req.Header.Get("Authorization"))
		require.Equal(t, "socks5://127.0.0.1:7891", upstream.proxies[i])
		require.NotNil(t, upstream.profiles[i])
	}
	require.Equal(t, "true", upstream.requests[1].URL.Query().Get("include_credits"))
}

func TestPerplexityAccountInfo_MissingAssignedProxyDoesNotSend(t *testing.T) {
	svc, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	proxyID := int64(19)
	account.ProxyID = &proxyID

	_, err := svc.doPerplexityRequest(context.Background(), account, http.MethodGet, perplexity.SessionPath)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
	require.Nil(t, profile.SessionValid)
	require.Nil(t, profile.FetchedAt)
	require.Zero(t, upstream.count())
	for _, name := range perplexityInfoSections {
		require.Equal(t, "error", profile.Sections[name].Status)
		require.Nil(t, profile.Sections[name].FetchedAt)
	}
}

func TestPerplexityAccountInfo_PartialFailureRetainsLastGoodTimes(t *testing.T) {
	var failCredits atomic.Bool
	svc, upstream, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if failCredits.Load() && req.URL.Path == perplexity.CreditsBalancePath {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"fake upstream failure"}`))
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	first := svc.FetchPerplexityAccountInfo(context.Background(), account)
	before := perplexityProfileFromSnapshot(first)
	account.Extra = map[string]any{perplexityQuotaSnapshotExtraKey: first}
	failCredits.Store(true)
	after := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))

	require.Equal(t, before.FetchedAt, after.FetchedAt, "partial refresh must not advance all-success timestamp")
	require.Equal(t, before.Snapshot["credits"], after.Snapshot["credits"])
	require.Equal(t, before.Sections["credits"].FetchedAt, after.Sections["credits"].FetchedAt)
	require.Equal(t, "stale", after.Sections["credits"].Status)
	require.Contains(t, after.Sections["credits"].Error, "502")
	require.Equal(t, "success", after.Sections["session"].Status)
	require.Equal(t, "success", after.Sections["rate_limits"].Status)
	require.Equal(t, 6, upstream.count())
	require.False(t, after.Sections["session"].FetchedAt.Before(*before.Sections["session"].FetchedAt))
	require.Equal(t, "success", before.Sections["credits"].Status, "must not mutate the prior snapshot")
}

func TestPerplexityAccountInfo_ExpiredSessionPreservesSnapshotAndMarksHealth(t *testing.T) {
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == perplexity.SessionPath {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"expired"}`))
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	repo := &perplexityInfoRepo{}
	svc.accountRepo = repo
	previous := "2026-01-02T03:04:05Z"
	account.Extra = map[string]any{perplexityQuotaSnapshotExtraKey: map[string]any{
		"fetched_at": previous, "session": map[string]any{"email": "old@example.invalid", "subscription_tier": "pro"},
	}}
	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
	info := perplexityUsageInfo(profile)
	require.True(t, info.NeedsReauth)
	require.Equal(t, "unauthenticated", info.ErrorCode)
	require.Equal(t, "stale", profile.Sections["session"].Status)
	require.Equal(t, previous, profile.Sections["session"].FetchedAt.Format(time.RFC3339))
	require.Equal(t, previous, profile.FetchedAt.Format(time.RFC3339))
	require.Equal(t, account.ID, repo.errorID)
	require.Contains(t, repo.message, "unauthenticated")
	require.NotContains(t, perplexityAccountInfoText(profile.persistedSnapshot()), "session is valid")
}

func TestPerplexityAccountInfo_UnauthenticatedAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		invalid bool
	}{
		{name: "empty user", body: `{"user":{}}`, invalid: true},
		{name: "anonymous", body: `{}`, invalid: true},
		{name: "html", body: `<html>blocked</html>`},
		{name: "array", body: `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == perplexity.SessionPath {
					_, _ = w.Write([]byte(tc.body))
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			})
			profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
			require.Nil(t, profile.FetchedAt)
			require.Equal(t, "error", profile.Sections["session"].Status)
			if tc.invalid {
				require.NotNil(t, profile.SessionValid)
				require.False(t, *profile.SessionValid)
			} else {
				require.Nil(t, profile.SessionValid, "unreadable response does not prove session expiration")
			}
		})
	}
}

func TestPerplexityAccountInfo_ForbiddenIsUnknownNotInventedExpiration(t *testing.T) {
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == perplexity.SessionPath {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<html>access denied</html>`))
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	repo := &perplexityInfoRepo{}
	svc.accountRepo = repo
	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
	require.Nil(t, profile.SessionValid)
	require.Equal(t, "error", profile.Sections["session"].Status)
	require.Contains(t, profile.Sections["session"].Error, "403")
	require.Zero(t, repo.errorID, "forbidden alone is not proof that a cookie expired")
	require.False(t, perplexityUsageInfo(profile).NeedsReauth)
}

func TestPerplexityAccountInfo_TotalFailureRetainsAllLastGoodDataAndTimes(t *testing.T) {
	var failing atomic.Bool
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	first := svc.FetchPerplexityAccountInfo(context.Background(), account)
	before := perplexityProfileFromSnapshot(first)
	account.Extra = map[string]any{perplexityQuotaSnapshotExtraKey: first}
	failing.Store(true)
	after := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), account))
	require.Equal(t, before.Snapshot, after.Snapshot)
	require.Equal(t, before.FetchedAt, after.FetchedAt)
	require.Nil(t, after.SessionValid, "old authenticated session is not proof of current validity")
	for _, name := range perplexityInfoSections {
		require.Equal(t, "stale", after.Sections[name].Status)
		require.Equal(t, before.Sections[name].FetchedAt, after.Sections[name].FetchedAt)
		require.NotEmpty(t, after.Sections[name].Error)
	}
	require.NotContains(t, perplexityAccountInfoText(after.persistedSnapshot()), "session is valid")
	require.NotContains(t, perplexityAccountInfoText(after.persistedSnapshot()), "rate-limit snapshot updated")
}

func TestPerplexitySessionHealth_RecoveryOnlyClearsItsOwnAuthenticationFailure(t *testing.T) {
	for _, message := range []string{"perplexity session is unauthenticated or expired; update the session cookie", "administrator disabled the account"} {
		svc, _, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
		repo := &perplexityInfoRepo{}
		svc.accountRepo = repo
		account.Status, account.ErrorMessage = StatusError, message
		require.NoError(t, svc.ValidatePerplexitySession(context.Background(), account))
		if strings.HasPrefix(message, "perplexity session is unauthenticated") {
			require.Equal(t, account.ID, repo.cleared)
		} else {
			require.Zero(t, repo.cleared)
		}
	}
}

func TestPerplexityAccountTest_SessionValidationDoesNotClaimModelInvocation(t *testing.T) {
	svc, upstream, account := newPerplexityInfoTestService(servePerplexityGoodProfile)
	ctx, rec := newTestContext()
	require.NoError(t, svc.testPerplexityAccountConnection(ctx, account, "not-a-tested-model"))
	require.Contains(t, rec.Body.String(), "session validation only")
	require.Contains(t, rec.Body.String(), `"success":true`)
	require.NotContains(t, rec.Body.String(), "not-a-tested-model")
	for _, req := range upstream.requests {
		require.NotEqual(t, perplexity.AskPath, req.URL.Path)
	}
}

func TestPerplexityAccountTest_InvalidSessionDoesNotSucceed(t *testing.T) {
	svc, _, account := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, "session") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		servePerplexityGoodProfile(w, req)
	})
	ctx, rec := newTestContext()
	require.Error(t, svc.testPerplexityAccountConnection(ctx, account, "sonar"))
	require.NotContains(t, rec.Body.String(), `"success":true`)
	require.Contains(t, rec.Body.String(), "unauthenticated")
}
