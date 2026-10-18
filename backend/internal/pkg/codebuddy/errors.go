package codebuddy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRateLimitCooldown applies to a 429 that names no reset time.
	DefaultRateLimitCooldown = 10 * time.Minute
	// MaxRateLimitCooldown caps any upstream-provided reset.
	MaxRateLimitCooldown = 24 * time.Hour
	// ModelNotServableCode is the business code for "service info not found".
	ModelNotServableCode = "11102"
)

var (
	resetTimeRE        = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s+(\d{2}:\d{2}:\d{2})\s*UTC\s*([+-]?\d+)`)
	notServableMessage = regexp.MustCompile(`(?i)service info not found|model .{0,80}not (?:found|supported)`)
)

// RateLimitResetAt resolves when a 429 lifts: Retry-After first (seconds or
// HTTP date), then a "YYYY-MM-DD HH:MM:SS UTC+8" stamp in the body, else the
// default cooldown. The result is capped at MaxRateLimitCooldown from now.
func RateLimitResetAt(header http.Header, body []byte, now time.Time) time.Time {
	limit := now.Add(MaxRateLimitCooldown)
	clamp := func(t time.Time) time.Time {
		if t.After(limit) {
			return limit
		}
		return t
	}
	if header != nil {
		if raw := strings.TrimSpace(header.Get("Retry-After")); raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil && seconds >= 0 {
				return clamp(now.Add(time.Duration(seconds * float64(time.Second))))
			}
			if at, err := http.ParseTime(raw); err == nil && at.After(now) {
				return clamp(at)
			}
		}
	}
	if match := resetTimeRE.FindSubmatch(body); match != nil {
		offset, err := strconv.Atoi(string(match[3]))
		if err == nil && offset >= -14 && offset <= 14 {
			zone := time.FixedZone(fmt.Sprintf("UTC%+d", offset), offset*3600)
			if at, err := time.ParseInLocation("2006-01-02 15:04:05", string(match[1])+" "+string(match[2]), zone); err == nil && at.After(now) {
				return clamp(at)
			}
		}
	}
	return now.Add(DefaultRateLimitCooldown)
}

// ModelNotServable reports whether a 400/404 says the model is not served on
// this account (code 11102 or a not-found/not-supported message).
func ModelNotServable(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound {
		return false
	}
	code, msg := envelopeCodeAndMessage(body)
	return code == ModelNotServableCode || notServableMessage.MatchString(msg)
}

func envelopeCodeAndMessage(body []byte) (string, string) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return "", ""
	}
	nodes := []map[string]any{payload}
	if inner, ok := payload["error"].(map[string]any); ok {
		nodes = append(nodes, inner)
	}
	var code, msg string
	for _, node := range nodes {
		for _, key := range []string{"code", "errCode", "error_code"} {
			if code == "" && node[key] != nil {
				code = strings.TrimSpace(fmt.Sprint(node[key]))
			}
		}
		for _, key := range []string{"msg", "message"} {
			if s, ok := node[key].(string); ok && msg == "" && strings.TrimSpace(s) != "" {
				msg = strings.TrimSpace(s)
			}
		}
	}
	return code, msg
}

// filterReply is the upstream's canned content-filter refusal.
const filterReply = "抱歉，系统检测到您当前输入的信息存在敏感内容，我无法响应您的请求，请检查后重新输入"

// IsContentFilterText reports whether text is the canned refusal, ignoring
// whitespace, comma width and trailing punctuation.
func IsContentFilterText(text string) bool {
	normalized := strings.Join(strings.Fields(text), "")
	normalized = strings.ReplaceAll(normalized, ",", "，")
	normalized = strings.TrimRight(normalized, "。.!！")
	return normalized == filterReply
}

// IsContentFilterError reports an explicit content-filter error body. Such a
// refusal is about the prompt, not the account: no cooldown, no failover.
func IsContentFilterError(body []byte) bool {
	var payload struct {
		Error map[string]any `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Error == nil {
		return false
	}
	for _, key := range []string{"code", "type"} {
		if s, _ := payload.Error[key].(string); s == "content_filter" || s == "content-filter" {
			return true
		}
	}
	msg, _ := payload.Error["message"].(string)
	return IsContentFilterText(msg)
}

// creditsExhaustedMarkers are phrases blaming an empty credit balance, which
// CodeBuddy nests in an otherwise ordinary 4xx business error.
var creditsExhaustedMarkers = []string{
	"insufficient credit", "insufficient_credit", "insufficient balance",
	"insufficient_balance", "insufficient quota", "credit not enough",
	"credit_not_enough", "quota exhausted", "quota_exhausted", "out of credits",
	"积分不足", "额度不足", "余额不足", "资源包已用完",
}

// CreditsExhausted reports whether an upstream rejection blames the balance.
func CreditsExhausted(status int, body []byte) bool {
	if status < 400 || len(body) == 0 {
		return false
	}
	if len(body) > 16<<10 {
		body = body[:16<<10]
	}
	lowered := strings.ToLower(string(body))
	for _, marker := range creditsExhaustedMarkers {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}
