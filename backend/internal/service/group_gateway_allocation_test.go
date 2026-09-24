package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupAccountAllocationIsRequestScopedAndFailClosed(t *testing.T) {
	groupID, otherGroup := int64(4), int64(5)
	accounts := []Account{{ID: 10}, {ID: 11}}
	ctx := WithGatewayAllocation(context.Background(), GatewayAllocation{
		GroupID: groupID, Enabled: true, AllowedIDs: map[int64]struct{}{10: {}},
	})
	require.Equal(t, []Account{{ID: 10}}, FilterGroupAccounts(ctx, &groupID, accounts))
	require.Empty(t, FilterGroupAccounts(ctx, &otherGroup, accounts))
	require.Empty(t, FilterGroupAccounts(ctx, nil, accounts))
	require.False(t, GroupAccountAllowedForRequest(ctx, 11))
	require.False(t, GroupAccountAllowed(ctx, &groupID, 11))
	require.True(t, GroupAccountAllowed(ctx, &groupID, 10))

	denied := WithGatewayAllocation(ctx, GatewayAllocation{GroupID: groupID, Enabled: true, AllowedIDs: map[int64]struct{}{}})
	require.Empty(t, FilterGroupAccounts(denied, &groupID, accounts))
	auto := WithGatewayAllocation(ctx, GatewayAllocation{GroupID: groupID, Enabled: true})
	require.Equal(t, accounts, FilterGroupAccounts(auto, &groupID, accounts))
	require.Empty(t, FilterGroupAccounts(auto, &otherGroup, accounts))
	require.Equal(t, accounts, FilterGroupAccounts(context.Background(), &otherGroup, accounts))
}
