//go:build unit

package service

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func conn(id string, nodeID int64, up, down int64, start time.Time) ClashConnection {
	return ClashConnection{ID: id, Inbound: ClashListenerName(nodeID), Upload: up, Download: down, Start: start}
}

func pendingOf(s *clashTrafficSampler, nodeID int64, date string) ClashTrafficIncrement {
	if inc := s.pending[clashTrafficKey{nodeID: nodeID, date: date}]; inc != nil {
		return *inc
	}
	return ClashTrafficIncrement{NodeID: nodeID, Date: date}
}

func TestParseClashListenerName(t *testing.T) {
	id, ok := ParseClashListenerName(ClashListenerName(42))
	require.True(t, ok)
	require.Equal(t, int64(42), id)
	for _, name := range []string{"", "l-", "l-x", "l-0", "l--3", "n-42", "DEFAULT-MIXED"} {
		_, ok := ParseClashListenerName(name)
		require.False(t, ok, name)
	}
}

func TestClashTrafficSamplerCountsDeltasPerNode(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, timezone.Location())
	date := clashTrafficDate(t0)
	s := newClashTrafficSampler(t0)

	// A connection opened after sampling started is counted from zero; one
	// opened before is only counted from what it moves from now on.
	s.observe([]ClashConnection{
		conn("fresh", 5, 100, 1000, t0.Add(time.Second)),
		conn("older", 5, 50, 500, t0.Add(-time.Minute)),
		{ID: "dns", Inbound: "", Upload: 9, Download: 9, Start: t0.Add(time.Second)},
		{ID: "foreign", Inbound: "DEFAULT-MIXED", Upload: 9, Download: 9, Start: t0.Add(time.Second)},
	}, t0.Add(2*time.Second))
	require.Equal(t, ClashTrafficIncrement{NodeID: 5, Date: date, Upload: 100, Download: 1000}, pendingOf(s, 5, date))
	require.Equal(t, ClashNodeLiveTraffic{Connections: 2}, s.liveSnapshot()[5], "no rate without a previous sample")

	s.observe([]ClashConnection{
		conn("fresh", 5, 160, 1600, t0.Add(time.Second)),
		conn("older", 5, 80, 800, t0.Add(-time.Minute)),
		conn("other", 6, 10, 20, t0.Add(4*time.Second)),
	}, t0.Add(5*time.Second))
	require.Equal(t, ClashTrafficIncrement{NodeID: 5, Date: date, Upload: 190, Download: 1900}, pendingOf(s, 5, date))
	require.Equal(t, ClashTrafficIncrement{NodeID: 6, Date: date, Upload: 10, Download: 20}, pendingOf(s, 6, date))
	require.Equal(t, ClashNodeLiveTraffic{UploadRate: 30, DownloadRate: 300, Connections: 2}, s.liveSnapshot()[5])

	// Closed connections are forgotten; a reused ID later counts afresh.
	s.observe([]ClashConnection{conn("other", 6, 10, 20, t0.Add(4*time.Second))}, t0.Add(8*time.Second))
	require.NotContains(t, s.seen, "fresh")
	require.NotContains(t, s.liveSnapshot(), int64(5))

	incs := s.drain()
	require.Equal(t, []ClashTrafficIncrement{
		{NodeID: 5, Date: date, Upload: 190, Download: 1900},
		{NodeID: 6, Date: date, Upload: 10, Download: 20},
	}, incs)
	require.Empty(t, s.drain())

	// A failed flush is put back and merged with newer traffic.
	s.restore(incs)
	s.observe([]ClashConnection{conn("other", 6, 15, 30, t0.Add(4*time.Second))}, t0.Add(11*time.Second))
	require.Equal(t, ClashTrafficIncrement{NodeID: 6, Date: date, Upload: 15, Download: 30}, pendingOf(s, 6, date))
	require.Equal(t, ClashTrafficIncrement{NodeID: 5, Date: date, Upload: 190, Download: 1900}, pendingOf(s, 5, date))
}

func TestClashTrafficSamplerResetBaselinesExistingConnections(t *testing.T) {
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, timezone.Location())
	s := newClashTrafficSampler(t0)
	s.observe([]ClashConnection{conn("c", 7, 100, 100, t0.Add(time.Second))}, t0.Add(2*time.Second))
	s.drain()

	// Taking over a shared core: bytes an existing connection moved before the
	// takeover were counted by the previous owner.
	takeover := t0.Add(time.Minute)
	s.reset(takeover)
	s.observe([]ClashConnection{conn("c", 7, 500, 500, t0.Add(time.Second))}, takeover.Add(time.Second))
	require.Empty(t, s.drain())
	s.observe([]ClashConnection{conn("c", 7, 520, 510, t0.Add(time.Second))}, takeover.Add(4*time.Second))
	require.Equal(t, []ClashTrafficIncrement{{NodeID: 7, Date: clashTrafficDate(takeover), Upload: 20, Download: 10}}, s.drain())
}

func TestClashTrafficSamplerSplitsDays(t *testing.T) {
	loc := timezone.Location()
	evening := time.Date(2026, 9, 27, 23, 59, 58, 0, loc)
	s := newClashTrafficSampler(evening.Add(-time.Minute))
	s.observe([]ClashConnection{conn("c", 3, 10, 10, evening.Add(-time.Second))}, evening)
	s.observe([]ClashConnection{conn("c", 3, 25, 40, evening.Add(-time.Second))}, evening.Add(3*time.Second))
	require.Equal(t, []ClashTrafficIncrement{
		{NodeID: 3, Date: "2026-09-27", Upload: 10, Download: 10},
		{NodeID: 3, Date: "2026-09-28", Upload: 15, Download: 30},
	}, s.drain())
}

func TestClashTrafficOwnershipOnlyGatesSharedCores(t *testing.T) {
	now := time.Now()
	owned, acquired := newClashTrafficOwnership(config.ClashPoolModeEmbedded, nil).hold(context.Background(), now)
	require.True(t, owned)
	require.False(t, acquired)
	// Without a database (single instance, tests) the shared core is sampled too.
	owned, _ = newClashTrafficOwnership(config.ClashPoolModeExternal, nil).hold(context.Background(), now)
	require.True(t, owned)
}

func TestClashManagerRecordsSampledTraffic(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "hk-01", "198.51.100.1")
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	ctx := context.Background()

	started := time.Now().Add(-time.Minute)
	sampler := newClashTrafficSampler(started)
	env.runtime.setConnections(conn("a", node.ID, 1000, 5000, started.Add(time.Second)))
	now := time.Now()
	manager.sampleTraffic(ctx, sampler, now)
	require.NotNil(t, manager.localTrafficLive())
	require.Equal(t, 1, manager.localTrafficLive().Nodes[node.ID].Connections)

	env.repo.trafficErr = errors.New("db down")
	manager.flushTraffic(ctx, sampler)
	require.Equal(t, int64(0), env.repo.trafficDay(node.ID, clashTrafficDate(now)).Upload)

	env.repo.trafficErr = nil
	env.runtime.setConnections(conn("a", node.ID, 1500, 5200, started.Add(time.Second)))
	manager.sampleTraffic(ctx, sampler, now.Add(3*time.Second))
	manager.flushTraffic(ctx, sampler)
	day := env.repo.trafficDay(node.ID, clashTrafficDate(now))
	require.Equal(t, int64(1500), day.Upload, "the failed flush is retried once, not lost or doubled")
	require.Equal(t, int64(5200), day.Download)

	// An unreachable core keeps the baselines instead of recounting.
	env.runtime.connsErr = errors.New("sidecar restarting")
	manager.sampleTraffic(ctx, sampler, now.Add(6*time.Second))
	env.runtime.connsErr = nil
	env.runtime.setConnections(conn("a", node.ID, 1600, 5200, started.Add(time.Second)))
	manager.sampleTraffic(ctx, sampler, now.Add(9*time.Second))
	require.Equal(t, []ClashTrafficIncrement{{NodeID: node.ID, Date: clashTrafficDate(now.Add(9 * time.Second)), Upload: 100}}, sampler.drain())
}

func TestClashListNodesAttachesTraffic(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	busy := env.repo.addNode(profile.ID, "busy", "198.51.100.1")
	idle := env.repo.addNode(profile.ID, "idle", "198.51.100.2")
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	ctx := context.Background()

	now := time.Now()
	dates := clashTrafficTrendDates(now)
	require.Len(t, dates, clashTrafficTrendDays)
	require.Equal(t, clashTrafficDate(now), dates[len(dates)-1])
	old := timezone.StartOfDay(now).AddDate(0, 0, -30).Format(clashTrafficDateLayout)
	require.NoError(t, env.repo.AddNodeTraffic(ctx, []ClashTrafficIncrement{
		{NodeID: busy.ID, Date: dates[len(dates)-1], Upload: 10, Download: 20},
		{NodeID: busy.ID, Date: dates[2], Upload: 1, Download: 2},
		{NodeID: busy.ID, Date: old, Upload: 100, Download: 200},
	}, now))
	manager.setLocalTrafficLive(&ClashTrafficLive{InstanceID: "me", UpdatedAt: now, Nodes: map[int64]ClashNodeLiveTraffic{
		busy.ID: {UploadRate: 5, DownloadRate: 50, Connections: 3},
	}})

	views, _, err := env.svc.ListNodes(ctx, ClashNodeFilter{}, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	byID := map[int64]ClashNodeView{}
	for _, view := range views {
		byID[view.ID] = view
	}

	traffic := byID[busy.ID].Traffic
	require.Equal(t, int64(111), traffic.Upload, "totals include days outside the trend window")
	require.Equal(t, int64(222), traffic.Download)
	require.Equal(t, int64(10), traffic.TodayUpload)
	require.Equal(t, int64(20), traffic.TodayDownload)
	require.Len(t, traffic.Daily, clashTrafficTrendDays)
	require.Equal(t, ClashTrafficDay{Date: dates[2], Upload: 1, Download: 2}, traffic.Daily[2])
	require.Equal(t, ClashTrafficDay{Date: dates[0]}, traffic.Daily[0], "days without traffic are zero-filled")
	require.Equal(t, ClashNodeLiveTraffic{UploadRate: 5, DownloadRate: 50, Connections: 3}, traffic.ClashNodeLiveTraffic)

	quiet := byID[idle.ID].Traffic
	require.Zero(t, quiet.Upload)
	require.Len(t, quiet.Daily, clashTrafficTrendDays)
	require.Zero(t, quiet.Connections)

	// Stale snapshots (a stopped sampler) no longer report throughput.
	manager.setLocalTrafficLive(&ClashTrafficLive{InstanceID: "me", UpdatedAt: now.Add(-time.Minute), Nodes: map[int64]ClashNodeLiveTraffic{
		busy.ID: {UploadRate: 5, Connections: 1},
	}})
	views, _, err = env.svc.ListNodes(ctx, ClashNodeFilter{Search: "busy"}, pagination.PaginationParams{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Zero(t, views[0].Traffic.Connections)

	profiles, err := env.svc.ListProfiles(ctx)
	require.NoError(t, err)
	require.Equal(t, ClashProfileTraffic{Upload: 111, Download: 222, TodayUpload: 10, TodayDownload: 20}, profiles[0].Traffic)
}

func TestClashListNodeIDsHonoursFilters(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	live := env.repo.addNode(profile.ID, "hk-01", "198.51.100.1")
	disabled := env.repo.addNode(profile.ID, "hk-02", "198.51.100.2", func(n *ClashNode) { n.Status = ClashNodeStatusDisabled })
	other := env.repo.addNode(profile.ID, "us-01", "198.51.100.3")
	ctx := context.Background()

	ids, err := env.svc.ListNodeIDs(ctx, ClashNodeFilter{Search: "hk"})
	require.NoError(t, err)
	require.Equal(t, []int64{live.ID, disabled.ID}, ids)

	ids, err = env.svc.ListNodeIDs(ctx, ClashNodeFilter{LiveOnly: true})
	require.NoError(t, err)
	require.Equal(t, []int64{live.ID, other.ID}, ids)
}

func TestNormalizeClashNodeSort(t *testing.T) {
	for _, sort := range []string{ClashNodeSortName, ClashNodeSortLatency, ClashNodeSortTrafficToday, ClashNodeSortTrafficTotal} {
		require.Equal(t, sort, NormalizeClashNodeSort(sort))
	}
	require.Equal(t, "", NormalizeClashNodeSort("id; DROP TABLE"))
}

func TestClashExitProbeKeepsLocationWhenFallbackOmitsIt(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "hk-01", "198.51.100.1", func(n *ClashNode) {
		n.ExitCountry, n.ExitCountryCode, n.ExitCity = "Hong Kong", "HK", "Central"
	})
	moved := env.repo.addNode(profile.ID, "hk-02", "198.51.100.2", func(n *ClashNode) {
		n.ExitCountry, n.ExitCountryCode = "Hong Kong", "HK"
	})
	port := func(n *ClashNode) string { return strconv.Itoa(env.repo.proxies[n.ProxyID].Port) }
	env.prober.bare = map[string]bool{port(node): true, port(moved): true}
	env.prober.byPort[port(node)] = "198.51.100.1"
	env.prober.byPort[port(moved)] = "198.51.100.9"

	ctx := context.Background()
	views, err := env.repo.ListAllNodeViews(ctx)
	require.NoError(t, err)
	env.svc.probeExits(ctx, views, withSettings(func(s *ClashPoolSettings) { s.PlatformChecksEnabled = false }))

	same := env.repo.node(node.ID)
	require.Equal(t, ClashExitOK, same.ExitStatus)
	require.Equal(t, "HK", same.ExitCountryCode, "an unchanged exit keeps its known location")
	require.Equal(t, "Central", same.ExitCity)

	changed := env.repo.node(moved.ID)
	require.Equal(t, "198.51.100.9", changed.ExitIP)
	require.Empty(t, changed.ExitCountryCode, "a new exit must not inherit the old location")
}
