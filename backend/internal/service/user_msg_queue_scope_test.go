//go:build unit

package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func umqTestMetadata(session string) string {
	return `{"device_id":"` + strings.Repeat("a", 64) + `","account_uuid":"","session_id":"` + session + `"}`
}

// 串行作用域：同一会话得到同一摘要，不同会话不同；metadata 优先于会话头；
// 单会话模式、scope = account、请求不带会话时退回账号级。
func TestUserMsgQueueScopeFor(t *testing.T) {
	svc := NewUserMessageQueueService(nil, nil, &config.UserMessageQueueConfig{})
	account := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	a1 := svc.ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-a")}, nil)
	a2 := svc.ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-a")}, nil)
	b := svc.ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-b")}, nil)
	require.Equal(t, int64(9), a1.AccountID)
	require.Len(t, a1.Session, umqSessionDigestBytes*2)
	require.Equal(t, a1, a2)
	require.NotEqual(t, a1.Session, b.Session)
	require.Equal(t, "9/"+a1.Session, a1.String())

	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "session-a")
	require.Equal(t, a1, svc.ScopeFor(account, &ParsedRequest{}, headers), "header fallback maps the same session to the same scope")
	require.Equal(t, b, svc.ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-b")}, headers), "metadata wins over the header")

	accountLevel := UserMsgQueueScope{AccountID: 9}
	require.Equal(t, accountLevel, svc.ScopeFor(account, &ParsedRequest{}, nil), "no session → account level")
	require.Equal(t, "9", accountLevel.String())
	headers.Set("X-Claude-Code-Session-Id", strings.Repeat("x", 129))
	require.Equal(t, accountLevel, svc.ScopeFor(account, &ParsedRequest{}, headers), "oversized header is ignored")

	single := &Account{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{"session_id_masking_enabled": true}}
	require.True(t, single.IsSessionIDMaskingEnabled())
	require.Equal(t, accountLevel, svc.ScopeFor(single, &ParsedRequest{MetadataUserID: umqTestMetadata("session-a")}, nil), "single-session mode stays account level")

	legacy := NewUserMessageQueueService(nil, nil, &config.UserMessageQueueConfig{Scope: config.UMQScopeAccount})
	require.Equal(t, accountLevel, legacy.ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-a")}, nil), "scope=account restores account-level locking")

	require.Equal(t, a1, NewUserMessageQueueService(nil, nil, nil).ScopeFor(account, &ParsedRequest{MetadataUserID: umqTestMetadata("session-a")}, nil), "nil config defaults to session scope")
}

type scopeRecordingUMQCache struct {
	cleanupWorkerUserMsgQueueCache
	lastBy   map[UserMsgQueueScope]int64
	nowMs    int64
	lastSeen []UserMsgQueueScope
}

func (c *scopeRecordingUMQCache) GetLastCompletedMs(_ context.Context, scope UserMsgQueueScope) (int64, error) {
	c.lastSeen = append(c.lastSeen, scope)
	return c.lastBy[scope], nil
}

func (c *scopeRecordingUMQCache) GetCurrentTimeMs(context.Context) (int64, error) {
	return c.nowMs, nil
}

// 最小间隔按会话计算：刚完成的会话要等，另一个会话不受影响。
func TestUserMsgQueueEnforceDelayPerSession(t *testing.T) {
	busy := UserMsgQueueScope{AccountID: 9, Session: "aa"}
	idle := UserMsgQueueScope{AccountID: 9, Session: "bb"}
	cache := &scopeRecordingUMQCache{nowMs: 10_000, lastBy: map[UserMsgQueueScope]int64{busy: 10_000}}
	svc := NewUserMessageQueueService(cache, nil, &config.UserMessageQueueConfig{MinDelayMs: 200, MaxDelayMs: 200})

	start := time.Now()
	require.NoError(t, svc.EnforceDelay(context.Background(), idle, 0))
	require.Less(t, time.Since(start), 100*time.Millisecond, "a session without history is not delayed")

	start = time.Now()
	require.NoError(t, svc.EnforceDelay(context.Background(), busy, 0))
	require.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond, "the session that just finished waits the minimum interval")
	require.Equal(t, []UserMsgQueueScope{idle, busy}, cache.lastSeen)
}
