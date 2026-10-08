//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUMQCacheForTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, service.UserMsgQueueCache) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, client, NewUserMsgQueueCache(client)
}

// 同一会话串行、不同会话并行；账号级锁（单会话模式、请求不带会话）与会话锁互不影响。
// 完成时间按会话记录。
func TestUserMsgQueueCacheSessionScopedLocks(t *testing.T) {
	server, client, cache := newUMQCacheForTest(t)
	ctx := t.Context()
	sessA := service.UserMsgQueueScope{AccountID: 9, Session: "0a1b"}
	sessB := service.UserMsgQueueScope{AccountID: 9, Session: "2c3d"}
	account := service.UserMsgQueueScope{AccountID: 9}

	ok, err := cache.AcquireLock(ctx, sessA, "req-a", 10_000)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = cache.AcquireLock(ctx, sessA, "req-a2", 10_000)
	require.NoError(t, err)
	require.False(t, ok, "the same session is serialized")
	ok, err = cache.AcquireLock(ctx, sessB, "req-b", 10_000)
	require.NoError(t, err)
	require.True(t, ok, "another session runs in parallel")
	ok, err = cache.AcquireLock(ctx, account, "req-acct", 10_000)
	require.NoError(t, err)
	require.True(t, ok, "the account-level lock is a separate scope")

	require.True(t, server.Exists("umq:{9}:s:0a1b:lock"))
	require.True(t, server.Exists("umq:{9}:lock"), "account-level keys keep the legacy name")
	members, err := client.ZRange(ctx, umqLockIndexKey, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"9", "9:0a1b", "9:2c3d"}, members)

	released, err := cache.ReleaseLock(ctx, sessA, "req-a")
	require.NoError(t, err)
	require.True(t, released)
	require.True(t, server.Exists("umq:{9}:s:0a1b:last"))
	lastA, err := cache.GetLastCompletedMs(ctx, sessA)
	require.NoError(t, err)
	require.Positive(t, lastA)
	lastB, err := cache.GetLastCompletedMs(ctx, sessB)
	require.NoError(t, err)
	require.Zero(t, lastB, "completion time is tracked per session")
	members, err = client.ZRange(ctx, umqLockIndexKey, 0, -1).Result()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"9", "9:2c3d"}, members)
}

// 后台清理识别会话级 member：无 TTL 的会话锁被删除，格式非法的 member 被移除。
func TestUserMsgQueueCacheReconcileSessionMembers(t *testing.T) {
	server, client, cache := newUMQCacheForTest(t)
	ctx := t.Context()
	require.NoError(t, server.Set("umq:{9}:s:0a1b:lock", "stuck"))
	for _, member := range []string{"9:0a1b", "9:", "9:XYZ", "0:0a1b"} {
		require.NoError(t, client.ZAdd(ctx, umqLockIndexKey, redis.Z{Score: 1, Member: member}).Err())
	}

	cleaned, err := cache.ReconcileExpiredLockCandidates(ctx, 100)
	require.NoError(t, err)
	require.Equal(t, 1, cleaned)
	require.False(t, server.Exists("umq:{9}:s:0a1b:lock"))
	members, err := client.ZRange(ctx, umqLockIndexKey, 0, -1).Result()
	require.NoError(t, err)
	require.Empty(t, members)
}

func TestUserMsgQueueCacheRejectsInvalidScope(t *testing.T) {
	server, _, cache := newUMQCacheForTest(t)
	ctx := t.Context()
	for _, scope := range []service.UserMsgQueueScope{
		{AccountID: 0},
		{AccountID: 9, Session: "Not-Hex"},
		{AccountID: 9, Session: "a:b"},
	} {
		_, err := cache.AcquireLock(ctx, scope, "req", 1000)
		require.Error(t, err, "scope %+v", scope)
		_, err = cache.ReleaseLock(ctx, scope, "req")
		require.Error(t, err)
		_, err = cache.GetLastCompletedMs(ctx, scope)
		require.Error(t, err)
	}
	require.Empty(t, server.Keys())
}
