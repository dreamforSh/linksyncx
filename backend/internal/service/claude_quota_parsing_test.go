package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClaudeFableUsageFromLimits(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(72 * time.Hour)

	t.Run("weekly scoped fable entry with unix seconds", func(t *testing.T) {
		raw := json.RawMessage(`[
			{"kind":"weekly_scoped","scope":{"model":{"display_name":"Opus"}},"percent":90,"resets_at":1},
			{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":42.5,"resets_at":` + jsonInt(resetAt.Unix()) + `}
		]`)
		progress := claudeFableUsageFromLimits(raw, now)
		require.NotNil(t, progress)
		require.InDelta(t, 42.5, progress.Utilization, 1e-9)
		require.NotNil(t, progress.ResetsAt)
		require.True(t, progress.ResetsAt.Equal(resetAt))
		require.Equal(t, int((72 * time.Hour).Seconds()), progress.RemainingSeconds)
	})

	t.Run("iso reset, string percent and milliseconds", func(t *testing.T) {
		raw := json.RawMessage(`[
			{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5"}},"percent":"12","resets_at":"` + resetAt.Format(time.RFC3339) + `"},
			{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable 5.1"}},"percent":30,"resets_at":` + jsonInt(resetAt.UnixMilli()) + `}
		]`)
		progress := claudeFableUsageFromLimits(raw, now)
		require.NotNil(t, progress)
		require.InDelta(t, 30, progress.Utilization, 1e-9, "the most constrained Fable entry wins")
		require.True(t, progress.ResetsAt.Equal(resetAt))
	})

	t.Run("ignores malformed and unrelated entries", func(t *testing.T) {
		require.Nil(t, claudeFableUsageFromLimits(nil, now))
		require.Nil(t, claudeFableUsageFromLimits(json.RawMessage(`null`), now))
		require.Nil(t, claudeFableUsageFromLimits(json.RawMessage(`{"kind":"weekly_scoped"}`), now))
		require.Nil(t, claudeFableUsageFromLimits(json.RawMessage(`[
			"oops",
			{"kind":"daily","scope":{"model":{"display_name":"Fable"}},"percent":5},
			{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}}},
			{"kind":"weekly_scoped","scope":{},"percent":5}
		]`), now))
	})
}

func TestBuildUsageInfo_FablePrefersLimitsOverOverageIncluded(t *testing.T) {
	svc := &AccountUsageService{}
	now := time.Now()
	limitsReset := now.Add(48 * time.Hour).Truncate(time.Second)

	var resp ClaudeUsageResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"five_hour": {"utilization": 10, "resets_at": "`+now.Add(time.Hour).UTC().Format(time.RFC3339)+`"},
		"seven_day_overage_included": {"utilization": 99, "resets_at": "`+now.Add(24*time.Hour).UTC().Format(time.RFC3339)+`"},
		"limits": [{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":7,"resets_at":`+jsonInt(limitsReset.Unix())+`}]
	}`), &resp))

	info := svc.buildUsageInfo(&resp, &now)
	require.NotNil(t, info.SevenDayFable)
	require.InDelta(t, 7, info.SevenDayFable.Utilization, 1e-9)
	require.True(t, info.SevenDayFable.ResetsAt.Equal(limitsReset))
}

func TestParseClaudeOAuthProfile_PlanTypes(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		body string
		plan string
	}{
		{"max 20x", `{"organization":{"uuid":"org-1","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x"}}`, ClaudePlanMax20x},
		{"max 5x", `{"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_5x"}}`, ClaudePlanMax5x},
		{"max unknown tier", `{"organization":{"organization_type":"claude_max"}}`, ClaudePlanMax},
		{"pro", `{"organization":{"organization_type":"claude_pro","rate_limit_tier":"default_claude_ai"}}`, ClaudePlanPro},
		{"team premium", `{"organization":{"organization_type":"claude_team","rate_limit_tier":"default_claude_max_5x"}}`, ClaudePlanTeamPremium},
		{"team standard", `{"organization":{"organization_type":"claude_team","rate_limit_tier":"default_claude_ai"}}`, ClaudePlanTeamStandard},
		// 席位只看 rate_limit_tier：seat_tier 写着 premium 也不算高级席
		{"team seat_tier ignored", `{"organization":{"organization_type":"claude_team","rate_limit_tier":"default_claude_ai","seat_tier":"premium"}}`, ClaudePlanTeamStandard},
		{"team unknown seat", `{"organization":{"organization_type":"claude_team"}}`, ClaudePlanTeam},
		{"enterprise", `{"organization":{"organization_type":"claude_enterprise"}}`, ClaudePlanEnterprise},
		{"account flags", `{"account":{"has_claude_max":true},"organization":{"rate_limit_tier":"default_claude_max_20x"}}`, ClaudePlanMax20x},
		{"pro flag", `{"account":{"has_claude_max":false,"has_claude_pro":true},"organization":{}}`, ClaudePlanPro},
		{"free", `{"account":{},"organization":{}}`, ClaudePlanFree},
		{"unknown org type", `{"organization":{"organization_type":"claude_student"}}`, "student"},
		{"malformed optional fields", `{"account":{"has_claude_max":"yes"},"organization":{"organization_type":"claude_pro","seat_tier":{"x":1},"has_extra_usage_enabled":"no"}}`, ClaudePlanPro},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, _, err := parseClaudeOAuthProfile([]byte(tc.body), now)
			require.NoError(t, err)
			require.Equal(t, tc.plan, info.PlanType)
			require.Equal(t, now.Format(time.RFC3339), info.UpdatedAt)
		})
	}

	info, orgUUID, err := parseClaudeOAuthProfile([]byte(`{"organization":{"uuid":"org-1","organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x","billing_type":"stripe_subscription","has_extra_usage_enabled":true}}`), now)
	require.NoError(t, err)
	require.Equal(t, "org-1", orgUUID)
	require.Equal(t, "claude_max", info.OrganizationType)
	require.Equal(t, "default_claude_max_20x", info.RateLimitTier)
	require.Equal(t, "stripe_subscription", info.BillingType)
	require.NotNil(t, info.ExtraUsageEnabled)
	require.True(t, *info.ExtraUsageEnabled)

	_, _, err = parseClaudeOAuthProfile([]byte(`not json`), now)
	require.Error(t, err)
}

func TestClaudeSubscriptionExtraRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	info := &ClaudeSubscriptionInfo{PlanType: ClaudePlanMax5x, RateLimitTier: "default_claude_max_5x", UpdatedAt: now.Add(-time.Hour).Format(time.RFC3339)}

	// 进程内合并的结构体与数据库读出的 map 都能读取
	require.Equal(t, ClaudePlanMax5x, readClaudeSubscription(map[string]any{claudeSubscriptionExtraKey: info}).PlanType)
	data, err := json.Marshal(info)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	extra := map[string]any{claudeSubscriptionExtraKey: decoded}
	require.Equal(t, ClaudePlanMax5x, readClaudeSubscription(extra).PlanType)

	require.False(t, claudeSubscriptionIsStale(extra, now))
	require.True(t, claudeSubscriptionIsStale(extra, now.Add(24*time.Hour)))
	require.True(t, claudeSubscriptionIsStale(nil, now))
	require.True(t, claudeSubscriptionIsStale(map[string]any{claudeSubscriptionExtraKey: map[string]any{"plan_type": "pro", "updated_at": "bad"}}, now))

	usage := &UsageInfo{}
	applyClaudeSubscriptionToUsage(usage, extra)
	require.Equal(t, "Max 5x", usage.SubscriptionTier)
	require.Equal(t, "default_claude_max_5x", usage.SubscriptionTierRaw)

	untouched := &UsageInfo{}
	applyClaudeSubscriptionToUsage(untouched, map[string]any{})
	require.Empty(t, untouched.SubscriptionTier)

	teamPremium := &UsageInfo{}
	applyClaudeSubscriptionToUsage(teamPremium, map[string]any{claudeSubscriptionExtraKey: map[string]any{
		"plan_type": ClaudePlanTeamPremium, "rate_limit_tier": "default_claude_max_5x", "updated_at": now.Format(time.RFC3339),
	}})
	require.Equal(t, "Team Premium", teamPremium.SubscriptionTier)
	require.Equal(t, "Team Standard", claudePlanDisplayName(ClaudePlanTeamStandard))
}

func TestClaudeSubscriptionIsStale_LegacyTeamSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	snapshot := func(plan, orgType, tier string) map[string]any {
		return map[string]any{claudeSubscriptionExtraKey: map[string]any{
			"plan_type": plan, "organization_type": orgType, "rate_limit_tier": tier, "updated_at": now.Format(time.RFC3339),
		}}
	}
	// 区分席位前写入的 Team 快照带着 tier：立即重拉
	require.True(t, claudeSubscriptionIsStale(snapshot(ClaudePlanTeam, "claude_team", "default_claude_max_5x"), now))
	// 上游本就没给 tier：重拉也分不出席位，按正常周期
	require.False(t, claudeSubscriptionIsStale(snapshot(ClaudePlanTeam, "claude_team", ""), now))
	require.False(t, claudeSubscriptionIsStale(snapshot(ClaudePlanTeamPremium, "claude_team", "default_claude_max_5x"), now))
	require.False(t, claudeSubscriptionIsStale(snapshot(ClaudePlanTeamStandard, "claude_team", "default_claude_ai"), now))
}

func TestParseClaudeCedarEmberStatus(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(20 * 24 * time.Hour).Format(time.RFC3339)
	past := now.Add(-time.Hour).Format(time.RFC3339)

	status := parseClaudeCedarEmberStatus(json.RawMessage(`{
		"eligible": true,
		"at_limit": false,
		"exhausted": ["seven_day"],
		"next_grant_id": "grant_a",
		"weekly_resets_at": "2026-10-01T00:00:00Z",
		"event_props": {"tier": "claude_max_20x", "surface": "claude_code_cli"},
		"grants": [
			{"id":"grant_a","label":"Opus 5.5 launch","resets_total":1,"resets_left":1,"ends_at":"` + future + `","clears":["five_hour","seven_day"],"usable_now":true,"use_requires_limit":false},
			{"id":"grant_b","resets_left":2,"ends_at":"` + past + `","clears":["five_hour"]},
			{"id":"BAD ID","resets_left":1},
			{"id":"grant_c","resets_left":1.5},
			{"id":"grant_d"}
		]
	}`))
	require.NotNil(t, status)
	require.True(t, status.Eligible)
	require.Equal(t, "claude_max_20x", status.Tier)
	require.Equal(t, []string{"seven_day"}, status.Exhausted)
	require.Len(t, status.Grants, 2, "grants with invalid ids or counts are dropped")

	first := status.Grants[0]
	require.True(t, first.Next)
	require.Equal(t, "grant_a", first.id)
	require.Equal(t, 1, first.ResetsTotal)
	require.False(t, first.UseRequiresLimit)
	require.True(t, status.Grants[1].UseRequiresLimit, "use_requires_limit defaults to true")
	require.False(t, status.Grants[1].Next)

	// grant id 不会被序列化到快照
	data, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(data), "grant_a")

	grant, err := status.nextClaimableGrant(now)
	require.NoError(t, err)
	require.Equal(t, "grant_a", grant.id)

	snapshot := &ClaudeResetSnapshot{CedarEmber: status}
	require.Equal(t, 1, snapshot.availableCount(now), "expired grants are not counted")

	require.Nil(t, parseClaudeCedarEmberStatus(json.RawMessage(`{"grants":[]}`)), "eligible is required")
	require.Nil(t, parseClaudeCedarEmberStatus(json.RawMessage(`null`)))
	require.Nil(t, parseClaudeCedarEmberStatus(json.RawMessage(`[1]`)))

	ineligible := parseClaudeCedarEmberStatus(json.RawMessage(`{"eligible":false,"ineligible_reason":"surface","grants":"nope"}`))
	require.NotNil(t, ineligible)
	require.Equal(t, "surface", ineligible.IneligibleReason)
	require.Empty(t, ineligible.Grants)
	_, err = ineligible.nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
}

func TestClaudeCedarEmberNextClaimableGrant(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour).Format(time.RFC3339)
	grant := func(mutate func(*ClaudeResetGrant)) *ClaudeCedarEmberStatus {
		g := ClaudeResetGrant{id: "g1", ResetsLeft: 1, EndsAt: future, Clears: []string{"five_hour"}, UsableNow: true, Next: true}
		mutate(&g)
		return &ClaudeCedarEmberStatus{Eligible: true, Grants: []ClaudeResetGrant{g}}
	}

	_, err := grant(func(*ClaudeResetGrant) {}).nextClaimableGrant(now)
	require.NoError(t, err)

	_, err = grant(func(g *ClaudeResetGrant) { g.Next = false }).nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
	_, err = grant(func(g *ClaudeResetGrant) { g.Paused = true }).nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
	_, err = grant(func(g *ClaudeResetGrant) { g.EndsAt = now.Add(-time.Minute).Format(time.RFC3339) }).nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
	_, err = grant(func(g *ClaudeResetGrant) { g.ResetsLeft = 0 }).nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
	_, err = grant(func(g *ClaudeResetGrant) { g.UsableNow = false }).nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)

	// 需要触顶才能用的券：未打满它能清除的限额时不可领取
	requiresLimit := grant(func(g *ClaudeResetGrant) { g.UseRequiresLimit = true; g.UsableNow = false })
	_, err = requiresLimit.nextClaimableGrant(now)
	require.ErrorIs(t, err, ErrClaudeResetRequiresLimit)
	requiresLimit.Exhausted = []string{"five_hour"}
	requiresLimit.Grants[0].UsableNow = true
	_, err = requiresLimit.nextClaimableGrant(now)
	require.NoError(t, err)
}

func TestParseClaudeJuniperTideStatusAndPreferredProgram(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	juniper := parseClaudeJuniperTideStatus(json.RawMessage(`{"eligible":true,"in_experiment":true,"arm":"reset","available":true,"weekly_resets_at":"2026-10-01T00:00:00Z","event_props":{"tier":"unknown"}}`))
	require.NotNil(t, juniper)
	require.True(t, juniper.claimable())
	require.Equal(t, 1, juniper.ResetsPerWeek, "resets_per_week defaults to 1")
	require.Empty(t, juniper.Tier, "unknown tiers are dropped")

	control := parseClaudeJuniperTideStatus(json.RawMessage(`{"eligible":true,"arm":"control","available":true}`))
	require.False(t, control.claimable())
	require.Nil(t, parseClaudeJuniperTideStatus(json.RawMessage(`{"available":true}`)))

	cedar := &ClaudeCedarEmberStatus{Eligible: true, Grants: []ClaudeResetGrant{{id: "g1", ResetsLeft: 1, UsableNow: true, Next: true, Clears: []string{"five_hour"}}}}
	snapshot := &ClaudeResetSnapshot{CedarEmber: cedar, JuniperTide: juniper}
	program, err := snapshot.preferredProgram(now)
	require.NoError(t, err)
	require.Equal(t, ClaudeResetProgramJuniperTide, program, "the weekly reset is spent first")
	require.Equal(t, 2, snapshot.availableCount(now))

	snapshot.JuniperTide = control
	program, err = snapshot.preferredProgram(now)
	require.NoError(t, err)
	require.Equal(t, ClaudeResetProgramCedarEmber, program)

	_, err = (&ClaudeResetSnapshot{}).preferredProgram(now)
	require.ErrorIs(t, err, ErrClaudeResetUnavailable)
}

func TestClaudeAccountAtSessionWall(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	windowEnd := now.Add(2 * time.Hour)
	weekly := now.Add(3 * 24 * time.Hour)
	soon := now.Add(90 * time.Minute)

	require.False(t, claudeAccountAtSessionWall(nil, now))
	require.False(t, claudeAccountAtSessionWall(&Account{}, now))
	require.True(t, claudeAccountAtSessionWall(&Account{SessionWindowEnd: &windowEnd, SessionWindowStatus: "rejected"}, now))
	require.True(t, claudeAccountAtSessionWall(&Account{SessionWindowEnd: &windowEnd, Extra: map[string]any{"session_window_utilization": 1.0}}, now))
	require.False(t, claudeAccountAtSessionWall(&Account{SessionWindowEnd: &windowEnd, Extra: map[string]any{"session_window_utilization": 0.4}}, now))
	require.True(t, claudeAccountAtSessionWall(&Account{RateLimitResetAt: &soon}, now))
	require.False(t, claudeAccountAtSessionWall(&Account{RateLimitResetAt: &weekly}, now), "a multi-day wait is the weekly limit")
}

func TestClaudeResetRequestIDIsStablePerGrantState(t *testing.T) {
	first := claudeResetRequestID(7, "grant_a", 2)
	require.Equal(t, first, claudeResetRequestID(7, "grant_a", 2), "a retry reuses the request id")
	require.NotEqual(t, first, claudeResetRequestID(7, "grant_a", 1), "the next claim gets a new id")
	require.NotEqual(t, first, claudeResetRequestID(8, "grant_a", 2))
	require.Regexp(t, claudeResetRequestIDPattern, first)
}

func TestParseClaudeResetClaim(t *testing.T) {
	result := parseClaudeResetClaim([]byte(`{"result":"reset","resets_left":0,"cleared":["five_hour",3],"weekly_resets_at":"2026-10-01T00:00:00Z"}`), ClaudeResetProgramCedarEmber)
	require.Equal(t, ClaudeResetResultReset, result.Result)
	require.NotNil(t, result.ResetsLeft)
	require.Zero(t, *result.ResetsLeft)
	require.Equal(t, []string{"five_hour"}, result.Cleared)
	require.True(t, result.Consumed())

	notLimited := parseClaudeResetClaim([]byte(`{"result":"NOT_LIMITED","reason":"not_limited"}`), ClaudeResetProgramJuniperTide)
	require.Equal(t, ClaudeResetResultNotLimited, notLimited.Result)
	require.False(t, notLimited.Consumed())

	require.Empty(t, parseClaudeResetClaim([]byte(`<html>`), ClaudeResetProgramCedarEmber).Result)
	require.True(t, (&ClaudeQuotaResetResult{Result: ClaudeResetResultAlreadyUsed}).Consumed())
}

func TestReadClaudeGroupResetCredits(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	require.Nil(t, readClaudeGroupResetCredits(nil, now))

	snapshot := &ClaudeResetSnapshot{
		CedarEmber: &ClaudeCedarEmberStatus{Eligible: true, Grants: []ClaudeResetGrant{
			{ResetsLeft: 1, EndsAt: "2026-10-22T07:00:00Z"},
			{ResetsLeft: 2, EndsAt: "2026-10-05T07:00:00Z"},
			{ResetsLeft: 1, EndsAt: "2026-09-01T00:00:00Z"},
			{ResetsLeft: 0, EndsAt: "2026-11-01T00:00:00Z"},
		}},
		JuniperTide: &ClaudeJuniperTideStatus{Eligible: true, Arm: "reset", Available: true},
	}
	credits := readClaudeGroupResetCredits(map[string]any{claudeResetSnapshotExtraKey: snapshot}, now)
	require.NotNil(t, credits)
	require.Equal(t, 4, credits.AvailableCount)
	require.Equal(t, []string{"2026-10-05T07:00:00Z", "2026-10-22T07:00:00Z"}, credits.ExpiresAt)

	empty := readClaudeGroupResetCredits(map[string]any{claudeResetSnapshotExtraKey: map[string]any{"fetched_at": "x"}}, now)
	require.NotNil(t, empty)
	require.Zero(t, empty.AvailableCount)
}

func jsonInt(value int64) string {
	data, _ := json.Marshal(value)
	return string(data)
}
