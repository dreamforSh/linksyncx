//go:build unit

package repository

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newClaudeRateLimitCacheForTest(t *testing.T) (*miniredis.Miniredis, service.ClaudeRateLimitCache) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, NewClaudeRateLimitCache(client)
}

// 只接受更晚收到的响应头：先写入的快照被后来、但更早收到的响应丢弃，并返回写入前的快照供比较。
func TestClaudeRateLimitCacheAppliesSnapshotsMonotonically(t *testing.T) {
	server, cache := newClaudeRateLimitCacheForTest(t)
	ctx := t.Context()

	applied, prev, err := cache.ApplyClaudeRateLimitSnapshot(ctx, 7, &service.ClaudeRateLimitSnapshot{AppliedAtMs: 1_000, Status: "allowed"})
	require.NoError(t, err)
	require.True(t, applied)
	require.Nil(t, prev)
	require.Equal(t, claudeQuotaTTL, server.TTL(claudeQuotaKey(7)))

	applied, prev, err = cache.ApplyClaudeRateLimitSnapshot(ctx, 7, &service.ClaudeRateLimitSnapshot{AppliedAtMs: 2_000, Status: "allowed_warning"})
	require.NoError(t, err)
	require.True(t, applied)
	require.NotNil(t, prev)
	require.Equal(t, "allowed", prev.Status)

	applied, prev, err = cache.ApplyClaudeRateLimitSnapshot(ctx, 7, &service.ClaudeRateLimitSnapshot{AppliedAtMs: 1_500, Status: "rejected"})
	require.NoError(t, err)
	require.False(t, applied, "a response received earlier than the stored one is dropped")
	require.Equal(t, "allowed_warning", prev.Status)

	applied, _, err = cache.ApplyClaudeRateLimitSnapshot(ctx, 7, &service.ClaudeRateLimitSnapshot{AppliedAtMs: 2_000, Status: "rejected"})
	require.NoError(t, err)
	require.False(t, applied, "the same receive time does not overwrite either")

	// 损坏的记录视同缺失。
	require.NoError(t, server.Set(claudeQuotaKey(8), "{not json"))
	applied, prev, err = cache.ApplyClaudeRateLimitSnapshot(ctx, 8, &service.ClaudeRateLimitSnapshot{AppliedAtMs: 1})
	require.NoError(t, err)
	require.True(t, applied)
	require.Nil(t, prev)
}

// 落库机会在多实例间共享：周期内只领取一次；有实质变化时强制领取并重新计时。
func TestClaudeRateLimitCacheClaimsPersistOncePerInterval(t *testing.T) {
	server, cache := newClaudeRateLimitCacheForTest(t)
	ctx := t.Context()

	due, err := cache.ClaimClaudeRateLimitPersist(ctx, 7, time.Minute, false)
	require.NoError(t, err)
	require.True(t, due)
	due, err = cache.ClaimClaudeRateLimitPersist(ctx, 7, time.Minute, false)
	require.NoError(t, err)
	require.False(t, due, "another instance already persisted within the interval")

	server.FastForward(30 * time.Second)
	due, err = cache.ClaimClaudeRateLimitPersist(ctx, 7, time.Minute, true)
	require.NoError(t, err)
	require.True(t, due, "a material change is always persisted")
	require.Equal(t, time.Minute, server.TTL(claudeQuotaPersistKey(7)), "the interval restarts after a forced persist")

	server.FastForward(61 * time.Second)
	due, err = cache.ClaimClaudeRateLimitPersist(ctx, 7, time.Minute, false)
	require.NoError(t, err)
	require.True(t, due, "the next periodic persist is due once the interval passed")
}
