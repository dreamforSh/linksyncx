package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	clashLeaderLockKey       = "clash_pool:leader"
	clashLeaderLockTTL       = 10 * time.Minute
	clashLeaderCycle         = 30 * time.Second
	clashLeaderCycleTimeout  = 8 * time.Minute
	clashSyncInterval        = 60 * time.Second
	clashSyncDebounce        = 2 * time.Second
	clashApplyMaxAttempts    = 8
	clashListenerCheckEvery  = 5 * time.Minute
	clashListenerProbeWait   = 3 * time.Second
	clashListenerConcurrency = 32
	clashStatusTTL           = 3 * time.Minute
	clashReclaimInterval     = time.Hour
)

// ClashManager owns the Clash pool lifecycle on one instance: it keeps the
// local core in sync with the database, and (when it wins the leader lock)
// refreshes subscriptions, probes nodes and reconciles account pauses.
type ClashManager struct {
	svc        *ClashService
	runtime    ClashRuntime
	notifier   ClashRuntimeNotifier
	lockCache  LeaderLockCache
	db         *sql.DB
	cfg        *config.Config
	instanceID string

	mu                sync.Mutex
	lastHash          string
	lastAppliedAt     time.Time
	lastErr           string
	listeners         int
	listenerFails     int
	lastListenerCheck time.Time
	lastReclaim       time.Time
	// trafficLive is this instance's latest throughput sample.
	trafficLive *ClashTrafficLive

	trigger chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	syncMu  sync.Mutex

	// probeListener verifies one listener; replaceable in tests.
	probeListener func(ctx context.Context, addr, user, pass string) error
}

// NewClashManager wires the lifecycle manager and hooks structural changes
// into the local sync loop.
func NewClashManager(svc *ClashService, runtime ClashRuntime, notifier ClashRuntimeNotifier, lockCache LeaderLockCache, db *sql.DB, cfg *config.Config) *ClashManager {
	m := &ClashManager{
		svc:        svc,
		runtime:    runtime,
		notifier:   notifier,
		lockCache:  lockCache,
		db:         db,
		cfg:        cfg,
		instanceID: uuid.NewString(),
		trigger:    make(chan struct{}, 1),

		probeListener: ProbeClashListener,
	}
	if svc != nil {
		svc.onStructuralChange = m.Trigger
		svc.localTrafficLive = m.localTrafficLive
	}
	return m
}

func (m *ClashManager) log() *zap.Logger { return m.svc.log() }

// Trigger requests a (debounced) local resync.
func (m *ClashManager) Trigger() {
	if m == nil {
		return
	}
	select {
	case m.trigger <- struct{}{}:
	default:
	}
}

// Start brings the local core up and applies the stored configuration before
// returning, so gateway traffic never hits listeners that are not bound yet.
// Failures degrade the pool instead of aborting startup.
func (m *ClashManager) Start(ctx context.Context) error {
	if m == nil || m.svc == nil {
		return nil
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	if m.svc.Enabled() {
		var startErr error
		if err := m.runtime.Start(ctx); err != nil {
			startErr = fmt.Errorf("start clash core: %w", err)
			_ = m.recordError(startErr)
		} else {
			host := m.cfg.ClashPool.EffectiveListenerHost()
			if moved, err := m.svc.repo.RehostManagedProxies(ctx, host); err != nil {
				m.log().Warn("rehost clash managed proxies failed", zap.Error(err))
			} else if len(moved) > 0 {
				m.log().Info("clash managed proxies moved to new listener host", zap.String("host", host), zap.Int("accounts", len(moved)))
			}
			if err := m.syncOnce(ctx); err != nil {
				startErr = err
			}
		}
		if m.notifier != nil {
			if err := m.notifier.SubscribeConfigChanged(loopCtx, m.Trigger); err != nil {
				m.log().Warn("subscribe clash config changes failed; relying on periodic sync", zap.Error(err))
			}
		}
		m.wg.Add(1)
		go m.syncLoop(loopCtx)
		m.wg.Add(1)
		go m.leaderLoop(loopCtx)
		m.wg.Add(1)
		go m.trafficLoop(loopCtx)
		return startErr
	}

	// Pool disabled: keep pausing accounts still bound to Clash exits.
	m.wg.Add(1)
	go m.leaderLoop(loopCtx)
	return nil
}

// Stop halts the loops and the local core.
func (m *ClashManager) Stop() {
	if m == nil || m.cancel == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
	// Nothing confirms tagged usernames once this manager stops.
	clashAccountRoutes.set(nil)
	if m.runtime != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := m.runtime.Stop(ctx); err != nil {
			m.log().Warn("stop clash core failed", zap.Error(err))
		}
	}
}

func (m *ClashManager) syncLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(clashSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.trigger:
			// Coalesce bursts (a refresh touching many nodes, several admins).
			select {
			case <-ctx.Done():
				return
			case <-time.After(clashSyncDebounce):
			}
		}
		syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if !m.runtime.Ready() {
			if err := m.runtime.Start(syncCtx); err != nil {
				_ = m.recordError(fmt.Errorf("start clash core: %w", err))
				cancel()
				continue
			}
		}
		_ = m.syncOnce(syncCtx)
		cancel()
	}
}

// syncOnce renders the configuration from the database and applies it when it
// differs from what the core runs. Nodes the core rejects are marked invalid
// and the render is retried without them.
func (m *ClashManager) syncOnce(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	var rendered *ClashRenderedConfig
	applied := false
	for attempt := 0; attempt < clashApplyMaxAttempts; attempt++ {
		nodes, err := m.svc.repo.ListRenderNodes(ctx)
		if err != nil {
			return m.recordError(fmt.Errorf("load clash nodes: %w", err))
		}
		rendered, err = RenderClashConfig(nodes, ClashRenderOptions{ListenAddress: m.cfg.ClashPool.EffectiveListenAddress()})
		if err != nil {
			return m.recordError(err)
		}
		routes := clashRenderAccountRoutes(nodes)
		if m.currentHash() == rendered.Hash {
			if ok, err := m.runtime.HasProxy(ctx, rendered.MarkerName); err == nil && ok {
				// The core runs exactly this render (another instance may have
				// applied it), so its tagged usernames are accepted.
				clashAccountRoutes.set(routes)
				m.maybeCheckListeners(ctx, nodes, false)
				m.publishStatus(ctx)
				return nil
			}
		}
		// Usernames this render drops stop being used before the apply; new
		// ones only after it succeeded.
		clashAccountRoutes.retain(routes)
		err = m.runtime.Apply(ctx, rendered.Payload)
		var cfgErr *ClashConfigError
		if errors.As(err, &cfgErr) {
			nodeID, ok := blameClashNode(cfgErr.Message, rendered)
			if !ok {
				return m.recordError(err)
			}
			m.log().Warn("clash core rejected a node; isolating it", zap.Int64("node_id", nodeID), zap.String("error", cfgErr.Message))
			if err := m.svc.repo.SetNodeStatus(ctx, nodeID, ClashNodeStatusInvalid, truncateClashMessage(cfgErr.Message)); err != nil {
				return m.recordError(err)
			}
			continue
		}
		if err != nil {
			return m.recordError(fmt.Errorf("apply clash config: %w", err))
		}
		m.mu.Lock()
		m.lastHash = rendered.Hash
		m.lastAppliedAt = time.Now().UTC()
		m.lastErr = ""
		m.listeners = rendered.Listeners
		m.mu.Unlock()
		clashAccountRoutes.set(routes)
		applied = true
		m.maybeCheckListeners(ctx, nodes, true)
		break
	}
	if !applied {
		return m.recordError(errors.New("clash configuration still rejected after isolating nodes"))
	}
	m.publishStatus(ctx)
	return nil
}

func blameClashNode(message string, rendered *ClashRenderedConfig) (int64, bool) {
	index, ok := ParseClashProxyIndexError(message)
	if !ok || rendered == nil || index < 0 || index >= len(rendered.ProxyNodeIDs) {
		return 0, false
	}
	return rendered.ProxyNodeIDs[index], true
}

func truncateClashMessage(message string) string {
	if len(message) > 500 {
		return message[:500]
	}
	return message
}

func (m *ClashManager) currentHash() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastHash
}

func (m *ClashManager) recordError(err error) error {
	if err == nil {
		return nil
	}
	m.mu.Lock()
	m.lastErr = err.Error()
	m.mu.Unlock()
	m.log().Warn("clash pool sync failed", zap.Error(err))
	return err
}

// maybeCheckListeners verifies every listener answers with our credentials: a
// bind failure is only logged by the core, and a foreign proxy squatting on
// the port could otherwise carry account traffic.
func (m *ClashManager) maybeCheckListeners(ctx context.Context, nodes []ClashRenderNode, force bool) {
	m.mu.Lock()
	due := force || time.Since(m.lastListenerCheck) >= clashListenerCheckEvery
	m.mu.Unlock()
	if !due || len(nodes) == 0 {
		return
	}
	host := m.listenerProbeHost()
	type failure struct {
		node ClashRenderNode
		err  error
	}
	var (
		failures []failure
		mu       sync.Mutex
		wg       sync.WaitGroup
	)
	sem := make(chan struct{}, clashListenerConcurrency)
	for _, node := range nodes {
		wg.Add(1)
		go func(node ClashRenderNode) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			addr := net.JoinHostPort(host, strconv.Itoa(node.ListenPort))
			if err := m.probeListener(ctx, addr, node.Username, node.Password); err != nil {
				mu.Lock()
				failures = append(failures, failure{node: node, err: err})
				mu.Unlock()
			}
		}(node)
	}
	wg.Wait()

	m.mu.Lock()
	m.lastListenerCheck = time.Now()
	m.listenerFails = len(failures)
	m.mu.Unlock()
	marked := false
	for _, f := range failures {
		m.log().Warn("clash listener self-check failed", zap.Int64("node_id", f.node.NodeID), zap.Int("port", f.node.ListenPort), zap.Error(f.err))
		if f.node.Config == nil {
			continue // placeholder: nothing routes through it anyway
		}
		if err := m.svc.repo.SetNodeStatus(ctx, f.node.NodeID, ClashNodeStatusInvalid, "listener unavailable: "+truncateClashMessage(f.err.Error())); err == nil {
			marked = true
		}
	}
	if marked {
		m.Trigger()
		if err := m.svc.ReconcilePauses(ctx); err != nil {
			m.log().Warn("reconcile clash pauses failed", zap.Error(err))
		}
	}
}

func (m *ClashManager) listenerProbeHost() string {
	if m.cfg.ClashPool.Mode == config.ClashPoolModeExternal {
		return m.cfg.ClashPool.EffectiveListenerHost()
	}
	addr := m.cfg.ClashPool.EffectiveListenAddress()
	if addr == "0.0.0.0" || addr == "::" || addr == "" {
		return "127.0.0.1"
	}
	return addr
}

// ProbeClashListener performs a SOCKS5 username/password handshake. Our
// listeners always demand credentials, so a server offering "no auth" is not ours.
func ProbeClashListener(ctx context.Context, addr, user, pass string) error {
	dialer := net.Dialer{Timeout: clashListenerProbeWait}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(clashListenerProbeWait))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	if reply[0] != 0x05 || reply[1] != 0x02 {
		return fmt.Errorf("port is served by something else (socks5 method %d)", reply[1])
	}
	if len(user) > 255 || len(pass) > 255 {
		return errors.New("listener credentials too long")
	}
	auth := make([]byte, 0, 3+len(user)+len(pass))
	auth = append(auth, 0x01, byte(len(user)))
	auth = append(auth, user...)
	auth = append(auth, byte(len(pass)))
	auth = append(auth, pass...)
	if _, err := conn.Write(auth); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks5 auth: %w", err)
	}
	if reply[1] != 0x00 {
		return errors.New("listener rejected our credentials")
	}
	return nil
}

func (m *ClashManager) localStatus() ClashInstanceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := ClashInstanceStatus{
		InstanceID:    m.instanceID,
		Mode:          m.svc.Mode(),
		ConfigHash:    m.lastHash,
		Listeners:     m.listeners,
		ListenerFails: m.listenerFails,
		LastAppliedAt: m.lastAppliedAt,
		LastError:     m.lastErr,
		UpdatedAt:     time.Now().UTC(),
	}
	if m.runtime != nil {
		status.Ready = m.runtime.Ready() && m.lastHash != ""
		status.Version = m.runtime.Version()
	}
	return status
}

func (m *ClashManager) publishStatus(ctx context.Context) {
	if m.notifier == nil {
		return
	}
	if err := m.notifier.PublishInstanceStatus(ctx, m.localStatus(), clashStatusTTL); err != nil {
		m.log().Debug("publish clash instance status failed", zap.Error(err))
	}
}

// ClashRuntimeStatus is the admin view of the pool runtime.
type ClashRuntimeStatus struct {
	Mode      string
	Enabled   bool
	Local     ClashInstanceStatus
	Instances []ClashInstanceStatus
}

// Status reports this instance and every instance that published recently.
func (m *ClashManager) Status(ctx context.Context) ClashRuntimeStatus {
	status := ClashRuntimeStatus{Mode: m.svc.Mode(), Enabled: m.svc.Enabled(), Local: m.localStatus()}
	if m.notifier != nil {
		if instances, err := m.notifier.ListInstanceStatuses(ctx); err == nil {
			status.Instances = instances
		}
	}
	return status
}

// Resync forces a local sync and asks every other instance to resync.
func (m *ClashManager) Resync(ctx context.Context) error {
	if err := m.svc.requireEnabled(); err != nil {
		return err
	}
	m.mu.Lock()
	m.lastHash = ""
	m.mu.Unlock()
	err := m.syncOnce(ctx)
	if m.notifier != nil {
		_ = m.notifier.NotifyConfigChanged(ctx)
	}
	return err
}

func (m *ClashManager) leaderLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(clashLeaderCycle)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		m.publishStatus(ctx)
		release, ok := tryAcquireSingletonLeaderLock(ctx, m.lockCache, m.db, clashLeaderLockKey, m.instanceID, clashLeaderLockTTL)
		if !ok {
			continue
		}
		cycleCtx, cancel := context.WithTimeout(ctx, clashLeaderCycleTimeout)
		m.leaderCycle(cycleCtx)
		cancel()
		release()
	}
}

func (m *ClashManager) leaderCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			m.log().Error("clash leader cycle panicked", zap.Any("panic", r))
		}
	}()
	if !m.svc.Enabled() {
		if err := m.svc.ReconcilePauses(ctx); err != nil {
			m.log().Warn("reconcile clash pauses failed", zap.Error(err))
		}
		return
	}
	settings := m.svc.poolSettings(ctx)
	m.refreshDueProfiles(ctx)

	// Probes need a core that runs the current configuration; otherwise a
	// local outage would be mistaken for dead nodes.
	synced := m.runtime.Ready() && m.currentHash() != ""
	if synced {
		views, err := m.svc.repo.ListAllNodeViews(ctx)
		if err != nil {
			m.log().Warn("load clash nodes failed", zap.Error(err))
		} else {
			now := time.Now()
			probeSettings := settings
			latencyViews, exitViews := views, views
			guardGlobalFailure := true
			// 暂停自动探测时只保留保障安全的探测（见 clashPausedProbeViews），AI 平台检测
			// 不承担安全职责，一并关闭。手动探测走 TestNodesLatency/ProbeNodesExit，不受影响。
			if settings.PauseAutomaticProbes {
				latencyViews, exitViews = clashPausedProbeViews(views)
				paused := *settings
				paused.PlatformChecksEnabled = false
				probeSettings = &paused
				// 这时延迟轮里只有已判定异常的节点，大多失败是常态：全局故障保护护不住任何节点
				// （失败也无法再降级），只会吞掉失败、不清零连续成功次数，让抖动节点绕过恢复阈值。
				guardGlobalFailure = false
			}
			if due := dueForLatency(latencyViews, probeSettings, now); len(due) > 0 {
				if _, err := m.svc.probeLatency(ctx, due, probeSettings, guardGlobalFailure); err != nil {
					m.log().Warn("clash latency round failed", zap.Error(err))
				}
			}
			if due := dueForExitProbe(exitViews, probeSettings, now); len(due) > 0 {
				m.svc.probeExits(ctx, due, probeSettings)
			}
		}
	}
	if err := m.svc.ReconcilePauses(ctx); err != nil {
		m.log().Warn("reconcile clash pauses failed", zap.Error(err))
	}

	if time.Since(m.lastReclaim) >= clashReclaimInterval {
		m.lastReclaim = time.Now()
		cutoff := time.Now().Add(-time.Duration(settings.MissingRetentionDays) * 24 * time.Hour)
		if reclaimed, err := m.svc.repo.ReclaimNodes(ctx, cutoff); err != nil {
			m.log().Warn("reclaim clash nodes failed", zap.Error(err))
		} else if len(reclaimed) > 0 {
			m.log().Info("reclaimed unused clash nodes", zap.Int("count", len(reclaimed)))
			m.svc.structuralChange(ctx)
		}
		if conflicts, err := m.svc.FindExitConflicts(ctx); err == nil {
			for _, conflict := range conflicts {
				m.log().Warn("clash exit shared beyond the per-exit limit", zap.String("exit", conflict.ExitKey), zap.Int("accounts", len(conflict.Accounts)))
			}
		}
	}
}

func (m *ClashManager) refreshDueProfiles(ctx context.Context) {
	profiles, err := m.svc.repo.ListProfiles(ctx)
	if err != nil {
		m.log().Warn("list clash profiles failed", zap.Error(err))
		return
	}
	now := time.Now()
	for _, profile := range profiles {
		// Uploaded files and pasted links have nothing to fetch; they are
		// re-parsed on demand.
		if !profile.Enabled || profile.RefreshIntervalMinutes <= 0 || profile.IsLocalSource() {
			continue
		}
		if profile.LastRefreshAt != nil && now.Sub(*profile.LastRefreshAt) < time.Duration(profile.RefreshIntervalMinutes)*time.Minute {
			continue
		}
		result, err := m.svc.RefreshProfile(ctx, profile.ID, false)
		if err != nil {
			m.log().Warn("scheduled clash refresh failed", zap.Int64("profile_id", profile.ID), zap.Error(err))
			continue
		}
		if result != nil && result.Status != ClashRefreshOK {
			m.log().Warn("scheduled clash refresh did not apply", zap.Int64("profile_id", profile.ID),
				zap.String("status", result.Status), zap.String("error", result.Error))
		}
	}
}

// clashPausedProbeViews 选出暂停自动探测时仍要探测的节点，其余定时探测全部停掉。
//   - 延迟：只复测健康检查失败的已绑定节点，让节点恢复后能解除账号暂停。健康状态只由探测写入，
//     不复测的话这些账号会一直暂停；反过来，健康的已绑定节点出故障不再被发现，只能靠请求失败切换账号。
//   - 出口：已绑定节点照常交给 dueForExitProbe（unknown/stale 立即复核，其余按出口探测间隔），
//     这样原地换出口的暂停、出口等于本机 IP 的自查、订阅刷新后的复核都继续生效。
//   - 未绑定节点只对从未做过出口探测（ExitCheckedAt 为空）的节点探一次，让新订阅的节点拿到
//     exit_ip、可以绑定。只要发出了探测请求，无论成败 probeOneExit 都会写入 ExitCheckedAt，所以
//     不会每轮重探；唯一不写的是受管代理行缺失、根本没发请求的情况，这类节点每轮占一个预算名额
//     但没有网络请求。仍受 ExitProbePerMinute 预算限制，且排在已绑定节点之后；dueForExitProbe
//     跳过已判定异常的节点，所以暂停前就异常的未绑定节点拿不到这次探测，要先手动测延迟。
//     其余未绑定节点保留暂停前的健康状态和出口 IP，绑定后才会按上面已绑定节点的规则复核。
func clashPausedProbeViews(views []ClashNodeView) (latency, exit []ClashNodeView) {
	for i := range views {
		view := &views[i]
		if len(view.Accounts) == 0 {
			if view.ExitCheckedAt == nil {
				exit = append(exit, *view)
			}
			continue
		}
		exit = append(exit, *view)
		if view.HealthStatus == ClashHealthUnhealthy {
			latency = append(latency, *view)
		}
	}
	return latency, exit
}

func clashNodeLive(view *ClashNodeView) bool {
	return view.Status == ClashNodeStatusActive && view.ProfileEnabled && !view.ProfileDeleted
}

func dueForLatency(views []ClashNodeView, settings *ClashPoolSettings, now time.Time) []ClashNodeView {
	out := make([]ClashNodeView, 0)
	for i := range views {
		view := &views[i]
		if !clashNodeLive(view) {
			continue
		}
		interval := time.Duration(settings.UnboundCheckIntervalSeconds) * time.Second
		if len(view.Accounts) > 0 {
			interval = time.Duration(settings.BoundCheckIntervalSeconds) * time.Second
		}
		if view.LastCheckedAt == nil || now.Sub(*view.LastCheckedAt) >= interval-clashLeaderCycle/2 {
			out = append(out, *view)
		}
	}
	return out
}

func dueForExitProbe(views []ClashNodeView, settings *ClashPoolSettings, now time.Time) []ClashNodeView {
	interval := time.Duration(settings.ExitProbeIntervalMinutes) * time.Minute
	due := make([]ClashNodeView, 0)
	for i := range views {
		view := &views[i]
		if !clashNodeLive(view) || view.HealthStatus == ClashHealthUnhealthy {
			continue
		}
		stale := view.ExitStatus == ClashExitUnknown || view.ExitStatus == ClashExitStale
		expired := view.ExitCheckedAt == nil || now.Sub(*view.ExitCheckedAt) >= interval
		if stale || expired {
			due = append(due, *view)
		}
	}
	// Bound nodes and never-probed nodes first.
	priority := func(v *ClashNodeView) int {
		p := 0
		if len(v.Accounts) == 0 {
			p += 2
		}
		if v.ExitIP != "" {
			p++
		}
		return p
	}
	sort.SliceStable(due, func(i, j int) bool { return priority(&due[i]) < priority(&due[j]) })
	budget := settings.ExitProbePerMinute * int(clashLeaderCycle/time.Second) / 60
	if budget < 1 {
		budget = 1
	}
	if len(due) > budget {
		due = due[:budget]
	}
	return due
}
