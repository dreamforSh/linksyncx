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
	"github.com/tidwall/gjson"
)

// memoryIdentityCache 是带账号级会话键的内存身份缓存，记录写入次数供节流断言。
type memoryIdentityCache struct {
	mu          sync.Mutex
	fingerprint map[int64]*Fingerprint
	masked      map[int64]string
	ambient     map[int64]string
	last        map[int64]string
	lastWrites  int
	lastErr     error
	replaces    int
	migrations  map[string]ClaudeSessionMigration
}

func newMemoryIdentityCache() *memoryIdentityCache {
	return &memoryIdentityCache{
		fingerprint: map[int64]*Fingerprint{},
		masked:      map[int64]string{},
		ambient:     map[int64]string{},
		last:        map[int64]string{},
	}
}

func (m *memoryIdentityCache) GetFingerprint(_ context.Context, id int64) (*Fingerprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fp := m.fingerprint[id]; fp != nil {
		clone := *fp
		return &clone, nil
	}
	return nil, nil
}

func (m *memoryIdentityCache) SetFingerprint(_ context.Context, id int64, fp *Fingerprint) (*Fingerprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *fp
	m.fingerprint[id] = &clone
	return &clone, nil
}

func (m *memoryIdentityCache) CreateFingerprint(ctx context.Context, id int64, fp *Fingerprint) (*Fingerprint, error) {
	if existing, _ := m.GetFingerprint(ctx, id); existing != nil {
		return existing, nil
	}
	return m.SetFingerprint(ctx, id, fp)
}

func (m *memoryIdentityCache) getOrCreate(store map[int64]string, id int64, candidate string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if store[id] == "" {
		store[id] = candidate
	}
	return store[id]
}

func (m *memoryIdentityCache) GetOrCreateMaskedSessionID(_ context.Context, id int64, candidate string) (string, error) {
	return m.getOrCreate(m.masked, id, candidate), nil
}

func (m *memoryIdentityCache) GetOrCreateAmbientSessionID(_ context.Context, id int64, candidate string) (string, error) {
	return m.getOrCreate(m.ambient, id, candidate), nil
}

func (m *memoryIdentityCache) SetLastActiveSessionID(_ context.Context, id int64, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastWrites++
	if m.lastErr != nil {
		return m.lastErr
	}
	m.last[id] = sessionID
	return nil
}

func (m *memoryIdentityCache) GetLastActiveSessionID(_ context.Context, id int64) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last[id], nil
}

// ReplaceFingerprint 与 Redis 实现同语义：存储中已是更高代次时不覆盖，返回存储值。
func (m *memoryIdentityCache) ReplaceFingerprint(_ context.Context, id int64, fp *Fingerprint) (*Fingerprint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replaces++
	if existing := m.fingerprint[id]; existing != nil && existing.IdentityEpoch > fp.IdentityEpoch {
		clone := *existing
		return &clone, nil
	}
	clone := *fp
	m.fingerprint[id] = &clone
	stored := clone
	return &stored, nil
}

func (m *memoryIdentityCache) OverwriteFingerprint(_ context.Context, id int64, fp *Fingerprint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *fp
	m.fingerprint[id] = &clone
	return nil
}

func (m *memoryIdentityCache) DeleteAccountSessions(_ context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.masked, id)
	delete(m.ambient, id)
	delete(m.last, id)
	return nil
}

func memoryMigrationKey(id int64, sessionKey string) string {
	return fmt.Sprintf("%d:%s", id, sessionKey)
}

func (m *memoryIdentityCache) GetClaudeSessionMigration(_ context.Context, id int64, sessionKey string, _ time.Duration) (*ClaudeSessionMigration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if migration, ok := m.migrations[memoryMigrationKey(id, sessionKey)]; ok {
		return &migration, nil
	}
	return nil, nil
}

func (m *memoryIdentityCache) SetClaudeSessionMigration(_ context.Context, id int64, sessionKey string, migration ClaudeSessionMigration, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.migrations == nil {
		m.migrations = map[string]ClaudeSessionMigration{}
	}
	m.migrations[memoryMigrationKey(id, sessionKey)] = migration
	return nil
}

func (m *memoryIdentityCache) DeleteClaudeSessionMigration(_ context.Context, id int64, sessionKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.migrations, memoryMigrationKey(id, sessionKey))
	return nil
}

// readFailsCreateSucceedsCache 模拟 Redis 读抖动但原子创建仍可用：创建脚本返回存储中的已有身份。
type readFailsCreateSucceedsCache struct {
	*memoryIdentityCache
	stored *Fingerprint
}

func (c *readFailsCreateSucceedsCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return nil, errors.New("redis read timeout")
}

func (c *readFailsCreateSucceedsCache) CreateFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	clone := *c.stored
	return &clone, nil
}

const p1ClientUA = "claude-cli/2.1.287 (external, cli)"

// 身份既读不到也无法持久化创建时，必须拒绝而不是带临时随机 device_id 出站。
func TestGetOrCreateFingerprintRefusesEphemeralIdentity(t *testing.T) {
	svc := NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(p1ClientUA))
	require.Nil(t, fp)
	require.ErrorIs(t, err, ErrClientIdentityUnavailable)

	// 读失败但原子创建返回了存储中的已有身份：沿用该身份，不报错也不换新设备。
	stored := &Fingerprint{ClientID: strings.Repeat("cd", 32), UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Unix()}
	svc = NewIdentityService(&readFailsCreateSucceedsCache{memoryIdentityCache: newMemoryIdentityCache(), stored: stored})
	fp, err = svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(p1ClientUA))
	require.NoError(t, err)
	require.Equal(t, stored.ClientID, fp.ClientID)
}

// 新格式 metadata.user_id 原位改写：保留客户端键序与其它字段，parent_session_id
// 用与 session_id 相同的账号作用域映射。
func TestRewriteUserIDPreservesFieldsAndMapsParentSession(t *testing.T) {
	svc := NewIdentityService(newMemoryIdentityCache())
	clientDevice := strings.Repeat("ab", 32)
	deviceID := strings.Repeat("ef", 32)
	const accountUUID = "99999999-8888-4777-8666-555555555555"
	const session, parent = "11111111-2222-4333-8444-555555555555", "66666666-7777-4888-8999-000000000000"
	uid := `{"ti":"trace","device_id":"` + clientDevice + `","account_uuid":"","session_id":"` + session +
		`","parent_session_id":"` + parent + `","tk":"tok"}`
	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":` + jsonQuote(uid) + `}}`)

	out, err := svc.RewriteUserID(body, 7, accountUUID, deviceID, p1ClientUA)
	require.NoError(t, err)
	got := gjson.GetBytes(out, "metadata.user_id").String()
	want := `{"ti":"trace","device_id":"` + deviceID + `","account_uuid":"` + accountUUID + `","session_id":"` +
		upstreamSessionIDFor(7, 0, session) + `","parent_session_id":"` + upstreamSessionIDFor(7, 0, parent) + `","tk":"tok"}`
	require.Equal(t, want, got)

	parsed := ParseMetadataUserID(got)
	require.NotNil(t, parsed)
	require.Equal(t, upstreamSessionIDFor(7, 0, parent), parsed.ParentSessionID)
}

// 单会话模式下账号只有一个会话：parent_session_id 删除，其它字段保留。
func TestRewriteUserIDWithMaskingDropsParentSession(t *testing.T) {
	cache := newMemoryIdentityCache()
	svc := NewIdentityService(cache)
	account := &Account{ID: 8, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
		Extra: map[string]any{"account_uuid": "acc", "session_id_masking_enabled": true}}
	uid := `{"device_id":"` + strings.Repeat("ab", 32) + `","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555",` +
		`"parent_session_id":"66666666-7777-4888-8999-000000000000","tk":"tok"}`
	body := []byte(`{"metadata":{"user_id":` + jsonQuote(uid) + `}}`)

	out, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "acc", &Fingerprint{ClientID: strings.Repeat("ef", 32), UserAgent: p1ClientUA})
	require.NoError(t, err)
	got := gjson.GetBytes(out, "metadata.user_id").String()
	require.False(t, gjson.Get(got, "parent_session_id").Exists())
	require.Equal(t, "tok", gjson.Get(got, "tk").String())
	require.Equal(t, cache.masked[8], gjson.Get(got, "session_id").String())
}

func TestResolveSessionIDWithoutMetadata(t *testing.T) {
	ctx := context.Background()
	account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	t.Run("client session header maps like the conversation", func(t *testing.T) {
		svc := NewIdentityService(newMemoryIdentityCache())
		const clientSession = "11111111-2222-4333-8444-555555555555"
		got := svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, clientSession)
		require.Equal(t, upstreamSessionIDFor(account.ID, 0, clientSession), got)

		// 与同一对话 messages 请求重写后的 session_id 一致。
		uid := FormatMetadataUserID(strings.Repeat("ab", 32), "", clientSession, "2.1.287")
		out, err := svc.RewriteUserID([]byte(`{"metadata":{"user_id":`+jsonQuote(uid)+`}}`), account.ID, "acc", strings.Repeat("ef", 32), p1ClientUA)
		require.NoError(t, err)
		require.Equal(t, got, ParseMetadataUserID(gjson.GetBytes(out, "metadata.user_id").String()).SessionID)
	})

	t.Run("reuses the last active session, then the ambient session", func(t *testing.T) {
		cache := newMemoryIdentityCache()
		svc := NewIdentityService(cache)
		ambient := svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, "")
		require.NotEmpty(t, ambient)
		require.Equal(t, ambient, svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, ""), "ambient session is stable")

		svc.TouchActiveSession(ctx, account.ID, "active-session")
		require.Equal(t, "active-session", svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, ""))
	})

	t.Run("single-session mode uses the masked session", func(t *testing.T) {
		cache := newMemoryIdentityCache()
		svc := NewIdentityService(cache)
		masked := &Account{ID: 10, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"session_id_masking_enabled": true}}
		got := svc.ResolveSessionIDWithoutMetadata(ctx, masked, 0, "11111111-2222-4333-8444-555555555555")
		require.Equal(t, cache.masked[10], got)
	})

	t.Run("store failure falls back to a deterministic bucket, never per-request random", func(t *testing.T) {
		svc := NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
		first := svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, "")
		require.Equal(t, first, svc.ResolveSessionIDWithoutMetadata(ctx, account, 0, ""))
		require.NotEqual(t, first, svc.ResolveSessionIDWithoutMetadata(ctx, &Account{ID: 11, Platform: PlatformAnthropic, Type: AccountTypeOAuth}, 0, ""))
	})
}

func TestTouchActiveSessionThrottlesWrites(t *testing.T) {
	ctx := context.Background()
	cache := newMemoryIdentityCache()
	svc := NewIdentityService(cache)

	svc.TouchActiveSession(ctx, 1, "s1")
	svc.TouchActiveSession(ctx, 1, "s1")
	require.Equal(t, 1, cache.lastWrites, "same session within the interval is written once")
	svc.TouchActiveSession(ctx, 1, "s2")
	require.Equal(t, 2, cache.lastWrites, "a different session is written immediately")

	cache.lastErr = errors.New("redis unavailable")
	svc.TouchActiveSession(ctx, 2, "s3")
	cache.lastErr = nil
	svc.TouchActiveSession(ctx, 2, "s3")
	require.Equal(t, "s3", cache.last[2], "a failed write is retried on the next touch")
}

// 请求构建拿不到身份时返回可换号错误，且不据此封禁账号。
func TestBuildUpstreamRequestFailsOverWhenIdentityUnavailable(t *testing.T) {
	svc := newAnthropicOAuthMappingGatewayService(&anthropicHTTPUpstreamRecorder{})
	svc.identityService = NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
	account.Extra = map[string]any{"account_uuid": "acc"}
	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	c.Request.Header.Set("User-Agent", p1ClientUA)

	_, _, err := svc.buildUpstreamRequest(context.Background(), c, account, []byte(`{"model":"claude-sonnet-4-5","messages":[]}`), "tok", "oauth", "claude-sonnet-4-5", false, false)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.True(t, failoverErr.RequestScopedTransient)
	require.True(t, failoverErr.ShouldRetryNextAccount())
}

// count_tokens 不带 metadata：会话头用客户端会话映射，与同一对话 messages 请求一致；
// 客户端也没带会话头时复用账号最近活跃会话，而不是逐请求随机。
func TestCountTokensSessionHeaderFollowsConversation(t *testing.T) {
	ctx := context.Background()
	cache := newMemoryIdentityCache()
	svc := newAnthropicOAuthMappingGatewayService(&anthropicHTTPUpstreamRecorder{})
	svc.identityService = NewIdentityService(cache)
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
	account.Extra = map[string]any{"account_uuid": "acc"}
	const clientSession = "11111111-2222-4333-8444-555555555555"

	c, _ := newAnthropicOAuthMappingTestContext("/v1/messages")
	c.Request.Header.Set("User-Agent", p1ClientUA)
	uid := FormatMetadataUserID(strings.Repeat("ab", 32), "", clientSession, "2.1.287")
	msgBody := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":` + jsonQuote(uid) + `},"messages":[{"role":"user","content":"hi"}]}`)
	msgReq, _, err := svc.buildUpstreamRequest(ctx, c, account, msgBody, "tok", "oauth", "claude-sonnet-4-5", false, false)
	require.NoError(t, err)
	conversationSession := getHeaderRaw(msgReq.Header, "X-Claude-Code-Session-Id")
	require.Equal(t, upstreamSessionIDFor(account.ID, 0, clientSession), conversationSession)

	ctBody := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	c.Request.Header.Set("X-Claude-Code-Session-Id", clientSession)
	ctReq, _, err := svc.buildCountTokensRequest(ctx, c, account, ctBody, "tok", "oauth", "claude-sonnet-4-5", false)
	require.NoError(t, err)
	require.Equal(t, conversationSession, getHeaderRaw(ctReq.Header, "X-Claude-Code-Session-Id"))

	c.Request.Header.Del("X-Claude-Code-Session-Id")
	ctReq, _, err = svc.buildCountTokensRequest(ctx, c, account, ctBody, "tok", "oauth", "claude-sonnet-4-5", true)
	require.NoError(t, err)
	require.Equal(t, conversationSession, getHeaderRaw(ctReq.Header, "X-Claude-Code-Session-Id"), "falls back to the account's last active session")
}

func jsonQuote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
