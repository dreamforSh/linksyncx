//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// emptyBucketMissCache 模拟 Redis 快照缓存对空分桶的行为：空快照视同未命中。
type emptyBucketMissCache struct {
	snapshotHydrationCache
}

func (c *emptyBucketMissCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	return nil, false, nil
}

type emptyBucketCountingRepo struct {
	*mockAccountRepoForPlatform
	accounts []Account
	calls    int
}

func (r *emptyBucketCountingRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	r.calls++
	return r.accounts, nil
}

func newEmptyBucketTestServices() (*GatewayService, *SchedulerSnapshotService, *emptyBucketCountingRepo) {
	repo := &emptyBucketCountingRepo{mockAccountRepoForPlatform: &mockAccountRepoForPlatform{}}
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	snapshot := NewSchedulerSnapshotService(&emptyBucketMissCache{}, nil, repo, nil, cfg)
	return &GatewayService{schedulerSnapshot: snapshot}, snapshot, repo
}

func TestIsSingleAntigravityAccountGroup_RemembersEmptyBucket(t *testing.T) {
	svc, snapshot, repo := newEmptyBucketTestServices()
	groupID := int64(7)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		require.False(t, svc.IsSingleAntigravityAccountGroup(ctx, &groupID))
	}
	require.Equal(t, 1, repo.calls, "空桶确认后，记忆期内的请求不应再回源 DB")
	require.True(t, snapshot.KnownEmptyBucket(&groupID, PlatformAntigravity, true))

	// 记忆过期后重新回源：分组此时已绑定唯一的 antigravity 账号。
	bucket := snapshot.bucketFor(&groupID, PlatformAntigravity, snapshot.resolveMode(PlatformAntigravity, true))
	snapshot.emptyBuckets.Store(bucket, time.Now().Add(-time.Second).UnixNano())
	repo.accounts = []Account{{ID: 11, Platform: PlatformAntigravity, Status: StatusActive, Schedulable: true}}

	require.True(t, svc.IsSingleAntigravityAccountGroup(ctx, &groupID))
	require.Equal(t, 2, repo.calls)
	require.False(t, snapshot.KnownEmptyBucket(&groupID, PlatformAntigravity, true), "回源到账号后应清除空桶记忆")
}

func TestIsSingleAntigravityAccountGroup_NonEmptyBucketIsNotRemembered(t *testing.T) {
	svc, snapshot, repo := newEmptyBucketTestServices()
	repo.accounts = []Account{{ID: 11, Platform: PlatformAntigravity, Status: StatusActive, Schedulable: true}}
	groupID := int64(8)

	for i := 0; i < 3; i++ {
		require.True(t, svc.IsSingleAntigravityAccountGroup(context.Background(), &groupID))
	}
	require.Equal(t, 3, repo.calls)
	require.False(t, snapshot.KnownEmptyBucket(&groupID, PlatformAntigravity, true))
}

func TestSchedulerSnapshotListSchedulableAccounts_EmptyBucketStillQueriesDB(t *testing.T) {
	_, snapshot, repo := newEmptyBucketTestServices()
	groupID := int64(9)

	for i := 0; i < 3; i++ {
		accounts, _, err := snapshot.ListSchedulableAccounts(context.Background(), &groupID, PlatformAntigravity, true)
		require.NoError(t, err)
		require.Empty(t, accounts)
	}
	require.True(t, snapshot.KnownEmptyBucket(&groupID, PlatformAntigravity, true))
	require.Equal(t, 3, repo.calls, "选号路径不读空桶记忆，新绑定的账号必须立即可见")
}
