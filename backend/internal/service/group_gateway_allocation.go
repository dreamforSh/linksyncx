package service

import (
	"context"
	"errors"
)

var (
	ErrGroupMemberRequired      = errors.New("membership in this group is required")
	ErrGroupQuotaExceeded       = errors.New("group daily request quota exceeded")
	ErrGroupConcurrencyExceeded = errors.New("group concurrent request limit exceeded")
	ErrGroupAccountRequired     = errors.New("no accounts assigned to this member")
	// 额度组成员的 5h / 7d 美元上限
	ErrGroupMemberUsage5hExceeded = errors.New("group 5-hour usage limit exceeded")
	ErrGroupMemberUsage7dExceeded = errors.New("group 7-day usage limit exceeded")
)

// GatewayAllocation is scoped to one authenticated request. A nil AllowedIDs
// means automatic scheduling; a non-nil empty set denies all accounts.
type GatewayAllocation struct {
	GroupID    int64
	AllowedIDs map[int64]struct{}
	Enabled    bool
}

type gatewayAllocationKey struct{}

func WithGatewayAllocation(ctx context.Context, allocation GatewayAllocation) context.Context {
	return context.WithValue(ctx, gatewayAllocationKey{}, allocation)
}

func GroupAccountAllowed(ctx context.Context, groupID *int64, accountID int64) bool {
	policy, ok := ctx.Value(gatewayAllocationKey{}).(GatewayAllocation)
	if !ok || !policy.Enabled {
		return true
	}
	if groupID == nil || *groupID != policy.GroupID {
		return false
	}
	return GroupAccountAllowedForRequest(ctx, accountID)
}

func GroupAccountAllowedForRequest(ctx context.Context, accountID int64) bool {
	policy, ok := ctx.Value(gatewayAllocationKey{}).(GatewayAllocation)
	if !ok || !policy.Enabled || policy.AllowedIDs == nil {
		return true
	}
	_, allowed := policy.AllowedIDs[accountID]
	return allowed
}

func FilterGroupAccounts(ctx context.Context, groupID *int64, accounts []Account) []Account {
	policy, ok := ctx.Value(gatewayAllocationKey{}).(GatewayAllocation)
	if !ok || !policy.Enabled {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if GroupAccountAllowed(ctx, groupID, account.ID) {
			filtered = append(filtered, account)
		}
	}
	return filtered
}

// GatewayAdmission returns a release function for an in-flight request.
// Read-only endpoints check membership without consuming a request.
func (s *GroupManagementService) GatewayAdmission(ctx context.Context, userID, groupID int64, admin, consume bool) (GatewayAllocation, func(), error) {
	return s.repo.GatewayAdmission(ctx, userID, groupID, admin, consume)
}
