//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
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

// Storage failure must not invent an unpersisted identity or silently bypass masking.
func TestIdentityService_CacheFailurePropagatesWithoutTransientIdentity(t *testing.T) {
	svc := NewIdentityService(&failingIdentityCache{err: errors.New("redis unavailable")})
	fp, err := svc.GetOrCreateFingerprint(context.Background(), 1, headersWithUA(claude.DefaultUserAgent()))
	require.ErrorContains(t, err, "read account identity")
	require.Nil(t, fp)

	account := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"account_uuid": "acc", "session_id_masking_enabled": true}}
	uid := FormatMetadataUserID(strings.Repeat("ab", 32), "acc", "11111111-2222-4333-8444-555555555555", "2.1.280")
	body, err := sjson.SetBytes([]byte(`{"model":"claude-sonnet-4-5","metadata":{}}`), "metadata.user_id", uid)
	require.NoError(t, err)
	out, err := svc.RewriteUserIDWithMasking(context.Background(), body, account, "acc", strings.Repeat("cd", 32), claude.DefaultUserAgent())
	require.ErrorContains(t, err, "account session unavailable")
	require.Nil(t, out)
}
