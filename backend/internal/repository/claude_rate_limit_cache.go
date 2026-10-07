package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	// claudeQuotaKeyPrefix 是 Claude 限流快照的热存储键（L4 配额层）：claude:quota:{account_id}。
	claudeQuotaKeyPrefix = "claude:quota:"
	// claudeQuotaTTL 覆盖最长的 7 天窗口并留余量；账号持续有流量时每次写入都会续期。
	claudeQuotaTTL = 8 * 24 * time.Hour
)

func claudeQuotaKey(accountID int64) string {
	return fmt.Sprintf("%s%d", claudeQuotaKeyPrefix, accountID)
}

func claudeQuotaPersistKey(accountID int64) string {
	return fmt.Sprintf("%s%d:persist", claudeQuotaKeyPrefix, accountID)
}

// applyClaudeQuotaScript 只接受比已存快照更晚收到的响应头（applied_at_ms 更大），与真实 CLI 宽限区
// 跟踪器的 fenceMs 同理。返回 {1 = 已写入 / 0 = 已丢弃, 写入前的快照（可能没有）}；已存记录损坏时视同缺失。
var applyClaudeQuotaScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if current then
    local ok, decoded = pcall(cjson.decode, current)
    if ok and type(decoded) == 'table' and (tonumber(decoded.applied_at_ms) or 0) >= tonumber(ARGV[2]) then
        return {0, current}
    end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
if current then
    return {1, current}
end
return {1}
`)

type claudeRateLimitCache struct {
	rdb *redis.Client
}

// NewClaudeRateLimitCache 创建 Claude 限流快照热存储。
func NewClaudeRateLimitCache(rdb *redis.Client) service.ClaudeRateLimitCache {
	return &claudeRateLimitCache{rdb: rdb}
}

func (c *claudeRateLimitCache) ApplyClaudeRateLimitSnapshot(ctx context.Context, accountID int64, snap *service.ClaudeRateLimitSnapshot) (bool, *service.ClaudeRateLimitSnapshot, error) {
	if snap == nil {
		return false, nil, nil
	}
	payload, err := json.Marshal(snap)
	if err != nil {
		return false, nil, err
	}
	raw, err := applyClaudeQuotaScript.Run(ctx, c.rdb, []string{claudeQuotaKey(accountID)}, payload, snap.AppliedAtMs, claudeQuotaTTL.Milliseconds()).Slice()
	if err != nil {
		return false, nil, err
	}
	if len(raw) == 0 {
		return false, nil, fmt.Errorf("claude rate limit apply: empty script result")
	}
	applied, _ := raw[0].(int64)
	var previous *service.ClaudeRateLimitSnapshot
	if len(raw) > 1 {
		if stored, ok := raw[1].(string); ok {
			var decoded service.ClaudeRateLimitSnapshot
			if json.Unmarshal([]byte(stored), &decoded) == nil {
				previous = &decoded
			}
		}
	}
	return applied == 1, previous, nil
}

func (c *claudeRateLimitCache) ClaimClaudeRateLimitPersist(ctx context.Context, accountID int64, interval time.Duration, force bool) (bool, error) {
	key := claudeQuotaPersistKey(accountID)
	if force {
		if err := c.rdb.Set(ctx, key, 1, interval).Err(); err != nil {
			return false, err
		}
		return true, nil
	}
	return c.rdb.SetNX(ctx, key, 1, interval).Result()
}
