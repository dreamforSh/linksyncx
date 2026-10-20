package service

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// claudeUnauthorizedRefreshInterval 同一账号两次 401 强制刷新之间的最小间隔。
//
// 刷新成功会立即解除冷却；如果 401 其实与 token 无关（例如上游对整个组织拒绝 OAuth），
// 刷新后马上又是 401。没有这个间隔，就会变成「每个请求刷新一次」：频繁轮换 refresh token
// 在上游看来很反常，也是风控信号。30 分钟内最多强刷一次，之后的 401 只走冷却。
const claudeUnauthorizedRefreshInterval = 30 * time.Minute

var errClaudeUnauthorizedRefreshThrottled = errors.New("claude 401 forced refresh throttled")

// claudeUnauthorizedRefreshLimiter Redis 不可用时的进程内限频。
type claudeUnauthorizedRefreshLimiter struct {
	mu   sync.Mutex
	last map[int64]time.Time
}

func (l *claudeUnauthorizedRefreshLimiter) acquire(accountID int64, now time.Time, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.last[accountID]; ok && now.Sub(last) < interval {
		return false
	}
	if l.last == nil {
		l.last = make(map[int64]time.Time)
	}
	l.last[accountID] = now
	return true
}

// claudeUnauthorizedRefreshExecutor 在 OAuthRefreshAPI 的锁内二次检查里决定是否强刷。
type claudeUnauthorizedRefreshExecutor struct {
	OAuthRefreshExecutor
	rejectedAccessToken string
	acquireSlot         func(context.Context) bool
}

// NeedsRefresh 不看 expires_at：被撤销的 token 往往离过期还早。只要库里的 access token
// 仍是被 401 拒绝的那一份就刷新；已被别的请求或后台刷新换新则不再重复刷新。
func (e *claudeUnauthorizedRefreshExecutor) NeedsRefresh(account *Account, _ time.Duration) bool {
	return account != nil && strings.TrimSpace(account.GetCredential("access_token")) == e.rejectedAccessToken
}

// Refresh 在真正调用上游前才占用限频名额：凭据已被换新、锁被占用时不消耗名额。
func (e *claudeUnauthorizedRefreshExecutor) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if !e.acquireSlot(ctx) {
		return nil, errClaudeUnauthorizedRefreshThrottled
	}
	return e.OAuthRefreshExecutor.Refresh(ctx, account)
}

// ForceRefreshAfterUnauthorized 实现 ClaudeUnauthorizedRefresher：上游 401 后强制刷新一次
// Anthropic OAuth / 带 refresh token 的 setup-token 凭据。走 OAuthRefreshAPI，与请求路径、
// 后台刷新共用分布式锁、DB 重读和 invalid_grant 竞争恢复；刷新经账号代理发出（fail-closed）。
func (p *ClaudeTokenProvider) ForceRefreshAfterUnauthorized(ctx context.Context, account *Account) (bool, error) {
	if p == nil || p.refreshAPI == nil || p.executor == nil || account == nil {
		return false, nil
	}
	if account.Platform != PlatformAnthropic || (account.Type != AccountTypeOAuth && account.Type != AccountTypeSetupToken) {
		return false, nil
	}
	if strings.TrimSpace(account.GetCredential("refresh_token")) == "" {
		return false, nil
	}
	rejected := strings.TrimSpace(account.GetCredential("access_token"))
	if rejected == "" {
		return false, nil
	}

	executor := &claudeUnauthorizedRefreshExecutor{
		OAuthRefreshExecutor: p.executor,
		rejectedAccessToken:  rejected,
		acquireSlot: func(ctx context.Context) bool {
			return p.acquireUnauthorizedRefreshSlot(ctx, account.ID)
		},
	}
	result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, executor, claudeTokenRefreshSkew)
	if errors.Is(err, errClaudeUnauthorizedRefreshThrottled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if result == nil || !result.Refreshed {
		return false, nil
	}
	// 新 token 已带新的 _token_version 落库。再清一次缓存：冷却期间别的路径可能把旧 token
	// 放回了缓存；清掉后下一个请求即使拿着旧的调度快照，也会经 CheckTokenVersion 读到新 token。
	if p.tokenCache != nil {
		if err := p.tokenCache.DeleteAccessToken(ctx, ClaudeTokenCacheKey(account)); err != nil {
			slog.Warn("claude_401_forced_refresh_cache_delete_failed", "account_id", account.ID, "error", err)
		}
	}
	return true, nil
}

// acquireUnauthorizedRefreshSlot 占用该账号的强刷名额：优先用 Redis（多实例共享），
// Redis 出错时退回进程内限频。名额靠 TTL 过期，从不主动释放。
func (p *ClaudeTokenProvider) acquireUnauthorizedRefreshSlot(ctx context.Context, accountID int64) bool {
	if p.tokenCache != nil {
		key := "claude:401_forced_refresh:account:" + strconv.FormatInt(accountID, 10)
		acquired, err := p.tokenCache.AcquireRefreshLock(ctx, key, claudeUnauthorizedRefreshInterval)
		if err == nil {
			return acquired
		}
		slog.Warn("claude_401_forced_refresh_slot_failed", "account_id", accountID, "error", err)
	}
	return p.unauthorizedRefreshLimiter.acquire(accountID, time.Now(), claudeUnauthorizedRefreshInterval)
}
