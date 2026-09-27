//go:build unit

package service

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRenderClashConfigDeterministicAndFailClosed(t *testing.T) {
	nodes := []ClashRenderNode{
		{NodeID: 7, ListenPort: 20002, Username: "u7", Password: "p7"}, // placeholder
		{NodeID: 3, ListenPort: 20001, Username: "u3", Password: "p3", Config: map[string]any{
			"name": "HK 01", "type": "ss", "server": "hk.example.com", "port": 8388, "cipher": "aes-128-gcm", "password": "pw",
		}},
	}
	first, err := RenderClashConfig(nodes, ClashRenderOptions{})
	require.NoError(t, err)
	reversed := []ClashRenderNode{nodes[1], nodes[0]}
	second, err := RenderClashConfig(reversed, ClashRenderOptions{})
	require.NoError(t, err)
	require.Equal(t, string(first.Payload), string(second.Payload), "render must be order independent")
	require.Equal(t, first.Hash, second.Hash)
	require.Equal(t, []int64{3}, first.ProxyNodeIDs)
	require.Equal(t, 2, first.Listeners)
	require.True(t, strings.HasPrefix(first.MarkerName, "__sub2api_cfg_"))

	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(first.Payload, &doc))
	require.Equal(t, []any{"MATCH,REJECT"}, doc["rules"])
	require.Equal(t, true, doc["unified-delay"])
	require.Equal(t, true, doc["tcp-concurrent"])
	require.NotContains(t, strings.ToUpper(string(first.Payload)), "DIRECT")

	listeners := doc["listeners"].([]any)
	byName := map[string]map[string]any{}
	for _, raw := range listeners {
		l := raw.(map[string]any)
		byName[l["name"].(string)] = l
	}
	require.Equal(t, "n-3", byName["l-3"]["proxy"])
	require.Equal(t, "REJECT", byName["l-7"]["proxy"])
	require.Equal(t, "127.0.0.1", byName["l-3"]["listen"])
	proxies := doc["proxies"].([]any)
	require.Equal(t, "n-3", proxies[0].(map[string]any)["name"])
	// The node's own config is not mutated.
	require.Equal(t, "HK 01", nodes[1].Config["name"])

	changed := append([]ClashRenderNode(nil), nodes...)
	changed[0].ListenPort = 20003
	third, err := RenderClashConfig(changed, ClashRenderOptions{})
	require.NoError(t, err)
	require.NotEqual(t, first.Hash, third.Hash)
	require.NotEqual(t, first.MarkerName, third.MarkerName)
}

func TestRenderClashConfigRejectsDuplicatePorts(t *testing.T) {
	_, err := RenderClashConfig([]ClashRenderNode{
		{NodeID: 1, ListenPort: 20001},
		{NodeID: 2, ListenPort: 20001},
	}, ClashRenderOptions{})
	require.Error(t, err)
}

func TestParseClashProxyIndexError(t *testing.T) {
	idx, ok := ParseClashProxyIndexError("proxy 12: missing server")
	require.True(t, ok)
	require.Equal(t, 12, idx)
	_, ok = ParseClashProxyIndexError("listener l-1: bad")
	require.False(t, ok)
}

func parsedNode(name, typ, server string, port int, password string) clashsub.Node {
	return clashsub.Node{Name: name, Type: typ, Server: server, Port: port, Config: map[string]any{
		"name": name, "type": typ, "server": server, "port": port, "password": password,
	}}
}

func storedNode(id int64, n clashsub.Node, status string) ClashNode {
	return ClashNode{ID: id, Name: n.Name, Type: n.Type, Server: n.Server, ServerPort: n.Port,
		Config: n.Config, ConfigHash: clashsub.ConfigHash(n.Config), Status: status}
}

func TestBuildClashNodeSyncPlanMatching(t *testing.T) {
	hk := parsedNode("HK 01", "trojan", "hk.example.com", 443, "pw")
	us := parsedNode("US 01", "trojan", "us.example.com", 443, "pw")
	jp := parsedNode("JP 01", "trojan", "jp.example.com", 443, "pw")
	gone := parsedNode("SG 01", "trojan", "sg.example.com", 443, "pw")
	existing := []ClashNode{
		storedNode(1, hk, ClashNodeStatusActive),
		storedNode(2, us, ClashNodeStatusActive),
		storedNode(3, jp, ClashNodeStatusActive),
		storedNode(4, gone, ClashNodeStatusActive),
	}

	renamed := hk
	renamed.Name = "HK 01 | 1x"
	renamed.Config = map[string]any{"name": renamed.Name, "type": "trojan", "server": "hk.example.com", "port": 443, "password": "pw"}
	rotated := parsedNode("US 01", "trojan", "us.example.com", 443, "rotated")
	moved := parsedNode("JP 01 new", "trojan", "jp.example.com", 443, "rotated")
	fresh := parsedNode("KR 01", "trojan", "kr.example.com", 443, "pw")

	plan, _, err := BuildClashNodeSyncPlan(existing, []clashsub.Node{renamed, rotated, moved, fresh}, ClashSyncOptions{ProfileName: "机场A"})
	require.NoError(t, err)

	updates := map[int64]ClashSyncUpdate{}
	for _, u := range plan.Updates {
		updates[u.NodeID] = u
	}
	require.False(t, updates[1].ConfigChanged, "rename keeps identical config")
	require.Equal(t, "HK 01 | 1x", updates[1].Name)
	require.True(t, updates[2].ConfigChanged, "same name, rotated password")
	require.True(t, updates[3].ConfigChanged, "same endpoint, new name and password")
	require.Len(t, plan.Inserts, 1)
	require.Equal(t, "KR 01", plan.Inserts[0].Name)
	require.Equal(t, "Clash·机场A·KR 01", plan.Inserts[0].ProxyName)
	require.Equal(t, []int64{4}, plan.Missing)
}

func TestBuildClashNodeSyncPlanStatusRules(t *testing.T) {
	a := parsedNode("A", "ss", "a.example.com", 1, "p")
	b := parsedNode("B", "ss", "b.example.com", 1, "p")
	c := parsedNode("C", "ss", "c.example.com", 1, "p")
	existing := []ClashNode{
		storedNode(1, a, ClashNodeStatusDisabled),
		storedNode(2, b, ClashNodeStatusMissing),
		storedNode(3, c, ClashNodeStatusInvalid),
	}
	existing[0].StatusReason = "admin"
	existing[2].StatusReason = "core rejected"
	plan, _, err := BuildClashNodeSyncPlan(existing, []clashsub.Node{a, b, c}, ClashSyncOptions{})
	require.NoError(t, err)
	status := map[int64]string{}
	for _, u := range plan.Updates {
		status[u.NodeID] = u.Status
	}
	require.Equal(t, ClashNodeStatusDisabled, status[1], "admin disable survives refresh")
	require.Equal(t, ClashNodeStatusActive, status[2], "returning node becomes active")
	require.Equal(t, ClashNodeStatusInvalid, status[3], "unchanged rejected config stays invalid")
	require.Empty(t, plan.Missing)
}

func TestBuildClashNodeSyncPlanScreensPrivateServers(t *testing.T) {
	local := parsedNode("local", "ss", "127.0.0.1", 1080, "p")
	lan := parsedNode("lan", "ss", "192.168.1.10", 1080, "p")
	plan, stats, err := BuildClashNodeSyncPlan(nil, []clashsub.Node{local, lan}, ClashSyncOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, stats.Private)
	for _, insert := range plan.Inserts {
		require.Equal(t, ClashNodeStatusInvalid, insert.Status)
	}
	plan, _, err = BuildClashNodeSyncPlan(nil, []clashsub.Node{local, lan}, ClashSyncOptions{AllowPrivateNodes: true})
	require.NoError(t, err)
	byName := map[string]string{}
	for _, insert := range plan.Inserts {
		byName[insert.Name] = insert.Status
	}
	require.Equal(t, ClashNodeStatusInvalid, byName["local"], "loopback is never allowed")
	require.Equal(t, ClashNodeStatusActive, byName["lan"])
}

func TestBuildClashNodeSyncPlanFiltersAndDropProtection(t *testing.T) {
	var existing []ClashNode
	var parsed []clashsub.Node
	for i, name := range []string{"HK 01", "HK 02", "US 01", "US 02", "剩余流量 10GB"} {
		n := parsedNode(name, "ss", strings.ReplaceAll(strings.ToLower(name), " ", "")+".example.com", 1, "p")
		parsed = append(parsed, n)
		if i < 4 {
			existing = append(existing, storedNode(int64(i+1), n, ClashNodeStatusActive))
		}
	}
	plan, stats, err := BuildClashNodeSyncPlan(existing, parsed, ClashSyncOptions{
		Exclude: regexp.MustCompile(DefaultClashExcludePattern),
	})
	require.NoError(t, err)
	require.Equal(t, 1, stats.Filtered)
	require.Empty(t, plan.Inserts)

	// Only one node left out of four active ones: guarded.
	_, _, err = BuildClashNodeSyncPlan(existing, parsed[:1], ClashSyncOptions{DropProtectionPercent: 50})
	var drop *ClashDropProtectionError
	require.True(t, errors.As(err, &drop))
	require.Equal(t, 4, drop.Before)
	require.Equal(t, 1, drop.After)

	plan, _, err = BuildClashNodeSyncPlan(existing, parsed[:1], ClashSyncOptions{DropProtectionPercent: 50, Force: true})
	require.NoError(t, err)
	require.Len(t, plan.Missing, 3)

	_, _, err = BuildClashNodeSyncPlan(existing, nil, ClashSyncOptions{})
	require.True(t, errors.As(err, &drop), "an empty subscription is always guarded")

	plan, _, err = BuildClashNodeSyncPlan(existing, parsed[:2], ClashSyncOptions{
		Include: regexp.MustCompile("^HK"),
	})
	require.NoError(t, err)
	require.Len(t, plan.Missing, 2)
}

func TestClashManagedProxyNameTruncates(t *testing.T) {
	name := clashManagedProxyName(strings.Repeat("订", 60), strings.Repeat("节", 60))
	require.Equal(t, 100, len([]rune(name)))
}
