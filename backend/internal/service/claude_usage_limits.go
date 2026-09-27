package service

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// claudeUsageLimit 是 /api/oauth/usage 响应 limits[] 的单个条目。percent 为 0-100，
// resets_at 可能是 Unix 秒也可能是 ISO 时间串（Claude Code 两种都接受）。
type claudeUsageLimit struct {
	Kind  string `json:"kind"`
	Scope *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
	Percent  json.RawMessage `json:"percent"`
	ResetsAt json.RawMessage `json:"resets_at"`
}

// claudeFableUsageFromLimits 从 limits[] 中找出 Fable 的周额度窗口（kind=weekly_scoped 且
// 模型名含 Fable）。Fable 5 与 5.1 共用同一份额度，出现多条时取使用率最高的一条。
func claudeFableUsageFromLimits(raw json.RawMessage, now time.Time) *UsageProgress {
	if isJSONNullOrEmpty(raw) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	var best *UsageProgress
	for _, item := range items {
		var limit claudeUsageLimit
		if err := json.Unmarshal(item, &limit); err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(limit.Kind), "weekly_scoped") || limit.Scope == nil || limit.Scope.Model == nil {
			continue
		}
		if !strings.Contains(strings.ToLower(limit.Scope.Model.DisplayName), "fable") {
			continue
		}
		percent, ok := jsonNumber(limit.Percent)
		if !ok {
			continue
		}
		progress := &UsageProgress{Utilization: percent}
		if resetAt, ok := parseClaudeTimestamp(limit.ResetsAt); ok {
			progress.ResetsAt = &resetAt
			if remaining := int(resetAt.Sub(now).Seconds()); remaining > 0 {
				progress.RemainingSeconds = remaining
			}
		}
		if best == nil || progress.Utilization > best.Utilization {
			best = progress
		}
	}
	return best
}

// parseClaudeTimestamp 解析 Unix 秒（自动识别毫秒）、数字字符串或 ISO 时间串。
func parseClaudeTimestamp(raw json.RawMessage) (time.Time, bool) {
	if isJSONNullOrEmpty(raw) {
		return time.Time{}, false
	}
	if value, ok := jsonNumber(raw); ok {
		return unixSecondsToTime(value)
	}
	text := jsonString(raw)
	if text == "" {
		return time.Time{}, false
	}
	if parsed, err := parseTime(text); err == nil {
		return parsed, true
	}
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return unixSecondsToTime(value)
	}
	return time.Time{}, false
}

func unixSecondsToTime(value float64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value > 1e11 {
		value /= 1000
	}
	return time.Unix(int64(value), 0), true
}

func isJSONNullOrEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// jsonNumber 宽松读取数字（接受数字字符串），格式不符返回 false。
func jsonNumber(raw json.RawMessage) (float64, bool) {
	if isJSONNullOrEmpty(raw) {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, true
	}
	if text := jsonString(raw); text != "" {
		if parsed, err := strconv.ParseFloat(text, 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

// jsonString 宽松读取字符串，非字符串返回空串。
func jsonString(raw json.RawMessage) string {
	if isJSONNullOrEmpty(raw) {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

// jsonBool 宽松读取布尔值，格式不符时返回 fallback。
func jsonBool(raw json.RawMessage, fallback bool) bool {
	if isJSONNullOrEmpty(raw) {
		return fallback
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

// jsonStringList 宽松读取字符串数组，跳过非字符串元素。
func jsonStringList(raw json.RawMessage) []string {
	if isJSONNullOrEmpty(raw) {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if value := jsonString(item); value != "" {
			out = append(out, value)
		}
	}
	return out
}
