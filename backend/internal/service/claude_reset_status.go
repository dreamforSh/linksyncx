package service

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Claude 的两种重置（与 Claude Code 2.1.283 的实现一致）：
//   - cedar_ember：官方发放的免费重置券（claude.ai 设置页的「Reset for free」）。状态在
//     GET /api/oauth/usage?cedar_ember=1 的 cedar_ember 块里；只能领取上游指定的 next_grant_id，
//     领取时携带 grant_id 与 request_id。
//   - juniper_tide：部分 Max 账号每周一次的会话（5 小时）重置，属于灰度实验。状态只在触顶读取
//     （?at_wall=1）时下发，也只能在 5 小时额度用尽时领取。
// 两者都通过 POST /api/organizations/{org}/reset_rate_limits 领取。

const (
	ClaudeResetProgramCedarEmber  = "cedar_ember"
	ClaudeResetProgramJuniperTide = "juniper_tide"

	claudeResetSnapshotExtraKey = "claude_reset_snapshot"

	// 领取结果（上游原值）。
	ClaudeResetResultReset       = "reset"
	ClaudeResetResultAlreadyUsed = "already_used"
	ClaudeResetResultNotLimited  = "not_limited"
)

var (
	claudeResetGrantIDPattern   = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
	claudeResetRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	claudeResetRequestNamespace = uuid.MustParse("5b0e7f3a-8c61-4d2e-9f17-3a6c2d8e4b90")
)

// ClaudeResetGrant 是一张免费重置券。上游 grant id 只在内存中用于领取，不落盘、不返回前端。
type ClaudeResetGrant struct {
	Label            string   `json:"label,omitempty"`
	ResetsTotal      int      `json:"resets_total"`
	ResetsLeft       int      `json:"resets_left"`
	StartsAt         string   `json:"starts_at,omitempty"`
	EndsAt           string   `json:"ends_at,omitempty"`
	Clears           []string `json:"clears"`
	Paused           bool     `json:"paused"`
	UsableNow        bool     `json:"usable_now"`
	UseRequiresLimit bool     `json:"use_requires_limit"`
	Blocking         []string `json:"blocking,omitempty"`
	// Next 标记上游指定的下一张可领取的券（只能领取这一张）。
	Next bool `json:"next"`

	id string
}

// ClaudeCedarEmberStatus 是免费重置券的状态。
type ClaudeCedarEmberStatus struct {
	Eligible         bool               `json:"eligible"`
	IneligibleReason string             `json:"ineligible_reason,omitempty"`
	AtLimit          bool               `json:"at_limit"`
	Exhausted        []string           `json:"exhausted,omitempty"`
	Grants           []ClaudeResetGrant `json:"grants"`
	WeeklyResetsAt   string             `json:"weekly_resets_at,omitempty"`
	CooldownUntil    string             `json:"cooldown_until,omitempty"`
	Tier             string             `json:"tier,omitempty"`
}

// ClaudeJuniperTideStatus 是每周会话重置的状态。
type ClaudeJuniperTideStatus struct {
	Eligible         bool   `json:"eligible"`
	IneligibleReason string `json:"ineligible_reason,omitempty"`
	InExperiment     bool   `json:"in_experiment"`
	Arm              string `json:"arm,omitempty"`
	Available        bool   `json:"available"`
	NextAvailableAt  string `json:"next_available_at,omitempty"`
	WeeklyResetsAt   string `json:"weekly_resets_at,omitempty"`
	ResetsPerWeek    int    `json:"resets_per_week,omitempty"`
	Tier             string `json:"tier,omitempty"`
}

// ClaudeResetSnapshot 是一次重置状态读取的结果，写入 extra.claude_reset_snapshot。
// AtWall 表示读取时账号处于 5 小时限额（只有此时才会拿到每周会话重置的状态）。
type ClaudeResetSnapshot struct {
	FetchedAt      string                   `json:"fetched_at"`
	AtWall         bool                     `json:"at_wall"`
	AvailableCount int                      `json:"available_count"`
	CedarEmber     *ClaudeCedarEmberStatus  `json:"cedar_ember,omitempty"`
	JuniperTide    *ClaudeJuniperTideStatus `json:"juniper_tide,omitempty"`
}

// ClaudeQuotaResetResult 是一次重置领取的结果。Result 取上游原值：reset（已重置）、
// already_used（此前同一请求已生效）、not_limited（未达限额，未消耗）、cooldown、
// ineligible、unavailable。
type ClaudeQuotaResetResult struct {
	Program         string   `json:"program"`
	Result          string   `json:"result"`
	Reason          string   `json:"reason,omitempty"`
	ResetsLeft      *int     `json:"resets_left,omitempty"`
	Cleared         []string `json:"cleared,omitempty"`
	WeeklyResetsAt  string   `json:"weekly_resets_at,omitempty"`
	CooldownUntil   string   `json:"cooldown_until,omitempty"`
	NextAvailableAt string   `json:"next_available_at,omitempty"`
}

// Consumed 表示上游确认重置已生效（本次或此前同一请求）。
func (r *ClaudeQuotaResetResult) Consumed() bool {
	return r != nil && (r.Result == ClaudeResetResultReset || r.Result == ClaudeResetResultAlreadyUsed)
}

func buildClaudeResetSnapshot(resp *ClaudeUsageResponse, atWall bool, now time.Time) *ClaudeResetSnapshot {
	snapshot := &ClaudeResetSnapshot{FetchedAt: now.UTC().Format(time.RFC3339), AtWall: atWall}
	if resp != nil {
		snapshot.CedarEmber = parseClaudeCedarEmberStatus(resp.CedarEmber)
		snapshot.JuniperTide = parseClaudeJuniperTideStatus(resp.JuniperTide)
	}
	snapshot.AvailableCount = snapshot.availableCount(now)
	return snapshot
}

// availableCount 汇总仍持有的重置次数：未过期券的剩余次数（暂停的券也计入，只是暂不可用）
// 加上当前可领取的每周会话重置。
func (s *ClaudeResetSnapshot) availableCount(now time.Time) int {
	if s == nil {
		return 0
	}
	count := 0
	if s.CedarEmber != nil && s.CedarEmber.Eligible {
		for _, grant := range s.CedarEmber.Grants {
			if grant.ResetsLeft > 0 && !claudeResetExpired(grant.EndsAt, now) {
				count += grant.ResetsLeft
			}
		}
	}
	if s.JuniperTide.claimable() {
		count++
	}
	return count
}

// preferredProgram 自动选择要领取的重置：优先每周会话重置（本周不用即作废），其次免费重置券。
func (s *ClaudeResetSnapshot) preferredProgram(now time.Time) (string, error) {
	if s == nil {
		return "", ErrClaudeResetUnavailable
	}
	if s.JuniperTide.claimable() {
		return ClaudeResetProgramJuniperTide, nil
	}
	if _, err := s.CedarEmber.nextClaimableGrant(now); err != nil {
		return "", err
	}
	return ClaudeResetProgramCedarEmber, nil
}

func (j *ClaudeJuniperTideStatus) claimable() bool {
	return j != nil && j.Eligible && j.Available && strings.EqualFold(j.Arm, "reset")
}

// nextClaimableGrant 返回上游指定的下一张券；不可领取时给出原因。
func (c *ClaudeCedarEmberStatus) nextClaimableGrant(now time.Time) (*ClaudeResetGrant, error) {
	if c == nil || !c.Eligible {
		return nil, ErrClaudeResetUnavailable
	}
	for i := range c.Grants {
		grant := &c.Grants[i]
		if !grant.Next {
			continue
		}
		if grant.ResetsLeft <= 0 || grant.Paused || claudeResetExpired(grant.EndsAt, now) {
			return nil, ErrClaudeResetUnavailable
		}
		if grant.UseRequiresLimit && !c.coversExhaustedLimit(grant) {
			return nil, ErrClaudeResetRequiresLimit
		}
		if !grant.UsableNow {
			return nil, ErrClaudeResetUnavailable
		}
		return grant, nil
	}
	return nil, ErrClaudeResetUnavailable
}

// coversExhaustedLimit 券能清除的限额里是否有已经打满的。
func (c *ClaudeCedarEmberStatus) coversExhaustedLimit(grant *ClaudeResetGrant) bool {
	for _, limit := range grant.Clears {
		for _, exhausted := range c.Exhausted {
			if limit == exhausted {
				return true
			}
		}
	}
	return false
}

// claudeResetExpired 无法解析的时间视为未过期（原样展示，避免少算可用次数）。
func claudeResetExpired(endsAt string, now time.Time) bool {
	endsAt = strings.TrimSpace(endsAt)
	if endsAt == "" {
		return false
	}
	parsed, err := parseTime(endsAt)
	if err != nil {
		return false
	}
	return !parsed.After(now)
}

// claudeResetRequestID 由账号、券与领取前的剩余次数派生：同一次领取的重试复用同一 request_id，
// 上游据此去重，超时重试不会重复扣卡；领取成功后剩余次数变化，下一次领取自然换成新 id。
func claudeResetRequestID(accountID int64, grantID string, resetsLeft int) string {
	name := fmt.Sprintf("claude-reset:%d:%s:%d", accountID, grantID, resetsLeft)
	return uuid.NewSHA1(claudeResetRequestNamespace, []byte(name)).String()
}

func parseClaudeCedarEmberStatus(raw json.RawMessage) *ClaudeCedarEmberStatus {
	if isJSONNullOrEmpty(raw) {
		return nil
	}
	var block struct {
		Eligible         json.RawMessage `json:"eligible"`
		IneligibleReason json.RawMessage `json:"ineligible_reason"`
		AtLimit          json.RawMessage `json:"at_limit"`
		Exhausted        json.RawMessage `json:"exhausted"`
		Grants           json.RawMessage `json:"grants"`
		NextGrantID      json.RawMessage `json:"next_grant_id"`
		WeeklyResetsAt   json.RawMessage `json:"weekly_resets_at"`
		CooldownUntil    json.RawMessage `json:"cooldown_until"`
		EventProps       json.RawMessage `json:"event_props"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil
	}
	var eligible bool
	if err := json.Unmarshal(block.Eligible, &eligible); err != nil {
		// eligible 是必填字段，缺失说明不是可信的状态块。
		return nil
	}
	status := &ClaudeCedarEmberStatus{
		Eligible:         eligible,
		IneligibleReason: jsonString(block.IneligibleReason),
		AtLimit:          jsonBool(block.AtLimit, false),
		Exhausted:        jsonStringList(block.Exhausted),
		Grants:           []ClaudeResetGrant{},
		WeeklyResetsAt:   jsonString(block.WeeklyResetsAt),
		CooldownUntil:    jsonString(block.CooldownUntil),
		Tier:             claudeEventPropsTier(block.EventProps),
	}
	nextID := jsonString(block.NextGrantID)
	var rawGrants []json.RawMessage
	if err := json.Unmarshal(block.Grants, &rawGrants); err != nil {
		rawGrants = nil
	}
	for _, rawGrant := range rawGrants {
		grant, ok := parseClaudeResetGrant(rawGrant)
		if !ok {
			continue
		}
		grant.Next = nextID != "" && grant.id == nextID
		status.Grants = append(status.Grants, grant)
	}
	return status
}

func parseClaudeResetGrant(raw json.RawMessage) (ClaudeResetGrant, bool) {
	var item struct {
		ID               json.RawMessage `json:"id"`
		Label            json.RawMessage `json:"label"`
		ResetsTotal      json.RawMessage `json:"resets_total"`
		ResetsLeft       json.RawMessage `json:"resets_left"`
		StartsAt         json.RawMessage `json:"starts_at"`
		EndsAt           json.RawMessage `json:"ends_at"`
		Clears           json.RawMessage `json:"clears"`
		Paused           json.RawMessage `json:"paused"`
		UsableNow        json.RawMessage `json:"usable_now"`
		UseRequiresLimit json.RawMessage `json:"use_requires_limit"`
		Blocking         json.RawMessage `json:"blocking"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return ClaudeResetGrant{}, false
	}
	id := jsonString(item.ID)
	if !claudeResetGrantIDPattern.MatchString(id) {
		return ClaudeResetGrant{}, false
	}
	resetsLeft, ok := jsonNumber(item.ResetsLeft)
	if !ok || resetsLeft < 0 || resetsLeft != math.Trunc(resetsLeft) {
		return ClaudeResetGrant{}, false
	}
	grant := ClaudeResetGrant{
		Label:      jsonString(item.Label),
		ResetsLeft: int(resetsLeft),
		StartsAt:   jsonString(item.StartsAt),
		EndsAt:     jsonString(item.EndsAt),
		Clears:     jsonStringList(item.Clears),
		Paused:     jsonBool(item.Paused, false),
		UsableNow:  jsonBool(item.UsableNow, false),
		// 缺省按“只能在触顶时使用”处理，与 Claude Code 一致。
		UseRequiresLimit: jsonBool(item.UseRequiresLimit, true),
		Blocking:         jsonStringList(item.Blocking),
		id:               id,
	}
	if grant.Clears == nil {
		grant.Clears = []string{}
	}
	if total, ok := jsonNumber(item.ResetsTotal); ok && total > 0 {
		grant.ResetsTotal = int(total)
	}
	return grant, true
}

func parseClaudeJuniperTideStatus(raw json.RawMessage) *ClaudeJuniperTideStatus {
	if isJSONNullOrEmpty(raw) {
		return nil
	}
	var block struct {
		Eligible         json.RawMessage `json:"eligible"`
		IneligibleReason json.RawMessage `json:"ineligible_reason"`
		InExperiment     json.RawMessage `json:"in_experiment"`
		Arm              json.RawMessage `json:"arm"`
		Available        json.RawMessage `json:"available"`
		NextAvailableAt  json.RawMessage `json:"next_available_at"`
		WeeklyResetsAt   json.RawMessage `json:"weekly_resets_at"`
		ResetsPerWeek    json.RawMessage `json:"resets_per_week"`
		EventProps       json.RawMessage `json:"event_props"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		return nil
	}
	var eligible bool
	if err := json.Unmarshal(block.Eligible, &eligible); err != nil {
		return nil
	}
	status := &ClaudeJuniperTideStatus{
		Eligible:         eligible,
		IneligibleReason: jsonString(block.IneligibleReason),
		InExperiment:     jsonBool(block.InExperiment, false),
		Arm:              jsonString(block.Arm),
		Available:        jsonBool(block.Available, false),
		NextAvailableAt:  jsonString(block.NextAvailableAt),
		WeeklyResetsAt:   jsonString(block.WeeklyResetsAt),
		ResetsPerWeek:    1,
		Tier:             claudeEventPropsTier(block.EventProps),
	}
	if perWeek, ok := jsonNumber(block.ResetsPerWeek); ok && perWeek >= 0 {
		status.ResetsPerWeek = int(perWeek)
	}
	return status
}

// claudeEventPropsTier 读取状态块 event_props.tier（claude_pro / claude_max_5x / claude_max_20x ...）。
func claudeEventPropsTier(raw json.RawMessage) string {
	if isJSONNullOrEmpty(raw) {
		return ""
	}
	var props struct {
		Tier json.RawMessage `json:"tier"`
	}
	if err := json.Unmarshal(raw, &props); err != nil {
		return ""
	}
	tier := jsonString(props.Tier)
	if strings.EqualFold(tier, "unknown") {
		return ""
	}
	return tier
}

// parseClaudeResetClaim 解析领取响应；result 缺失时返回空 Result，由调用方按不可读处理。
func parseClaudeResetClaim(body []byte, program string) *ClaudeQuotaResetResult {
	result := &ClaudeQuotaResetResult{Program: program}
	var block struct {
		Result          json.RawMessage `json:"result"`
		Reason          json.RawMessage `json:"reason"`
		ResetsLeft      json.RawMessage `json:"resets_left"`
		Cleared         json.RawMessage `json:"cleared"`
		WeeklyResetsAt  json.RawMessage `json:"weekly_resets_at"`
		CooldownUntil   json.RawMessage `json:"cooldown_until"`
		NextAvailableAt json.RawMessage `json:"next_available_at"`
	}
	if err := json.Unmarshal(body, &block); err != nil {
		return result
	}
	result.Result = strings.ToLower(jsonString(block.Result))
	result.Reason = jsonString(block.Reason)
	if left, ok := jsonNumber(block.ResetsLeft); ok && left >= 0 {
		value := int(left)
		result.ResetsLeft = &value
	}
	result.Cleared = jsonStringList(block.Cleared)
	result.WeeklyResetsAt = jsonString(block.WeeklyResetsAt)
	result.CooldownUntil = jsonString(block.CooldownUntil)
	result.NextAvailableAt = jsonString(block.NextAvailableAt)
	return result
}

// readClaudeResetSnapshot 读取 extra.claude_reset_snapshot；值可能是数据库读出的 map，
// 也可能是进程内合并的结构体，统一经 JSON 解析。
func readClaudeResetSnapshot(extra map[string]any) *ClaudeResetSnapshot {
	raw, ok := extra[claudeResetSnapshotExtraKey]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot ClaudeResetSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil
	}
	return &snapshot
}
