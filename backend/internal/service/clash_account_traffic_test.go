//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestClashAccountProxyUsernameRoundTrip(t *testing.T) {
	user := ClashAccountProxyUsername("sub2api-n", 42)
	id, ok := ParseClashAccountProxyUsername(user)
	require.True(t, ok)
	require.EqualValues(t, 42, id)
	for _, bad := range []string{"sub2api-n", "", "x~a", "x~a0", "x~a-3", "x~a07", "x~a1b"} {
		_, ok := ParseClashAccountProxyUsername(bad)
		require.False(t, ok, bad)
	}
}

func TestClashAccountRouteTableRetainThenSet(t *testing.T) {
	table := &clashAccountRouteTable{}
	table.set(map[int64]map[int64]struct{}{10: {1: {}, 2: {}}})
	require.True(t, table.active(10, 2))

	// Before applying a render without account 2, its username stops being used;
	// a newly bound account 3 only becomes active once the apply succeeded.
	next := map[int64]map[int64]struct{}{10: {1: {}, 3: {}}}
	table.retain(next)
	require.True(t, table.active(10, 1))
	require.False(t, table.active(10, 2))
	require.False(t, table.active(10, 3))
	table.set(next)
	require.True(t, table.active(10, 3))
}

func TestAccountProxyURLUsesTaggedUsernameOnlyWhenConfirmed(t *testing.T) {
	t.Cleanup(func() { clashAccountRoutes.set(nil) })
	proxy := &Proxy{ID: 77, Protocol: "http", Host: "127.0.0.1", Port: 20001, Username: "sub2api-n", Password: "pw", Source: ProxySourceClash}
	proxyID := proxy.ID
	account := &Account{ID: 5, ProxyID: &proxyID, Proxy: proxy}

	clashAccountRoutes.set(nil)
	got, err := account.ProxyURLForOutbound()
	require.NoError(t, err)
	require.Equal(t, "http://sub2api-n:pw@127.0.0.1:20001", got, "unconfirmed: shared credentials")

	clashAccountRoutes.set(map[int64]map[int64]struct{}{77: {5: {}}})
	got, err = account.ProxyURLForOutbound()
	require.NoError(t, err)
	require.Equal(t, "http://sub2api-n~a5:pw@127.0.0.1:20001", got)

	// Manual proxies are never rewritten, even with a matching route.
	manual := &Proxy{ID: 77, Protocol: "http", Host: "10.0.0.1", Port: 8080, Username: "u", Password: "p"}
	manualAccount := &Account{ID: 5, ProxyID: &proxyID, Proxy: manual}
	got, err = manualAccount.ProxyURLForOutbound()
	require.NoError(t, err)
	require.Equal(t, "http://u:p@10.0.0.1:8080", got)
}

func TestRenderClashConfigAddsAccountUsernames(t *testing.T) {
	rendered, err := RenderClashConfig([]ClashRenderNode{
		{NodeID: 1, ListenPort: 20001, ProxyID: 10, AccountIDs: []int64{9, 3}, Username: "u1", Password: "p1", Config: map[string]any{"name": "a", "type": "http", "server": "1.2.3.4", "port": 80}},
	}, ClashRenderOptions{})
	require.NoError(t, err)
	payload := string(rendered.Payload)
	require.Contains(t, payload, "username: u1\n")
	require.Less(t, strings.Index(payload, "username: u1~a3"), strings.Index(payload, "username: u1~a9"), "users are sorted for a stable hash")

	again, err := RenderClashConfig([]ClashRenderNode{
		{NodeID: 1, ListenPort: 20001, ProxyID: 10, AccountIDs: []int64{3, 9}, Username: "u1", Password: "p1", Config: map[string]any{"name": "a", "type": "http", "server": "1.2.3.4", "port": 80}},
	}, ClashRenderOptions{})
	require.NoError(t, err)
	require.Equal(t, rendered.Hash, again.Hash)

	routes := clashRenderAccountRoutes([]ClashRenderNode{{NodeID: 1, ProxyID: 10, AccountIDs: []int64{3}, Username: "u1"}, {NodeID: 2, ProxyID: 11}})
	require.Equal(t, map[int64]map[int64]struct{}{10: {3: {}}}, routes)
}

func TestClashTrafficSamplerSplitsAccounts(t *testing.T) {
	t0 := time.Date(2026, 10, 18, 12, 0, 0, 0, timezone.Location())
	date := clashTrafficDate(t0)
	s := newClashTrafficSampler(t0)
	tagged := func(id string, accountID, up, down int64) ClashConnection {
		c := conn(id, 5, up, down, t0.Add(time.Second))
		c.User = ClashAccountProxyUsername("n", accountID)
		return c
	}
	shared := conn("shared", 5, 40, 400, t0.Add(time.Second))
	shared.User = "n"

	s.observe([]ClashConnection{tagged("a", 1, 100, 1000), tagged("b", 2, 10, 20), shared}, t0.Add(2*time.Second))
	s.observe([]ClashConnection{tagged("a", 1, 130, 1300), tagged("b", 2, 10, 20), shared}, t0.Add(5*time.Second))

	require.Equal(t, ClashTrafficIncrement{NodeID: 5, Date: date, Upload: 180, Download: 1720}, pendingOf(s, 5, date), "node totals still include unattributed bytes")
	accounts := s.drainAccounts()
	require.Equal(t, []ClashAccountTrafficIncrement{
		{AccountID: 1, NodeID: 5, Date: date, Upload: 130, Download: 1300},
		{AccountID: 2, NodeID: 5, Date: date, Upload: 10, Download: 20},
	}, accounts)
	require.Equal(t, ClashNodeLiveTraffic{UploadRate: 10, DownloadRate: 100, Connections: 1}, s.liveAccountSnapshot()[5][1])

	s.restoreAccounts(accounts)
	require.Len(t, s.drainAccounts(), 2, "failed flushes are kept for the next one")
}

func TestClashNodeListingAttachesAccountTraffic(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "hk", "198.51.100.1")
	first := env.repo.bind("first", node.ProxyID, nil)
	second := env.repo.bind("second", node.ProxyID, nil)
	today := clashTrafficDate(time.Now())
	require.NoError(t, env.repo.AddAccountTraffic(context.Background(), []ClashAccountTrafficIncrement{
		{AccountID: first.ID, NodeID: node.ID, Date: today, Upload: 5, Download: 50},
	}, time.Now()))

	views, err := env.repo.ListAllNodeViews(context.Background())
	require.NoError(t, err)
	env.svc.attachNodeTraffic(context.Background(), views)
	byID := map[int64]ClashAccountTraffic{}
	for _, v := range views {
		for _, acc := range v.Accounts {
			byID[acc.ID] = acc.Traffic
		}
	}
	require.Equal(t, ClashAccountTraffic{Upload: 5, Download: 50, TodayUpload: 5, TodayDownload: 50, RecentUpload: 5, RecentDownload: 50}, byID[first.ID])
	require.Equal(t, ClashAccountTraffic{}, byID[second.ID])
}

func TestClashManagerSyncConfirmsAccountRoutes(t *testing.T) {
	t.Cleanup(func() { clashAccountRoutes.set(nil) })
	clashAccountRoutes.set(nil)
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "hk", "198.51.100.1")
	acc := env.repo.bind("first", node.ProxyID, nil)
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(context.Context, string, string, string) error { return nil }

	require.NoError(t, manager.syncOnce(context.Background()))
	require.True(t, clashAccountRoutes.active(node.ProxyID, acc.ID), "applied config accepts the account username")

	// A failed apply after unbinding never leaves the dropped username active.
	env.repo.unbind(acc.ID)
	env.runtime.applyErrs = []error{context.DeadlineExceeded}
	require.Error(t, manager.syncOnce(context.Background()))
	require.False(t, clashAccountRoutes.active(node.ProxyID, acc.ID))

	manager.Stop()
	require.False(t, clashAccountRoutes.active(node.ProxyID, acc.ID))
}
