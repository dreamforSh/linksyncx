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

	// 账号级会话键（新键按 {platform}:{layer}:{account}:{sub} 命名）。
	// ambient：账号空闲时的环境会话；last：最近活跃的上游会话。
	accountSessionKeyPrefix = "claude:session:"
	accountSessionTTL       = 15 * time.Minute
)

func ambientSessionKey(accountID int64) string {
	return fmt.Sprintf("%s%d:ambient", accountSessionKeyPrefix, accountID)
}

func lastActiveSessionKey(accountID int64) string {
	return fmt.Sprintf("%s%d:last", accountSessionKeyPrefix, accountID)
}

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

// replaceFingerprintScript 写入数据库确认过的身份：存储中的合法记录 IdentityEpoch 高于 ARGV[2] 时
// （并发轮换已先写入新身份）不覆盖并返回存储值；记录缺失、损坏或代次不高于本次写入时写入 ARGV[1]。
var replaceFingerprintScript = redis.NewScript(storedClientIDLua + `
local current = redis.call('GET', KEYS[1])
if storedClientID(current) then
    local storedEpoch = tonumber(cjson.decode(current).IdentityEpoch) or 0
    if storedEpoch > tonumber(ARGV[2]) then return current end
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[3])
return ARGV[1]
`)

// ReplaceFingerprint 写入数据库确认过的身份（含轮换后的新身份），返回写入后存储中的身份。
// 不能像 SetFingerprint 那样因存储中是旧 ClientID 而拒写，但不让旧代次覆盖新代次。
func (c *identityCache) ReplaceFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) (*service.Fingerprint, error) {
	if fp == nil || fp.ClientID == "" {
		return nil, fmt.Errorf("account identity requires a client ID")
	}
	val, err := json.Marshal(fp)
	if err != nil {
		return nil, err
	}
	stored, err := replaceFingerprintScript.Run(ctx, c.rdb, []string{fingerprintKey(accountID)}, val, fp.IdentityEpoch, fingerprintTTL.Milliseconds()).Text()
	if err != nil {
		return nil, err
	}
	return decodeFingerprint(stored)
}

// OverwriteFingerprint 无条件覆盖指纹，只用于缓存与数据库不一致时以库为准纠正缓存。
func (c *identityCache) OverwriteFingerprint(ctx context.Context, accountID int64, fp *service.Fingerprint) error {
	if fp == nil || fp.ClientID == "" {
		return fmt.Errorf("account identity requires a client ID")
	}
	val, err := json.Marshal(fp)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, fingerprintKey(accountID), val, fingerprintTTL).Err()
}

// DeleteAccountSessions 删除账号级会话键：伪装会话、环境会话与最近活跃会话。
func (c *identityCache) DeleteAccountSessions(ctx context.Context, accountID int64) error {
	return c.rdb.Del(ctx, maskedSessionKey(accountID), ambientSessionKey(accountID), lastActiveSessionKey(accountID)).Err()
}

// claudeSessionMigrationKey 是对话换号到该账号时的迁移水位线：claude:session:{account}:{K}:migrated。
func claudeSessionMigrationKey(accountID int64, sessionKey string) string {
	return fmt.Sprintf("%s%d:%s:migrated", accountSessionKeyPrefix, accountID, sessionKey)
}

func (c *identityCache) GetClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string, ttl time.Duration) (*service.ClaudeSessionMigration, error) {
	raw, err := c.rdb.GetEx(ctx, claudeSessionMigrationKey(accountID, sessionKey), ttl).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var migration service.ClaudeSessionMigration
	if err := json.Unmarshal([]byte(raw), &migration); err != nil {
		return nil, err
	}
	return &migration, nil
}

func (c *identityCache) SetClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string, migration service.ClaudeSessionMigration, ttl time.Duration) error {
	payload, err := json.Marshal(migration)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, claudeSessionMigrationKey(accountID, sessionKey), payload, ttl).Err()
}

func (c *identityCache) DeleteClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string) error {
	return c.rdb.Del(ctx, claudeSessionMigrationKey(accountID, sessionKey)).Err()
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

// GetOrCreateAmbientSessionID 与伪装会话同一语义（原子 get-or-create + 滑动 TTL），
// 键不同：环境会话只服务账号空闲时没有可映射会话的请求。
func (c *identityCache) GetOrCreateAmbientSessionID(ctx context.Context, accountID int64, candidate string) (string, error) {
	if candidate == "" {
		return "", fmt.Errorf("session candidate must not be empty")
	}
	return maskedSessionScript.Run(ctx, c.rdb, []string{ambientSessionKey(accountID)}, candidate, accountSessionTTL.Milliseconds()).Text()
}

func (c *identityCache) SetLastActiveSessionID(ctx context.Context, accountID int64, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id must not be empty")
	}
	return c.rdb.Set(ctx, lastActiveSessionKey(accountID), sessionID, accountSessionTTL).Err()
}

func (c *identityCache) GetLastActiveSessionID(ctx context.Context, accountID int64) (string, error) {
	val, err := c.rdb.Get(ctx, lastActiveSessionKey(accountID)).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}
