package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func managedAPIKey(managedType string) *APIKey {
	groupID := int64(9)
	return &APIKey{
		ID: 13, UserID: 7, GroupID: &groupID, Key: "sk-managed-" + managedType, Status: StatusActive,
		User: &User{ID: 7, Status: StatusActive, Role: RoleUser},
		Group: &Group{
			ID: groupID, Name: "acme", Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true,
			Kind: GroupKindManaged, ManagedType: managedType, RateMultiplier: 1.5,
			ImageRateIndependent: true, ImageRateMultiplier: 2, VideoRateIndependent: true, VideoRateMultiplier: 3,
		},
	}
}

// 订阅组请求不扣余额：文本、图片、视频倍率都视为 0，用量照常记录。
func TestManagedSubscriptionGroupBillsAtZeroMultiplier(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.Local)
	subscription := managedAPIKey(ManagedGroupTypeSubscription)
	text, image := computePeakAwareMultipliers(subscription, 1.5, now)
	require.Zero(t, text)
	require.Zero(t, image)
	require.Zero(t, resolveImageRateMultiplier(subscription, 1.5))
	require.Zero(t, resolveVideoRateMultiplier(subscription, 1.5))

	// 额度组按正常倍率计费
	quota := managedAPIKey(ManagedGroupTypeQuota)
	text, image = computePeakAwareMultipliers(quota, 1.5, now)
	require.InDelta(t, 1.5, text, 1e-9)
	require.InDelta(t, 2.0, image, 1e-9)
	require.InDelta(t, 3.0, resolveVideoRateMultiplier(quota, 1.5), 1e-9)
}

// 额度组：实际扣费同时计入成员的组内 5h / 7d 用量；该字段不参与幂等指纹。
func TestBuildUsageBillingCommandTracksQuotaGroupMemberUsage(t *testing.T) {
	build := func(apiKey *APIKey) *UsageBillingCommand {
		return buildUsageBillingCommand("req-1", nil, &postUsageBillingParams{
			Cost:          &CostBreakdown{ActualCost: 1.25, TotalCost: 1},
			User:          apiKey.User,
			APIKey:        apiKey,
			Account:       &Account{ID: 5, Type: AccountTypeOAuth},
			APIKeyService: &apiKeyQuotaUpdaterStub{},
		})
	}

	quota := build(managedAPIKey(ManagedGroupTypeQuota))
	require.Equal(t, int64(9), quota.GroupID)
	require.Equal(t, 1.25, quota.GroupMemberCost)
	require.Equal(t, 1.25, quota.BalanceCost)

	channelKey := managedAPIKey(ManagedGroupTypeQuota)
	channelKey.Group.Kind, channelKey.Group.ManagedType = GroupKindChannel, ""
	channel := build(channelKey)
	require.Zero(t, channel.GroupID)
	require.Zero(t, channel.GroupMemberCost)
	require.Equal(t, quota.RequestFingerprint, channel.RequestFingerprint, "group usage must not change the idempotency fingerprint")

	subscription := build(managedAPIKey(ManagedGroupTypeSubscription))
	require.Zero(t, subscription.GroupMemberCost)
}

// 订阅组不扣余额：余额耗尽也能通过计费预检；额度组照常检查余额。
func TestCheckBillingEligibilitySkipsBalanceForManagedSubscription(t *testing.T) {
	cache := &balanceEligibilityCacheStubLite{balance: 0}
	svc := NewBillingCacheService(cache, nil, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(svc.Stop)

	subscription := managedAPIKey(ManagedGroupTypeSubscription)
	require.NoError(t, svc.CheckBillingEligibility(context.Background(), subscription.User, subscription, subscription.Group, nil, PlatformAnthropic))

	quota := managedAPIKey(ManagedGroupTypeQuota)
	require.ErrorIs(t, svc.CheckBillingEligibility(context.Background(), quota.User, quota, quota.Group, nil, PlatformAnthropic), ErrInsufficientBalance)
}

type balanceEligibilityCacheStubLite struct {
	billingCacheWorkerStub
	balance float64
}

func (s *balanceEligibilityCacheStubLite) GetUserBalance(context.Context, int64) (float64, error) {
	return s.balance, nil
}

func TestAPIKeyAuthSnapshotCarriesManagedGroupType(t *testing.T) {
	apiKey := managedAPIKey(ManagedGroupTypeSubscription)
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.Equal(t, GroupKindManaged, materialized.Group.Kind)
	require.Equal(t, ManagedGroupTypeSubscription, materialized.Group.ManagedType)
	require.True(t, ManagedSubscriptionBilling(materialized))

	// v24 快照缺少分组类型，必须作废重新加载
	_, used, err = svc.applyAuthCacheEntry(apiKey.Key, &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{Version: 24}})
	require.NoError(t, err)
	require.False(t, used)
}
