package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	// identityCacheRenewInterval：缓存记录超过该时长未写入时续期 Redis TTL；配置了身份库时
	// 顺带与数据库对账一次（修复重置身份时缓存写入失败留下的旧身份）。
	identityCacheRenewInterval = 24 * time.Hour
	// identityAdoptionRetryInterval 是收编存量 Redis 身份失败（数据库不可用）后的重试间隔，
	// 期间继续使用缓存中的身份，不逐请求打库。
	identityAdoptionRetryInterval = time.Minute
)

// GetOrCreateAccountFingerprint 是生产路径的身份入口：带上账号主人（extra.account_uuid），
// 账号换了主人时轮换身份，避免两个上游账号共用同一台「设备」。
func (s *IdentityService) GetOrCreateAccountFingerprint(ctx context.Context, account *Account, headers http.Header) (*Fingerprint, error) {
	if account == nil {
		return nil, fmt.Errorf("%w: account is nil", ErrClientIdentityUnavailable)
	}
	return s.getOrCreateFingerprint(ctx, account.ID, strings.TrimSpace(account.GetExtraString("account_uuid")), headers)
}

// getOrCreatePersistedFingerprint 以数据库为身份真相源、Redis 为缓存：
//   - Redis 命中且已入库：直接使用（热路径不访问数据库）；
//   - Redis 命中但未入库（第二阶段之前写入的存量身份）：原样收编进数据库，不生成新 device_id；
//   - Redis 未命中：读数据库，命中后回填缓存；库中也没有时新建，必须先写库成功；
//   - Redis 读失败且库中无记录：失败关闭，不新建（存量身份可能只在暂时读不到的 Redis 里）。
//
// 账号主人（owner）与身份记录的主人都已知且不同时，轮换身份（identity_epoch + 1、新 device_id）。
func (s *IdentityService) getOrCreatePersistedFingerprint(ctx context.Context, accountID int64, owner string, headers http.Header) (*Fingerprint, error) {
	clientUA := strings.TrimSpace(headers.Get("User-Agent"))
	uaAcceptable := isAcceptableFingerprintUserAgent(clientUA)

	cached, cacheErr := s.cache.GetFingerprint(ctx, accountID)
	if cacheErr != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to read fingerprint for account %d: %v", accountID, cacheErr)
		cached = nil
	}

	fp := cached
	if fp != nil && !fp.Persisted {
		fp = s.adoptCachedIdentity(ctx, accountID, owner, fp)
	}
	loadedFromStore := false
	if fp == nil {
		rec, err := s.store.Get(ctx, accountID)
		if err != nil {
			return nil, clientIdentityUnavailable(accountID, err)
		}
		if rec == nil {
			if cacheErr != nil {
				return nil, clientIdentityUnavailable(accountID, fmt.Errorf("identity cache unavailable and no persisted identity: %w", cacheErr))
			}
			return s.createPersistedIdentity(ctx, accountID, owner, headers, clientUA, uaAcceptable)
		}
		fp = fingerprintFromRecord(rec)
		loadedFromStore = true
	}

	if clientIdentityOwnerChanged(fp.OwnerRef, owner) {
		return s.rotatePersistedIdentity(ctx, accountID, fp, owner, headers, "account owner changed")
	}

	before := *fp
	headersChanged := upgradeFingerprintFromHeaders(accountID, fp, headers, clientUA, uaAcceptable)
	if headersChanged || (fp.OwnerRef == "" && owner != "") {
		rec, err := s.store.UpdateHeaders(ctx, accountID, fp.IdentityEpoch, fp.ClientID, owner, fp.UserAgent, clientIdentityHeadersOf(fp))
		if err != nil || rec == nil {
			logger.LegacyPrintf("service.identity", "Warning: failed to persist fingerprint update for account %d: %v", accountID, errOrMissing(err))
			// 写库失败不改缓存（缓存不能领先于库）。本次只做与请求无关的确定性修正，不应用随
			// 客户端变化的合并升级，避免数据库故障期间同一账号的 UA 在请求之间来回变。
			fallback := before
			upgradeFingerprintFromHeaders(accountID, &fallback, http.Header{}, "", false)
			return &fallback, nil
		}
		stored := fingerprintFromRecord(rec)
		if stored.IdentityEpoch != fp.IdentityEpoch || stored.ClientID != fp.ClientID {
			logger.LegacyPrintf("service.identity", "Fingerprint for account %d was rotated concurrently, using identity epoch %d", accountID, stored.IdentityEpoch)
		}
		return s.replaceCachedIdentity(ctx, accountID, stored), nil
	}

	if loadedFromStore {
		return s.replaceCachedIdentity(ctx, accountID, fp), nil
	}
	if time.Since(time.Unix(fp.UpdatedAt, 0)) > identityCacheRenewInterval {
		return s.renewCachedIdentity(ctx, accountID, fp), nil
	}
	return fp, nil
}

// renewCachedIdentity 续期缓存 TTL，并与数据库对账：库中记录为准（重置身份时若缓存写入失败，
// 这里在 24 小时内纠正）；读库失败时只续期缓存。
func (s *IdentityService) renewCachedIdentity(ctx context.Context, accountID int64, fp *Fingerprint) *Fingerprint {
	rec, err := s.store.Get(ctx, accountID)
	if err == nil && rec != nil {
		stored := fingerprintFromRecord(rec)
		if stored.IdentityEpoch != fp.IdentityEpoch || stored.ClientID != fp.ClientID {
			logger.LegacyPrintf("service.identity", "Cached fingerprint for account %d was stale, using persisted identity epoch %d", accountID, stored.IdentityEpoch)
		}
		return s.replaceCachedIdentity(ctx, accountID, stored)
	}
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to reconcile fingerprint for account %d: %v", accountID, err)
	}
	fp.UpdatedAt = time.Now().Unix()
	stored, err := s.cache.SetFingerprint(ctx, accountID, fp)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to refresh fingerprint for account %d: %v", accountID, err)
		return fp
	}
	if stored != nil {
		return stored
	}
	return fp
}

// createPersistedIdentity 为没有任何身份的账号新建身份：先写库（先写者胜），再回填缓存。
func (s *IdentityService) createPersistedIdentity(ctx context.Context, accountID int64, owner string, headers http.Header, clientUA string, uaAcceptable bool) (*Fingerprint, error) {
	if !uaAcceptable && clientUA != "" {
		logger.LegacyPrintf("service.identity",
			"Rejected fingerprint user-agent for account %d: %q (malformed or implausible version)",
			accountID, clientUA)
	}
	fp := s.createFingerprintFromHeaders(headers)
	fp.ClientID = generateClientID()
	fp.OwnerRef = owner

	rec, err := s.store.Insert(ctx, clientIdentityRecordFrom(accountID, fp))
	if err != nil || rec == nil {
		return nil, clientIdentityUnavailable(accountID, errOrMissing(err))
	}
	stored := fingerprintFromRecord(rec)
	if stored.ClientID != fp.ClientID {
		logger.LegacyPrintf("service.identity", "Fingerprint for account %d was created concurrently, using client_id: %s", accountID, stored.ClientID)
	} else {
		logger.LegacyPrintf("service.identity", "Created new fingerprint for account %d with client_id: %s", accountID, stored.ClientID)
	}
	return s.replaceCachedIdentity(ctx, accountID, stored), nil
}

// adoptCachedIdentity 把第二阶段之前只存在 Redis 的身份原样写入数据库（不换 device_id），
// 之后以库中记录为准。数据库不可用时继续使用缓存身份，并在 identityAdoptionRetryInterval 后重试。
func (s *IdentityService) adoptCachedIdentity(ctx context.Context, accountID int64, owner string, cached *Fingerprint) *Fingerprint {
	if !s.adoptionDue(accountID) {
		return cached
	}
	rec, err := s.adoptIntoStore(ctx, accountID, owner, cached)
	if err != nil {
		s.deferAdoption(accountID)
		logger.LegacyPrintf("service.identity", "Warning: failed to adopt cached fingerprint for account %d, keep using the cached identity: %v", accountID, err)
		return cached
	}
	return s.replaceCachedIdentity(ctx, accountID, fingerprintFromRecord(rec))
}

// adoptIntoStore 返回库中身份；库中没有时以缓存身份建立（先写者胜）。
func (s *IdentityService) adoptIntoStore(ctx context.Context, accountID int64, owner string, cached *Fingerprint) (*ClientIdentityRecord, error) {
	rec, err := s.store.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		return rec, nil
	}
	candidate := *cached
	if candidate.OwnerRef == "" {
		candidate.OwnerRef = owner
	}
	rec, err = s.store.Insert(ctx, clientIdentityRecordFrom(accountID, &candidate))
	if err != nil || rec == nil {
		return nil, errOrMissing(err)
	}
	if rec.DeviceID == candidate.ClientID {
		logger.LegacyPrintf("service.identity", "Adopted cached fingerprint for account %d into the identity store (client_id: %s)", accountID, rec.DeviceID)
	}
	return rec, nil
}

// rotatePersistedIdentity 把账号身份换成一台新「设备」：identity_epoch + 1、新 device_id，UA 与头取自
// 本次请求（headers 为空时用默认值），并清掉旧身份下的账号级会话键。写库失败时失败关闭，
// 不继续用旧身份服务新主人。
func (s *IdentityService) rotatePersistedIdentity(ctx context.Context, accountID int64, current *Fingerprint, owner string, headers http.Header, reason string) (*Fingerprint, error) {
	next := s.createFingerprintFromHeaders(headers)
	next.ClientID = generateClientID()
	next.OwnerRef = owner

	rec, err := s.store.Rotate(ctx, current.IdentityEpoch, clientIdentityRecordFrom(accountID, next))
	if err != nil || rec == nil {
		return nil, clientIdentityUnavailable(accountID, fmt.Errorf("rotate identity (%s): %w", reason, errOrMissing(err)))
	}
	stored := fingerprintFromRecord(rec)
	if stored.ClientID == next.ClientID {
		logger.LegacyPrintf("service.identity", "Rotated client identity for account %d (%s): epoch %d -> %d", accountID, reason, current.IdentityEpoch, stored.IdentityEpoch)
	} else {
		logger.LegacyPrintf("service.identity", "Client identity for account %d was rotated concurrently, using identity epoch %d", accountID, stored.IdentityEpoch)
	}
	stored = s.replaceCachedIdentity(ctx, accountID, stored)
	s.forgetAccountSessions(ctx, accountID)
	return stored, nil
}

// ResetClientIdentity 由管理员触发，把账号换成一台新「设备」（identity_epoch + 1、新 device_id、
// 默认 UA 与头）。账号还没有任何身份时返回 (nil, nil)：下次请求会直接新建。
func (s *IdentityService) ResetClientIdentity(ctx context.Context, account *Account) (*Fingerprint, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("client identity store is not configured")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	owner := strings.TrimSpace(account.GetExtraString("account_uuid"))

	rec, err := s.store.Get(ctx, account.ID)
	if err != nil {
		return nil, fmt.Errorf("read client identity: %w", err)
	}
	if rec == nil {
		// 只在 Redis 里的存量身份先收编，保证 epoch 从它的代次往上走。
		cached, cacheErr := s.cache.GetFingerprint(ctx, account.ID)
		if cacheErr != nil {
			return nil, fmt.Errorf("read cached client identity: %w", cacheErr)
		}
		if cached == nil {
			return nil, nil
		}
		if rec, err = s.adoptIntoStore(ctx, account.ID, owner, cached); err != nil {
			return nil, fmt.Errorf("adopt cached client identity: %w", err)
		}
	}
	return s.rotatePersistedIdentity(ctx, account.ID, fingerprintFromRecord(rec), owner, http.Header{}, "reset by admin")
}

// replaceCachedIdentity 用库中确认过的身份覆盖缓存，返回此后应使用的身份。缓存中已是更高代次的
// 身份时不回退（慢请求不能把重置前的旧身份写回缓存），交给 reconcileCachedIdentity 对账。
// 写缓存失败只记日志（库已是准）。
func (s *IdentityService) replaceCachedIdentity(ctx context.Context, accountID int64, fp *Fingerprint) *Fingerprint {
	fp.UpdatedAt = time.Now().Unix()
	stored, err := s.cache.ReplaceFingerprint(ctx, accountID, fp)
	if err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache persisted fingerprint for account %d: %v", accountID, err)
		return fp
	}
	if stored == nil || stored.IdentityEpoch <= fp.IdentityEpoch {
		return fp
	}
	return s.reconcileCachedIdentity(ctx, accountID, stored)
}

// reconcileCachedIdentity 处理缓存代次高于本次写入的情况：库与缓存一致时是并发轮换先写入了缓存，
// 沿用缓存；不一致（如数据库回滚到更低代次）时以库为准覆盖缓存。读库失败时沿用缓存中的新代次。
func (s *IdentityService) reconcileCachedIdentity(ctx context.Context, accountID int64, cached *Fingerprint) *Fingerprint {
	rec, err := s.store.Get(ctx, accountID)
	if err != nil || rec == nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to reconcile cached fingerprint for account %d: %v", accountID, errOrMissing(err))
		return cached
	}
	current := fingerprintFromRecord(rec)
	if current.IdentityEpoch == cached.IdentityEpoch && current.ClientID == cached.ClientID {
		return cached
	}
	logger.LegacyPrintf("service.identity", "Cached fingerprint for account %d (identity epoch %d) disagrees with the identity store (epoch %d), using the store",
		accountID, cached.IdentityEpoch, current.IdentityEpoch)
	if err := s.cache.OverwriteFingerprint(ctx, accountID, current); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to cache persisted fingerprint for account %d: %v", accountID, err)
	}
	return current
}

// forgetAccountSessions 清掉旧身份下的账号级会话（伪装、环境、最近活跃），新身份从新会话开始。
func (s *IdentityService) forgetAccountSessions(ctx context.Context, accountID int64) {
	if err := s.cache.DeleteAccountSessions(ctx, accountID); err != nil {
		logger.LegacyPrintf("service.identity", "Warning: failed to clear account sessions for account %d after identity rotation: %v", accountID, err)
	}
	s.lastActiveMu.Lock()
	delete(s.lastActiveWrites, accountID)
	s.lastActiveMu.Unlock()
}

func (s *IdentityService) adoptionDue(accountID int64) bool {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	retryAt, ok := s.adoptRetryAt[accountID]
	return !ok || !time.Now().Before(retryAt)
}

func (s *IdentityService) deferAdoption(accountID int64) {
	s.adoptMu.Lock()
	s.adoptRetryAt[accountID] = time.Now().Add(identityAdoptionRetryInterval)
	s.adoptMu.Unlock()
}

// clientIdentityOwnerChanged 报告身份记录的主人与账号当前主人都已知且不同。
func clientIdentityOwnerChanged(stored, current string) bool {
	stored, current = strings.TrimSpace(stored), strings.TrimSpace(current)
	return stored != "" && current != "" && !strings.EqualFold(stored, current)
}

func clientIdentityUnavailable(accountID int64, err error) error {
	logger.LegacyPrintf("service.identity", "Warning: client identity for account %d is unavailable, refusing to use an ephemeral identity: %v", accountID, err)
	return fmt.Errorf("%w: account %d: %w", ErrClientIdentityUnavailable, accountID, err)
}

func errOrMissing(err error) error {
	if err != nil {
		return err
	}
	return errors.New("identity store returned no record")
}
