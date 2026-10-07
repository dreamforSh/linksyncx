//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// memorySessionLimitCache 按 Redis 脚本语义实现会话名额：RegisterSession 检查上限，
// RegisterBoundSession 无条件登记。
type memorySessionLimitCache struct {
	SessionLimitCache

	mu            sync.Mutex
	sessions      map[int64]map[string]struct{}
	registerCalls int
	unregistered  []string
}

func newMemorySessionLimitCache() *memorySessionLimitCache {
	return &memorySessionLimitCache{sessions: map[int64]map[string]struct{}{}}
}

func (m *memorySessionLimitCache) accountSessions(accountID int64) map[string]struct{} {
	if m.sessions[accountID] == nil {
		m.sessions[accountID] = map[string]struct{}{}
	}
	return m.sessions[accountID]
}

func (m *memorySessionLimitCache) RegisterSession(_ context.Context, accountID int64, sessionUUID string, maxSessions int, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registerCalls++
	sessions := m.accountSessions(accountID)
	if _, ok := sessions[sessionUUID]; ok {
		return true, nil
	}
	if len(sessions) >= maxSessions {
		return false, nil
	}
	sessions[sessionUUID] = struct{}{}
	return true, nil
}

func (m *memorySessionLimitCache) RegisterBoundSession(_ context.Context, accountID int64, sessionUUID string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accountSessions(accountID)[sessionUUID] = struct{}{}
	return nil
}

func (m *memorySessionLimitCache) UnregisterSession(_ context.Context, accountID int64, sessionUUID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.accountSessions(accountID), sessionUUID)
	m.unregistered = append(m.unregistered, sessionUUID)
	return nil
}

func (m *memorySessionLimitCache) GetWindowCostBatch(context.Context, []int64) (map[int64]float64, error) {
	return map[int64]float64{}, nil
}

func (m *memorySessionLimitCache) count(accountID int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions[accountID])
}

func claudeBudgetSettings(raw string) (*SettingService, *settingRepoStub) {
	values := map[string]string{}
	if raw != "" {
		values[SettingKeyClaudeDefaultMaxSessions] = raw
	}
	repo := &settingRepoStub{values: values}
	return NewSettingService(repo, &config.Config{}), repo
}

func claudeOAuthAccount(id int64, extra map[string]any) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 5, Extra: extra}
}

func TestClaudeSessionBudget(t *testing.T) {
	cases := []struct {
		name          string
		account       *Account
		defaultBudget int
		want          int
	}{
		{"nil account", nil, 5, 0},
		{"api key account", &Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Extra: map[string]any{"max_sessions": 3}}, 5, 0},
		{"openai oauth account", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, 5, 0},
		{"oauth uses the system default", claudeOAuthAccount(1, nil), 5, 5},
		{"setup-token uses the system default", &Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}, 5, 5},
		{"explicit max_sessions wins", claudeOAuthAccount(1, map[string]any{"max_sessions": 8}), 5, 8},
		{"max_sessions 0 means use the default", claudeOAuthAccount(1, map[string]any{"max_sessions": 0}), 5, 5},
		{"default 0 means unlimited", claudeOAuthAccount(1, nil), 0, 0},
		{"single-session mode is one session", claudeOAuthAccount(1, map[string]any{"session_id_masking_enabled": true, "max_sessions": 3}), 5, 1},
		{"single-session mode ignores an unlimited default", claudeOAuthAccount(1, map[string]any{"session_id_masking_enabled": true}), 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ClaudeSessionBudget(tc.account, tc.defaultBudget))
		})
	}
}

func TestGetClaudeDefaultMaxSessions(t *testing.T) {
	ctx := context.Background()
	for raw, want := range map[string]int{"": 5, "0": 0, "12": 12, " 7 ": 7, "-1": 5, "abc": 5, "1001": 5} {
		svc, _ := claudeBudgetSettings(raw)
		require.Equal(t, want, svc.GetClaudeDefaultMaxSessions(ctx), "raw=%q", raw)
	}

	var nilService *SettingService
	require.Equal(t, DefaultClaudeMaxSessions, nilService.GetClaudeDefaultMaxSessions(ctx))

	failing := NewSettingService(&settingRepoStub{err: errors.New("db down")}, &config.Config{})
	require.Equal(t, DefaultClaudeMaxSessions, failing.GetClaudeDefaultMaxSessions(ctx), "a DB failure falls back to the built-in default")

	svc, repo := claudeBudgetSettings("3")
	for i := 0; i < 3; i++ {
		require.Equal(t, 3, svc.GetClaudeDefaultMaxSessions(ctx))
	}
	require.Equal(t, 1, repo.getValueCalls, "the hot path reads the cached value")
}

// 未单独配置 max_sessions 的 OAuth 账号用系统默认预算：名额满时拒绝新对话，已绑定对话照常登记。
func TestCheckAndRegisterSessionUsesDefaultBudget(t *testing.T) {
	ctx := context.Background()
	cache := newMemorySessionLimitCache()
	settings, _ := claudeBudgetSettings("2")
	svc := &GatewayService{sessionLimitCache: cache, settingService: settings}
	acc := claudeOAuthAccount(7, nil)

	require.True(t, svc.checkAndRegisterSession(ctx, acc, "a", false))
	require.True(t, svc.checkAndRegisterSession(ctx, acc, "b", false))
	require.True(t, svc.checkAndRegisterSession(ctx, acc, "a", false), "an active session is refreshed")
	require.False(t, svc.checkAndRegisterSession(ctx, acc, "c", false), "a new conversation is rejected once the budget is full")

	require.True(t, svc.checkAndRegisterSession(ctx, acc, "c", true), "a bound conversation always passes")
	require.Equal(t, 3, cache.count(7), "the account may briefly exceed its budget")

	require.True(t, svc.checkAndRegisterSession(ctx, acc, "", false), "requests without a conversation key are not counted")
	require.True(t, svc.checkAndRegisterSession(ctx, &Account{ID: 8, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}, "x", false))
	require.Zero(t, cache.count(8))
}

// 单会话模式：预算为 1，同一时刻只接一个新对话。
func TestCheckAndRegisterSessionSingleSessionMode(t *testing.T) {
	ctx := context.Background()
	cache := newMemorySessionLimitCache()
	svc := &GatewayService{sessionLimitCache: cache}
	acc := claudeOAuthAccount(9, map[string]any{"session_id_masking_enabled": true})

	require.True(t, svc.checkAndRegisterSession(ctx, acc, "a", false))
	require.False(t, svc.checkAndRegisterSession(ctx, acc, "b", false))
}

// 默认预算设为 0：恢复旧行为，未配置 max_sessions 的账号不限会话数，也不再登记名额。
func TestCheckAndRegisterSessionDefaultZeroIsUnlimited(t *testing.T) {
	ctx := context.Background()
	cache := newMemorySessionLimitCache()
	settings, _ := claudeBudgetSettings("0")
	svc := &GatewayService{sessionLimitCache: cache, settingService: settings}
	acc := claudeOAuthAccount(10, nil)

	for _, session := range []string{"a", "b", "c", "d", "e", "f"} {
		require.True(t, svc.checkAndRegisterSession(ctx, acc, session, false))
	}
	require.Zero(t, cache.registerCalls)
}

// 释放名额的适用条件与登记一致：默认预算生效的账号会释放，默认预算为 0 时不释放。
func TestReleaseAccountSessionFollowsDefaultBudget(t *testing.T) {
	ctx := context.Background()
	acc := claudeOAuthAccount(11, nil)

	cache := newMemorySessionLimitCache()
	svc := &GatewayService{sessionLimitCache: cache}
	svc.ReleaseAccountSession(ctx, acc, "a")
	require.Equal(t, []string{"a"}, cache.unregistered)

	cache = newMemorySessionLimitCache()
	settings, _ := claudeBudgetSettings("0")
	svc = &GatewayService{sessionLimitCache: cache, settingService: settings}
	svc.ReleaseAccountSession(ctx, acc, "a")
	require.Empty(t, cache.unregistered)
}

type claudeBudgetSchedulerFixture struct {
	svc      *GatewayService
	ctx      context.Context
	groupID  int64
	cache    *mockGatewayCacheForPlatform
	sessions *memorySessionLimitCache
}

// newClaudeBudgetSchedulerFixture：分组 10 里两个未单独配置 max_sessions 的 OAuth 账号，
// 账号 1 优先级更高，系统默认预算 1。
func newClaudeBudgetSchedulerFixture(t *testing.T, loadBatch bool, bindings map[string]int64) *claudeBudgetSchedulerFixture {
	t.Helper()
	groupID := int64(10)
	accountRepo := &mockAccountRepoForPlatform{
		accounts: []Account{
			{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 1, Status: StatusActive, Schedulable: true, Concurrency: 5},
			{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Priority: 2, Status: StatusActive, Schedulable: true, Concurrency: 5},
		},
		accountsByID: map[int64]*Account{},
	}
	for i := range accountRepo.accounts {
		accountRepo.accountsByID[accountRepo.accounts[i].ID] = &accountRepo.accounts[i]
	}
	group := &Group{ID: groupID, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true}
	cfg := testConfig()
	cfg.Gateway.Scheduling.LoadBatchEnabled = loadBatch
	cache := &mockGatewayCacheForPlatform{sessionBindings: bindings}
	sessions := newMemorySessionLimitCache()
	settings, _ := claudeBudgetSettings("1")
	svc := &GatewayService{
		accountRepo:        accountRepo,
		groupRepo:          &mockGroupRepoForGateway{groups: map[int64]*Group{groupID: group}},
		cache:              cache,
		cfg:                cfg,
		concurrencyService: NewConcurrencyService(&mockConcurrencyCache{}),
		sessionLimitCache:  sessions,
		settingService:     settings,
	}
	return &claudeBudgetSchedulerFixture{
		svc:      svc,
		ctx:      context.WithValue(context.Background(), ctxkey.Group, group),
		groupID:  groupID,
		cache:    cache,
		sessions: sessions,
	}
}

// 已绑定对话回到原账号时不受名额约束：账号 1 的名额已被另一个对话占满，绑定在账号 1 的对话
// 仍留在账号 1（不换号）；没有绑定的新对话则被调度到账号 2。
func TestSelectAccountWithLoadAwareness_BoundConversationIgnoresFullBudget(t *testing.T) {
	for _, loadBatch := range []bool{true, false} {
		t.Run(map[bool]string{true: "load_batch", false: "legacy"}[loadBatch], func(t *testing.T) {
			f := newClaudeBudgetSchedulerFixture(t, loadBatch, map[string]int64{"bound": 1})
			require.NoError(t, f.sessions.RegisterBoundSession(f.ctx, 1, "other", time.Minute))

			result, err := f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "bound", "", nil, "", 0)
			require.NoError(t, err)
			require.Equal(t, int64(1), result.Account.ID, "a bound conversation stays on its account")
			require.Equal(t, 2, f.sessions.count(1), "the bound conversation is registered again")
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}

			result, err = f.svc.SelectAccountWithLoadAwareness(f.ctx, &f.groupID, "fresh", "", nil, "", 0)
			require.NoError(t, err)
			require.Equal(t, int64(2), result.Account.ID, "a new conversation skips the full account")
			require.Equal(t, 1, f.sessions.count(2))
		})
	}
}

// 分组容量按生效预算汇总：默认预算、单会话模式都计入，非 Claude OAuth 账号不计。
func TestGroupCapacityUsesEffectiveSessionBudget(t *testing.T) {
	accountRepo := &groupCapacityAccountRepoStub{
		rows: []GroupAccountCapacityRow{
			{GroupID: 10, AccountID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Concurrency: 1},
			{GroupID: 10, AccountID: 2, Platform: PlatformAnthropic, Type: AccountTypeSetupToken, Concurrency: 1,
				Extra: map[string]any{"session_id_masking_enabled": true}},
			{GroupID: 10, AccountID: 3, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Concurrency: 1,
				Extra: map[string]any{"max_sessions": 9}},
		},
	}
	sessionCache := &groupCapacitySessionCacheStub{counts: map[int64]int{1: 2, 2: 1}}
	settings, _ := claudeBudgetSettings("3")
	svc := NewGroupCapacityService(accountRepo, &groupCapacityGroupRepoStub{groupIDs: []int64{10}}, nil, sessionCache, nil, settings)

	results, err := svc.GetAllGroupCapacity(context.Background())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, 4, results[0].SessionsMax, "default budget 3 + single-session 1")
	require.Equal(t, 3, results[0].SessionsUsed)
	require.Equal(t, 5*time.Minute, sessionCache.idleTimeouts[1])
}
