//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	"github.com/stretchr/testify/require"
)

func TestClashSyncNewSharedEndpointCannotStealExistingIdentities(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate_credentials=%t", rotate), func(t *testing.T) {
			fresh := parsedNode("new primary", "vless", "shared.example.com", 443, "new")
			parsed := []clashsub.Node{fresh}
			var existing []ClashNode
			for i, name := range []string{"Japan", "Taiwan", "Korea", "US backup"} {
				node := parsedNode(name, "vless", "shared.example.com", 443, name)
				stored := storedNode(int64(i+1), node, ClashNodeStatusActive)
				stored.ProxyID, stored.ListenPort = int64(100+i), 20000+i
				existing = append(existing, stored)
				if rotate {
					node = parsedNode(name, "vless", "shared.example.com", 443, name+"-rotated")
				}
				parsed = append(parsed, node)
			}
			plan, _, err := BuildClashNodeSyncPlan(existing, parsed, ClashSyncOptions{})
			require.NoError(t, err)
			require.Len(t, plan.Inserts, 1)
			require.Equal(t, fresh.Name, plan.Inserts[0].Name)
			require.Len(t, plan.Updates, len(existing))
			for i, update := range plan.Updates {
				require.Equal(t, existing[i].ID, update.NodeID, update.Name)
				require.Equal(t, existing[i].Name, update.Name)
				require.Equal(t, rotate, update.ConfigChanged)
			}
			require.Empty(t, plan.Missing)
		})
	}
}

func TestClashSyncIdenticalConfigurationsKeepNamesWhenReordered(t *testing.T) {
	a := parsedNode("A", "vless", "shared.example.com", 443, "same")
	b := parsedNode("B", "vless", "shared.example.com", 443, "same")
	fresh := parsedNode("new alias", "vless", "shared.example.com", 443, "same")
	plan, _, err := BuildClashNodeSyncPlan([]ClashNode{
		storedNode(1, a, ClashNodeStatusActive), storedNode(2, b, ClashNodeStatusDisabled),
	}, []clashsub.Node{fresh, b, a}, ClashSyncOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Inserts, 1)
	require.Equal(t, fresh.Name, plan.Inserts[0].Name)
	require.Len(t, plan.Updates, 2)
	require.EqualValues(t, 2, plan.Updates[0].NodeID)
	require.Equal(t, "B", plan.Updates[0].Name)
	require.Equal(t, ClashNodeStatusDisabled, plan.Updates[0].Status)
	require.EqualValues(t, 1, plan.Updates[1].NodeID)
	require.Equal(t, "A", plan.Updates[1].Name)
	require.Empty(t, plan.Missing)
}

func TestClashSyncConfigurationIdentityTakesPriorityOverChangedNameMatch(t *testing.T) {
	original := parsedNode("A", "vless", "shared.example.com", 443, "original")
	renamed := parsedNode("new alias", "vless", "shared.example.com", 443, "original")
	rotated := parsedNode("A", "vless", "shared.example.com", 443, "rotated")
	plan, _, err := BuildClashNodeSyncPlan([]ClashNode{
		storedNode(1, original, ClashNodeStatusActive),
	}, []clashsub.Node{rotated, renamed}, ClashSyncOptions{})
	require.NoError(t, err)
	require.Len(t, plan.Updates, 1)
	require.EqualValues(t, 1, plan.Updates[0].NodeID)
	require.Equal(t, renamed.Name, plan.Updates[0].Name)
	require.False(t, plan.Updates[0].ConfigChanged)
	require.Len(t, plan.Inserts, 1)
	require.Equal(t, rotated.Name, plan.Inserts[0].Name)
	require.Empty(t, plan.Missing)
}

func TestClashSyncAmbiguousEndpointDoesNotGuessIdentities(t *testing.T) {
	for _, counts := range [][2]int{{1, 2}, {2, 1}, {2, 2}} {
		t.Run(fmt.Sprintf("%d_old_%d_new", counts[0], counts[1]), func(t *testing.T) {
			var existing []ClashNode
			var parsed []clashsub.Node
			for i := 0; i < counts[0]; i++ {
				node := parsedNode(fmt.Sprintf("old-%d", i), "vless", "shared.example.com", 443, "old")
				existing = append(existing, storedNode(int64(i+1), node, ClashNodeStatusActive))
			}
			for i := 0; i < counts[1]; i++ {
				parsed = append(parsed, parsedNode(fmt.Sprintf("new-%d", i), "vless", "shared.example.com", 443, "rotated"))
			}
			plan, _, err := BuildClashNodeSyncPlan(existing, parsed, ClashSyncOptions{})
			require.NoError(t, err)
			require.Len(t, plan.Inserts, counts[1])
			require.Empty(t, plan.Updates)
			require.Len(t, plan.Missing, counts[0])
		})
	}
}
