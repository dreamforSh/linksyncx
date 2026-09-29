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

// Redis 不可用时身份服务降级而不是拒绝请求：指纹用本次请求的临时值，会话伪装退回常规重写。
func TestIdentityService_CacheFailureDegradesInsteadOfFailingRequest(t *testing.T) {
	svc := NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(claude.DefaultUserAgent()))
	require.NoError(t, err)
	require.NotNil(t, fp)
	require.NotEmpty(t, fp.ClientID)
	require.Equal(t, claude.DefaultUserAgent(), fp.UserAgent)

	account := &Account{ID: 1, Extra: map[string]any{"account_uuid": "acc", "session_id_masking_enabled": true}}
	uid := FormatMetadataUserID(strings.Repeat("ab", 32), "acc", "11111111-2222-4333-8444-555555555555", "2.1.280")
	body, err := sjson.SetBytes([]byte(`{"model":"claude-sonnet-4-5","metadata":{}}`), "metadata.user_id", uid)
	require.NoError(t, err)
	out, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "acc", fp.ClientID, fp.UserAgent)
	require.NoError(t, err)
	parsed := ParseMetadataUserID(gjson.GetBytes(out, "metadata.user_id").String())
	require.NotNil(t, parsed)
	require.Equal(t, fp.ClientID, parsed.DeviceID)
	require.Equal(t, "acc", parsed.AccountUUID)
}
