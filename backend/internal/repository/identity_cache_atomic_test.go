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

func TestCRSIdentityConcurrentCreationUsesOnePersistentWinner(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })
	cache := NewIdentityCache(redisClient)
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
	reloaded, err := service.NewIdentityService(NewIdentityCache(redisClient)).GetOrCreateFingerprint(t.Context(), 42, http.Header{})
	require.NoError(t, err)
	require.Equal(t, winner, reloaded.ClientID)
}

func TestCRSIdentityCorruptionIsNotReplaced(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, server.Set(fingerprintKey(7), "invalid-json"))
	fp, err := service.NewIdentityService(NewIdentityCache(client)).GetOrCreateFingerprint(t.Context(), 7, http.Header{})
	require.Error(t, err)
	require.Nil(t, fp)
	got, err := server.Get(fingerprintKey(7))
	require.NoError(t, err)
	require.Equal(t, "invalid-json", got)
}

func TestCRSIdentityCreateKeepsExistingWinner(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewIdentityCache(client)
	for i := 0; i < 3; i++ {
		fp, err := cache.CreateFingerprint(t.Context(), 9, &service.Fingerprint{ClientID: fmt.Sprint(i)})
		require.NoError(t, err)
		require.Equal(t, "0", fp.ClientID)
	}
}

func TestCRSIdentityEmptyJSONIsCorruption(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for _, payload := range []string{"null", "{}"} {
		require.NoError(t, server.Set(fingerprintKey(11), payload))
		fp, err := service.NewIdentityService(NewIdentityCache(client)).GetOrCreateFingerprint(t.Context(), 11, http.Header{})
		require.ErrorContains(t, err, "no client ID")
		require.Nil(t, fp)
		got, err := server.Get(fingerprintKey(11))
		require.NoError(t, err)
		require.Equal(t, payload, got)
	}
}

type refreshRaceCache struct {
	service.IdentityCache
	beforeRefresh func()
}

func (c *refreshRaceCache) SetFingerprint(ctx context.Context, id int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	c.beforeRefresh()
	return c.IdentityCache.SetFingerprint(ctx, id, fp)
}

func TestCRSIdentityExpiredSnapshotCannotReplaceNewWinner(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewIdentityCache(client)
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

func TestCRSMaskedSessionConcurrentCreationAndRenewal(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewIdentityCache(client)
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
	server.FastForward(maskedSessionTTL - time.Second)
	same, err := cache.GetOrCreateMaskedSessionID(t.Context(), 15, "different")
	require.NoError(t, err)
	require.Equal(t, winner, same)
	server.FastForward(2 * time.Second)
	stored, err := server.Get(maskedSessionKey(15))
	require.NoError(t, err)
	require.Equal(t, winner, stored)
	server.FastForward(maskedSessionTTL)
	next, err := cache.GetOrCreateMaskedSessionID(t.Context(), 15, "new-window")
	require.NoError(t, err)
	require.Equal(t, "new-window", next)
}
