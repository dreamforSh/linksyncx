//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

// memoryClientIdentityStore 是按真实 SQL 语义实现条件写的内存身份库。
type memoryClientIdentityStore struct {
	mu        sync.Mutex
	rows      map[int64]ClientIdentityRecord
	err       error
	getCalls  int
	updateErr error
}

func newMemoryClientIdentityStore() *memoryClientIdentityStore {
	return &memoryClientIdentityStore{rows: map[int64]ClientIdentityRecord{}}
}

func (m *memoryClientIdentityStore) row(accountID int64) *ClientIdentityRecord {
	rec, ok := m.rows[accountID]
	if !ok {
		return nil
	}
	clone := rec
	return &clone
}

func (m *memoryClientIdentityStore) Get(_ context.Context, accountID int64) (*ClientIdentityRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getCalls++
	if m.err != nil {
		return nil, m.err
	}
	return m.row(accountID), nil
}

func (m *memoryClientIdentityStore) Insert(_ context.Context, rec *ClientIdentityRecord) (*ClientIdentityRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if _, ok := m.rows[rec.AccountID]; !ok {
		stored := *rec
		stored.CreatedAt, stored.UpdatedAt = time.Now(), time.Now()
		m.rows[rec.AccountID] = stored
	}
	return m.row(rec.AccountID), nil
}

func (m *memoryClientIdentityStore) UpdateHeaders(_ context.Context, accountID, epoch int64, deviceID, ownerRef, userAgent string, headers ClientIdentityHeaders) (*ClientIdentityRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	rec, ok := m.rows[accountID]
	if ok && rec.IdentityEpoch == epoch && rec.DeviceID == deviceID {
		rec.UserAgent, rec.Headers = userAgent, headers
		if rec.OwnerRef == "" {
			rec.OwnerRef = ownerRef
		}
		rec.UpdatedAt = time.Now()
		m.rows[accountID] = rec
	}
	return m.row(accountID), nil
}

func (m *memoryClientIdentityStore) Rotate(_ context.Context, expectedEpoch int64, next *ClientIdentityRecord) (*ClientIdentityRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	rec, ok := m.rows[next.AccountID]
	switch {
	case !ok:
		stored := *next
		stored.IdentityEpoch = expectedEpoch + 1
		m.rows[next.AccountID] = stored
	case rec.IdentityEpoch == expectedEpoch:
		stored := *next
		stored.IdentityEpoch = rec.IdentityEpoch + 1
		stored.CreatedAt = rec.CreatedAt
		m.rows[next.AccountID] = stored
	}
	return m.row(next.AccountID), nil
}

const p2ClientUA = "claude-cli/2.1.287 (external, cli)"

func p2Headers(ua string) http.Header {
	h := http.Header{}
	h.Set("User-Agent", ua)
	h.Set("X-Stainless-OS", "MacOS")
	h.Set("X-Stainless-Arch", "arm64")
	return h
}

func p2Account(id int64, owner string) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": owner}}
}

// 新账号：先写库再写缓存，身份带主人、代次 0、已入库标记。
func TestPersistedIdentityCreatesInStoreFirst(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	svc := ProvideIdentityService(cache, store)

	fp, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(1, "owner-a"), p2Headers(p2ClientUA))
	require.NoError(t, err)
	require.True(t, fp.Persisted)
	require.Equal(t, "owner-a", fp.OwnerRef)
	require.Zero(t, fp.IdentityEpoch)

	rec := store.row(1)
	require.NotNil(t, rec)
	require.Equal(t, fp.ClientID, rec.DeviceID)
	require.Equal(t, "MacOS", rec.Headers.OS)
	require.Equal(t, fp.ClientID, cache.fingerprint[1].ClientID)

	again, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(1, "owner-a"), p2Headers(p2ClientUA))
	require.NoError(t, err)
	require.Equal(t, fp.ClientID, again.ClientID, "the identity is stable")
}

// 第二阶段之前只在 Redis 里的身份：原样收编进库，device_id 与 UA 都不变，主人补上。
func TestPersistedIdentityAdoptsLegacyCachedIdentity(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	legacy := &Fingerprint{ClientID: strings.Repeat("ab", 32), UserAgent: claude.DefaultUserAgent(), StainlessOS: "Windows", UpdatedAt: time.Now().Unix()}
	cache.fingerprint[2] = legacy
	svc := ProvideIdentityService(cache, store)

	fp, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(2, "owner-b"), http.Header{})
	require.NoError(t, err)
	require.Equal(t, legacy.ClientID, fp.ClientID)
	require.True(t, fp.Persisted)

	rec := store.row(2)
	require.NotNil(t, rec)
	require.Equal(t, legacy.ClientID, rec.DeviceID)
	require.Equal(t, "Windows", rec.Headers.OS)
	require.Equal(t, "owner-b", rec.OwnerRef)
	require.True(t, cache.fingerprint[2].Persisted, "the cache now carries the persisted identity")
}

// 收编时数据库不可用：继续用缓存身份，重试有间隔，不逐请求打库。
func TestPersistedIdentityAdoptionBacksOffWhenStoreIsDown(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	legacy := &Fingerprint{ClientID: strings.Repeat("cd", 32), UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Unix()}
	cache.fingerprint[3] = legacy
	store.err = errors.New("db down")
	svc := ProvideIdentityService(cache, store)

	for i := 0; i < 3; i++ {
		fp, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(3, "owner"), http.Header{})
		require.NoError(t, err)
		require.Equal(t, legacy.ClientID, fp.ClientID)
	}
	require.Equal(t, 1, store.getCalls, "adoption is retried only after the backoff interval")

	store.err = nil
	svc.adoptMu.Lock()
	svc.adoptRetryAt[3] = time.Now().Add(-time.Second)
	svc.adoptMu.Unlock()
	fp, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(3, "owner"), http.Header{})
	require.NoError(t, err)
	require.True(t, fp.Persisted)
	require.Equal(t, legacy.ClientID, store.row(3).DeviceID)
}

// Redis 读失败或被清空时以库为准：库中有身份就照常服务并回填缓存。
func TestPersistedIdentityServesFromStoreWhenCacheMissesOrFails(t *testing.T) {
	store := newMemoryClientIdentityStore()
	store.rows[4] = ClientIdentityRecord{AccountID: 4, Platform: PlatformAnthropic, DeviceID: strings.Repeat("ef", 32), UserAgent: claude.DefaultUserAgent(), OwnerRef: "owner"}

	cache := newMemoryIdentityCache()
	fp, err := ProvideIdentityService(cache, store).GetOrCreateAccountFingerprint(context.Background(), p2Account(4, "owner"), http.Header{})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("ef", 32), fp.ClientID)
	require.Equal(t, fp.ClientID, cache.fingerprint[4].ClientID, "the cache is backfilled")

	fp, err = ProvideIdentityService(&failingIdentityCache{err: errors.New("redis down")}, store).GetOrCreateAccountFingerprint(context.Background(), p2Account(4, "owner"), http.Header{})
	require.NoError(t, err, "a Redis outage no longer fails accounts whose identity is persisted")
	require.Equal(t, strings.Repeat("ef", 32), fp.ClientID)
}

// Redis 读失败且库中无记录：不能新建（存量身份可能只在暂时读不到的 Redis 里）；
// 库读失败同样失败关闭。
func TestPersistedIdentityRefusesToCreateWhenIdentityMayExist(t *testing.T) {
	store := newMemoryClientIdentityStore()
	_, err := ProvideIdentityService(&failingIdentityCache{err: errors.New("redis down")}, store).GetOrCreateAccountFingerprint(context.Background(), p2Account(5, "owner"), http.Header{})
	require.ErrorIs(t, err, ErrClientIdentityUnavailable)
	require.Nil(t, store.row(5), "no identity is created while the cache cannot be read")

	store.err = errors.New("db down")
	_, err = ProvideIdentityService(newMemoryIdentityCache(), store).GetOrCreateAccountFingerprint(context.Background(), p2Account(5, "owner"), http.Header{})
	require.ErrorIs(t, err, ErrClientIdentityUnavailable)
}

// 账号换了主人：身份轮换为新设备，代次 + 1，账号级会话清空，上游会话映射随之变化。
func TestPersistedIdentityRotatesWhenOwnerChanges(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	svc := ProvideIdentityService(cache, store)
	ctx := context.Background()

	first, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(6, "owner-a"), p2Headers(p2ClientUA))
	require.NoError(t, err)
	cache.ambient[6], cache.last[6], cache.masked[6] = "ambient", "last", "masked"

	same, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(6, "OWNER-A"), p2Headers(p2ClientUA))
	require.NoError(t, err)
	require.Equal(t, first.ClientID, same.ClientID, "owner comparison ignores case")
	unknown, err := svc.GetOrCreateFingerprint(ctx, 6, p2Headers(p2ClientUA))
	require.NoError(t, err)
	require.Equal(t, first.ClientID, unknown.ClientID, "an unknown owner never rotates")

	rotated, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(6, "owner-b"), p2Headers(p2ClientUA))
	require.NoError(t, err)
	require.NotEqual(t, first.ClientID, rotated.ClientID)
	require.Equal(t, int64(1), rotated.IdentityEpoch)
	require.Equal(t, "owner-b", rotated.OwnerRef)
	require.Equal(t, rotated.ClientID, store.row(6).DeviceID)
	require.Empty(t, cache.ambient[6])
	require.Empty(t, cache.last[6])
	require.Empty(t, cache.masked[6])

	const session = "11111111-2222-4333-8444-555555555555"
	require.NotEqual(t, upstreamSessionIDFor(6, 0, session), upstreamSessionIDFor(6, rotated.IdentityEpoch, session))
}

// 代次 0 沿用引入代次之前的种子：上线时进行中的对话会话 ID 不变。
func TestUpstreamSessionMappingKeepsLegacySeedAtEpochZero(t *testing.T) {
	const session = "11111111-2222-4333-8444-555555555555"
	require.Equal(t, generateUUIDFromSeed(fmt.Sprintf("%d::%s", 7, session)), upstreamSessionIDFor(7, 0, session))
	require.NotEqual(t, upstreamSessionIDFor(7, 1, session), upstreamSessionIDFor(7, 2, session))
}

// 客户端送来更新版本：先写库再更新缓存；写库失败不改缓存，本次不应用合并升级。
func TestPersistedIdentityUpgradeWritesStoreFirst(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	svc := ProvideIdentityService(cache, store)
	ctx := context.Background()
	base, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(8, "owner"), p2Headers(claude.DefaultUserAgent()))
	require.NoError(t, err)

	newer := bumpPatch(t, claude.DefaultUserAgent())
	store.updateErr = errors.New("db write failed")
	fallback, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(8, "owner"), p2Headers(newer))
	require.NoError(t, err)
	require.Equal(t, base.UserAgent, fallback.UserAgent, "without a persisted write the client-specific upgrade is not applied")
	require.Equal(t, base.UserAgent, cache.fingerprint[8].UserAgent, "the cache never gets ahead of the store")

	store.updateErr = nil
	upgraded, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(8, "owner"), p2Headers(newer))
	require.NoError(t, err)
	require.Equal(t, newer, upgraded.UserAgent)
	require.Equal(t, newer, store.row(8).UserAgent)
	require.Equal(t, newer, cache.fingerprint[8].UserAgent)
}

// 升级写库时发现身份已被其它实例轮换：以库中的新身份为准。
func TestPersistedIdentityUpgradeDetectsConcurrentRotation(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	svc := ProvideIdentityService(cache, store)
	ctx := context.Background()
	_, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(9, "owner"), p2Headers(claude.DefaultUserAgent()))
	require.NoError(t, err)

	// 另一实例轮换了身份，但本实例的缓存还是旧的。
	rotated := store.row(9)
	rotated.IdentityEpoch, rotated.DeviceID = 1, strings.Repeat("99", 32)
	store.rows[9] = *rotated

	got, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(9, "owner"), p2Headers(bumpPatch(t, claude.DefaultUserAgent())))
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("99", 32), got.ClientID)
	require.Equal(t, int64(1), got.IdentityEpoch)
	require.Equal(t, got.ClientID, cache.fingerprint[9].ClientID)
}

// 缓存续期时与库对账：重置身份时若缓存写入失败，24 小时内纠正为库中身份。
func TestPersistedIdentityRenewalReconcilesWithStore(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	stale := &Fingerprint{ClientID: strings.Repeat("aa", 32), UserAgent: claude.DefaultUserAgent(), Persisted: true, OwnerRef: "owner",
		UpdatedAt: time.Now().Add(-25 * time.Hour).Unix()}
	cache.fingerprint[10] = stale
	store.rows[10] = ClientIdentityRecord{AccountID: 10, Platform: PlatformAnthropic, IdentityEpoch: 1, DeviceID: strings.Repeat("bb", 32), UserAgent: claude.DefaultUserAgent(), OwnerRef: "owner"}

	got, err := ProvideIdentityService(cache, store).GetOrCreateAccountFingerprint(context.Background(), p2Account(10, "owner"), http.Header{})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("bb", 32), got.ClientID)
	require.Equal(t, int64(1), got.IdentityEpoch)
	require.Equal(t, got.ClientID, cache.fingerprint[10].ClientID)
}

// racingResetStore 在 UpdateHeaders 落库之后、调用方回写缓存之前插入一次操作（如管理员重置）。
type racingResetStore struct {
	*memoryClientIdentityStore
	afterUpdate func()
}

func (s *racingResetStore) UpdateHeaders(ctx context.Context, accountID, epoch int64, deviceID, ownerRef, userAgent string, headers ClientIdentityHeaders) (*ClientIdentityRecord, error) {
	rec, err := s.memoryClientIdentityStore.UpdateHeaders(ctx, accountID, epoch, deviceID, ownerRef, userAgent, headers)
	if hook := s.afterUpdate; hook != nil {
		s.afterUpdate = nil
		hook()
	}
	return rec, err
}

// 慢请求与管理员重置并发：请求在重置前写库（代次 0），重置把代次 1 写进缓存后请求才回写缓存——
// 旧代次不能盖掉重置后的身份，这个请求本身也改用新身份。
func TestPersistedIdentityResetIsNotUndoneBySlowRequest(t *testing.T) {
	cache := newMemoryIdentityCache()
	store := &racingResetStore{memoryClientIdentityStore: newMemoryClientIdentityStore()}
	svc := ProvideIdentityService(cache, store)
	ctx := context.Background()
	_, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(15, "owner"), p2Headers(claude.DefaultUserAgent()))
	require.NoError(t, err)

	var reset *Fingerprint
	var resetErr error
	store.afterUpdate = func() { reset, resetErr = svc.ResetClientIdentity(ctx, p2Account(15, "owner")) }
	got, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(15, "owner"), p2Headers(bumpPatch(t, claude.DefaultUserAgent())))
	require.NoError(t, err)
	require.NoError(t, resetErr)
	require.NotNil(t, reset, "precondition: the reset ran between the store write and the cache write")
	require.Equal(t, int64(1), reset.IdentityEpoch)
	require.Equal(t, reset.ClientID, cache.fingerprint[15].ClientID, "the reset identity stays cached")
	require.Equal(t, int64(1), cache.fingerprint[15].IdentityEpoch)
	require.Equal(t, reset.ClientID, got.ClientID, "the slow request switches to the reset identity")
}

// 数据库回滚到更低代次（如从备份恢复）：缓存不能一直领先于库，对账后以库为准，此后照常走缓存。
func TestPersistedIdentityCacheFollowsStoreAfterRollback(t *testing.T) {
	cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
	cache.fingerprint[16] = &Fingerprint{ClientID: strings.Repeat("dd", 32), UserAgent: claude.DefaultUserAgent(), Persisted: true, OwnerRef: "owner",
		IdentityEpoch: 3, UpdatedAt: time.Now().Add(-25 * time.Hour).Unix()}
	store.rows[16] = ClientIdentityRecord{AccountID: 16, Platform: PlatformAnthropic, DeviceID: strings.Repeat("ee", 32), UserAgent: claude.DefaultUserAgent(), OwnerRef: "owner"}
	svc := ProvideIdentityService(cache, store)

	got, err := svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(16, "owner"), http.Header{})
	require.NoError(t, err)
	require.Equal(t, strings.Repeat("ee", 32), got.ClientID)
	require.Zero(t, got.IdentityEpoch)
	require.Equal(t, got.ClientID, cache.fingerprint[16].ClientID)
	require.Zero(t, cache.fingerprint[16].IdentityEpoch)

	calls := store.getCalls
	_, err = svc.GetOrCreateAccountFingerprint(context.Background(), p2Account(16, "owner"), http.Header{})
	require.NoError(t, err)
	require.Equal(t, calls, store.getCalls, "once reconciled, requests are served from the cache again")
}

func TestResetClientIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("rotates a persisted identity", func(t *testing.T) {
		cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
		svc := ProvideIdentityService(cache, store)
		before, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(11, "owner"), p2Headers(p2ClientUA))
		require.NoError(t, err)
		cache.last[11] = "last"

		after, err := svc.ResetClientIdentity(ctx, p2Account(11, "owner"))
		require.NoError(t, err)
		require.NotEqual(t, before.ClientID, after.ClientID)
		require.Equal(t, int64(1), after.IdentityEpoch)
		require.Equal(t, defaultFingerprint().StainlessOS, after.StainlessOS, "a reset starts from default headers")
		require.Equal(t, after.ClientID, cache.fingerprint[11].ClientID)
		require.Empty(t, cache.last[11])

		next, err := svc.GetOrCreateAccountFingerprint(ctx, p2Account(11, "owner"), p2Headers(p2ClientUA))
		require.NoError(t, err)
		require.Equal(t, after.ClientID, next.ClientID, "requests after the reset use the new identity")
	})

	t.Run("adopts a cache-only identity before rotating", func(t *testing.T) {
		cache, store := newMemoryIdentityCache(), newMemoryClientIdentityStore()
		cache.fingerprint[12] = &Fingerprint{ClientID: strings.Repeat("cc", 32), UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Unix()}
		after, err := ProvideIdentityService(cache, store).ResetClientIdentity(ctx, p2Account(12, "owner"))
		require.NoError(t, err)
		require.NotEqual(t, strings.Repeat("cc", 32), after.ClientID)
		require.Equal(t, int64(1), after.IdentityEpoch)
	})

	t.Run("nothing to reset", func(t *testing.T) {
		after, err := ProvideIdentityService(newMemoryIdentityCache(), newMemoryClientIdentityStore()).ResetClientIdentity(ctx, p2Account(13, "owner"))
		require.NoError(t, err)
		require.Nil(t, after)
	})

	t.Run("requires the identity store", func(t *testing.T) {
		_, err := NewIdentityService(newMemoryIdentityCache()).ResetClientIdentity(ctx, p2Account(14, "owner"))
		require.Error(t, err)
	})
}

// bumpPatch 把 claude-cli UA 的补丁号加一，得到一个比当前版本新、仍在允许范围内的 UA。
func bumpPatch(t *testing.T, ua string) string {
	t.Helper()
	major, minor, patch, ok := parseUserAgentVersion(ua)
	require.True(t, ok)
	return fmt.Sprintf("claude-cli/%d.%d.%d (external, cli)", major, minor, patch+1)
}
