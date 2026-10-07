package service

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
)

// sessionWindowMockRepo is a minimal AccountRepository mock that records the
// rate-limit snapshots persisted by UpdateSessionWindow. Unrelated methods panic if invoked.
type sessionWindowMockRepo struct {
	// captured calls
	patches           []patchCall
	clearRateLimitIDs []int64
}

var _ AccountRepository = (*sessionWindowMockRepo)(nil)

type patchCall struct {
	ID    int64
	Patch ClaudeRateLimitPatch
}

func (m *sessionWindowMockRepo) ApplyClaudeRateLimitPatch(_ context.Context, id int64, patch ClaudeRateLimitPatch) (bool, error) {
	m.patches = append(m.patches, patchCall{ID: id, Patch: patch})
	return true, nil
}
func (m *sessionWindowMockRepo) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	panic("unexpected: session windows are persisted through ApplyClaudeRateLimitPatch")
}
func (m *sessionWindowMockRepo) UpdateSessionWindowEnd(_ context.Context, _ int64, _ time.Time) error {
	return nil
}
func (m *sessionWindowMockRepo) UpdateExtra(context.Context, int64, map[string]any) error {
	panic("unexpected: passive usage is persisted through ApplyClaudeRateLimitPatch")
}
func (m *sessionWindowMockRepo) ClearRateLimit(_ context.Context, id int64) error {
	m.clearRateLimitIDs = append(m.clearRateLimitIDs, id)
	return nil
}
func (m *sessionWindowMockRepo) ClearAntigravityQuotaScopes(_ context.Context, _ int64) error {
	return nil
}
func (m *sessionWindowMockRepo) ClearModelRateLimits(_ context.Context, _ int64) error {
	return nil
}
func (m *sessionWindowMockRepo) ClearTempUnschedulable(_ context.Context, _ int64) error {
	return nil
}

// --- Unused interface methods (panic on unexpected call) ---

func (m *sessionWindowMockRepo) Create(context.Context, *Account) error { panic("unexpected") }
func (m *sessionWindowMockRepo) GetByID(context.Context, int64) (*Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) GetByIDs(context.Context, []int64) ([]*Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ExistsByID(context.Context, int64) (bool, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) GetByCRSAccountID(context.Context, string) (*Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) FindByExtraField(context.Context, string, any) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) Update(context.Context, *Account) error { panic("unexpected") }
func (m *sessionWindowMockRepo) Delete(context.Context, int64) error    { panic("unexpected") }
func (m *sessionWindowMockRepo) List(context.Context, pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string) ([]Account, *pagination.PaginationResult, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListAllWithFilters(context.Context, string, string, string, string, int64, string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListByGroup(context.Context, int64) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListActive(context.Context) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListOAuthRefreshCandidates(context.Context) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) UpdateLastUsed(context.Context, int64) error { panic("unexpected") }
func (m *sessionWindowMockRepo) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) SetError(context.Context, int64, string) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ClearError(context.Context, int64) error { panic("unexpected") }
func (m *sessionWindowMockRepo) SetSchedulable(context.Context, int64, bool) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) BindGroups(context.Context, int64, []int64) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulable(context.Context) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableByPlatforms(context.Context, []string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListSchedulableUngroupedByPlatforms(context.Context, []string) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) SetRateLimited(context.Context, int64, time.Time) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) SetModelRateLimit(context.Context, int64, string, time.Time, ...string) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) SetOverloaded(context.Context, int64, time.Time) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) IncrementQuotaUsed(context.Context, int64, float64) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ResetQuotaUsedAndClearRateLimitCooldown(context.Context, int64) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) RevertProxyFallback(context.Context, int64) error {
	panic("unexpected")
}
func (m *sessionWindowMockRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	panic("unexpected")
}

// newRateLimitServiceForTest creates a RateLimitService with the given mock repo.
func newRateLimitServiceForTest(repo AccountRepository) *RateLimitService {
	return &RateLimitService{accountRepo: repo}
}

// onlyPatch returns the single persisted patch, failing the test otherwise.
func onlyPatch(t *testing.T, repo *sessionWindowMockRepo) ClaudeRateLimitPatch {
	t.Helper()
	if len(repo.patches) != 1 {
		t.Fatalf("expected 1 persisted rate-limit patch, got %d", len(repo.patches))
	}
	return repo.patches[0].Patch
}

func TestUpdateSessionWindow_UsesResetHeader(t *testing.T) {
	// The reset header provides the real window end as a Unix timestamp.
	// UpdateSessionWindow should use it instead of the hour-truncated prediction.
	resetUnix := time.Now().Add(3 * time.Hour).Unix()
	wantEnd := time.Unix(resetUnix, 0)
	wantStart := wantEnd.Add(-5 * time.Hour)

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 42} // no existing window → needInitWindow=true
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))

	svc.UpdateSessionWindow(context.Background(), account, headers)

	patch := onlyPatch(t, repo)
	if repo.patches[0].ID != 42 {
		t.Errorf("expected account ID 42, got %d", repo.patches[0].ID)
	}
	if patch.SessionWindowEnd == nil || !patch.SessionWindowEnd.Equal(wantEnd) {
		t.Errorf("expected window end %v, got %v", wantEnd, patch.SessionWindowEnd)
	}
	if patch.SessionWindowStart == nil || !patch.SessionWindowStart.Equal(wantStart) {
		t.Errorf("expected window start %v, got %v", wantStart, patch.SessionWindowStart)
	}
	if patch.SessionWindowStatus != "allowed" {
		t.Errorf("expected status 'allowed', got %q", patch.SessionWindowStatus)
	}
	snap, ok := patch.Extra[claudeRateLimitExtraKey].(*ClaudeRateLimitSnapshot)
	if !ok || snap.Windows["5h"].Status != "allowed" || snap.Windows["5h"].ResetAt != resetUnix || snap.AppliedAtMs != patch.AppliedAtMs {
		t.Errorf("expected the rate-limit snapshot to be persisted with the patch, got %#v", patch.Extra[claudeRateLimitExtraKey])
	}
}

func TestUpdateSessionWindow_FallbackPredictionWhenNoResetHeader(t *testing.T) {
	// When the reset header is absent, should fall back to hour-truncated prediction.
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 10} // no existing window
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed_warning")
	// No anthropic-ratelimit-unified-5h-reset header

	now := time.Now()
	expectedStart := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	expectedEnd := expectedStart.Add(5 * time.Hour)

	svc.updateSessionWindowAt(context.Background(), account, headers, now)

	patch := onlyPatch(t, repo)
	if patch.SessionWindowEnd == nil {
		t.Fatal("expected window end to be set (fallback prediction)")
	}
	// Fallback: start = current hour truncated, end = start + 5h
	if !patch.SessionWindowEnd.Equal(expectedEnd) {
		t.Errorf("expected fallback end %v, got %v", expectedEnd, *patch.SessionWindowEnd)
	}
	if patch.SessionWindowStart == nil || !patch.SessionWindowStart.Equal(expectedStart) {
		t.Errorf("expected fallback start %v, got %v", expectedStart, patch.SessionWindowStart)
	}
}

func TestUpdateSessionWindow_CorrectsStalePrediction(t *testing.T) {
	// When the stored SessionWindowEnd is wrong (from a previous prediction),
	// and the reset header provides the real time, it should update the window.
	staleEnd := time.Now().Add(2 * time.Hour)             // existing prediction: 2h from now
	realResetUnix := time.Now().Add(4 * time.Hour).Unix() // real reset: 4h from now
	wantEnd := time.Unix(realResetUnix, 0)

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{
		ID:               55,
		SessionWindowEnd: &staleEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", realResetUnix))

	svc.UpdateSessionWindow(context.Background(), account, headers)

	patch := onlyPatch(t, repo)
	if patch.SessionWindowEnd == nil || !patch.SessionWindowEnd.Equal(wantEnd) {
		t.Errorf("expected corrected end %v, got %v", wantEnd, patch.SessionWindowEnd)
	}
}

func TestUpdateSessionWindow_NoUpdateWhenHeaderMatchesStored(t *testing.T) {
	// If the reset header matches the stored SessionWindowEnd, no window update needed.
	futureUnix := time.Now().Add(3 * time.Hour).Unix()
	existingEnd := time.Unix(futureUnix, 0)

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{
		ID:               77,
		SessionWindowEnd: &existingEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", futureUnix)) // same as stored

	svc.UpdateSessionWindow(context.Background(), account, headers)

	patch := onlyPatch(t, repo)
	// windowStart and windowEnd should be nil (no update needed)
	if patch.SessionWindowStart != nil || patch.SessionWindowEnd != nil {
		t.Errorf("expected nil start/end (no window change needed), got start=%v end=%v", patch.SessionWindowStart, patch.SessionWindowEnd)
	}
	// Status is still updated
	if patch.SessionWindowStatus != "allowed" {
		t.Errorf("expected status 'allowed', got %q", patch.SessionWindowStatus)
	}
}

func TestUpdateSessionWindow_ClearsUtilizationOnWindowReset(t *testing.T) {
	// When needInitWindow=true and window is set, passive usage the response did not
	// bring back is cleared, and the new utilization is stored in the same write.
	resetUnix := time.Now().Add(3 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 33} // no existing window → needInitWindow=true
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.15")

	svc.UpdateSessionWindow(context.Background(), account, headers)

	updates := onlyPatch(t, repo).Extra
	if val, ok := updates["session_window_utilization"].(float64); !ok || val != 0.15 {
		t.Errorf("expected utilization stored as 0.15, got %v", updates["session_window_utilization"])
	}
	if val, present := updates["passive_usage_7d_utilization"]; !present || val != nil {
		t.Errorf("expected stale 7d utilization cleared to nil, got present=%v val=%v", present, val)
	}
	if _, ok := updates["passive_usage_sampled_at"].(string); !ok {
		t.Errorf("expected passive_usage_sampled_at to be refreshed, got %v", updates["passive_usage_sampled_at"])
	}
}

func TestUpdateSessionWindow_NoClearUtilizationOnCorrection(t *testing.T) {
	// When correcting a stale prediction (needInitWindow=false), utilization should NOT be cleared.
	staleEnd := time.Now().Add(2 * time.Hour)
	realResetUnix := time.Now().Add(4 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{
		ID:               66,
		SessionWindowEnd: &staleEnd,
	}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", realResetUnix))
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.30")

	svc.UpdateSessionWindow(context.Background(), account, headers)

	updates := onlyPatch(t, repo).Extra
	if val, ok := updates["session_window_utilization"].(float64); !ok || val != 0.30 {
		t.Errorf("expected utilization 0.30, got %v", updates["session_window_utilization"])
	}
	if _, present := updates["passive_usage_7d_utilization"]; present {
		t.Errorf("expected passive 7d usage to be left untouched on a correction")
	}
}

func TestUpdateSessionWindow_SamplesFable7dOiHeaders(t *testing.T) {
	// 被动采样应收集 7d_oi（Fable 专属 7d 窗口）的 utilization 和 reset。
	existingEnd := time.Now().Add(3 * time.Hour)
	resetOIUnix := time.Now().Add(80 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 90, SessionWindowEnd: &existingEnd} // needInitWindow=false
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d_oi-utilization", "0.87")
	headers.Set("anthropic-ratelimit-unified-7d_oi-reset", fmt.Sprintf("%d", resetOIUnix))

	svc.UpdateSessionWindow(context.Background(), account, headers)

	updates := onlyPatch(t, repo).Extra
	if val, ok := updates["passive_usage_7d_oi_utilization"].(float64); !ok || val != 0.87 {
		t.Errorf("expected passive_usage_7d_oi_utilization=0.87, got %v", updates["passive_usage_7d_oi_utilization"])
	}
	if val, ok := updates["passive_usage_7d_oi_reset"].(int64); !ok || val != resetOIUnix {
		t.Errorf("expected passive_usage_7d_oi_reset=%d, got %v", resetOIUnix, updates["passive_usage_7d_oi_reset"])
	}
}

func TestUpdateSessionWindow_ClearsFable7dOiOnWindowReset(t *testing.T) {
	// 5h 窗口重置时应连同清除 7d_oi 被动采样数据，与 7d 行为一致。
	resetUnix := time.Now().Add(3 * time.Hour).Unix()

	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 91} // no existing window → needInitWindow=true
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetUnix))

	svc.UpdateSessionWindow(context.Background(), account, headers)

	clearUpdates := onlyPatch(t, repo).Extra
	for _, key := range []string{"passive_usage_7d_oi_utilization", "passive_usage_7d_oi_reset"} {
		if val, present := clearUpdates[key]; !present || val != nil {
			t.Errorf("expected %s cleared to nil on window reset, got present=%v val=%v", key, present, val)
		}
	}
}

func TestUpdateSessionWindow_NoStatusHeader(t *testing.T) {
	// Should return immediately if no unified rate-limit header is present.
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)

	account := &Account{ID: 1}

	svc.UpdateSessionWindow(context.Background(), account, http.Header{})

	if len(repo.patches) != 0 {
		t.Errorf("expected nothing persisted when no rate-limit header is present, got %d", len(repo.patches))
	}
}

// Without a 5h status header only the snapshot itself is persisted (no window columns, no passive usage).
func TestUpdateSessionWindow_SnapshotOnlyWithoutFiveHourStatus(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "allowed")
	headers.Set("anthropic-ratelimit-unified-7d-utilization", "0.4")

	svc.UpdateSessionWindow(context.Background(), &Account{ID: 2}, headers)

	patch := onlyPatch(t, repo)
	if patch.SessionWindowStatus != "" || patch.SessionWindowStart != nil || patch.SessionWindowEnd != nil {
		t.Errorf("expected window columns untouched, got %#v", patch)
	}
	if len(patch.Extra) != 1 || patch.Extra[claudeRateLimitExtraKey] == nil {
		t.Errorf("expected only the snapshot to be persisted, got %#v", patch.Extra)
	}
}

// Responses received earlier than the last applied one (concurrent requests finishing out of
// order) are dropped: nothing is persisted and they cannot clear a newer rate limit.
func TestUpdateSessionWindow_DropsStaleResponses(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	resetAt := time.Now().Add(time.Hour)
	limited := &Account{ID: 5, RateLimitResetAt: &resetAt}
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "allowed")

	now := time.Now()
	svc.updateSessionWindowAt(context.Background(), &Account{ID: 5}, headers, now)
	svc.updateSessionWindowAt(context.Background(), limited, headers, now.Add(-time.Second))

	if len(repo.patches) != 1 {
		t.Fatalf("expected the stale response to be dropped, got %d patches", len(repo.patches))
	}
	if len(repo.clearRateLimitIDs) != 0 {
		t.Errorf("a stale response must not clear the rate limit")
	}
}

// Unchanged snapshots are persisted at most once per interval; material changes
// (here a whole-percent utilization change) are persisted immediately.
func TestUpdateSessionWindow_ThrottlesUnchangedSnapshots(t *testing.T) {
	repo := &sessionWindowMockRepo{}
	svc := newRateLimitServiceForTest(repo)
	resetAt := time.Now().Add(3 * time.Hour).Unix()
	headersAt := func(utilization string) http.Header {
		h := http.Header{}
		h.Set("anthropic-ratelimit-unified-5h-status", "allowed")
		h.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", resetAt))
		h.Set("anthropic-ratelimit-unified-5h-utilization", utilization)
		return h
	}
	account := &Account{ID: 8}
	t0 := time.Now()

	svc.updateSessionWindowAt(context.Background(), account, headersAt("0.200"), t0)
	svc.updateSessionWindowAt(context.Background(), account, headersAt("0.203"), t0.Add(10*time.Second))
	if len(repo.patches) != 1 {
		t.Fatalf("a sub-percent change within the interval is not persisted, got %d patches", len(repo.patches))
	}
	svc.updateSessionWindowAt(context.Background(), account, headersAt("0.210"), t0.Add(20*time.Second))
	if len(repo.patches) != 2 {
		t.Fatalf("a whole-percent change is persisted immediately, got %d patches", len(repo.patches))
	}
	svc.updateSessionWindowAt(context.Background(), account, headersAt("0.212"), t0.Add(50*time.Second))
	if len(repo.patches) != 2 {
		t.Fatalf("the interval restarts after a persisted change, got %d patches", len(repo.patches))
	}
	svc.updateSessionWindowAt(context.Background(), account, headersAt("0.212"), t0.Add(81*time.Second))
	if len(repo.patches) != 3 {
		t.Fatalf("an unchanged snapshot is persisted again once the interval passed, got %d patches", len(repo.patches))
	}
	if got := repo.patches[2].Patch.Extra["session_window_utilization"]; got != 0.212 {
		t.Errorf("the periodic write carries the latest utilization, got %v", got)
	}
}
