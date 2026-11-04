//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// The OpenAI gateway type-asserts its GatewayCache to this method set; if the
// Redis cache stops satisfying it, stored responses silently become per-process.
type openAIResponsesStateBlobStore interface {
	SetOpenAIResponsesState(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	GetOpenAIResponsesStates(ctx context.Context, keys []string) ([][]byte, error)
	DeleteOpenAIResponsesState(ctx context.Context, key string) error
}

func TestGatewayCacheOpenAIResponsesState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store, ok := NewGatewayCache(rdb).(openAIResponsesStateBlobStore)
	require.True(t, ok)
	ctx := context.Background()

	require.NoError(t, store.SetOpenAIResponsesState(ctx, "1:resp_a", []byte(`{"v":1}`), time.Minute))
	require.NoError(t, store.SetOpenAIResponsesState(ctx, "1:resp_c", []byte(`{"v":3}`), time.Minute))
	got, err := store.GetOpenAIResponsesStates(ctx, []string{"1:resp_a", "1:resp_b", "1:resp_c"})
	require.NoError(t, err)
	require.Equal(t, `{"v":1}`, string(got[0]))
	require.Nil(t, got[1])
	require.Equal(t, `{"v":3}`, string(got[2]))
	require.Equal(t, time.Minute, mr.TTL(openAIResponsesStatePrefix+"1:resp_a"))

	require.NoError(t, store.DeleteOpenAIResponsesState(ctx, "1:resp_a"))
	got, err = store.GetOpenAIResponsesStates(ctx, []string{"1:resp_a"})
	require.NoError(t, err)
	require.Nil(t, got[0])

	got, err = store.GetOpenAIResponsesStates(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}
