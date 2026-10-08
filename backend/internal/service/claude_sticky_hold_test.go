//go:build unit

package service

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type stickyHoldFixture struct {
	svc         *GatewayService
	ctx         context.Context
	groupID     int64
	concurrency *mockConcurrencyCache
}

// newStickyHoldFixture：分组 10 里两个 OAuth 账号，对话 "conv" 绑定在账号 1（优先级更高）；
// mutate 调整账号 1 的状态。
func newStickyHoldFixture(t *testing.T, settings map[string]string, mutate func(*Account)) *stickyHoldFixture {
	t.Helper()
	groupID := int64(10)
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{groupID}},
			{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5, GroupIDs: []int64{groupID}},
		},
		accountsByID: map[int64]*Account{},
	}
	if mutate != nil {
		mutate(&accountRepo.accounts[0])
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
	concurrency := &mockConcurrencyCache{}
	if settings == nil {
		settings = map[string]string{}
	}
	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
		cache:              &mockGatewayCacheForPlatform{sessionBindings: map[string]int64{"conv": 1}},
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(concurrency),
		settingService:     NewSettingService(&settingRepoStub{values: settings}, &config.Config{}),
	}
	ctx := WithClaudeStickyHold(context.WithValue(context.Background(), ctxkey.Group, group))
	return &stickyHoldFixture{svc: svc, ctx: ctx, groupID: groupID, concurrency: concurrency}
}

func (f *stickyHoldFixture) selectFor(t *testing.T, ctx context.Context) (*AccountSelectionResult, error) {
	t.Helper()
	return f.svc.SelectAccountWithLoadAwareness(ctx, &f.groupID, "conv", "", nil, "", 0)
}

func allowedSnapshotExtra(t *testing.T, status string) map[string]any {
	t.Helper()
	return claudeRateLimitExtra(t, &ClaudeRateLimitSnapshot{AppliedAtMs: time.Now().UnixMilli(), Status: status})
}

func customRuleReason(t *testing.T) string {
	t.Helper()
	return `{"until_unix":` + strconv.FormatInt(time.Now().Add(5*time.Minute).Unix(), 10) + `,"status_code":500,"rule_index":0,"error_message":"overloaded"}`
}

// 过载冷却只影响新对话：已绑定对话继续打原账号。没有声明能处理 ClaudeStickyHoldError 的入口保持原有换号行为。
func TestStickyHoldKeepsOverloadedBoundAccount(t *testing.T) {
	f := newStickyHoldFixture(t, nil, func(a *Account) {
		until := time.Now().Add(10 * time.Minute)
		a.OverloadUntil = &until
	})

	selection, err := f.selectFor(t, f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)

	legacyCtx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: f.groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true})
	selection, err = f.selectFor(t, legacyCtx)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID, "entries that did not opt in keep switching")
}

// 没有表明额度耗尽的短时限流（如无重置头的 429 兜底冷却）：不换号，让客户端按剩余时间重试。
func TestStickyHoldRetryLaterOnTransientRateLimit(t *testing.T) {
	f := newStickyHoldFixture(t, nil, func(a *Account) {
		until := time.Now().Add(30 * time.Second)
		a.RateLimitResetAt = &until
		a.Extra = allowedSnapshotExtra(t, "allowed")
	})

	_, err := f.selectFor(t, f.ctx)
	holdErr, ok := AsClaudeStickyHoldError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, int64(1), holdErr.AccountID)
	require.Equal(t, "rate_limited", holdErr.Reason)
	require.InDelta(t, 30, holdErr.RetryAfter.Seconds(), 2)
	require.Equal(t, 30, holdErr.RetryAfterSeconds())
}

// 额度耗尽是硬性原因：照常换号。没有限流快照时沿用旧行为，任何限流都视为耗尽。
func TestStickyHoldSwitchesOnQuotaExhaustion(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"snapshot rejected": allowedSnapshotExtra(t, "rejected"),
		"no snapshot":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			f := newStickyHoldFixture(t, nil, func(a *Account) {
				until := time.Now().Add(2 * time.Hour)
				a.RateLimitResetAt = &until
				a.Extra = extra
			})
			selection, err := f.selectFor(t, f.ctx)
			require.NoError(t, err)
			require.Equal(t, int64(2), selection.Account.ID)
		})
	}
}

// 自定义规则触发的临时停调：预计恢复不超过阈值时不换号；超过阈值、或停调不是规则触发的（如代理故障）时换号。
func TestStickyHoldTempUnschedulableRules(t *testing.T) {
	cases := []struct {
		name     string
		left     time.Duration
		reason   string
		wantHold bool
	}{
		{"custom rule within the threshold", 5 * time.Minute, customRuleReason(t), true},
		{"custom rule beyond the threshold", 30 * time.Minute, customRuleReason(t), false},
		{"transport failure", 5 * time.Minute, "upstream transport error (proxy/network): connection refused", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newStickyHoldFixture(t, nil, func(a *Account) {
				until := time.Now().Add(tc.left)
				a.TempUnschedulableUntil = &until
				a.TempUnschedulableReason = tc.reason
			})
			selection, err := f.selectFor(t, f.ctx)
			if tc.wantHold {
				holdErr, ok := AsClaudeStickyHoldError(err)
				require.True(t, ok, "got %v", err)
				require.Equal(t, "temp_unschedulable", holdErr.Reason)
				return
			}
			require.NoError(t, err)
			require.Equal(t, int64(2), selection.Account.ID)
		})
	}
}

// 原账号并发已满：等待队列有空位时在原账号排队；队列也满时返回可重试错误，不换号。
func TestStickyHoldQueuesOnTheBoundAccount(t *testing.T) {
	f := newStickyHoldFixture(t, nil, nil)
	f.concurrency.acquireResults = map[int64]bool{1: false}

	selection, err := f.selectFor(t, f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)
	require.False(t, selection.Acquired)
	require.NotNil(t, selection.WaitPlan)
	require.Equal(t, 3, selection.WaitPlan.MaxWaiting)

	f.concurrency.waitCounts = map[int64]int{1: 3}
	_, err = f.selectFor(t, f.ctx)
	holdErr, ok := AsClaudeStickyHoldError(err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, "queue_full", holdErr.Reason)
}

// 关闭开关即恢复原有换号行为。
func TestStickyHoldDisabledRestoresSwitching(t *testing.T) {
	f := newStickyHoldFixture(t, map[string]string{SettingKeyClaudeStickyHoldEnabled: "false"}, func(a *Account) {
		until := time.Now().Add(30 * time.Second)
		a.RateLimitResetAt = &until
		a.Extra = allowedSnapshotExtra(t, "allowed")
	})
	selection, err := f.selectFor(t, f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
}

// 绑定账号已被移出分组：不再使用（候选列表按分组过滤，单独读取的账号同样校验分组）。
func TestStickyHoldIgnoresAccountsOutsideTheGroup(t *testing.T) {
	f := newStickyHoldFixture(t, nil, func(a *Account) {
		until := time.Now().Add(10 * time.Minute)
		a.OverloadUntil = &until
		a.GroupIDs = []int64{99}
	})
	selection, err := f.selectFor(t, f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
}

func TestIsClaudeStickySoftFailure(t *testing.T) {
	exhausted := http.Header{}
	exhausted.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	transient := http.Header{}
	transient.Set("anthropic-ratelimit-unified-status", "allowed")
	cases := []struct {
		name string
		err  *UpstreamFailoverError
		want bool
	}{
		{"nil", nil, false},
		{"overloaded", &UpstreamFailoverError{StatusCode: 529}, true},
		{"server error", &UpstreamFailoverError{StatusCode: 503}, true},
		{"transient transport error", &UpstreamFailoverError{StatusCode: http.StatusBadGateway}, true},
		{"dead proxy", &UpstreamFailoverError{StatusCode: http.StatusBadGateway, AccountUnavailable: true}, false},
		{"credential failure", &UpstreamFailoverError{StatusCode: http.StatusBadGateway, Stage: GatewayFailureStageAccountAuth}, false},
		{"unauthorized", &UpstreamFailoverError{StatusCode: 401}, false},
		{"forbidden", &UpstreamFailoverError{StatusCode: 403}, false},
		{"429 without exhaustion", &UpstreamFailoverError{StatusCode: 429, ResponseHeaders: transient}, true},
		{"429 without headers", &UpstreamFailoverError{StatusCode: 429}, true},
		{"429 window exhausted", &UpstreamFailoverError{StatusCode: 429, ResponseHeaders: exhausted}, false},
		{"429 credits required", &UpstreamFailoverError{StatusCode: 429, ResponseBody: []byte(`{"error":{"details":{"error_code":"credits_required"}}}`)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, IsClaudeStickySoftFailure(tc.err))
		})
	}

	withRetryAfter := http.Header{}
	withRetryAfter.Set("retry-after", "12")
	require.Equal(t, 12*time.Second, ClaudeStickyUpstreamRetryAfter(&UpstreamFailoverError{ResponseHeaders: withRetryAfter}))
	require.Equal(t, claudeStickyHoldUpstreamRetryAfter, ClaudeStickyUpstreamRetryAfter(&UpstreamFailoverError{}))
}

func TestClaudeRateLimitExhausted(t *testing.T) {
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour).Unix()
	windowExtra := func(window ClaudeRateLimitWindow) map[string]any {
		return claudeRateLimitExtra(t, &ClaudeRateLimitSnapshot{AppliedAtMs: now.UnixMilli(), Status: "allowed", Windows: map[string]ClaudeRateLimitWindow{"7d": window}})
	}
	cases := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"no snapshot", &Account{}, true},
		{"session window rejected", &Account{SessionWindowEnd: &future, SessionWindowStatus: "rejected", Extra: allowedSnapshotExtra(t, "allowed")}, true},
		{"snapshot rejected", &Account{Extra: allowedSnapshotExtra(t, "rejected")}, true},
		{"7d window rejected", &Account{Extra: windowExtra(ClaudeRateLimitWindow{Status: "rejected"})}, true},
		{"7d window full", &Account{Extra: windowExtra(ClaudeRateLimitWindow{Utilization: float64Ptr(1)})}, true},
		{"7d window already reset", &Account{Extra: windowExtra(ClaudeRateLimitWindow{Status: "rejected", ResetAt: past})}, false},
		{"allowed", &Account{Extra: allowedSnapshotExtra(t, "allowed")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, claudeRateLimitExhausted(tc.account, now))
		})
	}
}

func TestIsCustomRuleTempUnschedReason(t *testing.T) {
	require.True(t, isCustomRuleTempUnschedReason(customRuleReason(t)))
	require.False(t, isCustomRuleTempUnschedReason(`{"until_unix":1}`))
	require.False(t, isCustomRuleTempUnschedReason("upstream transport error (proxy/network): timeout"))
	require.False(t, isCustomRuleTempUnschedReason("{not json"))
}

func TestGetClaudeStickyHoldSettings(t *testing.T) {
	ctx := context.Background()
	defaults := NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{}).GetClaudeStickyHoldSettings(ctx)
	require.True(t, defaults.Enabled)
	require.Equal(t, 10*time.Minute, defaults.MaxWait)

	custom := NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeyClaudeStickyHoldEnabled:        "false",
		SettingKeyClaudeStickyHoldMaxWaitMinutes: "25",
	}}, &config.Config{}).GetClaudeStickyHoldSettings(ctx)
	require.False(t, custom.Enabled)
	require.Equal(t, 25*time.Minute, custom.MaxWait)

	invalid := NewSettingService(&settingRepoStub{values: map[string]string{SettingKeyClaudeStickyHoldMaxWaitMinutes: "0"}}, &config.Config{}).GetClaudeStickyHoldSettings(ctx)
	require.Equal(t, 10*time.Minute, invalid.MaxWait, "out-of-range values fall back to the default")
}

const migrationBody = `{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[` +
	`{"role":"user","content":"hi <b>&"},` +
	`{"role":"assistant","content":[{"type":"thinking","thinking":"old","signature":"sig-old"},{"type":"text","text":"hello"}]},` +
	`{"role":"user","content":"again"},` +
	`{"role":"assistant","content":[{"type":"redacted_thinking","data":"x"}]},` +
	`{"role":"user","content":"more"}]}`

// 精确删除水位线之前 assistant 消息里的 thinking：其余字节原样保留，删空的消息换成占位文本。
func TestStripClaudeThinkingBefore(t *testing.T) {
	out, rewrite := stripClaudeThinkingBefore([]byte(migrationBody), 5)
	require.Equal(t, claudeHistoryStripped, rewrite)
	require.Equal(t, `{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[`+
		`{"role":"user","content":"hi <b>&"},`+
		`{"role":"assistant","content":[{"type":"text","text":"hello"}]},`+
		`{"role":"user","content":"again"},`+
		`{"role":"assistant","content":[{"type":"text","text":"(assistant content removed)"}]},`+
		`{"role":"user","content":"more"}]}`, string(out))

	partial, rewrite := stripClaudeThinkingBefore([]byte(migrationBody), 2)
	require.Equal(t, claudeHistoryStripped, rewrite)
	require.Contains(t, string(partial), `"redacted_thinking"`, "messages after the watermark come from the new account")
	require.NotContains(t, string(partial), `"sig-old"`)

	_, rewrite = stripClaudeThinkingBefore([]byte(migrationBody), 1)
	require.Equal(t, claudeHistoryUnchanged, rewrite)
	_, rewrite = stripClaudeThinkingBefore([]byte(`{"messages":"oops"}`), 3)
	require.Equal(t, claudeHistoryUnchanged, rewrite)
}

// 工具调用循环跨了换号：最后一条 assistant 消息（带 tool_use 与 thinking）在水位线之前，API 要求原样回传
// 它的 thinking，这一个请求改走 FilterThinkingBlocksForRetry。
func TestStripClaudeThinkingBeforeToolLoopFallsBackToRetryFilter(t *testing.T) {
	body := `{"model":"claude-sonnet-4-5","thinking":{"type":"enabled","budget_tokens":1024},"messages":[` +
		`{"role":"user","content":"run it"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"sig-old"},{"type":"tool_use","id":"t1","name":"bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	_, rewrite := stripClaudeThinkingBefore([]byte(body), 3)
	require.Equal(t, claudeHistoryRetryFilter, rewrite)

	withoutThinking := strings.Replace(body, `"thinking":{"type":"enabled","budget_tokens":1024},`, "", 1)
	_, rewrite = stripClaudeThinkingBefore([]byte(withoutThinking), 3)
	require.Equal(t, claudeHistoryStripped, rewrite, "without extended thinking the block can simply be dropped")
}

// 水位线生命周期：换号时记录并剥离；之后在新账号上按水位线剥离；客户端改写历史（条数变少）时删除水位线。
func TestPrepareClaudeSessionBodyLifecycle(t *testing.T) {
	cache := newMemoryIdentityCache()
	svc := &GatewayService{
		identityService: NewIdentityService(cache),
		settingService:  NewSettingService(&settingRepoStub{values: map[string]string{}}, &config.Config{}),
	}
	ctx := WithClaudeStickyHold(context.Background())
	newAccount := &Account{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	out, changed := svc.PrepareClaudeSessionBody(ctx, newAccount, "conv", 1, []byte(migrationBody))
	require.True(t, changed)
	require.NotContains(t, string(out), "sig-old")
	migration, err := cache.GetClaudeSessionMigration(ctx, 2, "conv", time.Hour)
	require.NoError(t, err)
	require.Equal(t, ClaudeSessionMigration{FromAccountID: 1, MessageCount: 5, At: migration.At}, *migration)

	// 下一轮：对话已绑定在新账号，新账号产生的 thinking（水位线之后）保留。
	next := strings.Replace(migrationBody, `{"role":"user","content":"more"}]}`,
		`{"role":"user","content":"more"},{"role":"assistant","content":[{"type":"thinking","thinking":"new","signature":"sig-new"},{"type":"text","text":"ok"}]},{"role":"user","content":"go"}]}`, 1)
	out, changed = svc.PrepareClaudeSessionBody(ctx, newAccount, "conv", 2, []byte(next))
	require.True(t, changed)
	require.NotContains(t, string(out), "sig-old")
	require.Contains(t, string(out), "sig-new")

	// 客户端压缩了历史：条数少于水位线，删除水位线，按普通请求处理。
	compacted := `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"summary"}]}`
	out, changed = svc.PrepareClaudeSessionBody(ctx, newAccount, "conv", 2, []byte(compacted))
	require.False(t, changed)
	require.Equal(t, compacted, string(out))
	migration, err = cache.GetClaudeSessionMigration(ctx, 2, "conv", time.Hour)
	require.NoError(t, err)
	require.Nil(t, migration)

	// 未声明的入口、关闭开关、新对话都不处理。
	_, changed = svc.PrepareClaudeSessionBody(context.Background(), newAccount, "conv", 1, []byte(migrationBody))
	require.False(t, changed)
	_, changed = svc.PrepareClaudeSessionBody(ctx, newAccount, "conv", 0, []byte(migrationBody))
	require.False(t, changed)
	disabled := &GatewayService{identityService: NewIdentityService(cache),
		settingService: NewSettingService(&settingRepoStub{values: map[string]string{SettingKeyClaudeStickyHoldEnabled: "false"}}, &config.Config{})}
	_, changed = disabled.PrepareClaudeSessionBody(ctx, newAccount, "conv", 1, []byte(migrationBody))
	require.False(t, changed)
}
