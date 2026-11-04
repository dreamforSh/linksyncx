package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const openAIResponsesStatePrefix = "openai_responses_state:"

// SetOpenAIResponsesState stores one emulated Responses turn record.
func (c *gatewayCache) SetOpenAIResponsesState(ctx context.Context, key string, payload []byte, ttl time.Duration) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" || len(payload) == 0 {
		return nil
	}
	return c.rdb.Set(ctx, openAIResponsesStatePrefix+key, payload, ttl).Err()
}

// GetOpenAIResponsesStates loads several turn records with one MGET. A missing
// key yields a nil entry at its position.
func (c *gatewayCache) GetOpenAIResponsesStates(ctx context.Context, keys []string) ([][]byte, error) {
	if c == nil || c.rdb == nil {
		return nil, errors.New("gateway cache unavailable")
	}
	if len(keys) == 0 {
		return nil, nil
	}
	redisKeys := make([]string, len(keys))
	for i, key := range keys {
		redisKeys[i] = openAIResponsesStatePrefix + strings.TrimSpace(key)
	}
	values, err := c.rdb.MGet(ctx, redisKeys...).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return make([][]byte, len(keys)), nil
		}
		return nil, err
	}
	out := make([][]byte, len(keys))
	for i, value := range values {
		if i >= len(out) {
			break
		}
		switch v := value.(type) {
		case string:
			out[i] = []byte(v)
		case []byte:
			out[i] = v
		}
	}
	return out, nil
}

// DeleteOpenAIResponsesState removes one turn record.
func (c *gatewayCache) DeleteOpenAIResponsesState(ctx context.Context, key string) error {
	if c == nil || c.rdb == nil {
		return errors.New("gateway cache unavailable")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	return c.rdb.Del(ctx, openAIResponsesStatePrefix+key).Err()
}
