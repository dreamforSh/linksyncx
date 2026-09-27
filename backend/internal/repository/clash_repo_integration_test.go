//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClashRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	repo := NewClashRepository(integrationDB)
	suffix := time.Now().Format("150405.000000")

	profile := &service.ClashProfile{
		Name: "clash-it-" + suffix, URLEncrypted: "enc", URLFingerprint: "fp-" + suffix, URLMasked: "https://x/***",
		UserAgent: "clash.meta", Enabled: true, RefreshIntervalMinutes: 360,
	}
	require.NoError(t, repo.CreateProfile(ctx, profile))
	var accountIDs []int64
	t.Cleanup(func() {
		for _, id := range accountIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM scheduler_outbox WHERE payload @> jsonb_build_object('account_ids', jsonb_build_array($1::bigint))`, id)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM accounts WHERE id = $1`, id)
		}
		rows, _ := integrationDB.QueryContext(ctx, `SELECT proxy_id FROM clash_nodes WHERE profile_id = $1`, profile.ID)
		var proxyIDs []int64
		for rows != nil && rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			proxyIDs = append(proxyIDs, id)
		}
		if rows != nil {
			_ = rows.Close()
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM clash_nodes WHERE profile_id = $1`, profile.ID)
		for _, id := range proxyIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM proxies WHERE id = $1`, id)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM clash_profiles WHERE id = $1`, profile.ID)
	})

	exists, err := repo.ExistsProfileName(ctx, profile.Name, 0)
	require.NoError(t, err)
	require.True(t, exists)
	exists, err = repo.ExistsProfileURL(ctx, profile.URLFingerprint, profile.ID)
	require.NoError(t, err)
	require.False(t, exists)

	cfg := func(name string) map[string]any {
		return map[string]any{"name": name, "type": "trojan", "server": strings.ToLower(name) + ".example.com", "port": 443, "password": "pw"}
	}
	plan := &service.ClashNodeSyncPlan{Inserts: []service.ClashSyncNewNode{
		{Name: "HK", Type: "trojan", Server: "hk.example.com", ServerPort: 443, Config: cfg("HK"), ConfigHash: "h1", Status: service.ClashNodeStatusActive, ProxyName: "Clash·it·HK"},
		{Name: "US", Type: "trojan", Server: "us.example.com", ServerPort: 443, Config: cfg("US"), ConfigHash: "h2", Status: service.ClashNodeStatusActive, ProxyName: "Clash·it·US"},
	}}
	ports := service.ClashPortRange{Start: 64000, End: 64999}
	n := 0
	res, err := repo.ApplyNodeSync(ctx, profile.ID, plan, ports, func() service.ClashManagedProxySpec {
		n++
		return service.ClashManagedProxySpec{Host: "127.0.0.1", Username: fmt.Sprintf("u%d", n), Password: "p"}
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Inserted)
	require.True(t, res.Structural)

	nodes, err := repo.ListNodesByProfile(ctx, profile.ID)
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	hk, us := nodes[0], nodes[1]
	require.NotEqual(t, hk.ListenPort, us.ListenPort)
	require.GreaterOrEqual(t, hk.ListenPort, ports.Start)

	var source, host string
	var port int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT source, host, port FROM proxies WHERE id = $1`, hk.ProxyID).Scan(&source, &host, &port))
	require.Equal(t, "clash", source)
	require.Equal(t, hk.ListenPort, port)

	render, err := repo.ListRenderNodes(ctx)
	require.NoError(t, err)
	live := map[int64]service.ClashRenderNode{}
	for _, r := range render {
		live[r.NodeID] = r
	}
	require.NotNil(t, live[hk.ID].Config)
	require.Equal(t, "u1", live[hk.ID].Username)

	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, credentials, extra, status, proxy_id, created_at, updated_at)
		VALUES ($1, 'anthropic', 'oauth', '{}', '{}', 'active', $2, NOW(), NOW()) RETURNING id`,
		"clash-acc-"+suffix, hk.ProxyID).Scan(&accountID))
	accountIDs = append(accountIDs, accountID)
	stats, err := repo.ListProfileStats(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats[profile.ID].Bound)
	require.Equal(t, 1, stats[profile.ID].BoundAccounts)

	// Rename HK, drop US.
	plan = &service.ClashNodeSyncPlan{
		Updates: []service.ClashSyncUpdate{{NodeID: hk.ID, Name: "HK 1x", Type: "trojan", Server: "hk.example.com", ServerPort: 443,
			Config: cfg("HK 1x"), ConfigHash: "h1", Status: service.ClashNodeStatusActive, ProxyName: "Clash·it·HK 1x"}},
		Missing: []int64{us.ID},
	}
	res, err = repo.ApplyNodeSync(ctx, profile.ID, plan, ports, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.Missing)

	views, page, err := repo.ListNodeViews(ctx, service.ClashNodeFilter{ProfileID: &profile.ID, Search: "HK"}, pagination.PaginationParams{Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, "HK 1x", views[0].Name)
	require.Len(t, views[0].Accounts, 1)

	render, err = repo.ListRenderNodes(ctx)
	require.NoError(t, err)
	for _, r := range render {
		require.NotEqual(t, us.ID, r.NodeID, "missing unbound nodes lose their listener")
	}

	// A missing but bound node keeps a REJECT placeholder.
	require.NoError(t, repo.SetNodeStatus(ctx, hk.ID, service.ClashNodeStatusMissing, "gone"))
	render, err = repo.ListRenderNodes(ctx)
	require.NoError(t, err)
	found := false
	for _, r := range render {
		if r.NodeID == hk.ID {
			found = true
			require.Nil(t, r.Config)
		}
	}
	require.True(t, found)

	until := time.Now().Add(30 * time.Minute)
	changed, err := repo.PauseAccounts(ctx, map[int64]string{accountID: service.ClashPauseReasonPrefix + " HK: gone"}, until, time.Now().Add(15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, []int64{accountID}, changed)
	changed, err = repo.PauseAccounts(ctx, map[int64]string{accountID: service.ClashPauseReasonPrefix + " HK: gone"}, until.Add(time.Minute), time.Now().Add(15*time.Minute))
	require.NoError(t, err)
	require.Empty(t, changed, "fresh pause is not rewritten")
	paused, err := repo.ListPausedAccounts(ctx)
	require.NoError(t, err)
	require.Contains(t, paused, accountID)

	var outbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM scheduler_outbox WHERE payload @> jsonb_build_object('account_ids', jsonb_build_array($1::bigint))`, accountID).Scan(&outbox))
	require.GreaterOrEqual(t, outbox, 1)

	cleared, err := repo.ClearPauses(ctx, []int64{accountID})
	require.NoError(t, err)
	require.Equal(t, []int64{accountID}, cleared)

	reclaimed, err := repo.ReclaimNodes(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Contains(t, reclaimed, us.ProxyID)
	require.NotContains(t, reclaimed, hk.ProxyID, "bound nodes are never reclaimed")

	require.NoError(t, repo.SoftDeleteProfile(ctx, profile.ID))
	_, err = repo.GetProfile(ctx, profile.ID)
	require.ErrorIs(t, err, service.ErrClashProfileNotFound)
}

func TestClashRepositoryTraffic(t *testing.T) {
	ctx := context.Background()
	repo := NewClashRepository(integrationDB)
	suffix := time.Now().Format("150405.000000")

	profile := &service.ClashProfile{
		Name: "clash-traffic-" + suffix, URLEncrypted: "enc", URLFingerprint: "fp-traffic-" + suffix, URLMasked: "https://x/***",
		UserAgent: "clash.meta", Enabled: true, RefreshIntervalMinutes: 360,
	}
	require.NoError(t, repo.CreateProfile(ctx, profile))
	t.Cleanup(func() {
		rows, _ := integrationDB.QueryContext(ctx, `SELECT proxy_id FROM clash_nodes WHERE profile_id = $1`, profile.ID)
		var proxyIDs []int64
		for rows != nil && rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			proxyIDs = append(proxyIDs, id)
		}
		if rows != nil {
			_ = rows.Close()
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM clash_nodes WHERE profile_id = $1`, profile.ID)
		for _, id := range proxyIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM proxies WHERE id = $1`, id)
		}
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM clash_profiles WHERE id = $1`, profile.ID)
	})

	cfg := func(name string) map[string]any {
		return map[string]any{"name": name, "type": "trojan", "server": strings.ToLower(name) + ".example.com", "port": 443, "password": "pw"}
	}
	n := 0
	_, err := repo.ApplyNodeSync(ctx, profile.ID, &service.ClashNodeSyncPlan{Inserts: []service.ClashSyncNewNode{
		{Name: "T-HK", Type: "trojan", Server: "hk.example.com", ServerPort: 443, Config: cfg("T-HK"), ConfigHash: "t1", Status: service.ClashNodeStatusActive, ProxyName: "Clash·t·HK"},
		{Name: "T-US", Type: "trojan", Server: "us.example.com", ServerPort: 443, Config: cfg("T-US"), ConfigHash: "t2", Status: service.ClashNodeStatusActive, ProxyName: "Clash·t·US"},
	}}, service.ClashPortRange{Start: 63000, End: 63999}, func() service.ClashManagedProxySpec {
		n++
		return service.ClashManagedProxySpec{Host: "127.0.0.1", Username: fmt.Sprintf("t%d", n), Password: "p"}
	})
	require.NoError(t, err)
	nodes, err := repo.ListNodesByProfile(ctx, profile.ID)
	require.NoError(t, err)
	hk, us := nodes[0], nodes[1]

	at := time.Now().UTC()
	// Duplicate keys are grouped; nodes that disappeared are dropped.
	require.NoError(t, repo.AddNodeTraffic(ctx, []service.ClashTrafficIncrement{
		{NodeID: hk.ID, Date: "2026-09-20", Upload: 5, Download: 50},
		{NodeID: hk.ID, Date: "2026-09-27", Upload: 10, Download: 100},
		{NodeID: hk.ID, Date: "2026-09-27", Upload: 1, Download: 1},
		{NodeID: us.ID, Date: "2026-09-27", Upload: 1000, Download: 1000},
		{NodeID: -1, Date: "2026-09-27", Upload: 1, Download: 1},
	}, at))
	// A second flush accumulates.
	require.NoError(t, repo.AddNodeTraffic(ctx, []service.ClashTrafficIncrement{
		{NodeID: hk.ID, Date: "2026-09-27", Upload: 4, Download: 9},
	}, at))

	traffic, err := repo.ListNodeTraffic(ctx, []int64{hk.ID, us.ID}, "2026-09-21")
	require.NoError(t, err)
	require.Equal(t, int64(20), traffic[hk.ID].Upload)
	require.Equal(t, int64(160), traffic[hk.ID].Download)
	require.NotNil(t, traffic[hk.ID].UpdatedAt)
	require.Equal(t, []service.ClashTrafficDay{{Date: "2026-09-27", Upload: 15, Download: 110}}, traffic[hk.ID].Days)

	profiles, err := repo.ListProfileTraffic(ctx, "2026-09-27")
	require.NoError(t, err)
	require.Equal(t, service.ClashProfileTraffic{Upload: 1020, Download: 1160, TodayUpload: 1015, TodayDownload: 1110}, profiles[profile.ID])

	page := pagination.PaginationParams{Page: 1, PageSize: 10}
	views, _, err := repo.ListNodeViews(ctx, service.ClashNodeFilter{ProfileID: &profile.ID, Sort: service.ClashNodeSortTrafficTotal}, page)
	require.NoError(t, err)
	require.Equal(t, []int64{us.ID, hk.ID}, []int64{views[0].ID, views[1].ID})
	ids, err := repo.ListNodeIDs(ctx, service.ClashNodeFilter{ProfileID: &profile.ID, Sort: service.ClashNodeSortTrafficToday, TrafficDate: "2026-09-20"})
	require.NoError(t, err)
	require.Equal(t, []int64{hk.ID, us.ID}, ids)

	require.NoError(t, repo.SetNodeStatus(ctx, us.ID, service.ClashNodeStatusDisabled, "disabled by admin"))
	ids, err = repo.ListNodeIDs(ctx, service.ClashNodeFilter{ProfileID: &profile.ID, LiveOnly: true})
	require.NoError(t, err)
	require.Equal(t, []int64{hk.ID}, ids)

	// Reclaimed nodes take their buckets with them.
	require.NoError(t, repo.SetNodeStatus(ctx, us.ID, service.ClashNodeStatusMissing, "gone"))
	_, err = repo.ReclaimNodes(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	var left int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM clash_node_traffic_daily WHERE node_id = $1`, us.ID).Scan(&left))
	require.Zero(t, left)
}
