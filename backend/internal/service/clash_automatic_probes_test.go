//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clashWritableSettingsRepo struct{ *settingRepoStub }

func (s *clashWritableSettingsRepo) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

// clashCountingProber 按节点监听端口统计出口探测请求，用来区分节点探测和本机出口自查（URL 为空）。
type clashCountingProber struct {
	inner   *fakeExitProber
	countMu sync.Mutex
	byPort  map[int]int
}

func (p *clashCountingProber) ProbeProxy(ctx context.Context, proxyURL string) (*ProxyExitInfo, int64, error) {
	if proxyURL != "" {
		if idx := strings.LastIndex(proxyURL, ":"); idx >= 0 {
			if port, err := strconv.Atoi(proxyURL[idx+1:]); err == nil {
				p.countMu.Lock()
				p.byPort[port]++
				p.countMu.Unlock()
			}
		}
	}
	return p.inner.ProbeProxy(ctx, proxyURL)
}

func (p *clashCountingProber) nodeCalls(node *ClashNode) int {
	p.countMu.Lock()
	defer p.countMu.Unlock()
	return p.byPort[node.ListenPort]
}

func countClashExitProbes(env *clashTestEnv) *clashCountingProber {
	counter := &clashCountingProber{inner: env.prober, byPort: map[int]int{}}
	env.svc.prober = counter
	return counter
}

func pausedProbeSettings(mutate ...func(*ClashPoolSettings)) *ClashPoolSettings {
	return withSettings(func(s *ClashPoolSettings) {
		s.PauseAutomaticProbes = true
		for _, m := range mutate {
			m(s)
		}
	})
}

// 只有显式写入的 true 才暂停；其余所有路径（含各种回退）都继续探测，这是反向命名的目的。
func TestClashPauseAutomaticProbesDefaultsToProbing(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		readErr   error
		paused    bool
	}{
		{name: "missing setting"},
		{name: "legacy record", raw: `{}`},
		{name: "explicit false", raw: `{"pause_automatic_probes":false}`},
		{name: "explicit true", raw: `{"pause_automatic_probes":true}`, paused: true},
		{name: "empty stored value", raw: ` `},
		{name: "null stored value", raw: `null`},
		{name: "null flag", raw: `{"pause_automatic_probes":null}`},
		{name: "invalid stored value", raw: `{"pause_automatic_probes":true,"health_timeout_ms":1}`},
		{name: "storage unavailable", readErr: errors.New("settings unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newClashTestEnv(t, nil)
			if tc.raw != "" {
				env.settings.values[SettingKeyClashPoolSettings] = tc.raw
			}
			env.settings.err = tc.readErr
			require.Equal(t, tc.paused, env.svc.poolSettings(context.Background()).PauseAutomaticProbes)
		})
	}
}

// 管理端 UpdateSettings 绑定进零值结构体；按固定 schema 构造请求、不认识该字段的自建脚本
// PUT 后必须仍在探测。
func TestClashPauseAutomaticProbesPutWithoutFieldKeepsProbing(t *testing.T) {
	env := newClashTestEnv(t, nil)
	env.svc.settings.settingRepo = &clashWritableSettingsRepo{env.settings}
	var body map[string]any
	raw, err := json.Marshal(defaultClashPoolSettings())
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &body))
	delete(body, "pause_automatic_probes")
	raw, err = json.Marshal(body)
	require.NoError(t, err)
	var req ClashPoolSettings
	require.NoError(t, json.Unmarshal(raw, &req))
	require.NoError(t, env.svc.settings.SetClashPoolSettings(context.Background(), &req))
	require.False(t, env.svc.poolSettings(context.Background()).PauseAutomaticProbes)
}

func TestClashPauseAutomaticProbesRoundTrip(t *testing.T) {
	env := newClashTestEnv(t, nil)
	env.svc.settings.settingRepo = &clashWritableSettingsRepo{env.settings}
	settings := defaultClashPoolSettings()
	settings.PauseAutomaticProbes = true
	require.NoError(t, env.svc.settings.SetClashPoolSettings(context.Background(), settings))
	loaded, err := env.svc.settings.GetClashPoolSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, settings, loaded)
}

// newSyncedClashManager 返回内核已同步配置的 manager，leaderCycle 才会进入探测分支。
func newSyncedClashManager(t *testing.T, env *clashTestEnv) *ClashManager {
	t.Helper()
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(context.Context, string, string, string) error { return nil }
	require.NoError(t, manager.syncOnce(context.Background()))
	return manager
}

func TestClashPausedProbesSkipRoutineRounds(t *testing.T) {
	env := newClashTestEnv(t, pausedProbeSettings())
	ctx := context.Background()
	profile := env.repo.addProfile("p", true)
	longAgo := time.Now().Add(-30 * 24 * time.Hour)
	// 正常模式下这两个未绑定节点每轮都会被探测：一个延迟和出口都已到期，另一个上次出口探测
	// 失败（exit 仍为 unknown）。它们都已做过出口探测，不属于一次性探测的范围。
	unbound := env.repo.addNode(profile.ID, "unbound", "198.51.100.2", func(n *ClashNode) { n.ExitCheckedAt = &longAgo })
	unboundFailed := env.repo.addNode(profile.ID, "unbound-failed", "", func(n *ClashNode) { n.ExitCheckedAt = &longAgo })
	checked := time.Now()
	bound := env.repo.addNode(profile.ID, "bound-ok", "198.51.100.1", func(n *ClashNode) { n.ExitCheckedAt = &checked; n.LastCheckedAt = &checked })
	env.repo.bind("acc", bound.ProxyID, nil)
	disabled := env.repo.addNode(profile.ID, "disabled", "", func(n *ClashNode) { n.Status = ClashNodeStatusDisabled })
	parked := env.repo.bind("parked", disabled.ProxyID, nil)
	var listenerChecks atomic.Int64
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(context.Context, string, string, string) error { listenerChecks.Add(1); return nil }
	require.NoError(t, manager.syncOnce(ctx))
	require.Positive(t, listenerChecks.Load(), "listener verification is not a probe")

	manager.leaderCycle(ctx)
	require.Empty(t, env.runtime.attempts, "no latency requests")
	require.Zero(t, env.prober.calls, "no exit, direct-IP or platform requests")
	require.Nil(t, env.repo.node(unbound.ID).LastCheckedAt)
	require.Equal(t, longAgo.Unix(), env.repo.node(unbound.ID).ExitCheckedAt.Unix())
	require.Equal(t, longAgo.Unix(), env.repo.node(unboundFailed.ID).ExitCheckedAt.Unix())
	require.Equal(t, checked.Unix(), env.repo.node(bound.ID).ExitCheckedAt.Unix())
	require.NotNil(t, env.repo.account(parked.ID).Until, "pause reconciliation still runs")
	require.False(t, manager.lastReclaim.IsZero(), "retention maintenance still runs")

	started := time.Now().Add(-time.Minute)
	sampler := newClashTrafficSampler(started)
	env.runtime.setConnections(conn("local counters", bound.ID, 100, 500, started.Add(time.Second)))
	now := time.Now()
	manager.sampleTraffic(ctx, sampler, now)
	manager.flushTraffic(ctx, sampler)
	require.EqualValues(t, 100, env.repo.trafficDay(bound.ID, clashTrafficDate(now)).Upload)
}

// 新订阅的节点没有 exit_ip 就不能绑定，所以暂停时仍对从未做过出口探测的未绑定节点探一次。
// 探测失败也会写入 ExitCheckedAt，之后不再重探；正常模式下 unknown 出口每轮都会重试，
// 用来对照说明不循环是暂停逻辑本身保证的。
func TestClashPausedProbesExitCheckNeverProbedUnboundNodeOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exitIP  string
		paused  bool
		calls   []int // 每轮 leaderCycle 之后该节点累计的出口探测次数
		success bool
	}{
		{name: "paused probe succeeds", exitIP: "198.51.100.5", paused: true, calls: []int{1, 1, 1}, success: true},
		{name: "paused probe fails", paused: true, calls: []int{1, 1, 1}},
		{name: "default retries a failing unknown exit", calls: []int{1, 2, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newClashTestEnv(t, withSettings(func(s *ClashPoolSettings) { s.PauseAutomaticProbes = tc.paused }))
			counter := countClashExitProbes(env)
			profile := env.repo.addProfile("p", true)
			fresh := time.Now()
			// 正常模式下健康的未绑定节点在间隔内不会做延迟探测，只观察出口探测。
			node := env.repo.addNode(profile.ID, "new", "", func(n *ClashNode) { n.LastCheckedAt = &fresh })
			env.prober.byPort[strconv.Itoa(node.ListenPort)] = tc.exitIP
			manager := newSyncedClashManager(t, env)
			for round, want := range tc.calls {
				manager.leaderCycle(context.Background())
				require.Equal(t, want, counter.nodeCalls(node), "round %d", round+1)
			}
			got := env.repo.node(node.ID)
			require.NotNil(t, got.ExitCheckedAt, "a failed attempt is recorded too")
			if tc.success {
				require.Equal(t, tc.exitIP, got.ExitIP)
				require.Equal(t, ClashExitOK, got.ExitStatus)
			} else {
				require.Empty(t, got.ExitIP)
				require.Equal(t, ClashExitUnknown, got.ExitStatus)
			}
			if tc.paused {
				require.Empty(t, env.runtime.attempts, "no latency probes for unbound nodes")
				require.Nil(t, got.PlatformChecks.CheckedAt, "platform checks stay off")
			}
		})
	}
}

// 一次性探测和已绑定节点共用 ExitProbePerMinute 预算：预算为每轮 1 个时，三个新节点分三轮
// 各探一次，第四轮不再探测。
func TestClashPausedProbesNeverProbedExitCheckRespectsBudget(t *testing.T) {
	env := newClashTestEnv(t, pausedProbeSettings(func(s *ClashPoolSettings) { s.ExitProbePerMinute = 2 }))
	counter := countClashExitProbes(env)
	profile := env.repo.addProfile("p", true)
	nodes := make([]*ClashNode, 0, 3)
	for i, ip := range []string{"198.51.100.11", "198.51.100.12", ""} {
		node := env.repo.addNode(profile.ID, "new-"+strconv.Itoa(i), "")
		env.prober.byPort[strconv.Itoa(node.ListenPort)] = ip
		nodes = append(nodes, node)
	}
	total := func() int {
		sum := 0
		for _, node := range nodes {
			sum += counter.nodeCalls(node)
		}
		return sum
	}
	manager := newSyncedClashManager(t, env)
	for round, want := range []int{1, 2, 3, 3} {
		manager.leaderCycle(context.Background())
		require.Equal(t, want, total(), "round %d", round+1)
	}
	for _, node := range nodes {
		require.Equal(t, 1, counter.nodeCalls(node), node.Name)
	}
}

// 订阅刷新改动已绑定节点后出口被标记为 stale；暂停时出口变更策略和本机 IP 自查仍须立即发现它，
// 不等出口探测间隔。
func TestClashPausedProbesStillVerifyStaleBoundExit(t *testing.T) {
	for _, tc := range []struct {
		name, newIP, reason string
	}{
		{"exit moved", "198.51.100.9", "[clash-exit] bound: exit IP changed to 198.51.100.9, awaiting confirmation"},
		{"exit is this server", "203.0.113.250", "[clash-exit] bound: node invalid: exit IP equals this server's own IP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newClashTestEnv(t, pausedProbeSettings())
			profile := env.repo.addProfile("p", true)
			checked := time.Now()
			node := env.repo.addNode(profile.ID, "bound", "198.51.100.1", func(n *ClashNode) {
				n.ExitStatus = ClashExitStale
				n.ExitCheckedAt = &checked
				n.LastCheckedAt = &checked
			})
			env.prober.byPort[strconv.Itoa(node.ListenPort)] = tc.newIP
			acc := env.repo.bind("acc", node.ProxyID, nil)
			newSyncedClashManager(t, env).leaderCycle(context.Background())
			require.Equal(t, tc.reason, env.repo.account(acc.ID).Reason)
			require.Nil(t, env.repo.node(node.ID).PlatformChecks.CheckedAt, "platform checks stay off")
		})
	}
}

func TestClashPausedProbesRecheckFailingBoundNode(t *testing.T) {
	env := newClashTestEnv(t, pausedProbeSettings(func(s *ClashPoolSettings) { s.RecoveryThreshold = 1 }))
	profile := env.repo.addProfile("p", true)
	node := env.repo.addNode(profile.ID, "was-down", "198.51.100.1", func(n *ClashNode) { n.HealthStatus = ClashHealthUnhealthy })
	acc := env.repo.bind("acc", node.ProxyID, nil)
	manager := newSyncedClashManager(t, env)
	require.NoError(t, env.svc.ReconcilePauses(context.Background()))
	require.NotNil(t, env.repo.account(acc.ID).Until, "the failing node pauses its account first")
	manager.leaderCycle(context.Background())
	require.Positive(t, env.runtime.attempts[ClashProxyName(node.ID)])
	require.Equal(t, ClashHealthHealthy, env.repo.node(node.ID).HealthStatus)
	require.Nil(t, env.repo.account(acc.ID).Until, "recovered node releases its accounts")
}

// expireClashLatencyChecks 清掉节点的上次延迟检测时间，模拟已经过了一个已绑定检测间隔。
func expireClashLatencyChecks(repo *fakeClashRepo, nodes []*ClashNode) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	for _, node := range nodes {
		repo.nodes[node.ID].LastCheckedAt = nil
	}
}

// 暂停时延迟轮里只有已判定异常的已绑定节点，它们大多失败是常态。全局故障保护若在这里生效，
// 会吞掉抖动节点的失败、不清零连续成功次数，让它经过「成功-失败-成功」就凑够恢复阈值并解除账号暂停。
func TestClashPausedProbesKeepRecoveryThresholdWhenMostRecheckedNodesFail(t *testing.T) {
	env := newClashTestEnv(t, pausedProbeSettings(func(s *ClashPoolSettings) { s.RecoveryThreshold = 2 }))
	ctx := context.Background()
	profile := env.repo.addProfile("p", true)
	unhealthy := func(n *ClashNode) { n.HealthStatus = ClashHealthUnhealthy }
	flapping := env.repo.addNode(profile.ID, "flapping", "198.51.100.1", unhealthy)
	acc := env.repo.bind("acc", flapping.ProxyID, nil)
	nodes := []*ClashNode{flapping}
	for i := 0; i < clashGlobalFailureMinimum-1; i++ {
		down := env.repo.addNode(profile.ID, "down-"+strconv.Itoa(i), "198.51.100.2"+strconv.Itoa(i), unhealthy)
		env.repo.bind("acc-"+strconv.Itoa(i), down.ProxyID, nil)
		env.runtime.delays[ClashProxyName(down.ID)] = errors.New("timeout")
		nodes = append(nodes, down)
	}
	manager := newSyncedClashManager(t, env)
	for round, step := range []struct {
		fails               bool
		successes, failures int
	}{
		{fails: false, successes: 1, failures: 0},
		{fails: true, successes: 0, failures: 1},
		{fails: false, successes: 1, failures: 0},
	} {
		expireClashLatencyChecks(env.repo, nodes)
		if step.fails {
			env.runtime.delays[ClashProxyName(flapping.ID)] = errors.New("timeout")
		} else {
			delete(env.runtime.delays, ClashProxyName(flapping.ID))
		}
		manager.leaderCycle(ctx)
		got := env.repo.node(flapping.ID)
		require.Equal(t, step.successes, got.ConsecutiveSuccesses, "round %d", round+1)
		require.Equal(t, step.failures, got.ConsecutiveFailures, "round %d", round+1)
		require.Equal(t, ClashHealthUnhealthy, got.HealthStatus, "round %d", round+1)
		require.NotNil(t, env.repo.account(acc.ID).Until, "round %d: the account stays paused", round+1)
	}
}

func TestClashAutomaticProbesDefaultKeepsScheduling(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("p", true)
	node := env.repo.addNode(profile.ID, "due", "")
	newSyncedClashManager(t, env).leaderCycle(context.Background())
	require.Positive(t, env.runtime.attempts[ClashProxyName(node.ID)])
	require.Positive(t, env.prober.calls)
}

// 原地换出口（节点配置不变、落地 IP 变化）正是 exit_change_policy 的主场景；暂停时已绑定节点
// 仍按出口探测间隔复核，未到期则不探测。
func TestClashPausedProbesStillCatchInPlaceExitRotation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		changed bool
	}{
		{"exit probe due", 13 * time.Hour, true},
		{"exit probe not due", time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newClashTestEnv(t, pausedProbeSettings())
			profile := env.repo.addProfile("p", true)
			checked := time.Now().Add(-tc.age)
			node := env.repo.addNode(profile.ID, "bound", "198.51.100.1", func(n *ClashNode) {
				n.ExitCheckedAt = &checked
				n.LastCheckedAt = &checked
			})
			env.prober.byPort[strconv.Itoa(node.ListenPort)] = "198.51.100.77"
			acc := env.repo.bind("acc", node.ProxyID, nil)
			newSyncedClashManager(t, env).leaderCycle(context.Background())
			require.Empty(t, env.runtime.attempts, "healthy bound nodes get no latency probes")
			if tc.changed {
				require.Equal(t, "[clash-exit] bound: exit IP changed to 198.51.100.77, awaiting confirmation", env.repo.account(acc.ID).Reason)
			} else {
				require.Zero(t, env.prober.calls)
				require.Empty(t, env.repo.account(acc.ID).Reason)
			}
			require.Nil(t, env.repo.node(node.ID).PlatformChecks.CheckedAt, "platform checks stay off")
		})
	}
}
