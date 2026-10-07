package service

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// claudePassiveUsageKeys 是从统一限流头被动采样、落库到 accounts.extra 的字段；5h 窗口重置时，
// 本次响应没有带回的字段会被清空，避免残留上个窗口的数据。
var claudePassiveUsageKeys = []string{
	"session_window_utilization",
	"passive_usage_7d_utilization",
	"passive_usage_7d_reset",
	"passive_usage_7d_oi_utilization",
	"passive_usage_7d_oi_reset",
	"passive_usage_sampled_at",
}

// SetClaudeRateLimitCache 注入 Claude 限流快照热存储（Redis）。
func (s *RateLimitService) SetClaudeRateLimitCache(cache ClaudeRateLimitCache) {
	s.claudeRateLimitCache = cache
}

// UpdateSessionWindow 从成功响应更新 Claude 限流快照与 5h 窗口状态。调用点在收到响应头之后、
// 开始转发响应体之前，此刻即「收到响应头的时刻」。快照先写热存储（只接受更晚收到的响应头），
// 有实质变化或距上次落库满 60 秒时才落库，落库同样按收到响应头的时刻单调。
func (s *RateLimitService) UpdateSessionWindow(ctx context.Context, account *Account, headers http.Header) {
	s.updateSessionWindowAt(ctx, account, headers, time.Now())
}

func (s *RateLimitService) updateSessionWindowAt(ctx context.Context, account *Account, headers http.Header, receivedAt time.Time) {
	if s == nil || account == nil {
		return
	}
	snap, persist := s.observeClaudeRateLimit(ctx, account.ID, headers, receivedAt)
	if snap == nil {
		return
	}
	status := headers.Get("anthropic-ratelimit-unified-5h-status")
	if persist {
		s.persistClaudeRateLimit(ctx, account.ID, buildClaudeSessionWindowPatch(account, headers, snap, status, receivedAt))
	}

	// 如果状态为allowed且之前有限流，说明窗口已重置，清除限流状态
	if status == "allowed" && account.IsRateLimited() {
		if err := s.ClearRateLimit(ctx, account.ID); err != nil {
			slog.Warn("rate_limit_clear_failed", "account_id", account.ID, "error", err)
		}
	}
}

// recordClaudeRateLimitSnapshot 记录错误响应（429 等）带回的统一限流头：与成功响应写同一份快照，
// 但只落库快照本身，窗口列与限流状态仍由错误处理负责。
func (s *RateLimitService) recordClaudeRateLimitSnapshot(ctx context.Context, account *Account, headers http.Header, receivedAt time.Time) {
	if s == nil || account == nil {
		return
	}
	snap, persist := s.observeClaudeRateLimit(ctx, account.ID, headers, receivedAt)
	if snap == nil || !persist {
		return
	}
	s.persistClaudeRateLimit(ctx, account.ID, ClaudeRateLimitPatch{
		AppliedAtMs: snap.AppliedAtMs,
		Extra:       map[string]any{claudeRateLimitExtraKey: snap},
	})
}

// observeClaudeRateLimit 解析并应用一次响应的统一限流头。返回 nil 表示没有统一限流头，或响应头
// 比已应用的更早收到（并发请求乱序完成）而被丢弃；persist 表示这次应当落库。
func (s *RateLimitService) observeClaudeRateLimit(ctx context.Context, accountID int64, headers http.Header, receivedAt time.Time) (snap *ClaudeRateLimitSnapshot, persist bool) {
	snap = ParseClaudeRateLimitSnapshot(headers, receivedAt)
	if snap == nil {
		return nil, false
	}
	applied, prev := s.applyClaudeRateLimit(ctx, accountID, snap)
	if !applied {
		return nil, false
	}
	force := claudeRateLimitMaterialChange(prev, snap)
	return snap, s.claimClaudeRateLimitPersist(ctx, accountID, receivedAt, force)
}

func (s *RateLimitService) applyClaudeRateLimit(ctx context.Context, accountID int64, snap *ClaudeRateLimitSnapshot) (bool, *ClaudeRateLimitSnapshot) {
	if s.claudeRateLimitCache != nil {
		applied, prev, err := s.claudeRateLimitCache.ApplyClaudeRateLimitSnapshot(ctx, accountID, snap)
		if err == nil {
			return applied, prev
		}
		s.warnClaudeRateLimitCacheFailure("apply", accountID, err)
	}
	return s.claudeRateLimitLocal.apply(accountID, snap)
}

func (s *RateLimitService) claimClaudeRateLimitPersist(ctx context.Context, accountID int64, now time.Time, force bool) bool {
	if s.claudeRateLimitCache != nil {
		due, err := s.claudeRateLimitCache.ClaimClaudeRateLimitPersist(ctx, accountID, claudeRateLimitPersistInterval, force)
		if err == nil {
			return due
		}
		s.warnClaudeRateLimitCacheFailure("claim", accountID, err)
	}
	return s.claudeRateLimitLocal.claimPersist(accountID, now, claudeRateLimitPersistInterval, force)
}

// warnClaudeRateLimitCacheFailure 记录热存储故障（此时退回进程内栅栏与节流）；每分钟至多一条，
// 避免 Redis 故障期间逐响应刷屏。
func (s *RateLimitService) warnClaudeRateLimitCacheFailure(op string, accountID int64, err error) {
	now := time.Now().UnixNano()
	last := s.claudeRateLimitWarnedAt.Load()
	if now-last < int64(time.Minute) || !s.claudeRateLimitWarnedAt.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("claude_rate_limit_cache_failed", "op", op, "account_id", accountID, "error", err)
}

func (s *RateLimitService) persistClaudeRateLimit(ctx context.Context, accountID int64, patch ClaudeRateLimitPatch) {
	if s.accountRepo == nil {
		return
	}
	applied, err := s.accountRepo.ApplyClaudeRateLimitPatch(ctx, accountID, patch)
	if err != nil {
		slog.Warn("claude_rate_limit_persist_failed", "account_id", accountID, "error", err)
		return
	}
	if !applied {
		slog.Debug("claude_rate_limit_persist_superseded", "account_id", accountID, "applied_at_ms", patch.AppliedAtMs)
	}
}

// buildClaudeSessionWindowPatch 把成功响应的限流快照转成落库内容：快照本身、5h 会话窗口列与
// 被动采样字段。窗口的推算与此前逐响应写库时一致；status 为空（没有 5h 状态头）时只落库快照。
func buildClaudeSessionWindowPatch(account *Account, headers http.Header, snap *ClaudeRateLimitSnapshot, status string, now time.Time) ClaudeRateLimitPatch {
	patch := ClaudeRateLimitPatch{
		AppliedAtMs: snap.AppliedAtMs,
		Extra:       map[string]any{claudeRateLimitExtraKey: snap},
	}
	if status == "" {
		return patch
	}
	patch.SessionWindowStatus = status

	// 检查是否需要初始化时间窗口
	// 对于 Setup Token 账号，首次成功请求时需要预测时间窗口
	var windowStart, windowEnd *time.Time
	needInitWindow := account.SessionWindowEnd == nil || now.After(*account.SessionWindowEnd)

	// 优先使用响应头中的真实重置时间（比预测更准确）
	if resetStr := headers.Get("anthropic-ratelimit-unified-5h-reset"); resetStr != "" {
		if ts, err := strconv.ParseInt(resetStr, 10, 64); err == nil {
			// 检测可能的毫秒时间戳（秒级约为 1e9，毫秒约为 1e12）
			if ts > 1e11 {
				slog.Warn("account_session_window_header_millis_detected", "account_id", account.ID, "raw_reset", resetStr)
				ts = ts / 1000
			}
			end := time.Unix(ts, 0)
			// 校验时间戳是否在合理范围内（不早于 5h 前，不晚于 7 天后）
			minAllowed := now.Add(-5 * time.Hour)
			maxAllowed := now.Add(7 * 24 * time.Hour)
			if end.Before(minAllowed) || end.After(maxAllowed) {
				slog.Warn("account_session_window_header_out_of_range", "account_id", account.ID, "raw_reset", resetStr, "parsed_end", end)
			} else if needInitWindow || account.SessionWindowEnd == nil || !end.Equal(*account.SessionWindowEnd) {
				// 窗口需要初始化，或者真实重置时间与已存储的不同，则更新
				start := end.Add(-5 * time.Hour)
				windowStart = &start
				windowEnd = &end
				slog.Info("account_session_window_from_header", "account_id", account.ID, "window_start", start, "window_end", end, "status", status)
			}
		} else {
			slog.Warn("account_session_window_header_parse_failed", "account_id", account.ID, "raw_reset", resetStr, "error", err)
		}
	}

	// 回退：如果没有真实重置时间且需要初始化窗口，使用预测
	if windowEnd == nil && needInitWindow && (status == "allowed" || status == "allowed_warning") {
		start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
		end := start.Add(5 * time.Hour)
		windowStart = &start
		windowEnd = &end
		slog.Info("account_session_window_initialized", "account_id", account.ID, "window_start", start, "window_end", end, "status", status)
	}
	patch.SessionWindowStart, patch.SessionWindowEnd = windowStart, windowEnd

	// 窗口重置时清除旧的 utilization 和被动采样数据，避免残留上个窗口的数据
	if windowEnd != nil && needInitWindow {
		for _, key := range claudePassiveUsageKeys {
			patch.Extra[key] = nil
		}
	}
	// 被动采样：从响应头收集 5h + 7d + 7d_oi utilization，与窗口一起落库
	for key, value := range passiveUsageUpdatesFromHeaders(headers, now) {
		patch.Extra[key] = value
	}
	return patch
}
