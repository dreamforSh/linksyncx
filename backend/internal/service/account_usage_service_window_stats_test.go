package service

import (
	"context"
	"sync"
	"testing"
	"time"

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
