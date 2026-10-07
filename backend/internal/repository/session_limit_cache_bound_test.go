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

func newSessionLimitCacheForTest(t *testing.T) (*miniredis.Miniredis, service.SessionLimitCache) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, NewSessionLimitCache(client, 5)
}

// 已绑定对话的会话不受上限约束：名额已满时照样登记，账号短暂超出预算；
// 登记前先清理空闲超时的会话。
func TestSessionLimitCacheRegisterBoundSessionIgnoresTheLimit(t *testing.T) {
	server, cache := newSessionLimitCacheForTest(t)
	ctx := t.Context()
	key := sessionLimitKey(1)
	server.SetTime(time.Unix(1_800_000_000, 0))

	allowed, err := cache.RegisterSession(ctx, 1, "a", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed)
	allowed, err = cache.RegisterSession(ctx, 1, "b", 1, time.Minute)
	require.NoError(t, err)
	require.False(t, allowed, "precondition: the budget is full")

	require.NoError(t, cache.RegisterBoundSession(ctx, 1, "b", time.Minute))
	members, err := server.ZMembers(key)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"a", "b"}, members)
	require.Equal(t, 2*time.Minute, server.TTL(key), "idle timeout + 60s, same as RegisterSession")

	allowed, err = cache.RegisterSession(ctx, 1, "c", 1, time.Minute)
	require.NoError(t, err)
	require.False(t, allowed, "new conversations stay rejected while the account is over budget")

	server.SetTime(time.Unix(1_800_000_000+90, 0))
	require.NoError(t, cache.RegisterBoundSession(ctx, 1, "b", time.Minute))
	members, err = server.ZMembers(key)
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, members, "idle sessions are cleaned before registering")

	require.NoError(t, cache.RegisterBoundSession(ctx, 1, "", time.Minute))
	members, err = server.ZMembers(key)
	require.NoError(t, err)
	require.Equal(t, []string{"b"}, members, "an empty session is ignored")
}
