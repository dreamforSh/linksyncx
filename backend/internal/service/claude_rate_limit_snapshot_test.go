package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func claudeRateLimitHeaders(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(claudeRateLimitHeaderPrefix+pairs[i], pairs[i+1])
	}
	return h
}

func TestParseClaudeRateLimitSnapshotFullHeaderSet(t *testing.T) {
	receivedAt := time.UnixMilli(1_800_000_000_123)
	headers := claudeRateLimitHeaders(
		"status", "allowed_warning",
		"reset", "1800003600",
		"representative-claim", "seven_day",
		"fallback", "available",
		"upgrade-paths", "max_5x, max_20x ,",
		"5h-status", "allowed",
		"5h-utilization", "0.42",
		"5h-reset", "1800001800",
		"7d-status", "allowed_warning",
		"7d-utilization", "0.81",
		"7d-reset", "1800300000",
		"7d-surpassed-threshold", "0.75",
		"7d_oi-utilization", "0.3",
		"7d_oi-reset", "1800400000",
		"overage-status", "rejected",
		"overage-reset", "1802000000",
		"overage-disabled-reason", "org_level_disabled",
		"overage-scope", "org",
		"overage-in-use", "true",
		"overage-period-monthly-utilization", "0.2",
		"overage-period-channel-utilization", "0.1",
		"grace-5h-utilization", "0.05",
		"grace-7d-utilization", "1.7",
		"slow-status", "active",
		"slow-offer", "treatment",
		"slow-retry-after", "30",
		"slow-max-wait", "600",
		"slow-budget-utilization", "1.4",
		"slow-budget-reset", "1800500000",
	)

	snap := ParseClaudeRateLimitSnapshot(headers, receivedAt)
	require.NotNil(t, snap)
	require.Equal(t, int64(1_800_000_000_123), snap.AppliedAtMs)
	require.Equal(t, "allowed_warning", snap.Status)
	require.Equal(t, int64(1800003600), snap.ResetAt)
	require.Equal(t, "seven_day", snap.RepresentativeClaim)
	require.True(t, snap.FallbackAvailable)
	require.Equal(t, []string{"max_5x", "max_20x"}, snap.UpgradePaths)

	require.Len(t, snap.Windows, 4)
	require.Equal(t, ClaudeRateLimitWindow{ResetAt: 1802000000}, snap.Windows["overage"],
		"the overage window shares overage-reset like the CLI; overage-status belongs to the overage block only")
	require.Equal(t, "allowed", snap.Windows["5h"].Status)
	require.InDelta(t, 0.42, *snap.Windows["5h"].Utilization, 1e-9)
	require.Equal(t, int64(1800001800), snap.Windows["5h"].ResetAt)
	require.Nil(t, snap.Windows["5h"].SurpassedThreshold)
	require.InDelta(t, 0.75, *snap.Windows["7d"].SurpassedThreshold, 1e-9)
	require.Equal(t, int64(1800400000), snap.Windows["7d_oi"].ResetAt)

	require.Equal(t, &ClaudeRateLimitOverage{
		Status: "rejected", ResetAt: 1802000000, DisabledReason: "org_level_disabled", Scope: "org", InUse: true,
		MonthlyUtilization: float64Ptr(0.2), ChannelUtilization: float64Ptr(0.1),
	}, snap.Overage)
	require.Equal(t, &ClaudeRateLimitGrace{FiveHourUtilization: 0.05, SevenDayUtilization: 1}, snap.Grace, "grace utilization is clamped to [0, 1]")
	require.Equal(t, "active", snap.Slow.Status)
	require.Equal(t, "treatment", snap.Slow.Offer)
	require.InDelta(t, 30, *snap.Slow.RetryAfterSeconds, 1e-9)
	require.InDelta(t, 600, *snap.Slow.MaxWaitSeconds, 1e-9)
	require.InDelta(t, 1, *snap.Slow.BudgetUtilization, 1e-9, "budget utilization is capped at 1")
	require.Equal(t, int64(1800500000), snap.Slow.BudgetResetAt)

	// 落库与热存储都存 JSON：往返后不丢字段。
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	var decoded ClaudeRateLimitSnapshot
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, *snap, decoded)
}

// 与真实 CLI 一致：数字字段按数字解析，非数字视为缺失；未知的低优先级状态记为 unrecognized。
func TestParseClaudeRateLimitSnapshotNormalizesValues(t *testing.T) {
	snap := ParseClaudeRateLimitSnapshot(claudeRateLimitHeaders(
		"5h-surpassed-threshold", "true",
		"5h-utilization", "abc",
		"5h-reset", "1800001800000",
		"7d-utilization", "NaN",
		"slow-status", "mystery",
		"slow-offer", "other",
		"slow-retry-after", "-5",
		"fallback", "unavailable",
	), time.Now())
	require.NotNil(t, snap)
	require.Nil(t, snap.Windows["5h"].SurpassedThreshold)
	require.Nil(t, snap.Windows["5h"].Utilization)
	require.Equal(t, int64(1800001800), snap.Windows["5h"].ResetAt, "millisecond timestamps are converted to seconds")
	_, has7d := snap.Windows["7d"]
	require.False(t, has7d, "a window with only invalid values is dropped")
	require.Equal(t, "unrecognized", snap.Slow.Status)
	require.Empty(t, snap.Slow.Offer)
	require.Nil(t, snap.Slow.RetryAfterSeconds)
	require.False(t, snap.FallbackAvailable)
	require.Nil(t, snap.Grace)
	require.Nil(t, snap.Overage)

	require.Nil(t, ParseClaudeRateLimitSnapshot(http.Header{"Content-Type": {"text/event-stream"}}, time.Now()))
	require.Nil(t, ParseClaudeRateLimitSnapshot(nil, time.Now()))
}

func TestClaudeRateLimitMaterialChange(t *testing.T) {
	base := func() *ClaudeRateLimitSnapshot {
		return &ClaudeRateLimitSnapshot{
			Status: "allowed", ResetAt: 100,
			Windows: map[string]ClaudeRateLimitWindow{
				"5h": {Status: "allowed", Utilization: float64Ptr(0.204), ResetAt: 100},
				"7d": {Utilization: float64Ptr(0.5), ResetAt: 200},
			},
		}
	}
	require.True(t, claudeRateLimitMaterialChange(nil, base()), "the first observation is always persisted")
	require.False(t, claudeRateLimitMaterialChange(base(), base()))

	mutations := map[string]func(*ClaudeRateLimitSnapshot){
		"status":        func(s *ClaudeRateLimitSnapshot) { s.Status = "allowed_warning" },
		"reset":         func(s *ClaudeRateLimitSnapshot) { s.ResetAt = 101 },
		"claim":         func(s *ClaudeRateLimitSnapshot) { s.RepresentativeClaim = "seven_day" },
		"window status": func(s *ClaudeRateLimitSnapshot) { w := s.Windows["5h"]; w.Status = "rejected"; s.Windows["5h"] = w },
		"window reset":  func(s *ClaudeRateLimitSnapshot) { w := s.Windows["7d"]; w.ResetAt = 201; s.Windows["7d"] = w },
		"whole percent": func(s *ClaudeRateLimitSnapshot) {
			w := s.Windows["5h"]
			w.Utilization = float64Ptr(0.21)
			s.Windows["5h"] = w
		},
		"surpassed": func(s *ClaudeRateLimitSnapshot) {
			w := s.Windows["7d"]
			w.SurpassedThreshold = float64Ptr(0.75)
			s.Windows["7d"] = w
		},
		"new window":         func(s *ClaudeRateLimitSnapshot) { s.Windows["7d_oi"] = ClaudeRateLimitWindow{ResetAt: 300} },
		"overage status":     func(s *ClaudeRateLimitSnapshot) { s.Overage = &ClaudeRateLimitOverage{Status: "allowed"} },
		"grace zone entered": func(s *ClaudeRateLimitSnapshot) { s.Grace = &ClaudeRateLimitGrace{FiveHourUtilization: 0.1} },
		"slow lane":          func(s *ClaudeRateLimitSnapshot) { s.Slow = &ClaudeRateLimitSlow{Status: "active"} },
	}
	for name, mutate := range mutations {
		next := base()
		mutate(next)
		require.True(t, claudeRateLimitMaterialChange(base(), next), name)
	}

	subPercent := base()
	subPercent.Windows["5h"] = ClaudeRateLimitWindow{Status: "allowed", Utilization: float64Ptr(0.2049), ResetAt: 100}
	subPercent.AppliedAtMs = 999
	require.False(t, claudeRateLimitMaterialChange(base(), subPercent), "sub-percent utilization drift waits for the periodic write")
}

// 编辑弹窗回传的 extra 带着打开弹窗时的旧快照：服务端保留库中的当前快照，栅栏时间不会倒退。
func TestUpdateAccountPreservesClaudeRateLimitSnapshot(t *testing.T) {
	accountID := int64(113)
	current := map[string]any{"applied_at_ms": float64(2000), "status": "allowed_warning"}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {ID: accountID, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive,
			Extra: map[string]any{claudeRateLimitExtraKey: current}},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{
			"custom":                "value",
			claudeRateLimitExtraKey: map[string]any{"applied_at_ms": float64(1000), "status": "allowed"},
		},
	})

	require.NoError(t, err)
	require.Equal(t, current, updated.Extra[claudeRateLimitExtraKey])
	require.Equal(t, "value", updated.Extra["custom"])
}

// 落库后从 accounts.extra 读到的是 JSON 解出的 map，测试按同样的形态构造账号。
func claudeRateLimitAccount(t *testing.T, snap *ClaudeRateLimitSnapshot) *Account {
	t.Helper()
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{claudeRateLimitExtraKey: decoded}}
}

func TestClaudeRateLimitUnderPressure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	fresh := now.Add(-time.Minute).UnixMilli()
	future := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	futureReset, _ := strconv.ParseInt(future, 10, 64)
	pastReset := now.Add(-time.Minute).Unix()

	cases := []struct {
		name string
		snap *ClaudeRateLimitSnapshot
		want bool
	}{
		{"healthy", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Status: "allowed"}, false},
		{"account-wide warning", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Status: "allowed_warning", RepresentativeClaim: "five_hour"}, true},
		{"model-scoped warning is ignored", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Status: "allowed_warning", RepresentativeClaim: "seven_day_opus"}, false},
		{"7d surpassed threshold", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Windows: map[string]ClaudeRateLimitWindow{
			"7d": {SurpassedThreshold: float64Ptr(0.75), ResetAt: futureReset}}}, true},
		{"surpassed threshold of a window that already reset", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Windows: map[string]ClaudeRateLimitWindow{
			"7d": {SurpassedThreshold: float64Ptr(0.75), ResetAt: pastReset}}}, false},
		{"Fable-only window is ignored", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Windows: map[string]ClaudeRateLimitWindow{
			"7d_oi": {Status: "allowed_warning", SurpassedThreshold: float64Ptr(0.9)}}}, false},
		{"grace zone", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Grace: &ClaudeRateLimitGrace{SevenDayUtilization: 0.1}}, true},
		{"grace zone of a reset window", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Grace: &ClaudeRateLimitGrace{FiveHourUtilization: 0.1},
			Windows: map[string]ClaudeRateLimitWindow{"5h": {ResetAt: pastReset}}}, false},
		{"slow lane", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Slow: &ClaudeRateLimitSlow{Status: "active"}}, true},
		{"slow lane not needed", &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Slow: &ClaudeRateLimitSlow{Status: "not_needed"}}, false},
		{"stale snapshot", &ClaudeRateLimitSnapshot{AppliedAtMs: now.Add(-2 * time.Hour).UnixMilli(), Slow: &ClaudeRateLimitSlow{Status: "active"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claudeRateLimitUnderPressure(claudeRateLimitAccount(t, tc.snap), now))
		})
	}

	require.False(t, claudeRateLimitUnderPressure(&Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, now), "no snapshot")
	apiKey := claudeRateLimitAccount(t, &ClaudeRateLimitSnapshot{AppliedAtMs: fresh, Slow: &ClaudeRateLimitSlow{Status: "active"}})
	apiKey.Type = AccountTypeAPIKey
	require.False(t, claudeRateLimitUnderPressure(apiKey, now), "only Claude OAuth / setup-token accounts carry these signals")
}
