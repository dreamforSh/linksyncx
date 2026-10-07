package service

import (
	"context"
	"sync"
	"time"
)

// ClaudeRateLimitCache 是限流快照的热存储（Redis，多实例共享）。
type ClaudeRateLimitCache interface {
	// ApplyClaudeRateLimitSnapshot 只在快照比已存的更晚收到（AppliedAtMs 更大）时写入，
	// 返回是否写入及写入前的快照（没有时为 nil）。
	ApplyClaudeRateLimitSnapshot(ctx context.Context, accountID int64, snap *ClaudeRateLimitSnapshot) (applied bool, previous *ClaudeRateLimitSnapshot, err error)
	// ClaimClaudeRateLimitPersist 领取一次落库机会：force 时直接领取并重新计时；否则距上次领取
	// 不足 interval 时返回 false。多实例共用，同一周期内只有一个实例落库。
	ClaimClaudeRateLimitPersist(ctx context.Context, accountID int64, interval time.Duration, force bool) (bool, error)
}

// ClaudeRateLimitPatch 是一次限流快照落库：Extra 合并写入 accounts.extra（含快照本身），可选地更新
// 5h 会话窗口列。只有 AppliedAtMs 大于库中快照的 applied_at_ms 时才写入，慢请求不能用旧响应头覆盖。
type ClaudeRateLimitPatch struct {
	AppliedAtMs         int64
	SessionWindowStatus string     // 空表示不修改
	SessionWindowStart  *time.Time // nil 表示不修改
	SessionWindowEnd    *time.Time // nil 表示不修改
	Extra               map[string]any
}

// claudeRateLimitLocalTracker 是热存储不可用或未配置时的进程内替代：同样的栅栏与落库节流，
// 只在本实例内生效；跨实例的顺序仍由落库时的栅栏保证。
type claudeRateLimitLocalTracker struct {
	mu          sync.Mutex
	latest      map[int64]*ClaudeRateLimitSnapshot
	persistedAt map[int64]time.Time
}

func (t *claudeRateLimitLocalTracker) apply(accountID int64, snap *ClaudeRateLimitSnapshot) (bool, *ClaudeRateLimitSnapshot) {
	t.mu.Lock()
	defer t.mu.Unlock()
	prev := t.latest[accountID]
	if prev != nil && prev.AppliedAtMs >= snap.AppliedAtMs {
		return false, prev
	}
	if t.latest == nil {
		t.latest = make(map[int64]*ClaudeRateLimitSnapshot)
	}
	t.latest[accountID] = snap
	return true, prev
}

func (t *claudeRateLimitLocalTracker) claimPersist(accountID int64, now time.Time, interval time.Duration, force bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.persistedAt[accountID]
	if !force && ok && now.Sub(last) < interval {
		return false
	}
	if t.persistedAt == nil {
		t.persistedAt = make(map[int64]time.Time)
	}
	t.persistedAt[accountID] = now
	return true
}
