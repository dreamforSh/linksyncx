package repository

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	clashConfigChangedChannel = "clash_pool:config_changed"
	clashInstanceKeyPrefix    = "clash_pool:instance:"
	clashTrafficLiveKeyPrefix = "clash_pool:traffic_live:"
)

type clashRuntimeNotifier struct {
	rdb *redis.Client
}

// NewClashRuntimeNotifier returns the Redis-backed Clash pool notifier.
func NewClashRuntimeNotifier(rdb *redis.Client) service.ClashRuntimeNotifier {
	return &clashRuntimeNotifier{rdb: rdb}
}

func (n *clashRuntimeNotifier) NotifyConfigChanged(ctx context.Context) error {
	if n == nil || n.rdb == nil {
		return nil
	}
	return n.rdb.Publish(ctx, clashConfigChangedChannel, time.Now().UnixNano()).Err()
}

func (n *clashRuntimeNotifier) SubscribeConfigChanged(ctx context.Context, handler func()) error {
	if n == nil || n.rdb == nil {
		return nil
	}
	pubsub := n.rdb.Subscribe(ctx, clashConfigChangedChannel)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return err
	}
	go func() {
		defer func() {
			if err := pubsub.Close(); err != nil {
				slog.Warn("failed to close clash pool subscriber", "error", err)
			}
		}()
		messages := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				if message != nil {
					handler()
				}
			}
		}
	}()
	return nil
}

func (n *clashRuntimeNotifier) PublishInstanceStatus(ctx context.Context, status service.ClashInstanceStatus, ttl time.Duration) error {
	if n == nil || n.rdb == nil || status.InstanceID == "" {
		return nil
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return n.rdb.Set(ctx, clashInstanceKeyPrefix+status.InstanceID, raw, ttl).Err()
}

func (n *clashRuntimeNotifier) ListInstanceStatuses(ctx context.Context) ([]service.ClashInstanceStatus, error) {
	if n == nil || n.rdb == nil {
		return nil, nil
	}
	values, err := n.scanValues(ctx, clashInstanceKeyPrefix)
	if err != nil || len(values) == 0 {
		return nil, err
	}
	out := make([]service.ClashInstanceStatus, 0, len(values))
	for _, text := range values {
		var status service.ClashInstanceStatus
		if json.Unmarshal([]byte(text), &status) == nil {
			out = append(out, status)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

func (n *clashRuntimeNotifier) PublishTrafficLive(ctx context.Context, live service.ClashTrafficLive, ttl time.Duration) error {
	if n == nil || n.rdb == nil || live.InstanceID == "" {
		return nil
	}
	raw, err := json.Marshal(live)
	if err != nil {
		return err
	}
	return n.rdb.Set(ctx, clashTrafficLiveKeyPrefix+live.InstanceID, raw, ttl).Err()
}

func (n *clashRuntimeNotifier) ListTrafficLive(ctx context.Context) ([]service.ClashTrafficLive, error) {
	if n == nil || n.rdb == nil {
		return nil, nil
	}
	values, err := n.scanValues(ctx, clashTrafficLiveKeyPrefix)
	if err != nil || len(values) == 0 {
		return nil, err
	}
	out := make([]service.ClashTrafficLive, 0, len(values))
	for _, text := range values {
		var live service.ClashTrafficLive
		if json.Unmarshal([]byte(text), &live) == nil {
			out = append(out, live)
		}
	}
	return out, nil
}

// scanValues returns the string values of every key under prefix; keys that
// expire between SCAN and MGET are skipped.
func (n *clashRuntimeNotifier) scanValues(ctx context.Context, prefix string) ([]string, error) {
	var (
		cursor uint64
		keys   []string
	)
	for {
		batch, next, err := n.rdb.Scan(ctx, cursor, prefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	values, err := n.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if text, ok := value.(string); ok {
			out = append(out, text)
		}
	}
	return out, nil
}
