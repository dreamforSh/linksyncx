package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func intPtr5h(v int) *int           { return &v }
func floatPtr5h(v float64) *float64 { return &v }

func TestCodexSnapshotLacksFiveHourWindow(t *testing.T) {
	cases := []struct {
		name     string
		snapshot *OpenAICodexUsageSnapshot
		want     bool
	}{
		{name: "nil", snapshot: nil},
		{name: "empty", snapshot: &OpenAICodexUsageSnapshot{}},
		{name: "primary weekly only", snapshot: &OpenAICodexUsageSnapshot{PrimaryUsedPercent: floatPtr5h(20), PrimaryWindowMinutes: intPtr5h(10080)}, want: true},
		{name: "secondary weekly only", snapshot: &OpenAICodexUsageSnapshot{SecondaryUsedPercent: floatPtr5h(20), SecondaryWindowMinutes: intPtr5h(10080)}, want: true},
		{name: "primary 5h only", snapshot: &OpenAICodexUsageSnapshot{PrimaryUsedPercent: floatPtr5h(20), PrimaryWindowMinutes: intPtr5h(300)}},
		{name: "primary without window minutes", snapshot: &OpenAICodexUsageSnapshot{PrimaryUsedPercent: floatPtr5h(20)}},
		{name: "both windows", snapshot: &OpenAICodexUsageSnapshot{
			PrimaryUsedPercent: floatPtr5h(20), PrimaryWindowMinutes: intPtr5h(10080),
			SecondaryUsedPercent: floatPtr5h(5), SecondaryWindowMinutes: intPtr5h(300),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.snapshot.LacksFiveHourWindow())
		})
	}
}

// 账号从双窗口（Plus）变成只有周窗口（Pro）后，旧的 5h 数据不能继续显示或参与调度。
func TestBuildCodexUsageExtraUpdates_WeeklyOnlyClearsStaleFiveHour(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stale := buildCodexUsageExtraUpdates(&OpenAICodexUsageSnapshot{
		PrimaryUsedPercent: floatPtr5h(96), PrimaryWindowMinutes: intPtr5h(300), PrimaryResetAfterSeconds: intPtr5h(3600),
		SecondaryUsedPercent: floatPtr5h(25), SecondaryWindowMinutes: intPtr5h(10080), SecondaryResetAfterSeconds: intPtr5h(86400),
		PrimaryOverSecondaryPercent: floatPtr5h(10),
	}, now)
	require.Equal(t, false, stale[codexFiveHourWindowAbsentKey])

	weekly := buildCodexUsageExtraUpdates(&OpenAICodexUsageSnapshot{
		PrimaryUsedPercent: floatPtr5h(25), PrimaryWindowMinutes: intPtr5h(10080), PrimaryResetAfterSeconds: intPtr5h(86400),
	}, now)
	require.Equal(t, true, weekly[codexFiveHourWindowAbsentKey])
	for _, key := range append(append([]string(nil), codexFiveHourExtraKeys...),
		"codex_secondary_used_percent", "codex_secondary_reset_after_seconds", "codex_secondary_window_minutes",
		"codex_primary_over_secondary_percent") {
		value, ok := weekly[key]
		require.True(t, ok, "%s must be cleared explicitly", key)
		require.Nil(t, value, key)
	}
	require.Equal(t, 25.0, weekly["codex_7d_used_percent"])
	require.Equal(t, 10080, weekly["codex_primary_window_minutes"])

	// 模拟 JSONB 合并：旧快照之上叠加新快照。
	extra := map[string]any{}
	for k, v := range stale {
		extra[k] = v
	}
	for k, v := range weekly {
		extra[k] = v
	}
	require.True(t, codexFiveHourWindowAbsent(extra))
	require.Nil(t, buildCodexUsageProgressFromExtra(extra, "5h", now))
	require.Nil(t, openAIThresholdCandidate(extra, "5h", now))
	window5h, window7d := openAICanonicalQuotaWindows(extra, now)
	require.False(t, window5h.hasUsed, "stale raw secondary must not be re-normalized into a 5h window")
	require.InDelta(t, 25.0, window7d.usedPercent, 1e-9)
	require.InDelta(t, 0.75, openAIQuotaHeadroomFactor(&Account{Extra: extra}, now), 1e-9)

	// 5h 窗口重新出现时标记翻回 false。
	again := buildCodexUsageExtraUpdates(&OpenAICodexUsageSnapshot{
		PrimaryUsedPercent: floatPtr5h(30), PrimaryWindowMinutes: intPtr5h(300),
		SecondaryUsedPercent: floatPtr5h(26), SecondaryWindowMinutes: intPtr5h(10080),
	}, now)
	require.Equal(t, false, again[codexFiveHourWindowAbsentKey])
	require.Equal(t, 30.0, again["codex_5h_used_percent"])
}

func TestBuildCodexUsageExtraUpdates_LegacySnapshotKeepsFiveHourMarker(t *testing.T) {
	updates := buildCodexUsageExtraUpdates(&OpenAICodexUsageSnapshot{PrimaryUsedPercent: floatPtr5h(10)}, time.Now())
	_, ok := updates[codexFiveHourWindowAbsentKey]
	require.False(t, ok, "snapshots without window minutes cannot prove the 5h window is absent")
}

func TestBuildCodexSparkWindowExtraUpdates_WeeklyOnlyMarksFiveHourAbsent(t *testing.T) {
	updates := buildCodexSparkWindowExtraUpdates(&OpenAIQuotaUsage{
		AdditionalRateLimits: []OpenAIAdditionalRateLimit{{
			MeteredFeature: "codex_bengalfox",
			RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{
				UsedPercent: 12, LimitWindowSeconds: 604800, ResetAfterSeconds: 3600,
			}},
		}},
	}, time.Now())
	require.Equal(t, true, updates[codexFiveHourWindowAbsentKey])
	require.Nil(t, updates["codex_5h_used_percent"])
	require.InDelta(t, 12.0, updates["codex_7d_used_percent"], 1e-9)
}

type codexFiveHourAbsentUsageLogRepo struct {
	UsageLogRepository
	starts []time.Time
}

func (r *codexFiveHourAbsentUsageLogRepo) GetAccountWindowStats(_ context.Context, _ int64, start time.Time) (*usagestats.AccountStats, error) {
	r.starts = append(r.starts, start)
	return &usagestats.AccountStats{Requests: 3}, nil
}

func TestGetOpenAIUsage_FiveHourWindowAbsent(t *testing.T) {
	now := time.Now().UTC()
	repo := &codexFiveHourAbsentUsageLogRepo{}
	svc := &AccountUsageService{usageLogRepo: repo}
	account := &Account{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFiveHourWindowAbsentKey: true,
			"codex_5h_used_percent":      nil,
			"codex_7d_used_percent":      40.0,
			"codex_7d_reset_at":          now.Add(48 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at":     now.Format(time.RFC3339),
		},
	}
	usage, err := svc.getOpenAIUsage(context.Background(), account, false)
	require.NoError(t, err)
	require.True(t, usage.FiveHourWindowAbsent)
	require.Nil(t, usage.FiveHour, "no fabricated 0% 5h bar")
	require.NotNil(t, usage.SevenDay)
	require.InDelta(t, 40.0, usage.SevenDay.Utilization, 1e-9)
	require.NotNil(t, usage.SevenDay.WindowStats)
	require.Len(t, repo.starts, 1, "only the 7d window stats are queried")
	require.False(t, shouldRefreshOpenAICodexSnapshot(account, usage, now), "absent 5h must not force a probe on every load")

	body, err := json.Marshal(usage)
	require.NoError(t, err)
	require.Contains(t, string(body), `"five_hour_window_absent":true`)
	require.Contains(t, string(body), `"five_hour":null`)

	view := groupAccountUsageView(account, usage, 0, false, now)
	require.True(t, view.FiveHourAbsent)
	require.Nil(t, view.FiveHour)
	require.False(t, view.UsageUnavailable)
}

func TestGetOpenAIUsage_UnknownFiveHourStillShowsLocalWindow(t *testing.T) {
	repo := &codexFiveHourAbsentUsageLogRepo{}
	svc := &AccountUsageService{usageLogRepo: repo}
	account := &Account{
		ID:       8,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"codex_5h_used_percent": 10.0,
			"codex_7d_used_percent": 20.0,
		},
	}
	usage, err := svc.getOpenAIUsage(context.Background(), account, false)
	require.NoError(t, err)
	require.False(t, usage.FiveHourWindowAbsent)
	require.NotNil(t, usage.FiveHour)
	require.InDelta(t, 10.0, usage.FiveHour.Utilization, 1e-9)
	require.Len(t, repo.starts, 2)
}
