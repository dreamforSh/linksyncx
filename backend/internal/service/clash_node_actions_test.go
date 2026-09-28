//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

func listClashNodeIDs(t *testing.T, svc *ClashService, visibility string) []int64 {
	t.Helper()
	views, _, err := svc.ListNodes(context.Background(), ClashNodeFilter{Visibility: visibility}, pagination.PaginationParams{Page: 1, PageSize: 100})
	require.NoError(t, err)
	ids := make([]int64, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.ID)
	}
	return ids
}

func TestClashHideNodesLifecycle(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	profile := env.repo.addProfile("A", true)
	bound := env.repo.addNode(profile.ID, "bound", "198.51.100.1")
	free := env.repo.addNode(profile.ID, "free", "198.51.100.2")
	adminOff := env.repo.addNode(profile.ID, "admin-off", "198.51.100.3", func(n *ClashNode) {
		n.Status, n.StatusReason = ClashNodeStatusDisabled, ClashNodeReasonDisabledByAdmin
	})
	gone := env.repo.addNode(profile.ID, "gone", "198.51.100.4", func(n *ClashNode) {
		n.Status, n.StatusReason = ClashNodeStatusMissing, "removed from subscription"
	})
	visible := env.repo.addNode(profile.ID, "visible", "198.51.100.5")
	acc := env.repo.bind("x", bound.ProxyID, nil)
	structural := 0
	env.svc.onStructuralChange = func() { structural++ }

	result, err := env.svc.UpdateNodes(ctx, ClashNodeActionHide, []int64{bound.ID, free.ID, adminOff.ID, gone.ID, 999999, bound.ID})
	require.NoError(t, err)
	require.Equal(t, ClashNodeActionHide, result.Action)
	require.Equal(t, 4, result.Updated)
	require.Equal(t, 1, result.Skipped, "duplicates are ignored, unknown ids skipped")
	require.Equal(t, 1, structural)
	require.ElementsMatch(t, []string{
		ClashListenerName(bound.ID), ClashListenerName(free.ID), ClashListenerName(adminOff.ID), ClashListenerName(gone.ID),
	}, env.runtime.closed, "hiding drops established connections")

	require.True(t, env.repo.node(bound.ID).Hidden)
	require.Equal(t, ClashNodeStatusDisabled, env.repo.node(bound.ID).Status)
	require.Equal(t, ClashNodeReasonHiddenByAdmin, env.repo.node(bound.ID).StatusReason)
	require.Equal(t, ClashNodeReasonDisabledByAdmin, env.repo.node(adminOff.ID).StatusReason, "an admin disable is kept")
	require.Equal(t, ClashNodeStatusMissing, env.repo.node(gone.ID).Status)
	require.Contains(t, env.repo.account(acc.ID).Reason, "node hidden", "accounts on a hidden node are paused")

	// Hidden nodes leave the default listing but stay reachable by filter.
	require.Equal(t, []int64{visible.ID}, listClashNodeIDs(t, env.svc, ""))
	require.Equal(t, []int64{bound.ID, free.ID, adminOff.ID, gone.ID}, listClashNodeIDs(t, env.svc, ClashNodeVisibilityHidden))
	require.Len(t, listClashNodeIDs(t, env.svc, ClashNodeVisibilityAll), 5)
	stats, err := env.repo.ListProfileStats(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, stats[profile.ID].Hidden)
	require.Equal(t, 1, stats[profile.ID].Total)

	// The selector keeps a hidden node only while an account still uses it.
	exits, err := env.svc.ListExits(ctx)
	require.NoError(t, err)
	byNode := map[int64]ClashExitOption{}
	for _, e := range exits.Exits {
		byNode[e.NodeID] = e
	}
	require.Contains(t, byNode, bound.ID)
	require.False(t, byNode[bound.ID].Available)
	require.Equal(t, "node hidden", byNode[bound.ID].UnavailableReason)
	require.NotContains(t, byNode, free.ID)
	require.NotContains(t, byNode, adminOff.ID)
	require.Contains(t, byNode, visible.ID)

	// Hiding again or disabling a hidden node changes nothing.
	result, err = env.svc.UpdateNodes(ctx, ClashNodeActionHide, []int64{bound.ID})
	require.NoError(t, err)
	require.Zero(t, result.Updated)
	result, err = env.svc.UpdateNodes(ctx, ClashNodeActionDisable, []int64{free.ID})
	require.NoError(t, err)
	require.Zero(t, result.Updated)
	require.Equal(t, 1, structural, "no-op actions do not resync the core")

	// Unhiding only revives what hiding took offline.
	result, err = env.svc.UpdateNodes(ctx, ClashNodeActionUnhide, []int64{bound.ID, adminOff.ID, gone.ID})
	require.NoError(t, err)
	require.Equal(t, 3, result.Updated)
	require.ElementsMatch(t, []int64{bound.ID, adminOff.ID, gone.ID}, result.NodeIDs)
	require.Equal(t, ClashNodeStatusActive, env.repo.node(bound.ID).Status)
	require.Empty(t, env.repo.node(bound.ID).StatusReason)
	require.Equal(t, ClashNodeStatusDisabled, env.repo.node(adminOff.ID).Status)
	require.Equal(t, ClashNodeStatusMissing, env.repo.node(gone.ID).Status)
	require.False(t, env.repo.node(gone.ID).Hidden)
	require.Empty(t, env.repo.account(acc.ID).Reason, "the pause is lifted once the node is back")

	// Enabling a hidden node brings it back into view as well.
	result, err = env.svc.UpdateNodes(ctx, ClashNodeActionEnable, []int64{free.ID, adminOff.ID, visible.ID})
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated, "the active node is skipped")
	require.Equal(t, ClashNodeStatusActive, env.repo.node(free.ID).Status)
	require.False(t, env.repo.node(free.ID).Hidden)
	require.Equal(t, ClashNodeStatusActive, env.repo.node(adminOff.ID).Status)

	// Batch disable only touches active nodes.
	env.runtime.closed = nil
	result, err = env.svc.UpdateNodes(ctx, ClashNodeActionDisable, []int64{free.ID, gone.ID})
	require.NoError(t, err)
	require.Equal(t, []int64{free.ID}, result.NodeIDs)
	require.Equal(t, []string{ClashListenerName(free.ID)}, env.runtime.closed)
	require.Equal(t, ClashNodeReasonDisabledByAdmin, env.repo.node(free.ID).StatusReason)
}

func TestClashUpdateNodesValidation(t *testing.T) {
	env := newClashTestEnv(t, nil)
	ctx := context.Background()
	profile := env.repo.addProfile("A", true)
	node := env.repo.addNode(profile.ID, "a", "198.51.100.1")

	_, err := env.svc.UpdateNodes(ctx, "explode", []int64{node.ID})
	requireClashReason(t, err, "CLASH_NODE_ACTION_INVALID")
	_, err = env.svc.UpdateNodes(ctx, ClashNodeActionHide, []int64{0, -1})
	requireClashReason(t, err, "CLASH_NODE_SELECTION_EMPTY")
	tooMany := make([]int64, 0, clashNodeActionLimit+1)
	for i := 1; i <= clashNodeActionLimit+1; i++ {
		tooMany = append(tooMany, int64(i))
	}
	_, err = env.svc.UpdateNodes(ctx, ClashNodeActionHide, tooMany)
	requireClashReason(t, err, "CLASH_TOO_MANY_NODES")

	// The single-node endpoints keep reporting unknown nodes.
	require.ErrorIs(t, env.svc.SetNodeEnabled(ctx, 999999, false), ErrClashNodeNotFound)
	require.NoError(t, env.svc.SetNodeEnabled(ctx, node.ID, false))
	require.Equal(t, ClashNodeStatusDisabled, env.repo.node(node.ID).Status)
	require.NoError(t, env.svc.SetNodeEnabled(ctx, node.ID, true))
	require.Equal(t, ClashNodeStatusActive, env.repo.node(node.ID).Status)
}

func TestBuildClashNodeSyncPlanKeepsHiddenNodesOffline(t *testing.T) {
	a := parsedNode("A", "ss", "a.example.com", 1, "p")
	b := parsedNode("B", "ss", "b.example.com", 1, "p")
	c := parsedNode("C", "ss", "c.example.com", 1, "p")
	d := parsedNode("D", "ss", "d.example.com", 1, "p")
	local := parsedNode("E", "ss", "127.0.0.1", 1, "p")
	existing := []ClashNode{
		storedNode(1, a, ClashNodeStatusDisabled),
		storedNode(2, b, ClashNodeStatusMissing),
		storedNode(3, c, ClashNodeStatusInvalid),
		storedNode(4, d, ClashNodeStatusDisabled),
		storedNode(5, local, ClashNodeStatusDisabled),
	}
	existing[0].StatusReason = ClashNodeReasonHiddenByAdmin
	existing[2].StatusReason = "core rejected"
	existing[3].StatusReason = ClashNodeReasonDisabledByAdmin
	existing[4].StatusReason = ClashNodeReasonHiddenByAdmin
	for i := range existing {
		existing[i].Hidden = true
	}
	// C's configuration changed, which would normally clear its invalid state.
	changed := parsedNode("C", "ss", "c.example.com", 1, "new-password")
	plan, _, err := BuildClashNodeSyncPlan(existing, []clashsub.Node{a, b, changed, d, local}, ClashSyncOptions{})
	require.NoError(t, err)
	type state struct{ status, reason string }
	got := map[int64]state{}
	for _, u := range plan.Updates {
		got[u.NodeID] = state{u.Status, u.StatusReason}
	}
	require.Equal(t, state{ClashNodeStatusDisabled, ClashNodeReasonHiddenByAdmin}, got[1])
	require.Equal(t, state{ClashNodeStatusDisabled, ClashNodeReasonHiddenByAdmin}, got[2], "a returning hidden node stays offline")
	require.Equal(t, state{ClashNodeStatusDisabled, ClashNodeReasonHiddenByAdmin}, got[3])
	require.Equal(t, state{ClashNodeStatusDisabled, ClashNodeReasonDisabledByAdmin}, got[4], "the admin disable reason is kept")
	require.Equal(t, ClashNodeStatusInvalid, got[5].status, "screening failures still win, so unhiding cannot revive the node")
	require.Empty(t, plan.Missing)
}
