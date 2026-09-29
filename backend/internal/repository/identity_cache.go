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
	fingerprintKeyPrefix   = "fingerprint:"
	fingerprintTTL         = 7 * 24 * time.Hour // 7天，配合每24小时懒续期可保持活跃账号永不过期
	maskedSessionKeyPrefix = "masked_session:"
	maskedSessionTTL       = 15 * time.Minute
)

// fingerprintKey generates the Redis key for account fingerprint cache.
func fingerprintKey(accountID int64) string {
	return fmt.Sprintf("%s%d", fingerprintKeyPrefix, accountID)
}

// maskedSessionKey generates the Redis key for masked session ID cache.
func maskedSessionKey(accountID int64) string {
	return fmt.Sprintf("%s%d", maskedSessionKeyPrefix, accountID)
}

type identityCache struct {
	rdb *redis.Client
}

func NewIdentityCache(rdb *redis.Client) service.IdentityCache {
	return &identityCache{rdb: rdb}
}

func (c *identityCache) GetFingerprint(ctx context.Context, accountID int64) (*service.Fingerprint, error) {
	key := fingerprintKey(accountID)
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeFingerprint(val)
}

func decodeFingerprint(val string) (*service.Fingerprint, error) {
	var fp service.Fingerprint
	if err := json.Unmarshal([]byte(val), &fp); err != nil {
		return nil, err
	}
	if fp.ClientID == "" {
		return nil, fmt.Errorf("stored account identity has no client ID")
	}
	return &fp, nil
}

// storedClientIDLua 解析存储中的指纹记录并取出 ClientID；记录缺失、损坏或没有 ClientID 时为 nil。
// cjson.decode 用 pcall 包住：真实 Redis 遇到非法 JSON 会抛错，miniredis 则返回 nil，两种都按损坏处理。
const storedClientIDLua = `
local function storedClientID(current)
    if not current then return nil end
    local ok, identity = pcall(cjson.decode, current)
    if not ok or type(identity) ~= 'table' then return nil end
    if type(identity.ClientID) ~= 'string' or identity.ClientID == '' then return nil end
    return identity.ClientID
end
`

// refreshFingerprintScript 续期/更新指纹：存储中仍是同一 ClientID（或记录缺失/损坏）时写入 ARGV[1]，
// 否则说明其它实例已写入新身份，不覆盖并返回存储中的记录。
var refreshFingerprintScript = redis.NewScript(storedClientIDLua + `
local current = redis.call('GET', KEYS[1])
local clientID = storedClientID(current)
if clientID and clientID ~= ARGV[2] then return current end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return ARGV[1]
`)

// createFingerprintScript 首次创建指纹：存储中已有合法记录时原样返回（先写者胜出），
// 否则写入 ARGV[1]；损坏记录（非法 JSON / 无 ClientID）视同缺失而被替换。
var createFingerprintScript = redis.NewScript(storedClientIDLua + `
local current = redis.call('GET', KEYS[1])
if storedClientID(current) then return current end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return ARGV[1]
`)

func (c *identityCache) SetFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	if fp == nil || fp.ClientID == "" {
		return nil, fmt.Errorf("account identity requires a client ID")
	}
	val, err := json.Marshal(fp)
	if err != nil {
		return nil, err
	}
	stored, err := refreshFingerprintScript.Run(ctx, c.rdb, []string{fingerprintKey(accountID)}, val, fp.ClientID, fingerprintTTL.Milliseconds()).Text()
	if err != nil {
		return nil, err
	}
	return decodeFingerprint(stored)
}

func (c *identityCache) CreateFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	if fp == nil || fp.ClientID == "" {
		return nil, fmt.Errorf("account identity requires a client ID")
	}
	val, err := json.Marshal(fp)
	if err != nil {
		return nil, err
	}
	stored, err := createFingerprintScript.Run(ctx, c.rdb, []string{fingerprintKey(accountID)}, val, fingerprintTTL.Milliseconds()).Text()
	if err != nil {
		return nil, err
	}
	return decodeFingerprint(stored)
}

var maskedSessionScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current or current == '' then current = ARGV[1] end
redis.call('SET', KEYS[1], current, 'PX', ARGV[2])
return current
`)

func (c *identityCache) GetOrCreateMaskedSessionID(ctx context.Context, accountID int64, candidate string) (string, error) {
	if candidate == "" {
		return "", fmt.Errorf("session candidate must not be empty")
	}
	return maskedSessionScript.Run(ctx, c.rdb, []string{maskedSessionKey(accountID)}, candidate, maskedSessionTTL.Milliseconds()).Text()
}
