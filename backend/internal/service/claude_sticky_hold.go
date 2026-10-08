package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// 额度未耗尽不换号（D9）：已绑定到 Claude OAuth / setup-token 账号的对话，只因硬性原因换号
// （额度耗尽、账号失效、模型不支持、管理员策略）；软性原因（过载、短时限流、自定义规则的短时停调、
// RPM、并发排队）不再驱逐对话，而是继续使用原账号，或把可重试错误交给客户端，由客户端自行退避。

const (
	// DefaultClaudeStickyHoldMaxWaitMinutes：自定义规则触发的临时停调，预计恢复时间不超过该值时不换号。
	DefaultClaudeStickyHoldMaxWaitMinutes = 10
	// MaxClaudeStickyHoldMaxWaitMinutes 是该阈值允许填写的上限。
	MaxClaudeStickyHoldMaxWaitMinutes = 120

	// claudeStickyHoldSameAccountRetries：已绑定对话遇到软性上游错误时，在原账号上的额外重试次数。
	claudeStickyHoldSameAccountRetries = 2
	// claudeStickyHoldQueueRetryAfter：原账号并发与等待队列都满时，建议客户端的重试间隔。
	claudeStickyHoldQueueRetryAfter = 5 * time.Second
	// claudeStickyHoldUpstreamRetryAfter：原账号上游软性错误重试用尽、上游未给出重试时间时的默认值。
	claudeStickyHoldUpstreamRetryAfter = 10 * time.Second

	claudeStickyHoldSettingsCacheTTL  = 60 * time.Second
	claudeStickyHoldSettingsErrorTTL  = 5 * time.Second
	claudeStickyHoldSettingsDBTimeout = 5 * time.Second
)

// ClaudeStickyHoldSettings 是「额度未耗尽不换号」的系统设置。
type ClaudeStickyHoldSettings struct {
	Enabled bool
	// MaxWait：自定义规则触发的临时停调（含模型级），剩余时间不超过它时不换号、返回可重试错误；
	// 超过时按账号失效换号。
	MaxWait time.Duration
}

func defaultClaudeStickyHoldSettings() ClaudeStickyHoldSettings {
	return ClaudeStickyHoldSettings{Enabled: true, MaxWait: DefaultClaudeStickyHoldMaxWaitMinutes * time.Minute}
}

type cachedClaudeStickyHoldSettings struct {
	value     ClaudeStickyHoldSettings
	expiresAt int64
}

// GetClaudeStickyHoldSettings 读取「额度未耗尽不换号」设置（进程内缓存 60 秒；读库失败用默认值）。
func (s *SettingService) GetClaudeStickyHoldSettings(ctx context.Context) ClaudeStickyHoldSettings {
	if s == nil || s.settingRepo == nil {
		return defaultClaudeStickyHoldSettings()
	}
	if cached, ok := s.claudeStickyHoldCache.Load().(*cachedClaudeStickyHoldSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.value
	}
	result, _, _ := s.claudeStickyHoldSF.Do(SettingKeyClaudeStickyHoldEnabled, func() (any, error) {
		if cached, ok := s.claudeStickyHoldCache.Load().(*cachedClaudeStickyHoldSettings); ok && cached != nil && time.Now().UnixNano() < cached.expiresAt {
			return cached.value, nil
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeStickyHoldSettingsDBTimeout)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, []string{SettingKeyClaudeStickyHoldEnabled, SettingKeyClaudeStickyHoldMaxWaitMinutes})
		ttl := claudeStickyHoldSettingsCacheTTL
		value := defaultClaudeStickyHoldSettings()
		if err != nil {
			slog.Warn("failed to get claude sticky hold settings, falling back to defaults", "error", err)
			ttl = claudeStickyHoldSettingsErrorTTL
		} else {
			value = parseClaudeStickyHoldSettings(values[SettingKeyClaudeStickyHoldEnabled], values[SettingKeyClaudeStickyHoldMaxWaitMinutes])
		}
		s.storeClaudeStickyHoldSettings(value, ttl)
		return value, nil
	})
	if value, ok := result.(ClaudeStickyHoldSettings); ok {
		return value
	}
	return defaultClaudeStickyHoldSettings()
}

func (s *SettingService) storeClaudeStickyHoldSettings(value ClaudeStickyHoldSettings, ttl time.Duration) {
	s.claudeStickyHoldCache.Store(&cachedClaudeStickyHoldSettings{value: value, expiresAt: time.Now().Add(ttl).UnixNano()})
}

// parseClaudeStickyHoldSettings 解析存储值：开关缺省为开启，阈值缺省或非法时为默认值。
func parseClaudeStickyHoldSettings(enabledRaw, maxWaitRaw string) ClaudeStickyHoldSettings {
	settings := defaultClaudeStickyHoldSettings()
	if v := strings.TrimSpace(enabledRaw); v != "" {
		settings.Enabled = v == "true"
	}
	settings.MaxWait = time.Duration(parseClaudeStickyHoldMaxWaitMinutes(maxWaitRaw)) * time.Minute
	return settings
}

func parseClaudeStickyHoldMaxWaitMinutes(raw string) int {
	var minutes int
	if _, err := fmt.Sscan(strings.TrimSpace(raw), &minutes); err != nil || validateClaudeStickyHoldMaxWaitMinutes(minutes) != nil {
		return DefaultClaudeStickyHoldMaxWaitMinutes
	}
	return minutes
}

func validateClaudeStickyHoldMaxWaitMinutes(minutes int) error {
	if minutes < 1 || minutes > MaxClaudeStickyHoldMaxWaitMinutes {
		return fmt.Errorf("claude_sticky_hold_max_wait_minutes must be between 1 and %d", MaxClaudeStickyHoldMaxWaitMinutes)
	}
	return nil
}

// ClaudeStickyHoldError 表示已绑定对话的原账号因软性原因暂时不能接单：不换号，由入口转成可重试错误
// （Anthropic 协议为 529 overloaded_error + x-should-retry + retry-after）。
type ClaudeStickyHoldError struct {
	AccountID  int64
	Reason     string
	RetryAfter time.Duration
}

func (e *ClaudeStickyHoldError) Error() string {
	return fmt.Sprintf("bound account %d temporarily unavailable (%s), retry after %s", e.AccountID, e.Reason, e.RetryAfter)
}

// RetryAfterSeconds 返回向上取整的重试秒数，至少 1 秒。
func (e *ClaudeStickyHoldError) RetryAfterSeconds() int {
	if e == nil || e.RetryAfter <= time.Second {
		return 1
	}
	return int(math.Ceil(e.RetryAfter.Seconds()))
}

// AsClaudeStickyHoldError 判断错误是否为 ClaudeStickyHoldError。
func AsClaudeStickyHoldError(err error) (*ClaudeStickyHoldError, bool) {
	var holdErr *ClaudeStickyHoldError
	if errors.As(err, &holdErr) && holdErr != nil {
		return holdErr, true
	}
	return nil, false
}

type claudeStickyHoldCtxKey struct{}

// WithClaudeStickyHold 声明调用方能处理 ClaudeStickyHoldError：只有声明过的入口才启用「额度未耗尽不换号」，
// 其它入口保持原有换号行为。
func WithClaudeStickyHold(ctx context.Context) context.Context {
	return context.WithValue(ctx, claudeStickyHoldCtxKey{}, true)
}

func claudeStickyHoldRequested(ctx context.Context) bool {
	enabled, _ := ctx.Value(claudeStickyHoldCtxKey{}).(bool)
	return enabled
}

// ClaudeStickyHoldApplies 报告本次请求里，绑定在该账号上的对话是否按「额度未耗尽不换号」处理。
func (s *GatewayService) ClaudeStickyHoldApplies(ctx context.Context, account *Account) bool {
	if s == nil || account == nil || !account.IsAnthropicOAuthOrSetupToken() || !claudeStickyHoldRequested(ctx) {
		return false
	}
	return s.settingService.GetClaudeStickyHoldSettings(ctx).Enabled
}

// ClaudeStickySameAccountRetries 是已绑定对话遇到软性上游错误时在原账号上的额外重试次数。
func ClaudeStickySameAccountRetries() int { return claudeStickyHoldSameAccountRetries }

// IsClaudeStickySoftFailure 判定一次上游失败对已绑定对话是否只是软性原因（不换号、在原账号重试）：
// 529、5xx、瞬时传输错误，以及没有表明额度耗尽的 429。凭据失败、401 / 403、账号不可用
// （代理失效等）、额度耗尽的 429 都是硬性原因。
func IsClaudeStickySoftFailure(err *UpstreamFailoverError) bool {
	if err == nil || err.IsCredentialFailure() || err.AccountUnavailable {
		return false
	}
	switch {
	case err.StatusCode == http.StatusTooManyRequests:
		return !claudeRateLimitHeadersExhausted(err.ResponseHeaders) && !isAnthropicCreditsRequiredBody(err.ResponseBody)
	case err.StatusCode == 529, err.StatusCode >= 500:
		return true
	default:
		return false
	}
}

// ClaudeStickyUpstreamRetryAfter 返回软性上游错误重试用尽后建议客户端的重试间隔：优先采用上游 retry-after。
func ClaudeStickyUpstreamRetryAfter(err *UpstreamFailoverError) time.Duration {
	if err != nil && err.ResponseHeaders != nil {
		var seconds float64
		if _, scanErr := fmt.Sscan(strings.TrimSpace(err.ResponseHeaders.Get("retry-after")), &seconds); scanErr == nil && seconds > 0 && seconds <= 600 {
			return time.Duration(seconds * float64(time.Second))
		}
	}
	return claudeStickyHoldUpstreamRetryAfter
}

// claudeRateLimitHeadersExhausted 报告响应头是否表明额度耗尽：总状态 rejected，或 5h / 7d / 7d_oi
// 任一窗口 rejected 或用量达到 100%。
func claudeRateLimitHeadersExhausted(headers http.Header) bool {
	snap := ParseClaudeRateLimitSnapshot(headers, time.Now())
	if snap == nil {
		return false
	}
	if snap.Status == "rejected" {
		return true
	}
	for _, name := range []string{"5h", "7d", "7d_oi"} {
		window := snap.Windows[name]
		if window.Status == "rejected" || (window.Utilization != nil && *window.Utilization >= 1-1e-9) {
			return true
		}
	}
	return false
}

func isAnthropicCreditsRequiredBody(body []byte) bool {
	return len(body) > 0 && strings.EqualFold(strings.TrimSpace(gjson.GetBytes(body, "error.details.error_code").String()), "credits_required")
}

// claudeRateLimitExhausted 报告账号当前处于的账号级限流是否属于额度耗尽（应换号）：最近的限流快照
// 显示总状态 rejected，或 5h / 7d 窗口 rejected / 用量达到 100%，或 5h 会话窗口状态为 rejected。
// 没有限流快照时沿用旧行为，任何限流都视为耗尽。
func claudeRateLimitExhausted(account *Account, now time.Time) bool {
	if account.SessionWindowEnd != nil && now.Before(*account.SessionWindowEnd) && strings.EqualFold(account.SessionWindowStatus, "rejected") {
		return true
	}
	snap, ok := account.Extra[claudeRateLimitExtraKey].(map[string]any)
	if !ok {
		return true
	}
	if status, _ := snap["status"].(string); status == "rejected" {
		return true
	}
	windows, _ := snap["windows"].(map[string]any)
	for _, name := range []string{"5h", "7d"} {
		window, _ := windows[name].(map[string]any)
		if reset := int64(parseExtraFloat64(window["reset_at"])); reset != 0 && reset <= now.Unix() {
			continue
		}
		if status, _ := window["status"].(string); status == "rejected" {
			return true
		}
		if parseExtraFloat64(window["utilization"]) >= 1-1e-9 {
			return true
		}
	}
	return false
}

// isCustomRuleTempUnschedReason 报告停调原因是否来自管理员配置的临时不可调度规则：这类原因是
// TempUnschedState 的 JSON（带 rule_index）。
func isCustomRuleTempUnschedReason(reason string) bool {
	reason = strings.TrimSpace(reason)
	if !strings.HasPrefix(reason, "{") {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(reason), &fields) != nil {
		return false
	}
	_, ok := fields["rule_index"]
	return ok
}

// claudeModelRateLimitHold 汇总本次请求模型上生效的模型级限流：返回剩余时间，以及是否全部来自
// 自定义规则（只有这种情况才可能不换号）。
func claudeModelRateLimitHold(ctx context.Context, account *Account, requestedModel string, now time.Time) (remaining time.Duration, allCustomRule bool, limited bool) {
	limits, _ := account.Extra[modelRateLimitsKey].(map[string]any)
	allCustomRule = true
	for _, key := range account.modelRateLimitKeysForRequest(ctx, requestedModel) {
		resetAt := account.modelRateLimitResetAt(key)
		if resetAt == nil || !now.Before(*resetAt) {
			continue
		}
		limited = true
		if left := resetAt.Sub(now); left > remaining {
			remaining = left
		}
		entry, _ := limits[key].(map[string]any)
		if reason, _ := entry["reason"].(string); !isCustomRuleTempUnschedReason(reason) {
			allCustomRule = false
		}
	}
	return remaining, allCustomRule && limited, limited
}

// stickyHoldEnv 是 SelectAccountWithLoadAwareness 已算好、判定粘性账号时需要复用的上下文。
type stickyHoldEnv struct {
	groupID             *int64
	sessionHash         string
	requestedModel      string
	platform            string
	useMixed            bool
	routingAccountIDs   []int64
	isChannelRestricted func(*Account) bool
	registerSession     func(*Account) bool
	cfg                 stickyHoldSchedulingConfig
}

type stickyHoldSchedulingConfig struct {
	maxWaiting  int
	waitTimeout time.Duration
}

// selectBoundClaudeAccount 处理已绑定到 Claude OAuth / setup-token 账号的对话（D9）。handled 为 false 时
// 交回原有选号流程：不适用、或遇到硬性原因（随后照常换号）。handled 为 true 时，要么继续使用原账号，
// 要么返回 ClaudeStickyHoldError 让客户端稍后重试，不换号。
func (s *GatewayService) selectBoundClaudeAccount(ctx context.Context, stickyAccountID int64, account *Account, env stickyHoldEnv) (*AccountSelectionResult, error, bool) {
	if env.sessionHash == "" || stickyAccountID <= 0 || !claudeStickyHoldRequested(ctx) {
		return nil, nil, false
	}
	if account == nil {
		account = s.loadBoundAccount(ctx, stickyAccountID, env.groupID)
	}
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return nil, nil, false
	}
	settings := s.settingService.GetClaudeStickyHoldSettings(ctx)
	if !settings.Enabled {
		return nil, nil, false
	}
	now := time.Now()
	hold := func(reason string, retryAfter time.Duration) (*AccountSelectionResult, error, bool) {
		slog.Debug("sticky.claude_hold", "account_id", account.ID, "reason", reason, "retry_after", retryAfter)
		return nil, &ClaudeStickyHoldError{AccountID: account.ID, Reason: reason, RetryAfter: retryAfter}, true
	}

	// 硬性原因：账号失效、管理员策略、模型不支持、额度耗尽 → 交回原流程换号。
	if !account.IsActive() || !account.Schedulable ||
		(account.AutoPauseOnExpired && account.ExpiresAt != nil && !now.Before(*account.ExpiresAt)) ||
		(len(env.routingAccountIDs) > 0 && !containsInt64(env.routingAccountIDs, account.ID)) ||
		!s.isGatewayAccountProfitEligible(ctx, account) ||
		!s.isAccountAllowedForPlatform(account, env.platform, env.useMixed) ||
		(env.requestedModel != "" && !s.isModelSupportedByAccountWithContext(ctx, account, env.requestedModel)) ||
		(env.isChannelRestricted != nil && env.isChannelRestricted(account)) ||
		!s.isAccountSchedulableForWindowCost(ctx, account, true) ||
		s.isAccountBlockedBySchedulingThreshold(ctx, account) {
		return nil, nil, false
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		if claudeRateLimitExhausted(account, now) {
			return nil, nil, false
		}
		return hold("rate_limited", account.RateLimitResetAt.Sub(now))
	}
	if account.TempUnschedulableUntil != nil && now.Before(*account.TempUnschedulableUntil) {
		left := account.TempUnschedulableUntil.Sub(now)
		if !isCustomRuleTempUnschedReason(account.TempUnschedulableReason) || left > settings.MaxWait {
			return nil, nil, false
		}
		return hold("temp_unschedulable", left)
	}
	if left, customRule, limited := claudeModelRateLimitHold(ctx, account, env.requestedModel, now); limited {
		if !customRule || left > settings.MaxWait {
			return nil, nil, false
		}
		return hold("model_temp_unschedulable", left)
	}
	// 软性原因：过载冷却只影响新对话选号，已绑定对话继续打原账号（真实 CLI 遇到 529 也在同一账号退避重试）。
	if !s.isAccountSchedulableForRPM(ctx, account, true) {
		return hold("rpm", time.Duration(60-now.Second())*time.Second)
	}

	result, err := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if err == nil && result.Acquired {
		env.registerSession(account)
		if s.cache != nil {
			_ = s.cache.RefreshSessionTTL(ctx, derefGroupID(env.groupID), env.sessionHash, stickySessionTTL)
		}
		selection, selErr := s.newSelectionResult(ctx, account, true, result.ReleaseFunc, nil)
		return selection, selErr, true
	}
	if s.concurrencyService != nil {
		if waiting, _ := s.concurrencyService.GetAccountWaitingCount(ctx, account.ID); waiting < env.cfg.maxWaiting {
			env.registerSession(account)
			selection, selErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
				AccountID:      account.ID,
				MaxConcurrency: account.Concurrency,
				Timeout:        env.cfg.waitTimeout,
				MaxWaiting:     env.cfg.maxWaiting,
			})
			return selection, selErr, true
		}
	}
	return hold("queue_full", claudeStickyHoldQueueRetryAfter)
}

// loadBoundAccount 读取不在候选列表里的绑定账号（过载、限流或停调的账号会被调度快照剔出候选列表）。
// 候选列表按分组过滤，这里同样只接受仍属于本分组的账号：账号被移出分组后绑定照常失效。
func (s *GatewayService) loadBoundAccount(ctx context.Context, accountID int64, groupID *int64) *Account {
	if !GroupAccountAllowedForRequest(ctx, accountID) {
		return nil
	}
	var (
		account *Account
		err     error
	)
	if s.schedulerSnapshot != nil {
		account, err = s.schedulerSnapshot.GetAccount(ctx, accountID)
	} else if s.accountRepo != nil {
		account, err = s.accountRepo.GetByID(ctx, accountID)
	}
	if err != nil || account == nil || !boundAccountInGroup(account, groupID) {
		return nil
	}
	return account
}

// boundAccountInGroup 按 GroupIDs（调度快照保留）或 AccountGroups 判断分组归属；groupID 为 nil 时
// 只接受未分组账号。
func boundAccountInGroup(account *Account, groupID *int64) bool {
	if groupID == nil {
		return len(account.GroupIDs) == 0 && len(account.AccountGroups) == 0
	}
	if containsInt64(account.GroupIDs, *groupID) {
		return true
	}
	for _, ag := range account.AccountGroups {
		if ag.GroupID == *groupID {
			return true
		}
	}
	return false
}
