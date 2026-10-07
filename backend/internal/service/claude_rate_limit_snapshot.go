package service

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Claude 订阅账号统一限流响应头（anthropic-ratelimit-unified-*）的完整快照，解析口径与真实
// Claude Code 一致：窗口 5h / 7d / 7d_oi / overage 的 utilization、reset、surpassed-threshold
// 都按数字解析（非数字视为缺失），另含总状态、代表窗口、fallback、upgrade-paths、overage、
// grace（宽限区）与 slow（低优先级通道）。

const (
	claudeRateLimitHeaderPrefix = "anthropic-ratelimit-unified-"
	// claudeRateLimitExtraKey 是快照落库到 accounts.extra 的键；其中 applied_at_ms 也是落库栅栏。
	claudeRateLimitExtraKey = "claude_rate_limit"
	// claudeRateLimitPersistInterval：快照没有实质变化时，至多每 60 秒落库一次。
	claudeRateLimitPersistInterval = 60 * time.Second
	// claudeRateLimitSoftSignalTTL：预警、宽限、低优先级信号只在快照一小时内有效，避免账号因不再被
	// 选中、快照不刷新而一直被降权。
	claudeRateLimitSoftSignalTTL = time.Hour
)

// claudeRateLimitWindows 是响应头里的窗口名，依次为 5 小时、7 天、7d_oi（Fable 专属 7 天窗口）与 overage。
var claudeRateLimitWindows = []string{"5h", "7d", "7d_oi", "overage"}

// ClaudeRateLimitWindow 是一个限流窗口的读数。
type ClaudeRateLimitWindow struct {
	Status             string   `json:"status,omitempty"`
	Utilization        *float64 `json:"utilization,omitempty"`
	ResetAt            int64    `json:"reset_at,omitempty"`
	SurpassedThreshold *float64 `json:"surpassed_threshold,omitempty"`
}

// ClaudeRateLimitOverage 是超额用量（按量付费）的状态，只用于排障展示，不参与调度。
type ClaudeRateLimitOverage struct {
	Status             string   `json:"status,omitempty"`
	ResetAt            int64    `json:"reset_at,omitempty"`
	DisabledReason     string   `json:"disabled_reason,omitempty"`
	Scope              string   `json:"scope,omitempty"`
	InUse              bool     `json:"in_use,omitempty"`
	MonthlyUtilization *float64 `json:"monthly_utilization,omitempty"`
	ChannelUtilization *float64 `json:"channel_utilization,omitempty"`
}

// ClaudeRateLimitGrace 是宽限区用量：任一大于 0 表示账号已越过限额、正在使用宽限额度。
type ClaudeRateLimitGrace struct {
	FiveHourUtilization float64 `json:"five_hour_utilization,omitempty"`
	SevenDayUtilization float64 `json:"seven_day_utilization,omitempty"`
}

// ClaudeRateLimitSlow 是低优先级通道（slow lane）的状态，status = active 表示请求正走低优先级。
type ClaudeRateLimitSlow struct {
	Status            string   `json:"status,omitempty"`
	Offer             string   `json:"offer,omitempty"`
	RetryAfterSeconds *float64 `json:"retry_after_seconds,omitempty"`
	MaxWaitSeconds    *float64 `json:"max_wait_seconds,omitempty"`
	BudgetUtilization *float64 `json:"budget_utilization,omitempty"`
	BudgetResetAt     int64    `json:"budget_reset_at,omitempty"`
}

// ClaudeRateLimitSnapshot 是某个时刻收到的一组统一限流头。AppliedAtMs 是收到响应头的时刻，
// 只有更晚收到的快照才能覆盖已应用的快照（与真实 CLI 宽限区跟踪器的 fenceMs 同理）。
type ClaudeRateLimitSnapshot struct {
	AppliedAtMs         int64                            `json:"applied_at_ms"`
	Status              string                           `json:"status,omitempty"`
	ResetAt             int64                            `json:"reset_at,omitempty"`
	RepresentativeClaim string                           `json:"representative_claim,omitempty"`
	FallbackAvailable   bool                             `json:"fallback_available,omitempty"`
	UpgradePaths        []string                         `json:"upgrade_paths,omitempty"`
	Windows             map[string]ClaudeRateLimitWindow `json:"windows,omitempty"`
	Overage             *ClaudeRateLimitOverage          `json:"overage,omitempty"`
	Grace               *ClaudeRateLimitGrace            `json:"grace,omitempty"`
	Slow                *ClaudeRateLimitSlow             `json:"slow,omitempty"`
}

// ParseClaudeRateLimitSnapshot 解析统一限流头；响应里没有任何统一限流头时返回 nil。
func ParseClaudeRateLimitSnapshot(headers http.Header, receivedAt time.Time) *ClaudeRateLimitSnapshot {
	if len(headers) == 0 {
		return nil
	}
	present := false
	get := func(name string) string {
		value := strings.TrimSpace(headers.Get(claudeRateLimitHeaderPrefix + name))
		if value != "" {
			present = true
		}
		return value
	}

	snap := &ClaudeRateLimitSnapshot{
		AppliedAtMs:         receivedAt.UnixMilli(),
		Status:              get("status"),
		ResetAt:             claudeRateLimitEpoch(get("reset")),
		RepresentativeClaim: get("representative-claim"),
		FallbackAvailable:   get("fallback") == "available",
	}
	if paths := get("upgrade-paths"); paths != "" {
		for _, path := range strings.Split(paths, ",") {
			if path = strings.TrimSpace(path); path != "" {
				snap.UpgradePaths = append(snap.UpgradePaths, path)
			}
		}
	}

	for _, name := range claudeRateLimitWindows {
		window := ClaudeRateLimitWindow{
			Utilization:        claudeRateLimitNumber(get(name + "-utilization")),
			ResetAt:            claudeRateLimitEpoch(get(name + "-reset")),
			SurpassedThreshold: claudeRateLimitNumber(get(name + "-surpassed-threshold")),
		}
		// overage-status 是超额用量本身的状态（见下方 Overage），不是窗口状态；overage-reset 两边共用，与 CLI 一致。
		if name != "overage" {
			window.Status = get(name + "-status")
		}
		if window != (ClaudeRateLimitWindow{}) {
			if snap.Windows == nil {
				snap.Windows = make(map[string]ClaudeRateLimitWindow, len(claudeRateLimitWindows))
			}
			snap.Windows[name] = window
		}
	}

	overage := ClaudeRateLimitOverage{
		Status:             get("overage-status"),
		ResetAt:            claudeRateLimitEpoch(get("overage-reset")),
		DisabledReason:     get("overage-disabled-reason"),
		Scope:              get("overage-scope"),
		InUse:              get("overage-in-use") == "true",
		MonthlyUtilization: claudeRateLimitNumber(get("overage-period-monthly-utilization")),
		ChannelUtilization: claudeRateLimitNumber(get("overage-period-channel-utilization")),
	}
	if overage != (ClaudeRateLimitOverage{}) {
		snap.Overage = &overage
	}

	grace := ClaudeRateLimitGrace{
		FiveHourUtilization: claudeRateLimitUnit(get("grace-5h-utilization")),
		SevenDayUtilization: claudeRateLimitUnit(get("grace-7d-utilization")),
	}
	if grace != (ClaudeRateLimitGrace{}) {
		snap.Grace = &grace
	}

	slow := ClaudeRateLimitSlow{
		Status:            claudeRateLimitSlowStatus(get("slow-status")),
		Offer:             claudeRateLimitSlowOffer(get("slow-offer")),
		RetryAfterSeconds: claudeRateLimitNonNegative(get("slow-retry-after")),
		MaxWaitSeconds:    claudeRateLimitNonNegative(get("slow-max-wait")),
		BudgetResetAt:     claudeRateLimitEpoch(get("slow-budget-reset")),
	}
	if budget := claudeRateLimitNonNegative(get("slow-budget-utilization")); budget != nil {
		capped := math.Min(1, *budget)
		slow.BudgetUtilization = &capped
	}
	if slow != (ClaudeRateLimitSlow{}) {
		snap.Slow = &slow
	}

	if !present {
		return nil
	}
	return snap
}

func claudeRateLimitNumber(raw string) *float64 {
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func claudeRateLimitNonNegative(raw string) *float64 {
	if value := claudeRateLimitNumber(raw); value != nil && *value >= 0 {
		return value
	}
	return nil
}

// claudeRateLimitUnit 把宽限区用量限制在 [0, 1]，缺失或非法为 0。
func claudeRateLimitUnit(raw string) float64 {
	value := claudeRateLimitNumber(raw)
	if value == nil {
		return 0
	}
	return math.Max(0, math.Min(1, *value))
}

// claudeRateLimitEpoch 解析 Unix 时间戳（秒，四舍五入；自动识别毫秒），缺失或非法为 0。
func claudeRateLimitEpoch(raw string) int64 {
	value := claudeRateLimitNumber(raw)
	if value == nil || *value <= 0 {
		return 0
	}
	ts := int64(math.Round(*value))
	if ts > 1e11 {
		ts /= 1000
	}
	return ts
}

func claudeRateLimitSlowStatus(raw string) string {
	switch raw {
	case "":
		return ""
	case "active", "not_needed", "slot_busy", "weekly_limit", "budget_exhausted", "ineligible", "off":
		return raw
	default:
		return "unrecognized"
	}
}

func claudeRateLimitSlowOffer(raw string) string {
	if raw == "treatment" || raw == "control" {
		return raw
	}
	return ""
}

// claudeRateLimitMaterialChange 报告新快照相对上一次已应用的快照是否有需要立即落库的变化：
// 总状态、代表窗口、任一窗口的状态 / 重置时间 / 预警阈值、用量的整数百分比，以及 overage、
// 宽限区、低优先级通道状态。用量按整数百分比比较，让自动停调阈值（整数百分比）及时生效。
func claudeRateLimitMaterialChange(prev, next *ClaudeRateLimitSnapshot) bool {
	if prev == nil || next == nil {
		return true
	}
	if prev.Status != next.Status || prev.ResetAt != next.ResetAt ||
		prev.RepresentativeClaim != next.RepresentativeClaim || prev.FallbackAvailable != next.FallbackAvailable ||
		len(prev.Windows) != len(next.Windows) {
		return true
	}
	for name, window := range next.Windows {
		old, ok := prev.Windows[name]
		if !ok || old.Status != window.Status || old.ResetAt != window.ResetAt ||
			claudeRateLimitPercent(old.Utilization) != claudeRateLimitPercent(window.Utilization) ||
			!equalOptionalFloat(old.SurpassedThreshold, window.SurpassedThreshold) {
			return true
		}
	}
	return claudeRateLimitOverageStatus(prev) != claudeRateLimitOverageStatus(next) ||
		prev.graceActive() != next.graceActive() ||
		claudeRateLimitSlowState(prev) != claudeRateLimitSlowState(next)
}

func claudeRateLimitPercent(utilization *float64) int {
	if utilization == nil {
		return -1
	}
	return int(math.Round(*utilization * 100))
}

func equalOptionalFloat(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func claudeRateLimitOverageStatus(snap *ClaudeRateLimitSnapshot) string {
	if snap.Overage == nil {
		return ""
	}
	return snap.Overage.Status
}

func claudeRateLimitSlowState(snap *ClaudeRateLimitSnapshot) string {
	if snap.Slow == nil {
		return ""
	}
	return snap.Slow.Status
}

func (s *ClaudeRateLimitSnapshot) graceActive() bool {
	return s.Grace != nil && (s.Grace.FiveHourUtilization > 0 || s.Grace.SevenDayUtilization > 0)
}

// claudeRateLimitUnderPressure 报告账号最近落库的限流快照是否带有「降低新对话优先级」的信号，
// 只看账号级窗口（5h、7d；7d_oi 与 overage 只约束部分模型或付费额度，不参与）：
//   - 代表窗口为账号级窗口时，总状态 allowed_warning；
//   - 未重置的 5h / 7d 窗口状态为 allowed_warning，或越过了预警阈值；
//   - 处于宽限区（对应窗口未重置时 grace 用量大于 0）；
//   - 进入低优先级通道（slow status = active）。
//
// 快照超过 claudeRateLimitSoftSignalTTL 未刷新时不再生效。
func claudeRateLimitUnderPressure(account *Account, now time.Time) bool {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return false
	}
	snap, ok := account.Extra[claudeRateLimitExtraKey].(map[string]any)
	if !ok {
		return false
	}
	appliedAt := int64(parseExtraFloat64(snap["applied_at_ms"]))
	if appliedAt <= 0 || now.Sub(time.UnixMilli(appliedAt)) > claudeRateLimitSoftSignalTTL {
		return false
	}

	windows, _ := snap["windows"].(map[string]any)
	// open 返回窗口读数及其是否尚未重置；窗口缺失或没有重置时间时按未重置处理。
	open := func(name string) (map[string]any, bool) {
		window, _ := windows[name].(map[string]any)
		reset := int64(parseExtraFloat64(window["reset_at"]))
		return window, reset == 0 || reset > now.Unix()
	}

	if claim, _ := snap["representative_claim"].(string); claim == "" || claim == "five_hour" || claim == "seven_day" {
		if status, _ := snap["status"].(string); status == "allowed_warning" {
			return true
		}
	}
	for _, name := range []string{"5h", "7d"} {
		window, isOpen := open(name)
		if !isOpen {
			continue
		}
		if status, _ := window["status"].(string); status == "allowed_warning" {
			return true
		}
		if _, surpassed := window["surpassed_threshold"]; surpassed {
			return true
		}
	}
	if grace, ok := snap["grace"].(map[string]any); ok {
		if _, isOpen := open("5h"); isOpen && parseExtraFloat64(grace["five_hour_utilization"]) > 0 {
			return true
		}
		if _, isOpen := open("7d"); isOpen && parseExtraFloat64(grace["seven_day_utilization"]) > 0 {
			return true
		}
	}
	if slow, ok := snap["slow"].(map[string]any); ok {
		if status, _ := slow["status"].(string); status == "active" {
			return true
		}
	}
	return false
}
