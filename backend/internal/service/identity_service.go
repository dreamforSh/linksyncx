package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// 预编译正则表达式（避免每次调用重新编译）
var (
	// 匹配 User-Agent 版本号: xxx/x.y.z
	userAgentVersionRegex = regexp.MustCompile(`/(\d+)\.(\d+)\.(\d+)`)

	// fingerprintUserAgentPattern 校验可写入账号级持久身份的 User-Agent 形态：
	// <product>/<major>.<minor>.<patch> 之后必须紧跟空白或字符串结束。
	// 版本号带 -local / -dev / +build 等后缀的本地构建一律不接受。
	fingerprintUserAgentPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/\d+\.\d+\.\d+(\s|$)`)

	// claudeCLIUAVersionPrefixRegex 匹配 claude-cli UA 开头的 "claude-cli/x.y.z" 版本号段，
	// 供版本下限抬升时就地替换版本号使用；UA 其余部分（括号内的真实客户端形态等）原样保留。
	claudeCLIUAVersionPrefixRegex = regexp.MustCompile(`(?i)^(claude-cli)/\d+\.\d+\.\d+`)
)

const (
	// claudeCLIUserAgentProduct 是官方 Claude Code CLI 的产品名（小写）。
	claudeCLIUserAgentProduct = "claude-cli"
	// maxFingerprintUserAgentLength 限制写入缓存的 User-Agent 长度。
	maxFingerprintUserAgentLength = 256
	// maxClaudeCLIMajorVersionSkew 是 claude-cli 主版本号相对 sub2api 自身伪装
	// 版本（claude.CLIVersion()）允许的最大超前量。给足两个大版本的升级
	// 窗口，同时挡掉 999 这类哨兵版本号。
	maxClaudeCLIMajorVersionSkew = 2
)

// isAcceptableFingerprintUserAgent 判断 User-Agent 是否可作为账号级持久身份写入缓存。
//
// 指纹是账号级、“只升不降”、活跃账号懒续期后近乎永不过期的持久状态，且系统内
// 没有重置入口。一旦写入畸形或哨兵版本（如 claude-cli/999.0.0-local），该账号
// 此后所有上游请求都会在 HTTP 头与请求体 cc_version 两处声称这个不存在的版本，
// 被上游判定为非正版客户端并持续返回不带限流重置头的 429；无重置头又会落到 5 秒
// 兜底冷却，账号池收缩后对外表现为 503 风暴。
//
// 校验必须放在创建与升级两条路径的共同入口：只在 isNewerVersion 处加校验是不够的，
// createFingerprintFromHeaders 首次创建时同样会原样保存畸形 UA，删键恢复后账号可被
// 同一客户端立即再次毒化。
func isAcceptableFingerprintUserAgent(ua string) bool {
	ua = strings.TrimSpace(ua)
	if ua == "" || len(ua) > maxFingerprintUserAgentLength {
		return false
	}
	if !fingerprintUserAgentPattern.MatchString(ua) {
		return false
	}
	// 非 claude-cli 产品不做版本区间约束：形态合法即可，避免误伤其他合法客户端。
	if extractProduct(ua) != claudeCLIUserAgentProduct {
		return true
	}
	major, _, _, ok := parseUserAgentVersion(ua)
	if !ok {
		return false
	}
	// 超前检查以运行期生效版本为基准（EffectiveCLIVersion）：同步跨大版本后，
	// 若仍与静态基线比较会误拒客户端上报的新版本。
	currentMajor, _, _, currentOK := parseUserAgentVersion(claudeCLIUserAgentProduct + "/" + claude.EffectiveCLIVersion())
	if !currentOK {
		return true
	}
	return major <= currentMajor+maxClaudeCLIMajorVersionSkew
}

// floorClaudeCLIUserAgentVersion 把 claude-cli UA 中的版本号抬升到运行期生效版本
// （claude.EffectiveCLIVersion()）下限：低于下限时就地升到下限并返回 changed=true，否则原样返回。
//
// 为什么需要这条：账号级指纹"只升不降"、活跃账号懒续期后近乎永不过期，且系统内没有重置
// 入口，defaultFingerprint 只在首次创建指纹时使用。存量账号缓存里的 claude-cli 版本停留在
// 历史值（如 claude-cli/2.1.220），客户端送来更旧的版本时 isNewerVersion 不会触发升级——
// 仅升 CLICurrentVersion 常量对所有已有账号完全无效，上游按指纹 UA 做客户端版本闸门
// （如 Fable 5.1 要求 >= 2.1.251）时旧指纹永远过不去。
//
// 约束：
//   - 只对 claude-cli 产品生效，其它产品一律不动，避免误伤别的合法客户端；
//   - 只升不降：版本等于或高于 CLICurrentVersion（含客户端上报的更新版本）时不做任何改动；
//   - 只替换 claude-cli/ 后的版本号段，UA 其余部分（如 "(external, claude-desktop-3p,
//     agent-sdk/0.3.100)"）原样保留，不重建整个字符串、不退化为 defaultFingerprint.UserAgent；
//   - X-Stainless-* 字段不在此处理，维持调用方的既有 merge 语义。
func floorClaudeCLIUserAgentVersion(ua string) (string, bool) {
	if extractProduct(ua) != claudeCLIUserAgentProduct {
		return ua, false
	}
	// 用 EffectiveCLIVersion()（面板/同步的运行期值，经 IsSupportedCLIVersion
	// 校验、恒 >= 内置基线）而非静态基线 CLICurrentVersion：
	// 运行期同步到更高版本后，存量低版本指纹才能被抬到新版本；"只升不降"语义不变。
	floor := claude.EffectiveCLIVersion()
	floorUA := claudeCLIUserAgentProduct + "/" + floor
	// isNewerVersion(floor, ua) 为 true 当且仅当下限版本严格高于 ua：
	// ua 等于或高于下限、产品名不一致、或版本无法解析时都不做改动。
	if !isNewerVersion(floorUA, ua) {
		return ua, false
	}
	floored := claudeCLIUAVersionPrefixRegex.ReplaceAllString(ua, "${1}/"+floor)
	if floored == ua {
		return ua, false
	}
	return floored, true
}

// defaultFingerprint 返回默认指纹值（当客户端未提供时使用）。
// UserAgent 不能在包 init 时固化：必须在每次使用时经 EffectiveCLIVersion 现取，
// 否则运行期同步到新版本后，新建账号会写入过期版本作为持久身份。
func defaultFingerprint() Fingerprint {
	return Fingerprint{
		UserAgent:               claude.DefaultUserAgent(),
		StainlessLang:           "js",
		StainlessPackageVersion: claude.SDKTSVersion,
		StainlessOS:             "Linux",
		StainlessArch:           "arm64",
		StainlessRuntime:        "node",
		StainlessRuntimeVersion: claude.SDKTSRuntimeVersion,
	}
}

// Fingerprint represents account fingerprint data
type Fingerprint struct {
	ClientID                string
	UserAgent               string
	StainlessLang           string
	StainlessPackageVersion string
	StainlessOS             string
	StainlessArch           string
	StainlessRuntime        string
	StainlessRuntimeVersion string
	UpdatedAt               int64 `json:",omitempty"` // Unix timestamp，用于判断是否需要续期TTL

	// IdentityEpoch 是身份代次：只在管理员重置客户端身份或账号换主人时递增，参与上游会话映射。
	IdentityEpoch int64 `json:",omitempty"`
	// OwnerRef 是身份所属的上游账号主人（extra.account_uuid）；空表示未知。
	OwnerRef string `json:",omitempty"`
	// Persisted 表示该记录已以数据库为准（缓存由库写入）；为 false 的存量 Redis 记录会被收编入库。
	Persisted bool `json:",omitempty"`
}

// IdentityCache defines cache operations for identity service
type IdentityCache interface {
	// GetFingerprint 读取账号指纹：记录不存在返回 (nil, nil)；存储故障或记录损坏返回 error。
	GetFingerprint(ctx context.Context, accountID int64) (*Fingerprint, error)
	// SetFingerprint 续期/更新指纹（原子）：只在存储中仍是同一 ClientID（或记录缺失/损坏）时写入；
	// 若期间其它实例已写入不同身份，则不覆盖并返回存储中的胜者。返回值为写入后的当前记录。
	SetFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) (*Fingerprint, error)
	// CreateFingerprint 首次创建指纹（原子）：存储中已有合法记录时返回该记录（先写者胜出），
	// 否则写入 fp（损坏记录会被替换）。多实例并发首建时保证所有实例拿到同一身份。
	CreateFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) (*Fingerprint, error)
	// GetOrCreateMaskedSessionID 获取固定的伪装会话ID（原子 get-or-create）：已存在则返回现有值，
	// 否则写入 candidate；两种情况都会刷新 15 分钟 TTL。返回值为 UUID 格式字符串。
	GetOrCreateMaskedSessionID(ctx context.Context, accountID int64, candidate string) (string, error)
	// GetOrCreateAmbientSessionID 获取账号空闲时使用的环境会话 ID（原子 get-or-create，
	// 刷新 15 分钟 TTL），供没有可映射会话、账号上也没有活跃会话的请求使用。
	GetOrCreateAmbientSessionID(ctx context.Context, accountID int64, candidate string) (string, error)
	// SetLastActiveSessionID 记录账号最近一次活跃的上游会话 ID（15 分钟 TTL）。
	SetLastActiveSessionID(ctx context.Context, accountID int64, sessionID string) error
	// GetLastActiveSessionID 读取账号最近活跃的上游会话 ID；不存在返回 ("", nil)。
	GetLastActiveSessionID(ctx context.Context, accountID int64) (string, error)
	// ReplaceFingerprint 写入数据库确认过的身份（含轮换后的新身份），返回写入后存储中的身份。
	// 不因存储中是另一个 ClientID 而拒写，但存储中已是更高 IdentityEpoch 的身份时不覆盖并返回存储值，
	// 防止慢请求把轮换前的旧身份写回缓存。
	ReplaceFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) (*Fingerprint, error)
	// OverwriteFingerprint 无条件写入指纹，只用于缓存与数据库不一致（如数据库回滚到更低代次）时以库为准纠正缓存。
	OverwriteFingerprint(ctx context.Context, accountID int64, fp *Fingerprint) error
	// DeleteAccountSessions 删除账号级会话键（伪装会话、环境会话、最近活跃会话），身份轮换后调用。
	DeleteAccountSessions(ctx context.Context, accountID int64) error
	// GetClaudeSessionMigration 读取对话换号到该账号时记录的迁移水位线并续期 ttl；不存在返回 (nil, nil)。
	GetClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string, ttl time.Duration) (*ClaudeSessionMigration, error)
	// SetClaudeSessionMigration 记录对话换号到该账号时的迁移水位线。
	SetClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string, migration ClaudeSessionMigration, ttl time.Duration) error
	// DeleteClaudeSessionMigration 删除迁移水位线（客户端改写了历史）。
	DeleteClaudeSessionMigration(ctx context.Context, accountID int64, sessionKey string) error
}

// ClaudeSessionMigration 是对话换号时的迁移水位线：此后在新账号上，messages[0:MessageCount) 来自旧账号，
// 其中的 thinking 签名由旧账号签发。
type ClaudeSessionMigration struct {
	FromAccountID int64 `json:"from_account_id"`
	MessageCount  int   `json:"message_count"`
	At            int64 `json:"at"`
}

// ErrClientIdentityUnavailable 表示账号的客户端身份（device_id 等）既读不到也无法持久化创建。
// 此时绝不能带临时随机身份出站：同一账号的设备 ID 抖动是最容易被关联的信号。
// 调用方应把它转成可换号的错误（见 claudeIdentityUnavailableFailover）。
var ErrClientIdentityUnavailable = errors.New("account client identity is unavailable")

// IdentityService 管理OAuth账号的请求身份指纹
type IdentityService struct {
	cache IdentityCache
	// store 是身份的真相源（数据库）；为 nil 时退回纯 Redis 语义（测试与旧装配）。
	store ClientIdentityStore

	// adoptRetryAt 记录收编存量 Redis 身份失败后的下次重试时间，数据库故障期间不逐请求打库。
	adoptMu      sync.Mutex
	adoptRetryAt map[int64]time.Time

	// lastActiveWrites 节流「最近活跃会话」的 Redis 写入：同一账号会话不变时
	// 每 lastActiveSessionRewriteInterval 才续期一次。
	lastActiveMu     sync.Mutex
	lastActiveWrites map[int64]lastActiveSessionWrite
}

type lastActiveSessionWrite struct {
	sessionID string
	at        time.Time
}

// lastActiveSessionRewriteInterval 是同一会话重复续期「最近活跃会话」键的最小间隔，
// 远小于该键的 15 分钟 TTL。
const lastActiveSessionRewriteInterval = time.Minute

// ambientSessionBucket 是 Redis 不可用时环境会话的确定性分桶长度，与环境会话 TTL 一致。
const ambientSessionBucket = 15 * time.Minute

// NewIdentityService 创建只用 Redis 的 IdentityService（测试与旧装配）。
func NewIdentityService(cache IdentityCache) *IdentityService {
	return &IdentityService{
		cache:            cache,
		lastActiveWrites: make(map[int64]lastActiveSessionWrite),
		adoptRetryAt:     make(map[int64]time.Time),
	}
}

// ProvideIdentityService 创建以数据库为身份真相源、Redis 为缓存的 IdentityService。
func ProvideIdentityService(cache IdentityCache, store ClientIdentityStore) *IdentityService {
	svc := NewIdentityService(cache)
	svc.store = store
	return svc
}

// GetOrCreateFingerprint 获取或创建账号的指纹（不知道账号主人时的入口）。
// 生产路径用 GetOrCreateAccountFingerprint：它带上账号主人，主人变化时轮换身份。
func (s *IdentityService) GetOrCreateFingerprint(ctx context.Context, accountID int64, headers http.Header) (*Fingerprint, error) {
	return s.getOrCreateFingerprint(ctx, accountID, "", headers)
}

func (s *IdentityService) getOrCreateFingerprint(ctx context.Context, accountID int64, owner string, headers http.Header) (*Fingerprint, error) {
	if s.store != nil {
		return s.getOrCreatePersistedFingerprint(ctx, accountID, owner, headers)
	}
	return s.getOrCreateCachedFingerprint(ctx, accountID, headers)
}

// getOrCreateCachedFingerprint 是未配置身份库时的纯 Redis 路径：
// 如果缓存存在，检测user-agent版本，新版本则更新；
// 如果缓存不存在，生成随机ClientID并从请求头创建指纹，然后缓存。
func (s *IdentityService) getOrCreateCachedFingerprint(ctx context.Context, accountID int64, headers http.Header) (*Fingerprint, error) {
	// 入口统一校验：创建与升级两条路径共用，任一路径漏掉都会让畸形 UA 被持久化。
	clientUA := strings.TrimSpace(headers.Get("User-Agent"))
	uaAcceptable := isAcceptableFingerprintUserAgent(clientUA)

	// 尝试从缓存获取指纹。读取失败（Redis 不可用 / 记录损坏）不阻塞请求：按缓存缺失处理，
	// 下方 CreateFingerprint 会原子地复用存储中仍合法的身份或替换损坏记录。
	cached, err := s.cache.GetFingerprint(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to read fingerprint for account %d: %v", accountID, err)
		cached = nil
	}
	if cached != nil {
		needWrite := upgradeFingerprintFromHeaders(accountID, cached, headers, clientUA, uaAcceptable)

		if !needWrite && time.Since(time.Unix(cached.UpdatedAt, 0)) > identityCacheRenewInterval {
			// 距上次写入超过24小时，续期TTL
			needWrite = true
		}

		if needWrite {
			cached.UpdatedAt = time.Now().Unix()
			// 只续期同一身份：若本地快照已过期、其它实例先写入了新身份，以存储中的胜者为准，
			// 避免过期快照把新身份覆盖回去。
			stored, err := s.cache.SetFingerprint(ctx, accountID, cached)
			if err != nil {
				logger.LegacyPrintf("service.identity", "Warning: failed to refresh fingerprint for account %d: %v", accountID, err)
			} else if stored != nil {
				if stored.ClientID != cached.ClientID {
					logger.LegacyPrintf("service.identity", "Fingerprint for account %d was replaced concurrently, using client_id: %s", accountID, stored.ClientID)
				}
				cached = stored
			}
		}
		return cached, nil
	}

	// 缓存不存在或解析失败，创建新指纹。首次创建同样是持久化写入，
	// 畸形 UA 在这里落库后就成了账号的长期身份，必须同样拒绝。
	if !uaAcceptable && clientUA != "" {
		logger.LegacyPrintf("service.identity",
			"Rejected fingerprint user-agent for account %d: %q (malformed or implausible version)",
			accountID, clientUA)
	}
	fp := s.createFingerprintFromHeaders(headers)

	// 生成随机ClientID
	fp.ClientID = generateClientID()
	fp.UpdatedAt = time.Now().Unix()

	// 保存到缓存（7天TTL，每24小时自动续期）。多实例并发首建时由存储裁定先写者，
	// 所有实例统一使用胜出的身份。写入失败时不能降级为未持久化的临时指纹：那样每个
	// 请求都会带一个新的随机 device_id 出站，账号身份随 Redis 故障抖动。改为返回
	// ErrClientIdentityUnavailable，由调用方换号。
	stored, err := s.cache.CreateFingerprint(ctx, accountID, fp)
	if err != nil || stored == nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to persist fingerprint for account %d, refusing to use an ephemeral identity: %v", accountID, err)
		if err == nil {
			err = errors.New("identity store returned no record")
		}
		return nil, fmt.Errorf("%w: account %d: %w", ErrClientIdentityUnavailable, accountID, err)
	}
	if stored.ClientID != fp.ClientID {
		logger.LegacyPrintf("service.identity", "Fingerprint for account %d was created concurrently, using client_id: %s", accountID, stored.ClientID)
		return stored, nil
	}

	logger.LegacyPrintf("service.identity", "Created new fingerprint for account %d with client_id: %s", accountID, fp.ClientID)
	return stored, nil
}

// upgradeFingerprintFromHeaders 对已有身份做就地修正：畸形 UA 自愈、客户端送来更新版本时的
// 合并升级、版本下限抬升。返回是否改动了 UA 或头。headers 为空时只做与请求无关的确定性
// 修正（自愈用默认 UA、版本下限），供写库失败时退回使用。
func upgradeFingerprintFromHeaders(accountID int64, fp *Fingerprint, headers http.Header, clientUA string, uaAcceptable bool) bool {
	changed := false

	// 只在真正阻止了一次写入时记录，便于定位污染源，同时避免被毒化客户端的
	// 高频重试刷屏（无重置头的 429 会落到 5 秒兜底冷却，重试相当密集）。
	if !uaAcceptable && clientUA != "" && isNewerVersion(clientUA, fp.UserAgent) {
		logger.LegacyPrintf("service.identity",
			"Rejected fingerprint user-agent for account %d: %q (malformed or implausible version)",
			accountID, clientUA)
	}

	if !isAcceptableFingerprintUserAgent(fp.UserAgent) {
		// 自愈：缓存中已是畸形/哨兵 UA（本次加固之前写入的）。指纹在活跃账号上
		// 懒续期后近乎永不过期——不在读取时纠正，存量被毒化的账号就只能手工恢复。
		poisoned := fp.UserAgent
		if uaAcceptable {
			mergeHeadersIntoFingerprint(fp, headers)
		} else {
			fp.UserAgent = defaultFingerprint().UserAgent
		}
		changed = true
		logger.LegacyPrintf("service.identity",
			"Replaced malformed cached fingerprint for account %d: %q -> %q",
			accountID, poisoned, fp.UserAgent)
	} else if uaAcceptable && isNewerVersion(clientUA, fp.UserAgent) {
		// 客户端送来更新版本时的常规升级：merge 语义 — 仅更新请求中实际携带的字段，
		// 保留缓存值，避免缺失的头被硬编码默认值覆盖（如新 CLI 版本 + 旧 SDK 默认值的不一致）
		mergeHeadersIntoFingerprint(fp, headers)
		changed = true
		logger.LegacyPrintf("service.identity", "Updated fingerprint for account %d: %s (merge update)", accountID, clientUA)
	}

	// 版本下限抬升（floor）：heal 与 healthy 两条路径共同的后置条件，与上面的客户端常规
	// 升级相互独立、二者取更新者。客户端送来更旧版本时 isNewerVersion 不触发，此处仍能把
	// 低于 CLICurrentVersion 的存量指纹就地抬到下限并持久化，否则升 CLICurrentVersion 对所有
	// 已有账号无效，新模型的客户端版本闸门（如 Fable 5.1 要求 >= 2.1.251）永远过不去。
	if flooredUA, ok := floorClaudeCLIUserAgentVersion(fp.UserAgent); ok {
		fp.UserAgent = flooredUA
		changed = true
		logger.LegacyPrintf("service.identity",
			"Floored cached fingerprint claude-cli version for account %d: %s", accountID, flooredUA)
	}
	return changed
}

// createFingerprintFromHeaders 从请求头创建指纹
func (s *IdentityService) createFingerprintFromHeaders(headers http.Header) *Fingerprint {
	fp := &Fingerprint{}

	// 获取User-Agent：只接受形态合法且版本合理的值，否则回退默认指纹。
	// 首次创建同样是持久化写入，必须与升级路径共用同一套校验。
	if ua := strings.TrimSpace(headers.Get("User-Agent")); isAcceptableFingerprintUserAgent(ua) {
		// 首次创建与缓存命中路径共用同一个版本下限：合法但过旧的 claude-cli UA
		// 落库时同样不能低于运行期生效版本（EffectiveCLIVersion），否则新账号一开始就带着过旧的持久身份。
		fp.UserAgent, _ = floorClaudeCLIUserAgentVersion(ua)
	} else {
		fp.UserAgent = defaultFingerprint().UserAgent
	}

	// 获取x-stainless-*头，如果没有则使用默认值
	df := defaultFingerprint()
	fp.StainlessLang = getHeaderOrDefault(headers, "X-Stainless-Lang", df.StainlessLang)
	fp.StainlessPackageVersion = getHeaderOrDefault(headers, "X-Stainless-Package-Version", df.StainlessPackageVersion)
	fp.StainlessOS = getHeaderOrDefault(headers, "X-Stainless-OS", df.StainlessOS)
	fp.StainlessArch = getHeaderOrDefault(headers, "X-Stainless-Arch", df.StainlessArch)
	fp.StainlessRuntime = getHeaderOrDefault(headers, "X-Stainless-Runtime", df.StainlessRuntime)
	fp.StainlessRuntimeVersion = getHeaderOrDefault(headers, "X-Stainless-Runtime-Version", df.StainlessRuntimeVersion)

	return fp
}

// mergeHeadersIntoFingerprint 将请求头中实际存在的字段合并到现有指纹中（用于版本升级场景）
// 关键语义：请求中有的字段 → 用新值覆盖；缺失的头 → 保留缓存中的已有值
// 与 createFingerprintFromHeaders 的区别：后者用于首次创建，缺失头回退到 defaultFingerprint；
// 本函数用于升级更新，缺失头保留缓存值，避免将已知的真实值退化为硬编码默认值
func mergeHeadersIntoFingerprint(fp *Fingerprint, headers http.Header) {
	// User-Agent：版本升级的触发条件，一定存在
	if ua := headers.Get("User-Agent"); ua != "" {
		fp.UserAgent = ua
	}
	// X-Stainless-* 头：仅在请求中实际携带时才更新，否则保留缓存值
	mergeHeader(headers, "X-Stainless-Lang", &fp.StainlessLang)
	mergeHeader(headers, "X-Stainless-Package-Version", &fp.StainlessPackageVersion)
	mergeHeader(headers, "X-Stainless-OS", &fp.StainlessOS)
	mergeHeader(headers, "X-Stainless-Arch", &fp.StainlessArch)
	mergeHeader(headers, "X-Stainless-Runtime", &fp.StainlessRuntime)
	mergeHeader(headers, "X-Stainless-Runtime-Version", &fp.StainlessRuntimeVersion)
}

// mergeHeader 如果请求头中存在该字段则更新目标值，否则保留原值
func mergeHeader(headers http.Header, key string, target *string) {
	if v := headers.Get(key); v != "" {
		*target = v
	}
}

// getHeaderOrDefault 获取header值，如果不存在则返回默认值
func getHeaderOrDefault(headers http.Header, key, defaultValue string) string {
	if v := headers.Get(key); v != "" {
		return v
	}
	return defaultValue
}

// ApplyFingerprint 将指纹应用到请求头（覆盖原有的x-stainless-*头）
// 使用 setHeaderRaw 保持原始大小写（如 X-Stainless-OS 而非 X-Stainless-Os）
func (s *IdentityService) ApplyFingerprint(req *http.Request, fp *Fingerprint) {
	if fp == nil {
		return
	}

	// 设置user-agent
	if fp.UserAgent != "" {
		setHeaderRaw(req.Header, "User-Agent", fp.UserAgent)
	}

	// 设置x-stainless-*头（保持与 claude.DefaultHeaders() 一致的大小写）
	if fp.StainlessLang != "" {
		setHeaderRaw(req.Header, "X-Stainless-Lang", fp.StainlessLang)
	}
	if fp.StainlessPackageVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Package-Version", fp.StainlessPackageVersion)
	}
	if fp.StainlessOS != "" {
		setHeaderRaw(req.Header, "X-Stainless-OS", fp.StainlessOS)
	}
	if fp.StainlessArch != "" {
		setHeaderRaw(req.Header, "X-Stainless-Arch", fp.StainlessArch)
	}
	if fp.StainlessRuntime != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime", fp.StainlessRuntime)
	}
	if fp.StainlessRuntimeVersion != "" {
		setHeaderRaw(req.Header, "X-Stainless-Runtime-Version", fp.StainlessRuntimeVersion)
	}
}

// RewriteUserID 重写body中的metadata.user_id
// 支持旧拼接格式和新 JSON 格式的 user_id 解析，
// 根据 fingerprintUA 版本选择输出格式。
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
//
// 会话映射按 identity_epoch = 0 计算（未轮换过身份的账号）；生产路径经
// RewriteUserIDWithMasking / rewriteUserIDWithMaskingView 带上指纹中的代次。
func (s *IdentityService) RewriteUserID(body []byte, accountID int64, accountUUID, cachedClientID, fingerprintUA string) ([]byte, error) {
	view := newJSONBodyView(body, nil)
	s.rewriteUserIDView(view, accountID, 0, accountUUID, cachedClientID, fingerprintUA)
	return view.data, nil
}

// rewriteUserIDView 是 RewriteUserID 作用于 jsonBodyView 的版本（原函数从不返回错误）。
func (s *IdentityService) rewriteUserIDView(view *jsonBodyView, accountID, identityEpoch int64, accountUUID, cachedClientID, fingerprintUA string) {
	if len(view.data) == 0 || accountUUID == "" || cachedClientID == "" {
		return
	}

	metadata := view.get("metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return
	}
	userID := userIDResult.String()
	if userID == "" {
		return
	}

	// 解析 user_id（兼容旧拼接格式和新 JSON 格式）
	parsed := ParseMetadataUserID(userID)
	if parsed == nil {
		return
	}

	newSessionHash := upstreamSessionIDFor(accountID, identityEpoch, parsed.SessionID)

	// 根据客户端版本选择输出格式。新格式下原位改写：保留客户端的键序与其它字段，
	// parent_session_id 用同一映射换成本账号作用域的值，父子会话关系不丢。
	version := ExtractCLIVersion(fingerprintUA)
	var newUserID string
	if parsed.IsNewFormat && IsNewMetadataFormatVersion(version) {
		parent := ""
		if parsed.ParentSessionID != "" {
			parent = upstreamSessionIDFor(accountID, identityEpoch, parsed.ParentSessionID)
		}
		if rewritten, ok := rewriteJSONMetadataUserID(userID, cachedClientID, accountUUID, newSessionHash, parent, false); ok {
			newUserID = rewritten
		}
	}
	if newUserID == "" {
		newUserID = FormatMetadataUserID(cachedClientID, accountUUID, newSessionHash, version)
	}
	if newUserID == userID {
		return
	}

	_ = view.setString("metadata.user_id", newUserID)
}

// upstreamSessionIDFor 把下游会话映射为账号作用域的上游会话 ID，取 UUID v4 形态。确定性映射：
// 同一对话在同一账号、同一身份代次下恒得同一会话 ID（等同真实 CLI --resume 沿用会话 ID），
// 换账号或身份轮换（epoch + 1）后自然变成新会话。
// 代次为 0 时沿用引入代次之前的种子 SHA256(accountID::clientSession)，上线时进行中的对话
// 会话 ID 不变；代次大于 0 时种子带上 e{epoch}（UUID 不含 "::"，不会与旧种子撞车）。
func upstreamSessionIDFor(accountID, identityEpoch int64, clientSessionID string) string {
	if identityEpoch <= 0 {
		return generateUUIDFromSeed(fmt.Sprintf("%d::%s", accountID, clientSessionID))
	}
	return generateUUIDFromSeed(fmt.Sprintf("%d::e%d::%s", accountID, identityEpoch, clientSessionID))
}

// RewriteUserIDWithMasking 重写body中的metadata.user_id，支持会话ID伪装
// 如果账号启用了会话ID伪装（session_id_masking_enabled），
// 则在完成常规重写后，将 session 部分替换为固定的伪装ID（15分钟内保持不变）
//
// 重要：此函数使用 json.RawMessage 保留其他字段的原始字节，
// 避免重新序列化导致 thinking 块等内容被修改。
func (s *IdentityService) RewriteUserIDWithMasking(ctx context.Context, body []byte, account *Account, accountUUID string, fp *Fingerprint) ([]byte, error) {
	view := newJSONBodyView(body, nil)
	s.rewriteUserIDWithMaskingView(ctx, view, account, accountUUID, fp)
	return view.data, nil
}

// rewriteUserIDWithMaskingView 是 RewriteUserIDWithMasking 作用于 jsonBodyView 的版本
// （原函数从不返回错误）。
func (s *IdentityService) rewriteUserIDWithMaskingView(ctx context.Context, view *jsonBodyView, account *Account, accountUUID string, fp *Fingerprint) {
	if fp == nil {
		return
	}
	fingerprintUA := fp.UserAgent
	// 先执行常规的 RewriteUserID 逻辑（会话映射带上身份代次）
	s.rewriteUserIDView(view, account.ID, fp.IdentityEpoch, accountUUID, fp.ClientID, fingerprintUA)

	// 检查是否启用会话ID伪装
	if !account.IsSessionIDMaskingEnabled() {
		return
	}

	metadata := view.get("metadata")
	if !metadata.Exists() || metadata.Type == gjson.Null {
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(metadata.Raw), "{") {
		return
	}

	userIDResult := metadata.Get("user_id")
	if !userIDResult.Exists() || userIDResult.Type != gjson.String {
		return
	}
	userID := userIDResult.String()
	if userID == "" {
		return
	}

	// 解析已重写的 user_id
	uidParsed := ParseMetadataUserID(userID)
	if uidParsed == nil {
		return
	}

	// 获取或生成固定的伪装 session ID：原子 get-or-create 并顺带刷新 15 分钟 TTL，
	// 并发首建时所有实例拿到同一个值，不会在一段会话内反复变化。
	candidate := generateRandomUUID()
	maskedSessionID, err := s.cache.GetOrCreateMaskedSessionID(ctx, account.ID, candidate)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to get masked session ID for account %d: %v", account.ID, err)
		return
	}
	if maskedSessionID == candidate {
		logger.LegacyPrintf("service.identity", "Generated new masked session ID for account %d: %s", account.ID, maskedSessionID)
	}

	// 单会话模式下整个账号只有一个会话，parent_session_id 会暴露出另一个会话，删除；
	// 新格式原位改写保留其它字段，旧格式用 FormatMetadataUserID 重建。
	version := ExtractCLIVersion(fingerprintUA)
	newUserID := ""
	if uidParsed.IsNewFormat && IsNewMetadataFormatVersion(version) {
		if rewritten, ok := rewriteJSONMetadataUserID(userID, uidParsed.DeviceID, uidParsed.AccountUUID, maskedSessionID, "", true); ok {
			newUserID = rewritten
		}
	}
	if newUserID == "" {
		newUserID = FormatMetadataUserID(uidParsed.DeviceID, uidParsed.AccountUUID, maskedSessionID, version)
	}

	slog.Debug("session_id_masking_applied",
		"account_id", account.ID,
		"before", userID,
		"after", newUserID,
	)

	if newUserID == userID {
		return
	}

	_ = view.setString("metadata.user_id", newUserID)
}

// ResolveSessionIDWithoutMetadata 为请求体里没有可映射 metadata.user_id 的 OAuth 请求
// （count_tokens、探测、metadata 透传关闭注入时）选出上游会话 ID，顺序：
//  1. 单会话模式：账号的伪装会话；
//  2. 客户端带了 X-Claude-Code-Session-Id：按同一映射换成本账号的会话 ID，
//     与该对话 messages 请求的会话一致（真实 CLI 的 count_tokens 带当前会话 ID）；
//  3. 账号上有活跃会话：复用最近活跃的那个（旁路请求挂在当前会话上）；
//  4. 账号空闲：环境会话。
//
// 存储不可用时退回按 15 分钟分桶的确定性 ID，绝不逐请求随机。
func (s *IdentityService) ResolveSessionIDWithoutMetadata(ctx context.Context, account *Account, identityEpoch int64, clientSessionID string) string {
	if account == nil {
		return generateRandomUUID()
	}
	if account.IsSessionIDMaskingEnabled() {
		if masked, err := s.cache.GetOrCreateMaskedSessionID(ctx, account.ID, generateRandomUUID()); err == nil && masked != "" {
			return masked
		}
	}
	if clientSessionID = strings.TrimSpace(clientSessionID); clientSessionID != "" && len(clientSessionID) <= 128 {
		return upstreamSessionIDFor(account.ID, identityEpoch, clientSessionID)
	}
	if last, err := s.cache.GetLastActiveSessionID(ctx, account.ID); err == nil && last != "" {
		return last
	} else if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to read last active session for account %d: %v", account.ID, err)
	}
	ambient, err := s.cache.GetOrCreateAmbientSessionID(ctx, account.ID, generateRandomUUID())
	if err == nil && ambient != "" {
		return ambient
	}
	logger.LegacyPrintf("service.identity", "Warning: failed to get ambient session for account %d, using bucketed fallback: %v", account.ID, err)
	bucket := time.Now().UnixNano() / int64(ambientSessionBucket)
	return generateUUIDFromSeed(fmt.Sprintf("%d::ambient::%d", account.ID, bucket))
}

// TouchActiveSession 记录账号最近活跃的上游会话 ID，供 ResolveSessionIDWithoutMetadata 复用。
// 同一会话在 lastActiveSessionRewriteInterval 内只写一次 Redis；写失败只记日志。
func (s *IdentityService) TouchActiveSession(ctx context.Context, accountID int64, sessionID string) {
	if s == nil || s.cache == nil || accountID <= 0 || sessionID == "" {
		return
	}
	now := time.Now()
	s.lastActiveMu.Lock()
	prev, ok := s.lastActiveWrites[accountID]
	if ok && prev.sessionID == sessionID && now.Sub(prev.at) < lastActiveSessionRewriteInterval {
		s.lastActiveMu.Unlock()
		return
	}
	s.lastActiveWrites[accountID] = lastActiveSessionWrite{sessionID: sessionID, at: now}
	s.lastActiveMu.Unlock()

	if err := s.cache.SetLastActiveSessionID(ctx, accountID, sessionID); err != nil {
		s.lastActiveMu.Lock()
		delete(s.lastActiveWrites, accountID)
		s.lastActiveMu.Unlock()
		logger.LegacyPrintf("service.identity", "Warning: failed to record last active session for account %d: %v", accountID, err)
	}
}

// generateRandomUUID 生成随机 UUID v4 格式字符串
func generateRandomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// fallback: 使用时间戳生成
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}

	// 设置 UUID v4 版本和变体位
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// generateClientID 生成64位十六进制客户端ID（32字节随机数）
func generateClientID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 极罕见的情况，使用时间戳+固定值作为fallback
		logger.LegacyPrintf("service.identity", "Warning: crypto/rand.Read failed: %v, using fallback", err)
		// 使用SHA256(当前纳秒时间)作为fallback
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		return hex.EncodeToString(h[:])
	}
	return hex.EncodeToString(b)
}

// generateUUIDFromSeed 从种子生成确定性UUID v4格式字符串
func generateUUIDFromSeed(seed string) string {
	hash := sha256.Sum256([]byte(seed))
	bytes := hash[:16]

	// 设置UUID v4版本和变体位
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

// parseUserAgentVersion 解析user-agent版本号
// 例如：claude-cli/2.1.2 -> (2, 1, 2)
func parseUserAgentVersion(ua string) (major, minor, patch int, ok bool) {
	// 匹配 xxx/x.y.z 格式
	matches := userAgentVersionRegex.FindStringSubmatch(ua)
	if len(matches) != 4 {
		return 0, 0, 0, false
	}
	major, _ = strconv.Atoi(matches[1])
	minor, _ = strconv.Atoi(matches[2])
	patch, _ = strconv.Atoi(matches[3])
	return major, minor, patch, true
}

// extractProduct 提取 User-Agent 中 "/" 前的产品名
// 例如：claude-cli/2.1.22 (external, cli) -> "claude-cli"
func extractProduct(ua string) string {
	if idx := strings.Index(ua, "/"); idx > 0 {
		return strings.ToLower(ua[:idx])
	}
	return ""
}

// isNewerVersion 比较版本号，判断newUA是否比cachedUA更新
// 要求产品名一致（防止浏览器 UA 如 Mozilla/5.0 误判为更新版本）
func isNewerVersion(newUA, cachedUA string) bool {
	// 校验产品名一致性
	newProduct := extractProduct(newUA)
	cachedProduct := extractProduct(cachedUA)
	if newProduct == "" || cachedProduct == "" || newProduct != cachedProduct {
		return false
	}

	newMajor, newMinor, newPatch, newOk := parseUserAgentVersion(newUA)
	cachedMajor, cachedMinor, cachedPatch, cachedOk := parseUserAgentVersion(cachedUA)

	if !newOk || !cachedOk {
		return false
	}

	// 比较版本号
	if newMajor > cachedMajor {
		return true
	}
	if newMajor < cachedMajor {
		return false
	}

	if newMinor > cachedMinor {
		return true
	}
	if newMinor < cachedMinor {
		return false
	}

	return newPatch > cachedPatch
}
