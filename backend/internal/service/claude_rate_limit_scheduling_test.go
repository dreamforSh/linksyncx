//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func claudeRateLimitExtra(t *testing.T, snap *ClaudeRateLimitSnapshot) map[string]any {
	t.Helper()
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return map[string]any{claudeRateLimitExtraKey: decoded}
}

// 两个同优先级的 OAuth 账号：账号 1 从未使用（LRU 本应选它），但限流快照显示它进入了低优先级通道；
// 账号 2 健康。新对话应选账号 2，已绑定在账号 1 的对话仍留在账号 1。
func newClaudeRateLimitSchedulerFixture(t *testing.T, bindings map[string]int64) (*GatewayService, context.Context, int64) {
	t.Helper()
	groupID := int64(10)
	usedAt := time.Now().Add(-time.Minute)
	pressured := claudeRateLimitExtra(t, &ClaudeRateLimitSnapshot{
		AppliedAtMs: time.Now().UnixMilli(), Status: "allowed", Slow: &ClaudeRateLimitSlow{Status: "active"},
	})
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, Extra: pressured},
			{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, LastUsedAt: &usedAt},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
		cache:              &mockGatewayCacheForPlatform{sessionBindings: bindings},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
	}
	return svc, context.WithValue(context.Background(), ctxkey.Group, group), groupID
}

func TestSelectAccountWithLoadAwareness_PrefersAccountsWithoutRateLimitPressure(t *testing.T) {
	svc, ctx, groupID := newClaudeRateLimitSchedulerFixture(t, map[string]int64{"bound": 1})

	result, err := svc.SelectAccountWithLoadAwareness(ctx, &groupID, "fresh", "", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(2), result.Account.ID, "a new conversation avoids the account in the low-priority lane")

	result, err = svc.SelectAccountWithLoadAwareness(ctx, &groupID, "bound", "", nil, "", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Account.ID, "a bound conversation is not moved by soft signals")
}

func TestFilterByClaudeRateLimitHealthOnlyLowersWeight(t *testing.T) {
	now := time.Now()
	pressured := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Extra: claudeRateLimitExtra(t, &ClaudeRateLimitSnapshot{AppliedAtMs: now.UnixMilli(), Grace: &ClaudeRateLimitGrace{FiveHourUtilization: 0.2}})}
	healthy := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	got := filterByClaudeRateLimitHealth([]accountWithLoad{{account: pressured}, {account: healthy}}, now)
	require.Len(t, got, 1)
	require.Equal(t, int64(2), got[0].account.ID)

	only := filterByClaudeRateLimitHealth([]accountWithLoad{{account: pressured}}, now)
	require.Len(t, only, 1, "an account under pressure is still selectable when it is the only candidate")
	both := filterByClaudeRateLimitHealth([]accountWithLoad{{account: pressured}, {account: pressured}}, now)
	require.Len(t, both, 2)
}

// claudeRateLimitPatchRepo 记录 429 路径落库的限流快照。
type claudeRateLimitPatchRepo struct {
	anthropicWindowLimitRepo
	patches []ClaudeRateLimitPatch
}

func (r *claudeRateLimitPatchRepo) ApplyClaudeRateLimitPatch(_ context.Context, _ int64, patch ClaudeRateLimitPatch) (bool, error) {
	r.patches = append(r.patches, patch)
	return true, nil
}

// 429 响应带回的统一限流头写入同一份快照，但只落库快照本身；窗口列与限流状态仍由 429 处理负责。
func TestHandleUpstreamError_429RecordsRateLimitSnapshot(t *testing.T) {
	repo := &claudeRateLimitPatchRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	resetAt := time.Now().Add(3 * time.Hour).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt, 10))

	svc.HandleUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(`{"type":"error"}`))

	require.Len(t, repo.patches, 1)
	patch := repo.patches[0]
	require.Empty(t, patch.SessionWindowStatus, "the 429 handler owns the session window columns")
	require.Nil(t, patch.SessionWindowStart)
	require.Len(t, patch.Extra, 1)
	snap, ok := patch.Extra[claudeRateLimitExtraKey].(*ClaudeRateLimitSnapshot)
	require.True(t, ok)
	require.Equal(t, "rejected", snap.Status)
	require.Equal(t, "five_hour", snap.RepresentativeClaim)
	require.Equal(t, 1, repo.rateLimitCalls, "the existing 429 handling still marks the account rate limited")
}
