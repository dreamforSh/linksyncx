//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type clashTestEnv struct {
	repo     *fakeClashRepo
	runtime  *fakeClashRuntime
	prober   *fakeExitProber
	settings *settingRepoStub
	cfg      *config.Config
	svc      *ClashService
}

func newClashTestEnv(t *testing.T, poolSettings *ClashPoolSettings) *clashTestEnv {
	t.Helper()
	repo := newFakeClashRepo()
	runtime := newFakeClashRuntime()
	prober := &fakeExitProber{directIP: "203.0.113.250", byPort: map[string]string{}}
	settingRepo := &settingRepoStub{values: map[string]string{}}
	if poolSettings != nil {
		raw, err := json.Marshal(poolSettings)
		require.NoError(t, err)
		settingRepo.values[SettingKeyClashPoolSettings] = string(raw)
	}
	cfg := &config.Config{}
	cfg.ClashPool = config.ClashPoolConfig{
		Mode: config.ClashPoolModeEmbedded, PortRangeStart: 20000, PortRangeEnd: 20100,
		Subscription: config.ClashPoolSubscriptionConfig{AllowPrivateHosts: true, MaxBodyBytes: 1 << 20, FetchTimeoutSeconds: 10, MaxNodesPerProfile: 100},
	}
	cfg.Totp.EncryptionKeyConfigured = true
	svc := NewClashService(cfg, repo, &fakeClashProxyRepo{repo: repo}, NewSettingService(settingRepo, cfg),
		fakeClashEncryptor{}, runtime, nil, prober, nil, nil)
	return &clashTestEnv{repo: repo, runtime: runtime, prober: prober, settings: settingRepo, cfg: cfg, svc: svc}
}

func withSettings(mutate func(*ClashPoolSettings)) *ClashPoolSettings {
	s := defaultClashPoolSettings()
	mutate(s)
	return s
}

func requireClashReason(t *testing.T, err error, reason string) {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, reason, infraerrors.Reason(err), "error: %v", err)
}

func ptr64(v int64) *int64 { return &v }

func TestClashGuardExclusivity(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("机场A", true)
	nodeA := env.repo.addNode(profile.ID, "hk-01", "198.51.100.1")
	sameExit := env.repo.addNode(profile.ID, "hk-02", "198.51.100.1") // another entry, same landing IP
	free := env.repo.addNode(profile.ID, "us-01", "198.51.100.2")
	manual := env.repo.addManualProxy()
	owner := env.repo.bind("owner", nodeA.ProxyID, nil)
	env.repo.bind("owner-shadow", nodeA.ProxyID, &owner.ID) // shadows never count

	ctx := context.Background()
	unlock, err := env.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(manual.ID)})
	require.NoError(t, err, "manual proxies are not guarded")
	unlock()

	_, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(sameExit.ProxyID)})
	requireClashReason(t, err, ClashErrCodeExitOccupied)
	require.Contains(t, err.Error(), "1 account")

	unlock, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(free.ProxyID)})
	require.NoError(t, err)
	unlock()

	// The owner re-saving its own binding is not a new occupant.
	unlock, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{AccountIDs: []int64{owner.ID}, OldProxyID: ptr64(free.ProxyID), NewProxyID: ptr64(nodeA.ProxyID)})
	require.NoError(t, err)
	unlock()

	_, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{AccountIDs: []int64{1, 2}, NewProxyID: ptr64(free.ProxyID), Bulk: true})
	requireClashReason(t, err, ClashErrCodeBulkUnsupported)
}

func TestClashGuardAvailabilityRules(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("机场A", true)
	unprobed := env.repo.addNode(profile.ID, "jp-01", "")
	missing := env.repo.addNode(profile.ID, "sg-01", "198.51.100.9", func(n *ClashNode) { n.Status = ClashNodeStatusMissing })
	healthy := env.repo.addNode(profile.ID, "us-01", "198.51.100.2")
	ctx := context.Background()

	_, err := env.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(unprobed.ProxyID)})
	requireClashReason(t, err, ClashErrCodeExitUnprobed)

	_, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(missing.ProxyID)})
	requireClashReason(t, err, ClashErrCodeExitUnavailable)

	// An account already parked on a missing node can still be edited.
	unlock, err := env.svc.CheckAccountBinding(ctx, ClashBindingCheck{AccountIDs: []int64{7}, OldProxyID: ptr64(missing.ProxyID), NewProxyID: ptr64(missing.ProxyID)})
	require.NoError(t, err)
	unlock()

	// Custom relays would receive the local proxy URL, even for an unchanged binding.
	_, err = env.svc.CheckAccountBinding(ctx, ClashBindingCheck{AccountIDs: []int64{7}, OldProxyID: ptr64(healthy.ProxyID), NewProxyID: ptr64(healthy.ProxyID), CustomRelay: true})
	requireClashReason(t, err, ClashErrCodeRelayUnsupported)

	env2 := newClashTestEnv(t, withSettings(func(s *ClashPoolSettings) {
		s.AllowUnprobedExitBinding = true
		s.MaxAccountsPerExit = 2
	}))
	p2 := env2.repo.addProfile("B", true)
	n2 := env2.repo.addNode(p2.ID, "kr-01", "")
	env2.repo.bind("first", n2.ProxyID, nil)
	unlock, err = env2.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(n2.ProxyID)})
	require.NoError(t, err, "unprobed allowed by settings and limit 2 leaves room")
	unlock()
	env2.repo.bind("second", n2.ProxyID, nil)
	_, err = env2.svc.CheckAccountBinding(ctx, ClashBindingCheck{NewAccounts: 1, NewProxyID: ptr64(n2.ProxyID)})
	requireClashReason(t, err, ClashErrCodeExitOccupied)
}

func TestClashReconcilePausesAndResumes(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("机场A", true)
	node := env.repo.addNode(profile.ID, "hk-01", "198.51.100.1")
	other := env.repo.addNode(profile.ID, "us-01", "198.51.100.2")
	acc := env.repo.bind("bound", node.ProxyID, nil)
	shadow := env.repo.bind("shadow", node.ProxyID, &acc.ID)
	fine := env.repo.bind("fine", other.ProxyID, nil)
	// A foreign pause (rate limit) must never be cleared by the pool.
	foreignUntil := time.Now().Add(time.Hour)
	env.repo.accounts[fine.ID].Reason, env.repo.accounts[fine.ID].Until = "rate limited", &foreignUntil

	ctx := context.Background()
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	require.Empty(t, env.repo.account(acc.ID).Reason, "healthy exits do not pause")

	require.NoError(t, env.repo.SetNodeStatus(ctx, node.ID, ClashNodeStatusMissing, "removed"))
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	paused := env.repo.account(acc.ID)
	require.True(t, strings.HasPrefix(paused.Reason, ClashPauseReasonPrefix))
	require.Contains(t, paused.Reason, "hk-01")
	require.NotNil(t, paused.Until)
	require.True(t, strings.HasPrefix(env.repo.account(shadow.ID).Reason, ClashPauseReasonPrefix))
	firstUntil := *paused.Until

	// Within the renewal window the pause is left alone.
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	require.Equal(t, firstUntil, *env.repo.account(acc.ID).Until)

	require.NoError(t, env.repo.SetNodeStatus(ctx, node.ID, ClashNodeStatusActive, ""))
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	require.Empty(t, env.repo.account(acc.ID).Reason)
	require.Empty(t, env.repo.account(shadow.ID).Reason)
	require.Equal(t, "rate limited", env.repo.account(fine.ID).Reason)

	// Exit changed in place (pause policy) also pauses.
	env.repo.nodes[node.ID].ExitStatus, env.repo.nodes[node.ID].ExitPendingIP = ClashExitChanged, "198.51.100.7"
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	require.Contains(t, env.repo.account(acc.ID).Reason, "198.51.100.7")
	require.NoError(t, env.svc.AcceptNodeExit(ctx, node.ID))
	require.Empty(t, env.repo.account(acc.ID).Reason)
	require.Equal(t, "198.51.100.7", env.repo.node(node.ID).ExitIP)

	// Profile disabled pauses every bound account.
	env.repo.profiles[profile.ID].Enabled = false
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	require.Contains(t, env.repo.account(fine.ID).Reason, "rate limited", "longer foreign pause kept")
	require.Contains(t, env.repo.account(acc.ID).Reason, "subscription disabled")
}

func TestClashProbeLatencyHysteresisAndGlobalGuard(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	var nodes []*ClashNode
	for i := 0; i < 6; i++ {
		nodes = append(nodes, env.repo.addNode(profile.ID, fmt.Sprintf("n%d", i), fmt.Sprintf("198.51.100.%d", i+1)))
	}
	settings := defaultClashPoolSettings()
	ctx := context.Background()
	views := func() []ClashNodeView {
		all, err := env.repo.ListAllNodeViews(ctx)
		require.NoError(t, err)
		return all
	}

	env.runtime.delays[ClashProxyName(nodes[0].ID)] = errors.New("timeout")
	for round := 1; round <= 3; round++ {
		_, err := env.svc.probeLatency(ctx, views(), settings, true)
		require.NoError(t, err)
		want := ClashHealthHealthy
		if round >= settings.FailureThreshold {
			want = ClashHealthUnhealthy
		}
		require.Equal(t, want, env.repo.node(nodes[0].ID).HealthStatus, "round %d", round)
	}
	delete(env.runtime.delays, ClashProxyName(nodes[0].ID))
	_, err := env.svc.probeLatency(ctx, views(), settings, true)
	require.NoError(t, err)
	require.Equal(t, ClashHealthUnhealthy, env.repo.node(nodes[0].ID).HealthStatus, "one success is not enough to recover")
	_, err = env.svc.probeLatency(ctx, views(), settings, true)
	require.NoError(t, err)
	require.Equal(t, ClashHealthHealthy, env.repo.node(nodes[0].ID).HealthStatus)

	// Every node failing at once looks like a local outage: no transitions.
	for _, n := range nodes {
		env.runtime.delays[ClashProxyName(n.ID)] = errors.New("network down")
	}
	for round := 0; round < 4; round++ {
		_, err = env.svc.probeLatency(ctx, views(), settings, true)
		require.NoError(t, err)
	}
	for _, n := range nodes {
		require.Equal(t, ClashHealthHealthy, env.repo.node(n.ID).HealthStatus)
	}
}

func TestClashProbeExitPolicies(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	fresh := env.repo.addNode(profile.ID, "fresh", "")
	bound := env.repo.addNode(profile.ID, "bound", "198.51.100.1")
	unbound := env.repo.addNode(profile.ID, "unbound", "198.51.100.2")
	direct := env.repo.addNode(profile.ID, "direct", "")
	env.repo.bind("acc", bound.ProxyID, nil)
	portOf := func(n *ClashNode) string { return strconv.Itoa(env.repo.proxies[n.ProxyID].Port) }
	env.prober.byPort[portOf(fresh)] = "198.51.100.10"
	env.prober.byPort[portOf(bound)] = "198.51.100.11"
	env.prober.byPort[portOf(unbound)] = "198.51.100.12"
	env.prober.byPort[portOf(direct)] = env.prober.directIP

	ctx := context.Background()
	views, err := env.repo.ListAllNodeViews(ctx)
	require.NoError(t, err)
	settings := withSettings(func(s *ClashPoolSettings) { s.PlatformChecksEnabled = false })
	env.svc.probeExits(ctx, views, settings)

	require.Equal(t, "198.51.100.10", env.repo.node(fresh.ID).ExitIP)
	require.Equal(t, ClashExitOK, env.repo.node(fresh.ID).ExitStatus)
	b := env.repo.node(bound.ID)
	require.Equal(t, "198.51.100.1", b.ExitIP, "bound node keeps its exit until accepted")
	require.Equal(t, ClashExitChanged, b.ExitStatus)
	require.Equal(t, "198.51.100.11", b.ExitPendingIP)
	require.Equal(t, "198.51.100.12", env.repo.node(unbound.ID).ExitIP, "unbound nodes follow silently")
	require.Equal(t, ClashNodeStatusInvalid, env.repo.node(direct.ID).Status, "exit equal to the server's own IP gives no isolation")

	// Accept policy applies the change even when bound.
	env2 := newClashTestEnv(t, nil)
	p2 := env2.repo.addProfile("A", true)
	n2 := env2.repo.addNode(p2.ID, "bound", "198.51.100.1")
	env2.repo.bind("acc", n2.ProxyID, nil)
	env2.prober.byPort[strconv.Itoa(env2.repo.proxies[n2.ProxyID].Port)] = "198.51.100.20"
	views2, err := env2.repo.ListAllNodeViews(ctx)
	require.NoError(t, err)
	env2.svc.probeExits(ctx, views2, withSettings(func(s *ClashPoolSettings) {
		s.PlatformChecksEnabled = false
		s.ExitChangePolicy = ClashExitChangeAccept
	}))
	require.Equal(t, "198.51.100.20", env2.repo.node(n2.ID).ExitIP)
	require.Equal(t, ClashExitOK, env2.repo.node(n2.ID).ExitStatus)

	// A failed probe marks a known exit stale without touching health.
	delete(env2.prober.byPort, strconv.Itoa(env2.repo.proxies[n2.ProxyID].Port))
	env2.prober.byPort[strconv.Itoa(env2.repo.proxies[n2.ProxyID].Port)] = ""
	views2, _ = env2.repo.ListAllNodeViews(ctx)
	env2.svc.probeExits(ctx, views2, withSettings(func(s *ClashPoolSettings) { s.PlatformChecksEnabled = false }))
	require.Equal(t, ClashExitStale, env2.repo.node(n2.ID).ExitStatus)
	require.Equal(t, ClashHealthHealthy, env2.repo.node(n2.ID).HealthStatus)
}

func subscriptionServer(t *testing.T, body *string, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, "clash.meta", r.Header.Get("User-Agent"))
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=100; expire=1924992000")
		_, _ = io.WriteString(w, *body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func clashSubscriptionYAML(names ...string) string {
	var b strings.Builder
	b.WriteString("proxies:\n")
	for _, name := range names {
		fmt.Fprintf(&b, "  - {name: %q, type: trojan, server: %s.example.com, port: 443, password: pw}\n", name, strings.ToLower(strings.Fields(name)[0]))
	}
	return b.String()
}

func TestClashRefreshLifecycle(t *testing.T) {
	env := newClashTestEnv(t, nil)
	var mu sync.Mutex
	body := clashSubscriptionYAML("HK 01", "US 01", "JP 01", "剩余流量 10GB")
	srv := subscriptionServer(t, &body, &mu)
	structural := 0
	env.svc.onStructuralChange = func() { structural++ }

	ctx := context.Background()
	name, url := "机场A", srv.URL+"/sub?token=secret"
	summary, refresh, err := env.svc.CreateProfile(ctx, ClashProfileInput{Name: &name, URL: &url})
	require.NoError(t, err)
	require.NotNil(t, refresh)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	require.Equal(t, 3, refresh.Inserted, "the info node is excluded by default")
	require.Equal(t, 1, refresh.Filtered)
	require.NotContains(t, summary.URLMasked, "secret")
	require.Equal(t, "enc:"+url, env.repo.profiles[summary.ID].URLEncrypted)
	require.GreaterOrEqual(t, structural, 1)

	nodes, err := env.repo.ListNodesByProfile(ctx, summary.ID)
	require.NoError(t, err)
	ports := map[string]int{}
	for _, n := range nodes {
		ports[n.Name] = n.ListenPort
		proxy := env.repo.proxies[n.ProxyID]
		require.Equal(t, ProxySourceClash, proxy.Source)
		require.Equal(t, "127.0.0.1", proxy.Host)
		require.NotEmpty(t, proxy.Username)
		require.Len(t, proxy.Password, 32)
	}

	// Renamed node keeps its listener; removed node goes missing.
	mu.Lock()
	body = clashSubscriptionYAML("HK 01 | 1x", "US 01", "KR 01")
	mu.Unlock()
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshOK, refresh.Status, refresh.Error)
	nodes, _ = env.repo.ListNodesByProfile(ctx, summary.ID)
	byName := map[string]ClashNode{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	require.Equal(t, ports["HK 01"], byName["HK 01 | 1x"].ListenPort)
	require.Equal(t, ClashNodeStatusMissing, byName["JP 01"].Status)

	// A sudden collapse is skipped unless forced.
	mu.Lock()
	body = clashSubscriptionYAML("KR 01")
	mu.Unlock()
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, false)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshSkipped, refresh.Status)
	refresh, err = env.svc.RefreshProfile(ctx, summary.ID, true)
	require.NoError(t, err)
	require.Equal(t, ClashRefreshOK, refresh.Status)
	require.Equal(t, 2, refresh.Missing)

	// Duplicate URL and name are rejected.
	_, _, err = env.svc.CreateProfile(ctx, ClashProfileInput{Name: &name, URL: &url})
	requireClashReason(t, err, ClashErrCodeProfileInvalid)
}

func TestClashCreateProfileRequiresEncryptionKey(t *testing.T) {
	env := newClashTestEnv(t, nil)
	env.cfg.Totp.EncryptionKeyConfigured = false
	name, url := "A", "https://example.com/sub"
	_, _, err := env.svc.CreateProfile(context.Background(), ClashProfileInput{Name: &name, URL: &url})
	requireClashReason(t, err, ClashErrCodeEncryptionRequired)
}

func TestClashFetchPolicy(t *testing.T) {
	strict := clashFetchPolicy{}
	lenient := clashFetchPolicy{AllowPrivateHosts: true}
	cases := []struct {
		url             string
		strict, lenient bool // true = allowed
	}{
		{"https://sub.example.com/api?token=x", true, true},
		{"http://127.0.0.1:25500/sub", false, true},
		{"http://localhost:25500/sub", false, true},
		{"http://10.1.2.3/sub", false, true},
		{"http://169.254.169.254/latest/meta-data", false, false},
		{"http://metadata.google.internal/", false, false},
		{"ftp://example.com/sub", false, false},
	}
	for _, tc := range cases {
		u := mustParseURL(tc.url)
		require.Equal(t, tc.strict, strict.validateURL(u) == nil, "strict %s", tc.url)
		require.Equal(t, tc.lenient, lenient.validateURL(u) == nil, "lenient %s", tc.url)
	}
	require.True(t, strict.ipBlocked(net.ParseIP("192.168.1.1")))
	require.False(t, lenient.ipBlocked(net.ParseIP("192.168.1.1")))
	require.True(t, lenient.ipBlocked(net.ParseIP("169.254.169.254")))

	require.Equal(t, "https://sub.example.com/***", maskClashURL("https://sub.example.com/api/v1/client/subscribe?token=abc"))
	require.Equal(t, "***", maskClashURL("not a url"))
	err := &net.OpError{Op: "dial", Err: errors.New("refused")}
	require.NotContains(t, redactClashURLError(fmt.Errorf("Get \"https://a.example.com/s?token=abc\": %w", err), mustParseURL("https://a.example.com/s?token=abc")), "token=abc")
}

func TestClashFetchRejectsLoopbackByDefault(t *testing.T) {
	var mu sync.Mutex
	body := clashSubscriptionYAML("HK 01")
	srv := subscriptionServer(t, &body, &mu)
	_, err := fetchClashSubscription(context.Background(), clashFetchPolicy{Timeout: 5 * time.Second, MaxBodyBytes: 1 << 20}, srv.URL, "clash.meta", nil)
	require.Error(t, err)
	res, err := fetchClashSubscription(context.Background(), clashFetchPolicy{AllowPrivateHosts: true, Timeout: 5 * time.Second, MaxBodyBytes: 1 << 20}, srv.URL, "clash.meta", nil)
	require.NoError(t, err)
	require.NotNil(t, res.UserInfo)
	require.EqualValues(t, 100, res.UserInfo.Total)
}

func TestClashPreviewShowsExcludedNodes(t *testing.T) {
	env := newClashTestEnv(t, nil)
	var mu sync.Mutex
	links := base64.StdEncoding.EncodeToString([]byte("trojan://pw@hk.example.com:443#HK\ntrojan://pw@info.example.com:443#%E5%89%A9%E4%BD%99%E6%B5%81%E9%87%8F"))
	srv := subscriptionServer(t, &links, &mu)
	url := srv.URL
	result, err := env.svc.PreviewProfile(context.Background(), ClashProfileInput{URL: &url})
	require.NoError(t, err)
	require.Equal(t, "uri_list", result.Format)
	require.Equal(t, 2, result.NodeCount)
	require.Equal(t, 1, result.Usable)
}

func TestClashManagerSyncIsolatesRejectedNodes(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	bad := env.repo.addNode(profile.ID, "bad", "198.51.100.1")
	good := env.repo.addNode(profile.ID, "good", "198.51.100.2")
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(context.Context, string, string, string) error { return nil }

	// The first proxy in id order is "bad".
	env.runtime.applyErrs = []error{&ClashConfigError{Message: "proxy 0: missing password"}}
	require.NoError(t, manager.syncOnce(context.Background()))
	require.Equal(t, ClashNodeStatusInvalid, env.repo.node(bad.ID).Status)
	require.Contains(t, env.repo.node(bad.ID).StatusReason, "missing password")
	require.Equal(t, ClashNodeStatusActive, env.repo.node(good.ID).Status)
	require.Equal(t, 1, env.runtime.appliedCount())

	// In sync: no re-apply.
	require.NoError(t, manager.syncOnce(context.Background()))
	require.Equal(t, 1, env.runtime.appliedCount())

	// Drift (core restarted without our config): re-apply.
	env.runtime.proxies = map[string]bool{}
	require.NoError(t, manager.syncOnce(context.Background()))
	require.Equal(t, 2, env.runtime.appliedCount())
}

func TestClashManagerListenerSelfCheckMarksSquattedPorts(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "hk", "198.51.100.1")
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(_ context.Context, addr, _, _ string) error {
		if strings.HasSuffix(addr, ":"+strconv.Itoa(node.ListenPort)) {
			return errors.New("port is served by something else")
		}
		return nil
	}
	require.NoError(t, manager.syncOnce(context.Background()))
	require.Equal(t, ClashNodeStatusInvalid, env.repo.node(node.ID).Status)
	require.Contains(t, env.repo.node(node.ID).StatusReason, "listener unavailable")
}

// fakeSocks5 accepts one method (2 = user/pass or 0 = none) on every connection.
func fakeSocks5(t *testing.T, method byte, user, pass string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 512)
				if _, err := io.ReadFull(conn, buf[:3]); err != nil {
					return
				}
				_, _ = conn.Write([]byte{5, method})
				if method != 2 {
					return
				}
				if _, err := io.ReadFull(conn, buf[:2]); err != nil {
					return
				}
				u := make([]byte, buf[1])
				_, _ = io.ReadFull(conn, u)
				_, _ = io.ReadFull(conn, buf[:1])
				p := make([]byte, buf[0])
				_, _ = io.ReadFull(conn, p)
				status := byte(1)
				if string(u) == user && string(p) == pass {
					status = 0
				}
				_, _ = conn.Write([]byte{1, status})
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestProbeClashListener(t *testing.T) {
	ctx := context.Background()
	ours := fakeSocks5(t, 2, "u", "p")
	require.NoError(t, ProbeClashListener(ctx, ours, "u", "p"))
	require.Error(t, ProbeClashListener(ctx, ours, "u", "wrong"))
	squatter := fakeSocks5(t, 0, "", "")
	require.ErrorContains(t, ProbeClashListener(ctx, squatter, "u", "p"), "something else")
	require.Error(t, ProbeClashListener(ctx, "127.0.0.1:1", "u", "p"))
}

func TestClashDueSelection(t *testing.T) {
	settings := defaultClashPoolSettings()
	now := time.Now()
	recent := now.Add(-10 * time.Second)
	old := now.Add(-time.Hour)
	views := []ClashNodeView{
		{ClashNode: ClashNode{ID: 1, Status: ClashNodeStatusActive, LastCheckedAt: &recent, ExitStatus: ClashExitOK, ExitIP: "a", ExitCheckedAt: &recent}, ProfileEnabled: true, Accounts: []ClashBoundAccount{{ID: 1}}},
		{ClashNode: ClashNode{ID: 2, Status: ClashNodeStatusActive, LastCheckedAt: &old, ExitStatus: ClashExitUnknown}, ProfileEnabled: true},
		{ClashNode: ClashNode{ID: 3, Status: ClashNodeStatusMissing}, ProfileEnabled: true},
		{ClashNode: ClashNode{ID: 4, Status: ClashNodeStatusActive, ExitStatus: ClashExitStale, ExitIP: "b"}, ProfileEnabled: false},
	}
	latency := dueForLatency(views, settings, now)
	require.Len(t, latency, 1)
	require.EqualValues(t, 2, latency[0].ID)
	exits := dueForExitProbe(views, settings, now)
	require.Len(t, exits, 1)
	require.EqualValues(t, 2, exits[0].ID)
}

func TestClashAlertMetrics(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	env.repo.profiles[profile.ID].LastRefreshStatus = ClashRefreshError
	n1 := env.repo.addNode(profile.ID, "a", "198.51.100.1")
	n2 := env.repo.addNode(profile.ID, "b", "198.51.100.1")
	env.repo.bind("x", n1.ProxyID, nil)
	env.repo.bind("y", n2.ProxyID, nil)
	ctx := context.Background()
	v, ok := env.svc.ClashAlertMetric(ctx, ClashMetricProfileRefreshFailed)
	require.True(t, ok)
	require.EqualValues(t, 1, v)
	v, ok = env.svc.ClashAlertMetric(ctx, ClashMetricExitConflicts)
	require.True(t, ok)
	require.EqualValues(t, 1, v, "two accounts landing on one exit IP")
	require.NoError(t, env.repo.SetNodeStatus(ctx, n1.ID, ClashNodeStatusDisabled, "admin"))
	require.NoError(t, env.svc.ReconcilePauses(ctx))
	v, ok = env.svc.ClashAlertMetric(ctx, ClashMetricExitPausedAccounts)
	require.True(t, ok)
	require.EqualValues(t, 1, v)
}

func TestValidateClashPoolSettings(t *testing.T) {
	s := defaultClashPoolSettings()
	require.NoError(t, validateClashPoolSettings(s))
	bad := *s
	bad.MaxAccountsPerExit = 0
	require.Error(t, validateClashPoolSettings(&bad))
	bad = *s
	bad.ExitChangePolicy = "ignore"
	require.Error(t, validateClashPoolSettings(&bad))
	bad = *s
	bad.HealthTestURL = "ftp://x"
	require.Error(t, validateClashPoolSettings(&bad))
}
