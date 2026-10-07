//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type failingIdentityCache struct{ err error }

func (c *failingIdentityCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return nil, c.err
}
func (c *failingIdentityCache) SetFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	return nil, c.err
}
func (c *failingIdentityCache) CreateFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	return nil, c.err
}
func (c *failingIdentityCache) GetOrCreateMaskedSessionID(context.Context, int64, string) (string, error) {
	return "", c.err
}
func (c *failingIdentityCache) GetOrCreateAmbientSessionID(context.Context, int64, string) (string, error) {
	return "", c.err
}
func (c *failingIdentityCache) SetLastActiveSessionID(context.Context, int64, string) error {
	return c.err
}
func (c *failingIdentityCache) GetLastActiveSessionID(context.Context, int64) (string, error) {
	return "", c.err
}
func (c *failingIdentityCache) ReplaceFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	return nil, c.err
}
func (c *failingIdentityCache) OverwriteFingerprint(context.Context, int64, *Fingerprint) error {
	return c.err
}
func (c *failingIdentityCache) DeleteAccountSessions(context.Context, int64) error {
	return c.err
}

// Redis 不可用时：拿不到持久化身份就拒绝（由调用方换号），绝不带临时随机 device_id 出站；
// 已知身份下的会话伪装失败仍退回常规重写，不阻塞请求。
func TestIdentityService_CacheFailureRefusesEphemeralIdentity(t *testing.T) {
	svc := NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(claude.DefaultUserAgent()))
	require.ErrorIs(t, err, ErrClientIdentityUnavailable)
	require.Nil(t, fp)

	deviceID := strings.Repeat("ef", 32)
	account := &Account{ID: 1, Extra: map[string]any{"account_uuid": "acc", "session_id_masking_enabled": true}}
	uid := FormatMetadataUserID(strings.Repeat("ab", 32), "acc", "11111111-2222-4333-8444-555555555555", "2.1.280")
	body, err := sjson.SetBytes([]byte(`{"model":"claude-sonnet-4-5","metadata":{}}`), "metadata.user_id", uid)
	require.NoError(t, err)
	out, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "acc", &Fingerprint{ClientID: deviceID, UserAgent: claude.DefaultUserAgent()})
	require.NoError(t, err)
	parsed := ParseMetadataUserID(gjson.GetBytes(out, "metadata.user_id").String())
	require.NotNil(t, parsed)
	require.Equal(t, deviceID, parsed.DeviceID)
	require.Equal(t, "acc", parsed.AccountUUID)
}
