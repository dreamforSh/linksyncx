//go:build unit

package repository

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newIdentityCacheForTest(t *testing.T) (*miniredis.Miniredis, service.IdentityCache) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return server, NewIdentityCache(client)
}

// 多实例并发首建同一账号的指纹：所有实例必须拿到同一个 ClientID，且它就是持久化的那个。
func TestIdentityConcurrentCreationUsesOnePersistentWinner(t *testing.T) {
	_, cache := newIdentityCacheForTest(t)
	ids := make(chan string, 32)
	failures := make(chan error, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fp, err := service.NewIdentityService(cache).GetOrCreateFingerprint(t.Context(), 42, http.Header{})
			if err != nil {
				failures <- err
				return
			}
			ids <- fp.ClientID
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	winner := ""
	for id := range ids {
		if winner == "" {
			winner = id
		}
		require.Equal(t, winner, id)
	}
	require.NotEmpty(t, winner)
	stored, err := cache.GetFingerprint(t.Context(), 42)
	require.NoError(t, err)
	require.Equal(t, winner, stored.ClientID)
	reloaded, err := service.NewIdentityService(cache).GetOrCreateFingerprint(t.Context(), 42, http.Header{})
	require.NoError(t, err)
	require.Equal(t, winner, reloaded.ClientID)
}

// 存储里的损坏记录（非法 JSON / null / 无 ClientID）视同缺失：读取报错，但首建会原子地把它替换掉，
// 之后所有实例稳定使用新身份，而不是每个请求各生成一个临时指纹直到旧键过期。
func TestIdentityCorruptedRecordIsReplacedOnCreate(t *testing.T) {
	for _, payload := range []string{"invalid-json", "null", "{}", `{"ClientID":""}`} {
		t.Run(payload, func(t *testing.T) {
			server, cache := newIdentityCacheForTest(t)
			require.NoError(t, server.Set(fingerprintKey(7), payload))
			_, err := cache.GetFingerprint(t.Context(), 7)
			require.Error(t, err)

			fp, err := service.NewIdentityService(cache).GetOrCreateFingerprint(t.Context(), 7, http.Header{})
			require.NoError(t, err)
			require.NotEmpty(t, fp.ClientID)
			stored, err := cache.GetFingerprint(t.Context(), 7)
			require.NoError(t, err)
			require.Equal(t, fp.ClientID, stored.ClientID)
			require.Equal(t, fingerprintTTL, server.TTL(fingerprintKey(7)))

			again, err := service.NewIdentityService(cache).GetOrCreateFingerprint(t.Context(), 7, http.Header{})
			require.NoError(t, err)
			require.Equal(t, fp.ClientID, again.ClientID)
		})
	}
}

func TestIdentityCreateKeepsExistingWinner(t *testing.T) {
	_, cache := newIdentityCacheForTest(t)
	for i := 0; i < 3; i++ {
		fp, err := cache.CreateFingerprint(t.Context(), 9, &service.Fingerprint{ClientID: fmt.Sprint(i)})
		require.NoError(t, err)
		require.Equal(t, "0", fp.ClientID)
	}
	_, err := cache.CreateFingerprint(t.Context(), 9, &service.Fingerprint{})
	require.Error(t, err, "an identity without a client ID must not be stored")
}

// 同一身份的续期：更新字段并刷新完整 TTL。
func TestIdentityRefreshKeepsSameIdentityAndRenewsTTL(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	_, err := cache.CreateFingerprint(t.Context(), 8, &service.Fingerprint{ClientID: "c8", UserAgent: "ua-old", UpdatedAt: 1})
	require.NoError(t, err)
	server.FastForward(fingerprintTTL / 2)
	refreshed, err := cache.SetFingerprint(t.Context(), 8, &service.Fingerprint{ClientID: "c8", UserAgent: "ua-new", UpdatedAt: 2})
	require.NoError(t, err)
	require.Equal(t, "ua-new", refreshed.UserAgent)
	require.EqualValues(t, 2, refreshed.UpdatedAt)
	require.Equal(t, fingerprintTTL, server.TTL(fingerprintKey(8)))
	stored, err := cache.GetFingerprint(t.Context(), 8)
	require.NoError(t, err)
	require.Equal(t, "ua-new", stored.UserAgent)
}

type refreshRaceCache struct {
	service.IdentityCache
	beforeRefresh func()
}

func (c *refreshRaceCache) SetFingerprint(ctx context.Context, id int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	c.beforeRefresh()
	return c.IdentityCache.SetFingerprint(ctx, id, fp)
}

// 实例 A 读到旧身份后准备续期，期间旧键过期、实例 B 已创建新身份：A 的续期不能把旧身份写回去，
// 而且 A 本次请求也要改用新身份。
func TestIdentityExpiredSnapshotCannotReplaceNewWinner(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	old := &service.Fingerprint{ClientID: "old", UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Add(-48 * time.Hour).Unix()}
	_, err := cache.CreateFingerprint(t.Context(), 14, old)
	require.NoError(t, err)
	raced := &refreshRaceCache{IdentityCache: cache, beforeRefresh: func() {
		server.FastForward(fingerprintTTL + time.Second)
		_, createErr := cache.CreateFingerprint(t.Context(), 14, &service.Fingerprint{ClientID: "new", UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Unix()})
		require.NoError(t, createErr)
	}}
	got, err := service.NewIdentityService(raced).GetOrCreateFingerprint(t.Context(), 14, http.Header{})
	require.NoError(t, err)
	require.Equal(t, "new", got.ClientID)
	stored, err := cache.GetFingerprint(t.Context(), 14)
	require.NoError(t, err)
	require.Equal(t, "new", stored.ClientID)
}

func TestMaskedSessionConcurrentCreationAndRenewal(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	ids := make(chan string, 32)
	failures := make(chan error, 32)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := cache.GetOrCreateMaskedSessionID(t.Context(), 15, fmt.Sprint(i))
			if err != nil {
				failures <- err
				return
			}
			ids <- id
		}(i)
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	winner := ""
	for id := range ids {
		if winner == "" {
			winner = id
		}
		require.Equal(t, winner, id)
	}
	require.NotEmpty(t, winner)
	// 每次调用都刷新 TTL：快到期时再来一次，值不变且不会过期。
	server.FastForward(maskedSessionTTL - time.Second)
	same, err := cache.GetOrCreateMaskedSessionID(t.Context(), 15, "different")
	require.NoError(t, err)
	require.Equal(t, winner, same)
	server.FastForward(2 * time.Second)
	stored, err := server.Get(maskedSessionKey(15))
	require.NoError(t, err)
	require.Equal(t, winner, stored)
	// 整个窗口内无请求后过期，下一次请求用新的候选值。
	server.FastForward(maskedSessionTTL)
	next, err := cache.GetOrCreateMaskedSessionID(t.Context(), 15, "new-window")
	require.NoError(t, err)
	require.Equal(t, "new-window", next)
	_, err = cache.GetOrCreateMaskedSessionID(t.Context(), 15, "")
	require.Error(t, err)
}

// 账号级会话键：环境会话原子 get-or-create 并滑动续期；最近活跃会话可覆盖、缺失返回空串。
func TestIdentityCacheAccountSessionKeys(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	ctx := t.Context()

	first, err := cache.GetOrCreateAmbientSessionID(ctx, 5, "ambient-a")
	require.NoError(t, err)
	require.Equal(t, "ambient-a", first)
	second, err := cache.GetOrCreateAmbientSessionID(ctx, 5, "ambient-b")
	require.NoError(t, err)
	require.Equal(t, "ambient-a", second, "an existing ambient session wins")
	require.Equal(t, accountSessionTTL, server.TTL(ambientSessionKey(5)))
	require.Equal(t, "claude:session:5:ambient", ambientSessionKey(5))

	last, err := cache.GetLastActiveSessionID(ctx, 5)
	require.NoError(t, err)
	require.Empty(t, last)
	require.NoError(t, cache.SetLastActiveSessionID(ctx, 5, "s1"))
	require.NoError(t, cache.SetLastActiveSessionID(ctx, 5, "s2"))
	last, err = cache.GetLastActiveSessionID(ctx, 5)
	require.NoError(t, err)
	require.Equal(t, "s2", last)
	require.Equal(t, accountSessionTTL, server.TTL(lastActiveSessionKey(5)))

	server.FastForward(accountSessionTTL + time.Second)
	last, err = cache.GetLastActiveSessionID(ctx, 5)
	require.NoError(t, err)
	require.Empty(t, last, "the last active session expires with its TTL")
}

// 迁移水位线：写入带 TTL，读取时续期，删除后读不到。
func TestIdentityCacheClaudeSessionMigration(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	ctx := t.Context()
	key := claudeSessionMigrationKey(5, "conv")
	require.Equal(t, "claude:session:5:conv:migrated", key)

	got, err := cache.GetClaudeSessionMigration(ctx, 5, "conv", time.Hour)
	require.NoError(t, err)
	require.Nil(t, got)

	migration := service.ClaudeSessionMigration{FromAccountID: 3, MessageCount: 12, At: 1_800_000_000}
	require.NoError(t, cache.SetClaudeSessionMigration(ctx, 5, "conv", migration, time.Hour))
	server.FastForward(30 * time.Minute)
	got, err = cache.GetClaudeSessionMigration(ctx, 5, "conv", time.Hour)
	require.NoError(t, err)
	require.Equal(t, migration, *got)
	require.Equal(t, time.Hour, server.TTL(key), "reading the watermark extends it together with the sticky binding")

	require.NoError(t, cache.DeleteClaudeSessionMigration(ctx, 5, "conv"))
	got, err = cache.GetClaudeSessionMigration(ctx, 5, "conv", time.Hour)
	require.NoError(t, err)
	require.Nil(t, got)
}

// ReplaceFingerprint 能替掉旧 ClientID（轮换后的新身份），但不让旧代次覆盖新代次；
// OverwriteFingerprint 无条件覆盖；DeleteAccountSessions 清掉伪装、环境与最近活跃三个会话键。
func TestIdentityCacheReplaceAndDeleteSessions(t *testing.T) {
	server, cache := newIdentityCacheForTest(t)
	ctx := t.Context()

	_, err := cache.CreateFingerprint(ctx, 6, &service.Fingerprint{ClientID: "old"})
	require.NoError(t, err)
	stored, err := cache.SetFingerprint(ctx, 6, &service.Fingerprint{ClientID: "new"})
	require.NoError(t, err)
	require.Equal(t, "old", stored.ClientID, "precondition: SetFingerprint refuses a different identity")

	stored, err = cache.ReplaceFingerprint(ctx, 6, &service.Fingerprint{ClientID: "new", IdentityEpoch: 1, Persisted: true})
	require.NoError(t, err)
	require.Equal(t, "new", stored.ClientID)
	got, err := cache.GetFingerprint(ctx, 6)
	require.NoError(t, err)
	require.Equal(t, "new", got.ClientID)
	require.Equal(t, int64(1), got.IdentityEpoch)
	require.True(t, got.Persisted)
	require.Equal(t, fingerprintTTL, server.TTL(fingerprintKey(6)))

	// 慢请求带着轮换前的代次回写：不覆盖，返回存储中的新身份。
	stored, err = cache.ReplaceFingerprint(ctx, 6, &service.Fingerprint{ClientID: "old", UserAgent: "late", Persisted: true})
	require.NoError(t, err)
	require.Equal(t, "new", stored.ClientID)
	require.Equal(t, int64(1), stored.IdentityEpoch)
	// 同代次写入（头升级）照常覆盖。
	stored, err = cache.ReplaceFingerprint(ctx, 6, &service.Fingerprint{ClientID: "new", IdentityEpoch: 1, UserAgent: "upgraded", Persisted: true})
	require.NoError(t, err)
	require.Equal(t, "upgraded", stored.UserAgent)

	require.NoError(t, cache.OverwriteFingerprint(ctx, 6, &service.Fingerprint{ClientID: "restored", Persisted: true}))
	got, err = cache.GetFingerprint(ctx, 6)
	require.NoError(t, err)
	require.Equal(t, "restored", got.ClientID, "overwrite ignores the epoch guard")
	require.Zero(t, got.IdentityEpoch)
	require.Equal(t, fingerprintTTL, server.TTL(fingerprintKey(6)))

	// 损坏记录视同缺失而被替换。
	require.NoError(t, server.Set(fingerprintKey(16), `{"IdentityEpoch":9}`))
	stored, err = cache.ReplaceFingerprint(ctx, 16, &service.Fingerprint{ClientID: "fresh"})
	require.NoError(t, err)
	require.Equal(t, "fresh", stored.ClientID)
	require.NoError(t, server.Set(fingerprintKey(17), "{not json"))
	stored, err = cache.ReplaceFingerprint(ctx, 17, &service.Fingerprint{ClientID: "fresh"})
	require.NoError(t, err)
	require.Equal(t, "fresh", stored.ClientID)

	_, err = cache.GetOrCreateMaskedSessionID(ctx, 6, "masked")
	require.NoError(t, err)
	_, err = cache.GetOrCreateAmbientSessionID(ctx, 6, "ambient")
	require.NoError(t, err)
	require.NoError(t, cache.SetLastActiveSessionID(ctx, 6, "last"))
	require.NoError(t, cache.DeleteAccountSessions(ctx, 6))
	for _, key := range []string{maskedSessionKey(6), ambientSessionKey(6), lastActiveSessionKey(6)} {
		require.False(t, server.Exists(key), key)
	}
	require.True(t, server.Exists(fingerprintKey(6)), "the fingerprint itself is kept")
}
