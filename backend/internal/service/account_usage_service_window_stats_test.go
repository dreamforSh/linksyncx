package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type windowStatsRecordingRepo struct {
	UsageLogRepository
	mu     sync.Mutex
	starts []time.Time
}

func (r *windowStatsRecordingRepo) GetAccountWindowStats(_ context.Context, _ int64, start time.Time) (*usagestats.AccountStats, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.starts = append(r.starts, start)
	// 按查询起点区分 5h / 7d 两份统计，便于断言各自落到哪个窗口
	if time.Since(start) > 24*time.Hour {
		return &usagestats.AccountStats{Requests: 70, Tokens: 7000, Cost: 12.5}, nil
	}
	return &usagestats.AccountStats{Requests: 5, Tokens: 500, Cost: 1.25}, nil
}

func TestAddWindowStats_AttachesSevenDayStatsAlignedToReset(t *testing.T) {
	repo := &windowStatsRecordingRepo{}
	svc := &AccountUsageService{usageLogRepo: repo, cache: NewUsageCache()}
	account := &Account{ID: 42, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	resetsAt := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	usage := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 10},
		SevenDay: &UsageProgress{Utilization: 40, ResetsAt: &resetsAt},
	}

	svc.addWindowStats(context.Background(), account, usage)

	require.NotNil(t, usage.FiveHour.WindowStats)
	require.Equal(t, 1.25, usage.FiveHour.WindowStats.Cost)
	require.NotNil(t, usage.SevenDay.WindowStats)
	require.Equal(t, 12.5, usage.SevenDay.WindowStats.Cost)
	require.Equal(t, int64(70), usage.SevenDay.WindowStats.Requests)
	require.Contains(t, repo.starts, resetsAt.Add(-7*24*time.Hour))

	// 1 分钟内再次查询走缓存，不重复打库
	again := &UsageInfo{
		FiveHour: &UsageProgress{Utilization: 10},
		SevenDay: &UsageProgress{Utilization: 40, ResetsAt: &resetsAt},
	}
	svc.addWindowStats(context.Background(), account, again)
	require.Len(t, repo.starts, 2)
	require.Equal(t, 12.5, again.SevenDay.WindowStats.Cost)
}

func TestAddWindowStats_SevenDayFallsBackToRollingWindowWithoutReset(t *testing.T) {
	repo := &windowStatsRecordingRepo{}
	svc := &AccountUsageService{usageLogRepo: repo, cache: NewUsageCache()}
	account := &Account{ID: 7, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	usage := &UsageInfo{SevenDay: &UsageProgress{Utilization: 20}}
	before := time.Now()
	svc.addWindowStats(context.Background(), account, usage)

	require.Len(t, repo.starts, 1)
	require.WithinDuration(t, before.Add(-7*24*time.Hour), repo.starts[0], time.Minute)
	require.NotNil(t, usage.SevenDay.WindowStats)
	require.Nil(t, usage.FiveHour)
}

func TestAntigravityQuotaGroupsKeepSummaryWindowOnly(t *testing.T) {
	remaining := 0.5
	groups := quotaGroupsFromSummary(&antigravity.RetrieveUserQuotaSummaryResponse{
		Buckets: []antigravity.QuotaSummaryBucket{{
			DisplayName: "Claude", RemainingFraction: &remaining,
			ResetTime: "2099-01-01T00:00:00Z", Window: " 7d ",
		}},
	})
	require.Len(t, groups, 1)
	require.Equal(t, "7d", groups[0].Window)
	require.Nil(t, groups[0].WindowStats)

	derived := deriveAntigravityQuotaGroups(map[string]antigravity.ModelInfo{
		"gemini-test": {DisplayName: "Gemini Test", QuotaInfo: &antigravity.ModelQuotaInfo{RemainingFraction: 0.25, ResetTime: "2099-01-01T00:00:00Z"}},
	})
	require.Len(t, derived, 1)
	require.Empty(t, derived[0].Window)
}

func TestWithAntigravityWindowStats(t *testing.T) {
	repo := &windowStatsRecordingRepo{}
	svc := &AccountUsageService{usageLogRepo: repo, cache: NewUsageCache()}
	account := &Account{ID: 9, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	ctx := context.Background()
	now := time.Now().UTC()
	soon := now.Add(2 * time.Hour).Truncate(time.Second)
	far := now.Add(48 * time.Hour).Truncate(time.Second)
	five := &UsageProgress{}
	cached := []AntigravityQuotaGroup{
		{Label: "soon", RemainingFraction: 0.4, ResetTime: soon.Format(time.RFC3339)},
		{Label: "week", RemainingFraction: 0.2, ResetTime: far.Format(time.RFC3339), Window: "7d"},
		{Label: "far-empty", RemainingFraction: 0.1, ResetTime: far.Format(time.RFC3339)},
		{Label: "too-long", RemainingFraction: 0.3, ResetTime: soon.Format(time.RFC3339), Window: "768h"},
	}
	usage := &UsageInfo{FiveHour: five, AntigravityQuotaGroups: cached}
	out := svc.withAntigravityWindowStats(ctx, account, usage)

	require.Nil(t, cached[0].WindowStats)
	require.Nil(t, cached[1].WindowStats)
	require.Nil(t, five.WindowStats)
	require.NotSame(t, usage, out)
	require.Same(t, five, out.FiveHour)
	require.NotNil(t, out.AntigravityQuotaGroups[0].WindowStats)
	require.Equal(t, 1.25, out.AntigravityQuotaGroups[0].WindowStats.Cost)
	require.Equal(t, 12.5, out.AntigravityQuotaGroups[1].WindowStats.Cost)
	require.Nil(t, out.AntigravityQuotaGroups[2].WindowStats)
	require.Nil(t, out.AntigravityQuotaGroups[3].WindowStats)
	require.Len(t, repo.starts, 2)
	require.WithinDuration(t, soon.Add(-5*time.Hour), repo.starts[0], time.Second)
	require.WithinDuration(t, far.Add(-7*24*time.Hour), repo.starts[1], time.Second)

	parsed, ok := antigravityQuotaWindowStart("18000s", soon.Format(time.RFC3339), now)
	require.True(t, ok)
	require.True(t, parsed.Equal(soon.Add(-5*time.Hour)))
	_, ok = antigravityQuotaWindowStart("768h", soon.Format(time.RFC3339), now)
	require.False(t, ok)

	sameReset := soon.Format(time.RFC3339)
	repo2 := &windowStatsRecordingRepo{}
	svc2 := &AccountUsageService{usageLogRepo: repo2, cache: NewUsageCache()}
	groups := []AntigravityQuotaGroup{
		{Label: "a", RemainingFraction: 0.5, ResetTime: sameReset, Window: "5h"},
		{Label: "b", RemainingFraction: 0.25, ResetTime: sameReset, Window: "five_hour"},
	}
	out2 := svc2.withAntigravityWindowStats(ctx, account, &UsageInfo{AntigravityQuotaGroups: groups})
	require.Len(t, repo2.starts, 1)
	require.Same(t, out2.AntigravityQuotaGroups[0].WindowStats, out2.AntigravityQuotaGroups[1].WindowStats)
	out3 := svc2.withAntigravityWindowStats(ctx, account, &UsageInfo{AntigravityQuotaGroups: groups})
	require.Len(t, repo2.starts, 1)
	require.Equal(t, 1.25, out3.AntigravityQuotaGroups[0].WindowStats.Cost)

	untimed := &UsageInfo{AntigravityQuotaGroups: []AntigravityQuotaGroup{{Label: "x", ResetTime: far.Format(time.RFC3339)}}}
	require.Same(t, untimed, svc2.withAntigravityWindowStats(ctx, account, untimed))

	repo3 := &windowStatsRecordingRepo{}
	direct := &AccountUsageService{usageLogRepo: repo3}
	out4 := direct.withAntigravityWindowStats(ctx, account, &UsageInfo{AntigravityQuotaGroups: groups[:1]})
	require.Len(t, repo3.starts, 1)
	require.Equal(t, 1.25, out4.AntigravityQuotaGroups[0].WindowStats.Cost)
}
