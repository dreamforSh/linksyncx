//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 调度缓存加固：命中一条"分配了代理但缺 Proxy 关系"的旧序列化条目时，GetAccount 必须
// 回源 DB（会 eager-load Proxy）而不是直接返回，否则下游 ProxyURLForOutbound 会把本可用的
// 代理账号误判为不可用（fail-closed 误报）。
func TestSchedulerSnapshotGetAccountRefreshesCachedAccountMissingProxyRelation(t *testing.T) {
	proxyID := int64(9)

	staleCached := &Account{ID: 1, Platform: PlatformOpenAI, ProxyID: &proxyID} // Proxy == nil（旧缓存）
	fresh := &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		ProxyID:  &proxyID,
		Proxy:    &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example.com", Port: 8080},
	}

	cache := &snapshotHydrationCache{accounts: map[int64]*Account{1: staleCached}}
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{1: fresh}}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	got, err := svc.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Proxy, "must return the DB copy with the proxy relation populated")
	require.Equal(t, 1, repo.getByIDCalls, "stale cached account must trigger exactly one DB fallback")

	url, err := got.ProxyURLForOutbound()
	require.NoError(t, err)
	require.Equal(t, "http://proxy.example.com:8080", url)
}

// 反例：缓存条目代理关系完整（或压根没配代理）时，GetAccount 直接返回缓存，不回源 DB。
func TestSchedulerSnapshotGetAccountKeepsCachedAccountWithProxyRelation(t *testing.T) {
	proxyID := int64(9)
	cachedComplete := &Account{
		ID:       1,
		Platform: PlatformOpenAI,
		ProxyID:  &proxyID,
		Proxy:    &Proxy{ID: proxyID, Protocol: "http", Host: "proxy.example.com", Port: 8080},
	}
	cache := &snapshotHydrationCache{accounts: map[int64]*Account{1: cachedComplete}}
	repo := &mockAccountRepoForPlatform{accountsByID: map[int64]*Account{}}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	got, err := svc.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Same(t, cachedComplete, got)
	require.Zero(t, repo.getByIDCalls, "a complete cached account must not hit the DB")
}
