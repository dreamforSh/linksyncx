package service

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/model"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// 模板名称在表上唯一（migrations/080），重名保存与操作已删除的模板是管理员最常碰到的两种失败。
// 仓储把唯一约束冲突与记录不存在翻译成这两个错误，handler 经 response.ErrorFrom 回 409 / 404，
// 前端按 reason 显示本地化文案；不翻译的话它们落到 infraerrors 的兜底，管理员只看到 500 "internal error"。
var (
	ErrTLSFingerprintProfileNotFound   = infraerrors.NotFound("TLS_PROFILE_NOT_FOUND", "TLS fingerprint profile not found")
	ErrTLSFingerprintProfileNameExists = infraerrors.Conflict("TLS_PROFILE_NAME_EXISTS", "TLS fingerprint profile name already exists")
)

// 保存校验失败（400）的稳定 reason。两条 ClientHello 自洽性规则各有一个 reason，key share 不在 curves 里时
// metadata.group 给出是哪个组；前端按 reason 用表单上的字段名（密钥共享组 / 椭圆曲线 / 扩展）拼本地化提示。
// message 仍是带 API 字段名的英文原文，前端没有对应文案时原样展示。其余校验失败（如缺名称）统一为
// TLS_PROFILE_INVALID，metadata.field 指明字段。
const (
	tlsProfileReasonInvalid                        = "TLS_PROFILE_INVALID"
	tlsProfileReasonKeyShareNotInCurves            = "TLS_PROFILE_KEY_SHARE_NOT_IN_CURVES"
	tlsProfileReasonKeyShareWithoutSupportedGroups = "TLS_PROFILE_KEY_SHARE_WITHOUT_SUPPORTED_GROUPS"
)

// tlsProfileValidationError 把 model.TLSFingerprintProfile.Validate 的错误变成带 reason 的 400。
// 原错误挂在 cause 上，调用方仍可用 errors.As 取到 *model.ValidationError。
func tlsProfileValidationError(err error) error {
	var validationErr *model.ValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	var (
		keyShareErr *tlsfingerprint.KeyShareNotInSupportedGroupsError
		appErr      *infraerrors.ApplicationError
	)
	switch {
	case errors.As(err, &keyShareErr):
		appErr = infraerrors.BadRequest(tlsProfileReasonKeyShareNotInCurves, err.Error()).
			WithMetadata(map[string]string{"group": strconv.FormatUint(uint64(keyShareErr.Group), 10)})
	case errors.Is(err, tlsfingerprint.ErrKeyShareWithoutSupportedGroups):
		appErr = infraerrors.BadRequest(tlsProfileReasonKeyShareWithoutSupportedGroups, err.Error())
	default:
		appErr = infraerrors.BadRequest(tlsProfileReasonInvalid, err.Error()).
			WithMetadata(map[string]string{"field": validationErr.Field})
	}
	return appErr.WithCause(err)
}

// TLSFingerprintProfileRepository 定义 TLS 指纹模板的数据访问接口
//
// GetByID 找不到时返回 (nil, nil)；Update / Delete 找不到时返回 ErrTLSFingerprintProfileNotFound，
// Create / Update 名称重复时返回 ErrTLSFingerprintProfileNameExists。
type TLSFingerprintProfileRepository interface {
	List(ctx context.Context) ([]*model.TLSFingerprintProfile, error)
	GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error)
	Create(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error)
	Update(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error)
	Delete(ctx context.Context, id int64) error
}

// TLSFingerprintProfileCache 定义 TLS 指纹模板的缓存接口
type TLSFingerprintProfileCache interface {
	Get(ctx context.Context) ([]*model.TLSFingerprintProfile, bool)
	Set(ctx context.Context, profiles []*model.TLSFingerprintProfile) error
	Invalidate(ctx context.Context) error
	NotifyUpdate(ctx context.Context) error
	SubscribeUpdates(ctx context.Context, handler func())
}

// TLSFingerprintProfileService TLS 指纹模板管理服务
type TLSFingerprintProfileService struct {
	repo  TLSFingerprintProfileRepository
	cache TLSFingerprintProfileCache

	// 本地 ID→Profile 映射缓存，用于 DoWithTLS 热路径快速查找
	localCache map[int64]*model.TLSFingerprintProfile
	// randomCandidates 是“随机”模式参与 rendezvous 的模板：localCache 中通过 tlsfingerprint.ValidateProfile
	// 的那部分，在 setLocalCache 时算好、与 localCache 一起整体替换，热路径只遍历切片，不逐请求校验。
	randomCandidates []*model.TLSFingerprintProfile
	localMu          sync.RWMutex
}

// NewTLSFingerprintProfileService 创建 TLS 指纹模板服务
func NewTLSFingerprintProfileService(
	repo TLSFingerprintProfileRepository,
	cache TLSFingerprintProfileCache,
) *TLSFingerprintProfileService {
	svc := &TLSFingerprintProfileService{
		repo:       repo,
		cache:      cache,
		localCache: make(map[int64]*model.TLSFingerprintProfile),
	}

	ctx := context.Background()
	if err := svc.reloadFromDB(ctx); err != nil {
		logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from DB on startup: %v", err)
		if fallbackErr := svc.refreshLocalCache(ctx); fallbackErr != nil {
			logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to load profiles from cache fallback on startup: %v", fallbackErr)
		}
	}

	if cache != nil {
		cache.SubscribeUpdates(ctx, func() {
			if err := svc.refreshLocalCache(context.Background()); err != nil {
				logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh cache on notification: %v", err)
			}
		})
	}

	return svc
}

// --- CRUD ---

// List 获取所有模板
func (s *TLSFingerprintProfileService) List(ctx context.Context) ([]*model.TLSFingerprintProfile, error) {
	return s.repo.List(ctx)
}

// GetByID 根据 ID 获取模板
func (s *TLSFingerprintProfileService) GetByID(ctx context.Context, id int64) (*model.TLSFingerprintProfile, error) {
	return s.repo.GetByID(ctx, id)
}

// Create 创建模板
func (s *TLSFingerprintProfileService) Create(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, tlsProfileValidationError(err)
	}

	created, err := s.repo.Create(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return created, nil
}

// Update 更新模板
func (s *TLSFingerprintProfileService) Update(ctx context.Context, profile *model.TLSFingerprintProfile) (*model.TLSFingerprintProfile, error) {
	if err := profile.Validate(); err != nil {
		return nil, tlsProfileValidationError(err)
	}

	updated, err := s.repo.Update(ctx, profile)
	if err != nil {
		return nil, err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return updated, nil
}

// Delete 删除模板
func (s *TLSFingerprintProfileService) Delete(ctx context.Context, id int64) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}

	refreshCtx, cancel := s.newCacheRefreshContext()
	defer cancel()
	s.invalidateAndNotify(refreshCtx)

	return nil
}

// --- 热路径：运行时 Profile 查找 ---

// GetProfileByID 根据 ID 从本地缓存获取 Profile（用于 DoWithTLS 热路径）
// 返回 nil 表示未找到，调用方应 fallback 到内置默认 Profile
func (s *TLSFingerprintProfileService) GetProfileByID(id int64) *tlsfingerprint.Profile {
	s.localMu.RLock()
	p, ok := s.localCache[id]
	s.localMu.RUnlock()

	if ok && p != nil {
		return p.ToTLSProfile()
	}
	return nil
}

// stickyProfileForAccount 为绑定“随机”（profile id = -1）的账号，在当前模板集合中确定性地选出一个模板。
//
// 一个账号就是一台固定设备（见 account_device_profile.go 的设备画像原则：同一账号的出站请求恒定
// 呈现同一设备形态）。按请求随机挑模板会让同一账号相邻两次请求呈现不同的 TLS 栈，不像任何真实
// 客户端；连接池键又带着模板摘要，还会把一个账号拆成 N 个并行连接池。
//
// 因此用 rendezvous（HRW）哈希代替 rand：对每个模板计算 tlsProfileRendezvousWeight(账号 ID, 模板 ID)，
// 取权重最大者。同一账号永远落在同一模板上，多实例部署之间也一致，且无需落库；新增一个模板
// 只会把约 1/N 的账号迁到这个新模板上，删除模板只影响原本落在它上面的账号。
// 权重函数对模板 ID 是单射，不同模板不会同权；平局取较小 ID 只是兜底，保证结果与候选顺序无关。
//
// 候选只有通过 ValidateProfile 的模板（randomCandidates）：不自洽的模板在 DoWithTLS 建池前必然被拒，
// 若参与选择，落在它上面的约 1/N 个账号会被永久钉住、每个请求都失败（这类错误不触发临时下线，
// 粘性会话还会在原账号上反复重试）。剔除它等价于它不存在，其余账号的选择不受影响；校验是模板的
// 纯函数，多实例的候选集合一致，选择仍然一致。没有任何有效模板时返回 nil，回落到内置默认 profile。
func (s *TLSFingerprintProfileService) stickyProfileForAccount(accountID int64) *tlsfingerprint.Profile {
	s.localMu.RLock()
	defer s.localMu.RUnlock()

	var (
		chosen     *model.TLSFingerprintProfile
		bestWeight uint64
	)
	for _, p := range s.randomCandidates {
		weight := tlsProfileRendezvousWeight(accountID, p.ID)
		if chosen == nil || weight > bestWeight || (weight == bestWeight && p.ID < chosen.ID) {
			chosen, bestWeight = p, weight
		}
	}
	if chosen == nil {
		return nil
	}
	return chosen.ToTLSProfile()
}

// tlsProfileRendezvousWeight 把 (账号 ID, 模板 ID) 混合成 64 位权重。splitmix64 终结器雪崩充分，
// 相邻 ID 的权重也互不相关，各模板胜出的概率均等；它是 64 位上的双射，所以同一账号下
// 不同模板 ID 的权重必然不同。
func tlsProfileRendezvousWeight(accountID, profileID int64) uint64 {
	return splitmix64(splitmix64(uint64(accountID)) ^ uint64(profileID))
}

func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// ResolveTLSProfile 根据 Account 的配置解析出运行时 TLS Profile
//
// 逻辑：
//  1. 未启用 TLS 指纹 → 返回 nil（不伪装）
//  2. 启用 + 绑定了 profile_id → 从缓存查找对应 profile
//  3. 启用 + 绑定“随机”（-1）→ 按账号 ID 在自洽的模板中固定选出一个（stickyProfileForAccount）
//  4. 启用 + 未绑定或找不到 → 返回空 Profile（使用代码内置默认值）
func (s *TLSFingerprintProfileService) ResolveTLSProfile(account *Account) *tlsfingerprint.Profile {
	if account == nil || !account.IsTLSFingerprintEnabled() {
		return nil
	}
	id := account.GetTLSFingerprintProfileID()
	if id > 0 {
		// 显式绑定的模板即使不自洽也原样返回，由 DoWithTLS 明确报错：这是管理员指定的设备，
		// 悄悄换成别的模板会掩盖配置错误，也会在管理员不知情时改变账号的指纹。
		if p := s.GetProfileByID(id); p != nil {
			return p
		}
	}
	if id == -1 {
		// “随机”按账号固定：每个账号确定性地落在一个模板上，而不是每个请求重新抽取。
		// 未绑定（id = 0）的路径不访问 s，对 nil 接收者仍然安全；本分支与 id > 0 分支一样需要已初始化的服务。
		if p := s.stickyProfileForAccount(account.ID); p != nil {
			return p
		}
	}
	// TLS 启用但无绑定 profile → 空 Profile → dialer 使用内置默认值
	return &tlsfingerprint.Profile{Name: "Built-in Default (Node.js 24.x)"}
}

// --- 缓存管理 ---

func (s *TLSFingerprintProfileService) refreshLocalCache(ctx context.Context) error {
	if s.cache != nil {
		if profiles, ok := s.cache.Get(ctx); ok {
			s.setLocalCache(profiles)
			return nil
		}
	}
	return s.reloadFromDB(ctx)
}

func (s *TLSFingerprintProfileService) reloadFromDB(ctx context.Context) error {
	profiles, err := s.repo.List(ctx)
	if err != nil {
		return err
	}

	if s.cache != nil {
		if err := s.cache.Set(ctx, profiles); err != nil {
			logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to set cache: %v", err)
		}
	}

	s.setLocalCache(profiles)
	return nil
}

func (s *TLSFingerprintProfileService) setLocalCache(profiles []*model.TLSFingerprintProfile) {
	m := make(map[int64]*model.TLSFingerprintProfile, len(profiles))
	for _, p := range profiles {
		if p == nil {
			continue
		}
		m[p.ID] = p
	}

	// 保存时已校验（model.TLSFingerprintProfile.Validate），这里兜住校验上线前存进库里的不自洽模板：
	// 只把自洽的模板放进“随机”候选。只在加载时（启动与模板增删改通知）记一次日志，不在请求路径上刷屏；
	// 模板里没有凭据，记 ID、名称与原因即可。
	candidates := make([]*model.TLSFingerprintProfile, 0, len(m))
	for _, p := range profiles {
		if p == nil || m[p.ID] != p {
			continue // 同 ID 重复时与 localCache 一致，以最后一个为准
		}
		if err := tlsfingerprint.ValidateProfile(p.ToTLSProfile()); err != nil {
			logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Template %d (%q) is excluded from Random selection: %v", p.ID, p.Name, err)
			continue
		}
		candidates = append(candidates, p)
	}

	s.localMu.Lock()
	s.localCache = m
	s.randomCandidates = candidates
	s.localMu.Unlock()
}

func (s *TLSFingerprintProfileService) newCacheRefreshContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}

func (s *TLSFingerprintProfileService) invalidateAndNotify(ctx context.Context) {
	if s.cache != nil {
		if err := s.cache.Invalidate(ctx); err != nil {
			logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to invalidate cache: %v", err)
		}
	}

	if err := s.reloadFromDB(ctx); err != nil {
		logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to refresh local cache: %v", err)
		// 经 setLocalCache 清空，“随机”候选随 localCache 一起清掉，不会继续选中已删除或已改动的模板。
		s.setLocalCache(nil)
	}

	if s.cache != nil {
		if err := s.cache.NotifyUpdate(ctx); err != nil {
			logger.LegacyPrintf("service.tls_fp_profile", "[TLSFPProfileService] Failed to notify cache update: %v", err)
		}
	}
}
