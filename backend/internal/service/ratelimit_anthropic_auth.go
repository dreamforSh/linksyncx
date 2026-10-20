package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

// Anthropic OAuth/setup-token 边缘 403 的计数口径。
//
// 边缘 403 不是账号证据，所以永不升级为 error；但出口被拦截时这个账号的每个新请求
// 都会先撞一次 403 再换号，所以短时间内反复出现时要临时停调，别让它一直被选中。
// 计数用 10 分钟的固定窗口：被拦截的出口很快就会攒满 3 次；而偶发的单个坏请求
// 在 failover 中逐账号重放，每个账号只记 1 次，不会把整组账号都停掉。
const (
	anthropicEdge403Threshold       = 3
	anthropicEdge403WindowMinutes   = 10
	anthropicEdge403CooldownMinutes = 10
)

// claude401ForcedRefreshTimeout 401 后台强制刷新的总时限（含刷新锁等待与上游刷新调用）。
const claude401ForcedRefreshTimeout = 30 * time.Second

// cloudflareEdgeErrorCodePattern 匹配 Cloudflare 给非浏览器客户端的纯文本拦截响应，
// 如 "error code: 1010"（浏览器签名被拦）、"error code: 1020"（防火墙规则拦截）。
var cloudflareEdgeErrorCodePattern = regexp.MustCompile(`^error code: 1\d{3}$`)

var htmlTitlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// isAnthropicEdgeForbidden 判断 403 是否由 Cloudflare / 代理等边缘层返回，而不是 Anthropic API
// 本身的判定。Anthropic API 的错误体总是 JSON；HTML 拦截页、Cloudflare 纯文本错误码、
// cf-mitigated 质询都说明请求没有到达 Anthropic，与账号凭据无关。
func isAnthropicEdgeForbidden(headers http.Header, body []byte) bool {
	if headers != nil && strings.EqualFold(strings.TrimSpace(headers.Get("cf-mitigated")), "challenge") {
		return true
	}
	if isHTMLResponse(body) {
		return true
	}
	if len(body) > 64 {
		return false
	}
	return cloudflareEdgeErrorCodePattern.MatchString(strings.ToLower(strings.TrimSpace(string(body))))
}

// describeEdgeForbidden 给运维看的简短描述：HTML 页取 <title>，纯文本取原文，附 cf-ray 便于排障。
// 不把整页 HTML 塞进账号的停调原因里。
func describeEdgeForbidden(headers http.Header, body []byte) string {
	desc := ""
	if isHTMLResponse(body) {
		head := body
		if len(head) > 4096 {
			head = head[:4096]
		}
		if m := htmlTitlePattern.FindSubmatch(head); len(m) == 2 {
			desc = strings.Join(strings.Fields(string(m[1])), " ")
		}
		if desc == "" {
			desc = "HTML block page"
		}
	} else {
		desc = strings.TrimSpace(string(body))
	}
	if desc == "" {
		desc = "edge challenge"
	}
	desc = truncateString(desc, 128)
	if headers != nil {
		if ray := strings.TrimSpace(headers.Get("cf-ray")); ray != "" {
			desc += " cf-ray=" + ray
		}
	}
	return desc
}

// handleAnthropicEdge403 处理 Anthropic OAuth/setup-token 的边缘 403：只计数和临时停调，
// 永不 SetError。数据面的换号不受影响（403 始终在 failover 状态集里）。
// 返回值与 handleOpenAI403 同口径：只有真正停调时才返回 true。
func (s *RateLimitService) handleAnthropicEdge403(ctx context.Context, account *Account, headers http.Header, responseBody []byte) (shouldDisable bool) {
	desc := describeEdgeForbidden(headers, responseBody)
	// 计数器按账号 ID 建键，与 OpenAI / 国产供应商共用同一实现；Anthropic 账号只有边缘 403
	// 会计入，管理员清除限流 / 恢复账号时会一并清零（ResetOpenAI403Counter）。
	if s.openAI403CounterCache == nil {
		slog.Warn("anthropic_edge_403_counter_unavailable", "account_id", account.ID, "detail", desc)
		return false
	}
	count, err := s.openAI403CounterCache.IncrementOpenAI403Count(ctx, account.ID, anthropicEdge403WindowMinutes)
	if err != nil {
		// 计数不可用时分不清是持续拦截还是偶发，宁可只换号也不处罚账号。
		slog.Warn("anthropic_edge_403_increment_failed", "account_id", account.ID, "error", err, "detail", desc)
		return false
	}
	if count < anthropicEdge403Threshold {
		slog.Warn("anthropic_edge_403_counted",
			"account_id", account.ID,
			"count", count,
			"threshold", anthropicEdge403Threshold,
			"detail", desc,
		)
		return false
	}

	until := time.Now().Add(anthropicEdge403CooldownMinutes * time.Minute)
	reason := fmt.Sprintf("Anthropic edge 403 x%d within %dm, proxy/egress IP likely blocked: %s",
		count, anthropicEdge403WindowMinutes, desc)
	s.notifyAccountSchedulingBlocked(account, until, "anthropic_edge_403")
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("anthropic_edge_403_set_temp_unschedulable_failed", "account_id", account.ID, "error", err)
	}
	slog.Warn("anthropic_edge_403_temp_unschedulable",
		"account_id", account.ID,
		"until", until,
		"count", count,
		"detail", desc,
	)
	return true
}

// ClaudeUnauthorizedRefresher 在上游 401 后强制刷新 Claude 凭据，由 ClaudeTokenProvider 实现。
// refreshed=false 且 err=nil 表示本次没有刷新（限频、凭据已被别的路径换新、锁被占用、账号已停用等）。
type ClaudeUnauthorizedRefresher interface {
	ForceRefreshAfterUnauthorized(ctx context.Context, account *Account) (refreshed bool, err error)
}

// SetClaudeUnauthorizedRefresher 注入 401 强制刷新器（可选依赖；未注入时 401 只做缓存失效 + 冷却）。
func (s *RateLimitService) SetClaudeUnauthorizedRefresher(refresher ClaudeUnauthorizedRefresher) {
	s.claudeUnauthorizedRefresher = refresher
}

// isAnthropicRefreshableSetupToken setup-token 类型在 Antigravity 也存在，401 的刷新语义只对 Anthropic 成立。
func isAnthropicRefreshableSetupToken(account *Account) bool {
	return account != nil && account.Platform == PlatformAnthropic && ClaudeSetupTokenRefreshable(account)
}

// startClaude401ForcedRefresh 在后台强制刷新一次 401 账号的凭据。
//
// 原先 401 只清缓存 + 冷却 10 分钟：access token 被撤销但还没到期时，请求路径按 expires_at
// 判断不需要刷新，后台刷新也只处理临近过期的账号（D7），于是 401 → 冷却 → 401 一直循环到
// token 自然过期。账号状态设计的失效矩阵要求：401 先刷新，刷新失败（invalid_grant）才标错误。
//
// 放到后台执行：失败的请求照常立即换号，不为刷新多等一次往返。
func (s *RateLimitService) startClaude401ForcedRefresh(ctx context.Context, account *Account, cooldownReason string) {
	if s == nil || s.claudeUnauthorizedRefresher == nil || account == nil {
		return
	}
	snapshot := snapshotOAuthRefreshAccount(account)
	parent := context.WithoutCancel(ctx)
	run := s.claudeForcedRefreshRunner
	if run == nil {
		run = func(fn func()) { go fn() }
	}
	run(func() {
		refreshCtx, cancel := context.WithTimeout(parent, claude401ForcedRefreshTimeout)
		defer cancel()
		s.runClaude401ForcedRefresh(refreshCtx, snapshot, cooldownReason)
	})
}

func (s *RateLimitService) runClaude401ForcedRefresh(ctx context.Context, account *Account, cooldownReason string) {
	refreshed, err := s.claudeUnauthorizedRefresher.ForceRefreshAfterUnauthorized(ctx, account)
	if err != nil {
		// invalid_grant：refresh token 本身已失效（撤销 / 过期 / 被他处轮换且不是并发竞争），
		// 账号无法自愈，标错误。并发竞争已在 OAuthRefreshAPI.tryRecoverFromRefreshRace 里排除。
		if isInvalidGrantError(err) {
			s.handleAuthError(ctx, account, "Token refresh failed after 401 (invalid_grant): "+logredact.RedactText(err.Error()))
			return
		}
		// 网络、代理不可用、token 端点 5xx / 429 / 边缘拦截等：不是账号证据，保持冷却即可。
		slog.Warn("claude_401_forced_refresh_failed",
			"account_id", account.ID,
			"error", logredact.RedactText(err.Error()),
		)
		return
	}
	if !refreshed {
		slog.Info("claude_401_forced_refresh_skipped", "account_id", account.ID)
		return
	}
	slog.Info("claude_401_forced_refresh_succeeded", "account_id", account.ID)
	s.clearClaude401Cooldown(ctx, account.ID, cooldownReason)
}

// clearClaude401Cooldown 刷新成功后解除本次 401 写下的冷却，让账号立即恢复。
// 只在停调原因仍是这次 401 时才清：期间若被 429、边缘 403 或管理员规则改写成更长的停调
// （SetTempUnschedulable 只延长不缩短），原因会变，就不动。不清模型级限流。
func (s *RateLimitService) clearClaude401Cooldown(ctx context.Context, accountID int64, cooldownReason string) {
	latest, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil || latest == nil {
		slog.Warn("claude_401_cooldown_reload_failed", "account_id", accountID, "error", err)
		return
	}
	if latest.TempUnschedulableUntil == nil || !time.Now().Before(*latest.TempUnschedulableUntil) ||
		latest.TempUnschedulableReason != cooldownReason {
		return
	}
	if err := s.accountRepo.ClearTempUnschedulable(ctx, accountID); err != nil {
		slog.Warn("claude_401_cooldown_clear_failed", "account_id", accountID, "error", err)
		return
	}
	if s.tempUnschedCache != nil {
		if err := s.tempUnschedCache.DeleteTempUnsched(ctx, accountID); err != nil {
			slog.Warn("claude_401_cooldown_cache_clear_failed", "account_id", accountID, "error", err)
		}
	}
	s.notifyAccountSchedulingBlockCleared(accountID)
	slog.Info("claude_401_cooldown_cleared", "account_id", accountID)
}
