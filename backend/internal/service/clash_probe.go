package service

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"go.uber.org/zap"
)

const (
	clashProbeConcurrency     = 16
	clashExitProbeConcurrency = 4
	clashManualProbeLimit     = 50
	// clashGlobalFailureRatio: when at least this share of a latency round
	// fails, the problem is almost certainly local (network or core), so no
	// node is marked unhealthy.
	clashGlobalFailureRatio   = 0.8
	clashGlobalFailureMinimum = 5
	// clashLatencyRetryDelay is the pause before one more attempt. A single
	// cold-start failure should not be the recorded result.
	clashLatencyRetryDelay = 200 * time.Millisecond
)

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return u
}

// ClashLatencyResult is one latency probe outcome.
type ClashLatencyResult struct {
	NodeID    int64  `json:"node_id"`
	Success   bool   `json:"success"`
	LatencyMs *int   `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
	Health    string `json:"health_status"`
}

// probeLatency runs delay tests through the core and records health with
// hysteresis. With guardGlobalFailure, a round where almost every node fails
// only records latency and errors (global failure protection).
func (s *ClashService) probeLatency(ctx context.Context, nodes []ClashNodeView, settings *ClashPoolSettings, guardGlobalFailure bool) ([]ClashLatencyResult, error) {
	if s.runtime == nil || !s.runtime.Ready() {
		return nil, infraerrors.ServiceUnavailable(ClashErrCodeRuntimeUnavailable, "clash core is not running")
	}
	timeout := time.Duration(settings.HealthTimeoutMs) * time.Millisecond
	results := make([]ClashLatencyResult, len(nodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, clashProbeConcurrency)
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			node := &nodes[i]
			res := ClashLatencyResult{NodeID: node.ID}
			delay, err := s.measureNodeDelay(ctx, ClashProxyName(node.ID), settings.HealthTestURL, timeout)
			if err != nil {
				res.Error = err.Error()
			} else {
				ms := int(delay / time.Millisecond)
				res.Success, res.LatencyMs = true, &ms
			}
			results[i] = res
		}(i)
	}
	wg.Wait()

	failures := 0
	for _, res := range results {
		if !res.Success {
			failures++
		}
	}
	suppress := guardGlobalFailure && len(results) >= clashGlobalFailureMinimum &&
		float64(failures) >= clashGlobalFailureRatio*float64(len(results))
	if suppress {
		s.log().Warn("clash latency round mostly failed; treating as a local problem and keeping node health",
			zap.Int("failed", failures), zap.Int("total", len(results)))
	}

	now := time.Now().UTC()
	updates := make([]ClashNodeHealthUpdate, 0, len(results))
	for i := range results {
		node := &nodes[i]
		res := &results[i]
		update := ClashNodeHealthUpdate{
			NodeID:               node.ID,
			HealthStatus:         node.HealthStatus,
			LatencyMs:            res.LatencyMs,
			ConsecutiveFailures:  node.ConsecutiveFailures,
			ConsecutiveSuccesses: node.ConsecutiveSuccesses,
			Error:                res.Error,
			CheckedAt:            now,
		}
		if res.Success {
			update.ConsecutiveSuccesses++
			update.ConsecutiveFailures = 0
			if update.HealthStatus != ClashHealthHealthy &&
				(update.HealthStatus == ClashHealthUnknown || update.ConsecutiveSuccesses >= settings.RecoveryThreshold) {
				update.HealthStatus = ClashHealthHealthy
			}
		} else if !suppress {
			update.ConsecutiveFailures++
			update.ConsecutiveSuccesses = 0
			if update.ConsecutiveFailures >= settings.FailureThreshold {
				update.HealthStatus = ClashHealthUnhealthy
			}
		}
		res.Health = update.HealthStatus
		updates = append(updates, update)
	}
	if err := s.repo.UpdateNodeHealth(ctx, updates); err != nil {
		return results, err
	}
	return results, nil
}

// measureNodeDelay probes once, then once more after clashLatencyRetryDelay
// when the first attempt fails and the caller is still waiting.
func (s *ClashService) measureNodeDelay(ctx context.Context, name, testURL string, timeout time.Duration) (time.Duration, error) {
	delay, err := s.runtime.DelayTest(ctx, name, testURL, timeout)
	if err == nil || ctx.Err() != nil {
		return delay, err
	}
	timer := time.NewTimer(clashLatencyRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-timer.C:
	}
	return s.runtime.DelayTest(ctx, name, testURL, timeout)
}

// TestNodesLatency runs an on-demand latency round for the given nodes (or a
// whole profile) and reconciles pauses afterwards.
func (s *ClashService) TestNodesLatency(ctx context.Context, nodeIDs []int64, profileID *int64) ([]ClashLatencyResult, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	nodes, err := s.selectNodes(ctx, nodeIDs, profileID, true)
	if err != nil {
		return nil, err
	}
	results, err := s.probeLatency(ctx, nodes, s.poolSettings(ctx), false)
	if err != nil {
		return results, err
	}
	return results, s.ReconcilePauses(ctx)
}

func (s *ClashService) selectNodes(ctx context.Context, nodeIDs []int64, profileID *int64, liveOnly bool) ([]ClashNodeView, error) {
	all, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[int64]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		wanted[id] = struct{}{}
	}
	out := make([]ClashNodeView, 0)
	for i := range all {
		node := &all[i]
		if len(wanted) > 0 {
			if _, ok := wanted[node.ID]; !ok {
				continue
			}
		} else if profileID != nil && node.ProfileID != *profileID {
			continue
		}
		if liveOnly && (node.Status != ClashNodeStatusActive || !node.ProfileEnabled || node.ProfileDeleted) {
			continue
		}
		out = append(out, *node)
	}
	if len(out) > clashManualProbeLimit && len(wanted) > 0 {
		return nil, infraerrors.BadRequest("CLASH_TOO_MANY_NODES", fmt.Sprintf("at most %d nodes per request", clashManualProbeLimit))
	}
	return out, nil
}

// ClashExitProbeResult is one egress probe outcome.
type ClashExitProbeResult struct {
	NodeID     int64  `json:"node_id"`
	Success    bool   `json:"success"`
	ExitIP     string `json:"exit_ip,omitempty"`
	Country    string `json:"country,omitempty"`
	ExitStatus string `json:"exit_status"`
	Error      string `json:"error,omitempty"`
}

// ProbeNodesExit probes egress IPs on demand.
func (s *ClashService) ProbeNodesExit(ctx context.Context, nodeIDs []int64, profileID *int64) ([]ClashExitProbeResult, error) {
	if err := s.requireEnabled(); err != nil {
		return nil, err
	}
	nodes, err := s.selectNodes(ctx, nodeIDs, profileID, true)
	if err != nil {
		return nil, err
	}
	if len(nodes) > clashManualProbeLimit {
		nodes = nodes[:clashManualProbeLimit]
	}
	results := s.probeExits(ctx, nodes, s.poolSettings(ctx))
	return results, s.ReconcilePauses(ctx)
}

func (s *ClashService) managedProxies(ctx context.Context, nodes []ClashNodeView) (map[int64]*Proxy, error) {
	ids := make([]int64, 0, len(nodes))
	for i := range nodes {
		ids = append(ids, nodes[i].ProxyID)
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*Proxy, len(proxies))
	for i := range proxies {
		out[proxies[i].ID] = &proxies[i]
	}
	return out, nil
}

// probeExits resolves each node's egress IP through its managed proxy. A
// failed probe only marks the exit stale; it never affects node health.
func (s *ClashService) probeExits(ctx context.Context, nodes []ClashNodeView, settings *ClashPoolSettings) []ClashExitProbeResult {
	results := make([]ClashExitProbeResult, len(nodes))
	if s.prober == nil || len(nodes) == 0 {
		return results
	}
	proxies, err := s.managedProxies(ctx, nodes)
	if err != nil {
		s.log().Warn("load clash managed proxies failed", zap.Error(err))
		return results
	}
	directIP := s.directEgressIP(ctx)

	var wg sync.WaitGroup
	sem := make(chan struct{}, clashExitProbeConcurrency)
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = s.probeOneExit(ctx, &nodes[i], proxies[nodes[i].ProxyID], settings, directIP)
		}(i)
	}
	wg.Wait()
	return results
}

const clashDirectIPCacheTTL = 10 * time.Minute

// directEgressIP returns this host's own egress IP (best effort, cached); a
// node whose exit equals it provides no isolation.
func (s *ClashService) directEgressIP(ctx context.Context) string {
	s.directMu.Lock()
	defer s.directMu.Unlock()
	if s.directIP != "" && time.Since(s.directIPAt) < clashDirectIPCacheTTL {
		return s.directIP
	}
	info, _, err := s.prober.ProbeProxy(ctx, "")
	if err != nil || info == nil {
		return s.directIP
	}
	s.directIP, s.directIPAt = strings.TrimSpace(info.IP), time.Now()
	return s.directIP
}

func (s *ClashService) probeOneExit(ctx context.Context, node *ClashNodeView, proxy *Proxy, settings *ClashPoolSettings, directIP string) ClashExitProbeResult {
	res := ClashExitProbeResult{NodeID: node.ID, ExitStatus: node.ExitStatus}
	now := time.Now().UTC()
	update := ClashNodeExitUpdate{
		NodeID:      node.ID,
		ExitIP:      node.ExitIP,
		Country:     node.ExitCountry,
		CountryCode: node.ExitCountryCode,
		Region:      node.ExitRegion,
		City:        node.ExitCity,
		ExitStatus:  node.ExitStatus,
		PendingIP:   node.ExitPendingIP,
		CheckedAt:   now,
	}
	if proxy == nil {
		res.Error = "managed proxy missing"
		return res
	}
	info, latencyMs, err := s.prober.ProbeProxy(ctx, proxy.URL())
	if err != nil || info == nil || strings.TrimSpace(info.IP) == "" {
		res.Error = "exit probe failed"
		if err != nil {
			res.Error = err.Error()
		}
		if update.ExitIP != "" && update.ExitStatus == ClashExitOK {
			update.ExitStatus = ClashExitStale
		}
		res.ExitStatus = update.ExitStatus
		if err := s.repo.UpdateNodeExit(ctx, update); err != nil {
			s.log().Warn("record clash exit probe failed", zap.Int64("node_id", node.ID), zap.Error(err))
		}
		return res
	}

	ip := normalizeClashExitIP(info.IP)
	res.Success, res.ExitIP, res.Country = true, ip, info.Country
	bound := len(node.Accounts) > 0
	switch {
	case update.ExitIP == "" || update.ExitIP == ip:
		applyClashExitGeo(&update, info, update.ExitIP == ip)
		update.ExitIP, update.ExitStatus, update.PendingIP = ip, ClashExitOK, ""
	case update.ExitStatus == ClashExitChanged && update.PendingIP == ip:
		// Still waiting for the admin to accept the same new address.
	case settings.ExitChangePolicy == ClashExitChangeAccept || !bound:
		applyClashExitGeo(&update, info, false)
		update.ExitIP, update.ExitStatus, update.PendingIP = ip, ClashExitOK, ""
		update.ChangedAt = &now
	default:
		update.ExitStatus, update.PendingIP = ClashExitChanged, ip
		update.ChangedAt = &now
	}
	res.ExitStatus = update.ExitStatus

	if settings.PlatformChecksEnabled {
		checks := runClashPlatformChecks(ctx, proxy.URL())
		checks.CheckedAt = &now
		update.PlatformChecks = &checks
	}
	if err := s.repo.UpdateNodeExit(ctx, update); err != nil {
		s.log().Warn("record clash exit probe failed", zap.Int64("node_id", node.ID), zap.Error(err))
	}
	if directIP != "" && normalizeClashExitIP(directIP) == ip {
		if err := s.repo.SetNodeStatus(ctx, node.ID, ClashNodeStatusInvalid, "exit IP equals this server's own IP"); err == nil {
			s.structuralChange(ctx)
		}
	}
	if s.latencyCache != nil {
		latency := latencyMs
		geo := ClashNodeExitUpdate{Country: info.Country, CountryCode: info.CountryCode, Region: info.Region, City: info.City}
		if update.ExitIP == ip {
			geo = update
		}
		_ = s.latencyCache.SetProxyLatency(ctx, proxy.ID, &ProxyLatencyInfo{
			Success: true, LatencyMs: &latency, Message: "clash exit probe",
			IPAddress: ip, Country: geo.Country, CountryCode: geo.CountryCode,
			Region: geo.Region, City: geo.City, UpdatedAt: now,
		})
	}
	return res
}

// applyClashExitGeo records the probed location. The fallback probe service
// reports the address without a location; an unchanged address then keeps
// its known location instead of being blanked.
func applyClashExitGeo(update *ClashNodeExitUpdate, info *ProxyExitInfo, sameIP bool) {
	if sameIP && strings.TrimSpace(info.Country) == "" && strings.TrimSpace(info.CountryCode) == "" {
		return
	}
	update.Country, update.CountryCode, update.Region, update.City = info.Country, info.CountryCode, info.Region, info.City
}

func normalizeClashExitIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return strings.TrimSpace(raw)
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.String()
}

// runClashPlatformChecks reuses the proxy quality targets through a
// short-lived transport so no idle tunnel is kept per node.
func runClashPlatformChecks(ctx context.Context, proxyURL string) ClashPlatformChecks {
	checks := ClashPlatformChecks{Results: make(map[string]string, len(proxyQualityTargets))}
	_, parsed, err := proxyurl.Parse(proxyURL)
	if err != nil || parsed == nil {
		return checks
	}
	transport := &http.Transport{
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: proxyQualityResponseHeaderTimeout,
	}
	if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
		return checks
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: proxyQualityRequestTimeout}
	for _, target := range proxyQualityTargets {
		item := runProxyQualityTarget(ctx, client, target)
		checks.Results[target.Target] = item.Status
	}
	return checks
}

// ReconcilePauses aligns Clash pauses with node availability: every account
// bound to an unavailable exit gets (or keeps) a [clash-exit] pause; pauses of
// accounts whose exit recovered, or that were rebound, are cleared.
func (s *ClashService) ReconcilePauses(ctx context.Context) error {
	views, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return err
	}
	disabledPool := !s.Enabled()
	desired := make(map[int64]string)
	for i := range views {
		view := &views[i]
		if len(view.Accounts) == 0 {
			continue
		}
		reason := view.unavailableReason()
		if disabledPool {
			reason = "clash proxy pool is disabled"
		}
		if reason == "" {
			continue
		}
		text := fmt.Sprintf("%s %s: %s", ClashPauseReasonPrefix, view.Name, reason)
		for _, acc := range view.Accounts {
			desired[acc.ID] = text
		}
	}
	paused, err := s.repo.ListPausedAccounts(ctx)
	if err != nil {
		return err
	}
	settings := s.poolSettings(ctx)
	ttl := time.Duration(settings.PauseTTLMinutes) * time.Minute
	now := time.Now().UTC()
	changed, err := s.repo.PauseAccounts(ctx, desired, now.Add(ttl), now.Add(ttl/2))
	if err != nil {
		return err
	}
	clear := make([]int64, 0)
	for id := range paused {
		if _, keep := desired[id]; !keep {
			clear = append(clear, id)
		}
	}
	cleared, err := s.repo.ClearPauses(ctx, clear)
	if err != nil {
		return err
	}
	s.dropTempUnschedCache(ctx, append(changed, cleared...))
	for _, id := range changed {
		if _, already := paused[id]; !already {
			s.log().Warn("clash exit unavailable; account paused", zap.Int64("account_id", id), zap.String("reason", desired[id]))
		}
	}
	for _, id := range cleared {
		s.log().Info("clash exit recovered; account pause cleared", zap.Int64("account_id", id))
	}
	return nil
}

func (s *ClashService) dropTempUnschedCache(ctx context.Context, accountIDs []int64) {
	if s.tempUnschedCache == nil {
		return
	}
	for _, id := range accountIDs {
		_ = s.tempUnschedCache.DeleteTempUnsched(ctx, id)
	}
}

// ClashExitOption is one selectable exit for the account proxy selector.
type ClashExitOption struct {
	ProxyID           int64
	NodeID            int64
	ProfileID         int64
	ProfileName       string
	NodeName          string
	Type              string
	Status            string
	HealthStatus      string
	LatencyMs         *int
	ExitIP            string
	ExitCountry       string
	ExitCountryCode   string
	ExitCity          string
	ExitStatus        string
	ExitPendingIP     string
	ExitKey           string
	Available         bool
	UnavailableReason string
	// Occupants are the non-shadow accounts on the same exit (across nodes).
	Occupants      []ClashBoundAccount
	PlatformChecks ClashPlatformChecks
}

// ClashExitList is the selector payload.
type ClashExitList struct {
	MaxAccountsPerExit       int
	AllowUnprobedExitBinding bool
	Exits                    []ClashExitOption
}

// ListExits returns every node usable as an account exit, with occupancy.
func (s *ClashService) ListExits(ctx context.Context) (*ClashExitList, error) {
	settings := s.poolSettings(ctx)
	views, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return nil, err
	}
	occupants := clashExitOccupants(views)
	list := &ClashExitList{
		MaxAccountsPerExit:       settings.MaxAccountsPerExit,
		AllowUnprobedExitBinding: settings.AllowUnprobedExitBinding,
		Exits:                    make([]ClashExitOption, 0, len(views)),
	}
	for i := range views {
		view := &views[i]
		// Deleted subscriptions and hidden nodes stay listed only while an
		// account is still bound to them, so that binding keeps resolving.
		if (view.ProfileDeleted || view.Hidden) && len(view.Accounts) == 0 {
			continue
		}
		key := view.ExitKey()
		list.Exits = append(list.Exits, ClashExitOption{
			ProxyID:           view.ProxyID,
			NodeID:            view.ID,
			ProfileID:         view.ProfileID,
			ProfileName:       view.ProfileName,
			NodeName:          view.Name,
			Type:              view.Type,
			Status:            view.Status,
			HealthStatus:      view.HealthStatus,
			LatencyMs:         view.LatencyMs,
			ExitIP:            view.ExitIP,
			ExitCountry:       view.ExitCountry,
			ExitCountryCode:   view.ExitCountryCode,
			ExitCity:          view.ExitCity,
			ExitStatus:        view.ExitStatus,
			ExitPendingIP:     view.ExitPendingIP,
			ExitKey:           key,
			Available:         view.Available(),
			UnavailableReason: view.unavailableReason(),
			Occupants:         occupants[key],
			PlatformChecks:    view.PlatformChecks,
		})
	}
	sort.SliceStable(list.Exits, func(i, j int) bool {
		if list.Exits[i].ProfileName != list.Exits[j].ProfileName {
			return list.Exits[i].ProfileName < list.Exits[j].ProfileName
		}
		return list.Exits[i].NodeName < list.Exits[j].NodeName
	})
	return list, nil
}

// clashExitOccupants groups non-shadow bound accounts by exit key.
func clashExitOccupants(views []ClashNodeView) map[string][]ClashBoundAccount {
	out := make(map[string][]ClashBoundAccount)
	for i := range views {
		key := views[i].ExitKey()
		for _, acc := range views[i].Accounts {
			if acc.ParentAccountID != nil {
				continue
			}
			out[key] = append(out[key], acc)
		}
	}
	return out
}

// ClashExitConflict is an exit carrying more accounts than allowed.
type ClashExitConflict struct {
	ExitKey  string
	Accounts []ClashBoundAccount
}

// FindExitConflicts reports exits over the per-exit limit (e.g. two nodes
// that turned out to share an egress IP after probing).
func (s *ClashService) FindExitConflicts(ctx context.Context) ([]ClashExitConflict, error) {
	settings := s.poolSettings(ctx)
	views, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return nil, err
	}
	conflicts := make([]ClashExitConflict, 0)
	for key, accounts := range clashExitOccupants(views) {
		if len(accounts) > settings.MaxAccountsPerExit {
			conflicts = append(conflicts, ClashExitConflict{ExitKey: key, Accounts: accounts})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ExitKey < conflicts[j].ExitKey })
	return conflicts, nil
}

// ClashBindingCheck describes an account proxy change to validate.
type ClashBindingCheck struct {
	// AccountIDs are the accounts being bound (empty for a new account).
	AccountIDs []int64
	// NewAccounts counts accounts about to be created with this binding.
	NewAccounts int
	OldProxyID  *int64
	NewProxyID  *int64
	// CustomRelay is true when the (resulting) account forwards through a
	// custom relay base URL, which would receive the local proxy URL.
	CustomRelay bool
	Bulk        bool
}

// ClashExitGuard validates account bindings to Clash exits.
type ClashExitGuard interface {
	CheckAccountBinding(ctx context.Context, check ClashBindingCheck) (unlock func(), err error)
	// OnBindingsChanged re-evaluates pauses and listener placeholders.
	OnBindingsChanged(ctx context.Context)
}

// OnBindingsChanged implements ClashExitGuard.
func (s *ClashService) OnBindingsChanged(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}
	s.structuralChange(context.WithoutCancel(ctx))
	go func() {
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.ReconcilePauses(reconcileCtx); err != nil {
			s.log().Warn("reconcile clash pauses failed", zap.Error(err))
		}
	}()
}

var _ ClashExitGuard = (*ClashService)(nil)

// CheckAccountBinding enforces exit exclusivity. It only validates when the
// binding changes to a Clash exit, so accounts parked on a missing node can
// still be edited. The returned unlock must be called after persisting.
func (s *ClashService) CheckAccountBinding(ctx context.Context, check ClashBindingCheck) (func(), error) {
	noop := func() {}
	if check.NewProxyID == nil || *check.NewProxyID <= 0 {
		return noop, nil
	}
	changed := check.OldProxyID == nil || *check.OldProxyID != *check.NewProxyID
	proxy, err := s.proxyRepo.GetByID(ctx, *check.NewProxyID)
	if err != nil || proxy == nil || !proxy.IsClashManaged() {
		return noop, nil
	}
	if check.CustomRelay {
		return noop, infraerrors.BadRequest(ClashErrCodeRelayUnsupported,
			"accounts using a custom relay base URL cannot use a Clash exit (the relay would receive a local proxy address)")
	}
	if !changed {
		return noop, nil
	}
	if check.Bulk {
		return noop, infraerrors.BadRequest(ClashErrCodeBulkUnsupported,
			"Clash exits are assigned one account at a time; bind them from the account editor")
	}

	s.guardMu.Lock()
	unlock := s.guardMu.Unlock
	fail := func(err error) (func(), error) {
		unlock()
		return noop, err
	}

	views, err := s.repo.ListAllNodeViews(ctx)
	if err != nil {
		return fail(err)
	}
	var target *ClashNodeView
	for i := range views {
		if views[i].ProxyID == *check.NewProxyID {
			target = &views[i]
			break
		}
	}
	if target == nil {
		return fail(ErrClashNodeNotFound)
	}
	if reason := target.unavailableReason(); reason != "" {
		return fail(infraerrors.BadRequest(ClashErrCodeExitUnavailable, "this Clash exit is unavailable: "+reason))
	}
	settings := s.poolSettings(ctx)
	if target.ExitIP == "" && !settings.AllowUnprobedExitBinding {
		return fail(infraerrors.BadRequest(ClashErrCodeExitUnprobed,
			"the egress IP of this Clash exit has not been probed yet; probe it first"))
	}
	self := make(map[int64]struct{}, len(check.AccountIDs))
	for _, id := range check.AccountIDs {
		self[id] = struct{}{}
	}
	others := make([]string, 0)
	for _, acc := range clashExitOccupants(views)[target.ExitKey()] {
		if _, mine := self[acc.ID]; !mine {
			others = append(others, acc.Name)
		}
	}
	incoming := len(check.AccountIDs) + check.NewAccounts
	if len(others)+incoming > settings.MaxAccountsPerExit {
		return fail(infraerrors.Conflict(ClashErrCodeExitOccupied,
			fmt.Sprintf("this exit already serves %d account(s) (limit %d)", len(others), settings.MaxAccountsPerExit)).
			WithMetadata(map[string]string{"accounts": strings.Join(limitStrings(others, 10), ", ")}))
	}
	return unlock, nil
}

// Clash pool alert metric types.
const (
	ClashMetricExitPausedAccounts   = "clash_exit_paused_account_count"
	ClashMetricProfileRefreshFailed = "clash_profile_refresh_failed_count"
	ClashMetricExitConflicts        = "clash_exit_conflict_count"
	ClashMetricRuntimeUnready       = "clash_runtime_unready_instance_count"
)

var _ ClashAlertMetrics = (*ClashService)(nil)

// ClashAlertMetric implements ClashAlertMetrics.
func (s *ClashService) ClashAlertMetric(ctx context.Context, metricType string) (float64, bool) {
	if s == nil || s.repo == nil {
		return 0, false
	}
	switch metricType {
	case ClashMetricExitPausedAccounts:
		paused, err := s.repo.ListPausedAccounts(ctx)
		if err != nil {
			return 0, false
		}
		return float64(len(paused)), true
	case ClashMetricProfileRefreshFailed:
		profiles, err := s.repo.ListProfiles(ctx)
		if err != nil {
			return 0, false
		}
		failed := 0
		for _, profile := range profiles {
			if profile.Enabled && (profile.LastRefreshStatus == ClashRefreshError || profile.LastRefreshStatus == ClashRefreshSkipped) {
				failed++
			}
		}
		return float64(failed), true
	case ClashMetricExitConflicts:
		conflicts, err := s.FindExitConflicts(ctx)
		if err != nil {
			return 0, false
		}
		return float64(len(conflicts)), true
	case ClashMetricRuntimeUnready:
		if !s.Enabled() || s.notifier == nil {
			return 0, true
		}
		instances, err := s.notifier.ListInstanceStatuses(ctx)
		if err != nil {
			return 0, false
		}
		unready := 0
		for _, instance := range instances {
			if !instance.Ready {
				unready++
			}
		}
		return float64(unready), true
	default:
		return 0, false
	}
}
