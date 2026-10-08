package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Claude OAuth 订阅档位来自 GET /api/oauth/profile 的 organization.organization_type 与
// rate_limit_tier，映射与 Claude Code 一致：claude_max→max、claude_pro→pro、claude_team→team、
// claude_enterprise→enterprise；rate_limit_tier=default_claude_max_20x 即 Max 20x。
// Team 席位同样按 Claude Code 的 isTeamPremiumSubscriber 判定：rate_limit_tier=default_claude_max_5x
// 为高级席，其余已知 tier 为标准席；seat_tier 不参与判定。
// 归一化后写入 extra.claude_subscription，账号列表据此显示档位徽章。

const (
	claudeSubscriptionExtraKey = "claude_subscription"
	// 档位极少变化：超过一天才在读取用量时后台重新拉取。
	claudeSubscriptionStaleAfter = 24 * time.Hour
)

const (
	ClaudePlanFree         = "free"
	ClaudePlanPro          = "pro"
	ClaudePlanMax          = "max"
	ClaudePlanMax5x        = "max_5x"
	ClaudePlanMax20x       = "max_20x"
	ClaudePlanTeam         = "team" // rate_limit_tier 缺失，无法区分席位
	ClaudePlanTeamStandard = "team_standard"
	ClaudePlanTeamPremium  = "team_premium"
	ClaudePlanEnterprise   = "enterprise"
)

// claudeTeamPremiumRateLimitTier 是 Team 高级席的 rate_limit_tier（与 Max 5x 同档）。
const claudeTeamPremiumRateLimitTier = "default_claude_max_5x"

// ClaudeSubscriptionInfo 是账号订阅档位的归一化快照（extra.claude_subscription）。
type ClaudeSubscriptionInfo struct {
	PlanType          string `json:"plan_type"`
	OrganizationType  string `json:"organization_type,omitempty"`
	RateLimitTier     string `json:"rate_limit_tier,omitempty"`
	SeatTier          string `json:"seat_tier,omitempty"`
	BillingType       string `json:"billing_type,omitempty"`
	ExtraUsageEnabled *bool  `json:"extra_usage_enabled,omitempty"`
	UpdatedAt         string `json:"updated_at"`
}

// claudeOAuthProfile 是 /api/oauth/profile 的宽松投影：订阅相关字段按原始 JSON 读取，
// 类型异常时按缺失处理，不让一个字段拖垮整份档位。
type claudeOAuthProfile struct {
	Account struct {
		HasClaudeMax json.RawMessage `json:"has_claude_max"`
		HasClaudePro json.RawMessage `json:"has_claude_pro"`
	} `json:"account"`
	Organization struct {
		UUID                 json.RawMessage `json:"uuid"`
		OrganizationType     json.RawMessage `json:"organization_type"`
		RateLimitTier        json.RawMessage `json:"rate_limit_tier"`
		SeatTier             json.RawMessage `json:"seat_tier"`
		BillingType          json.RawMessage `json:"billing_type"`
		HasExtraUsageEnabled json.RawMessage `json:"has_extra_usage_enabled"`
	} `json:"organization"`
}

// parseClaudeOAuthProfile 解析 profile 响应，返回归一化档位与组织 UUID。
func parseClaudeOAuthProfile(body []byte, now time.Time) (*ClaudeSubscriptionInfo, string, error) {
	var profile claudeOAuthProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, "", fmt.Errorf("decode oauth profile: %w", err)
	}
	org := profile.Organization
	orgType := strings.ToLower(jsonString(org.OrganizationType))
	tier := jsonString(org.RateLimitTier)
	info := &ClaudeSubscriptionInfo{
		PlanType: claudePlanTypeFromProfile(
			orgType,
			tier,
			jsonBool(profile.Account.HasClaudeMax, false),
			jsonBool(profile.Account.HasClaudePro, false),
		),
		OrganizationType: orgType,
		RateLimitTier:    tier,
		SeatTier:         jsonString(org.SeatTier),
		BillingType:      jsonString(org.BillingType),
		UpdatedAt:        now.UTC().Format(time.RFC3339),
	}
	if !isJSONNullOrEmpty(org.HasExtraUsageEnabled) {
		enabled := jsonBool(org.HasExtraUsageEnabled, false)
		info.ExtraUsageEnabled = &enabled
	}
	return info, jsonString(org.UUID), nil
}

func claudePlanTypeFromProfile(orgType, rateLimitTier string, hasMax, hasPro bool) string {
	switch orgType {
	case "claude_max":
		return claudeMaxPlanType(rateLimitTier)
	case "claude_pro":
		return ClaudePlanPro
	case "claude_team":
		return claudeTeamPlanType(rateLimitTier)
	case "claude_enterprise":
		return ClaudePlanEnterprise
	}
	switch {
	case hasMax:
		return claudeMaxPlanType(rateLimitTier)
	case hasPro:
		return ClaudePlanPro
	case orgType == "":
		return ClaudePlanFree
	default:
		// 未知的组织类型保留原值（去掉 claude_ 前缀），前端原样展示。
		return strings.TrimPrefix(orgType, "claude_")
	}
}

func claudeMaxPlanType(rateLimitTier string) string {
	tier := strings.ToLower(rateLimitTier)
	switch {
	case strings.Contains(tier, "20x"):
		return ClaudePlanMax20x
	case strings.Contains(tier, "5x"):
		return ClaudePlanMax5x
	default:
		return ClaudePlanMax
	}
}

func claudeTeamPlanType(rateLimitTier string) string {
	tier := strings.TrimSpace(rateLimitTier)
	switch {
	case strings.EqualFold(tier, claudeTeamPremiumRateLimitTier):
		return ClaudePlanTeamPremium
	case tier != "":
		return ClaudePlanTeamStandard
	default:
		return ClaudePlanTeam
	}
}

// claudePlanDisplayName 是档位的展示名，用于 UsageInfo.SubscriptionTier（配额监控的套餐等级）。
func claudePlanDisplayName(plan string) string {
	switch plan {
	case ClaudePlanFree:
		return "Free"
	case ClaudePlanPro:
		return "Pro"
	case ClaudePlanMax:
		return "Max"
	case ClaudePlanMax5x:
		return "Max 5x"
	case ClaudePlanMax20x:
		return "Max 20x"
	case ClaudePlanTeam:
		return "Team"
	case ClaudePlanTeamStandard:
		return "Team Standard"
	case ClaudePlanTeamPremium:
		return "Team Premium"
	case ClaudePlanEnterprise:
		return "Enterprise"
	default:
		return plan
	}
}

// readClaudeSubscription 读取 extra.claude_subscription；值可能是数据库读出的 map，
// 也可能是进程内合并的结构体，统一经 JSON 解析。
func readClaudeSubscription(extra map[string]any) *ClaudeSubscriptionInfo {
	raw, ok := extra[claudeSubscriptionExtraKey]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var info ClaudeSubscriptionInfo
	if err := json.Unmarshal(data, &info); err != nil || strings.TrimSpace(info.PlanType) == "" {
		return nil
	}
	return &info
}

// claudeSubscriptionIsStale 档位缺失、时间无法解析或超过一天时需要重新拉取。
// 区分 Team 席位之前写入的快照（plan_type=team 但带 rate_limit_tier）也立即重拉。
func claudeSubscriptionIsStale(extra map[string]any, now time.Time) bool {
	info := readClaudeSubscription(extra)
	if info == nil {
		return true
	}
	if info.PlanType == ClaudePlanTeam && info.OrganizationType == "claude_team" && strings.TrimSpace(info.RateLimitTier) != "" {
		return true
	}
	updatedAt, err := time.Parse(time.RFC3339, info.UpdatedAt)
	if err != nil {
		return true
	}
	return now.Sub(updatedAt) >= claudeSubscriptionStaleAfter
}

// applyClaudeSubscriptionToUsage 把已知档位写进 UsageInfo 的订阅等级字段。
func applyClaudeSubscriptionToUsage(usage *UsageInfo, extra map[string]any) {
	if usage == nil {
		return
	}
	info := readClaudeSubscription(extra)
	if info == nil {
		return
	}
	usage.SubscriptionTier = claudePlanDisplayName(info.PlanType)
	usage.SubscriptionTierRaw = info.RateLimitTier
	if usage.SubscriptionTierRaw == "" {
		usage.SubscriptionTierRaw = info.OrganizationType
	}
}
