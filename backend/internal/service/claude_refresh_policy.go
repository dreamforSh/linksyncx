package service

import (
	"strconv"
	"strings"
	"time"
)

// Claude 凭据的后台刷新策略（D7）。真实 CLI「不用就不刷」：只在请求前距过期不足 5 分钟时
// 刷新。后台刷新因此只覆盖三类账号：
//  1. 最近 24 小时内有过成功请求、access token 即将过期的账号；
//  2. refresh token 进入最后 3 天、还没确认期限能否顺延的账号，做一次保活刷新；
//  3. refresh token 进入最后 3 天且确认期限固定的账号：不再保活，账号列表提示需重新授权。
//
// 其余闲置账号不刷新，被调度时在请求路径上按 5 分钟窗口刷新。
const (
	// claudeRefreshActiveWindow 第 1 类：最近这么久内有过成功请求才算活跃账号。
	claudeRefreshActiveWindow = 24 * time.Hour
	// claudeRefreshTokenNoticeWindow 第 2 / 3 类：refresh token 剩余不足这么久时保活或提示，
	// 与 CLI 的提示阈值一致。
	claudeRefreshTokenNoticeWindow = 3 * 24 * time.Hour
	// claudeRefreshTokenFixedExpiryKey 凭据里记录「保活后期限没有顺延」的 refresh token 期限
	// （Unix 秒）。只在与当前 refresh_token_expires_at 相等时生效，重新授权或期限顺延后自然失效。
	claudeRefreshTokenFixedExpiryKey = "refresh_token_expiry_fixed_at"
)

type claudeBackgroundRefreshKind int

const (
	claudeBackgroundRefreshNone claudeBackgroundRefreshKind = iota
	// claudeBackgroundRefreshActive 第 1 类：活跃账号的 access token 即将过期
	claudeBackgroundRefreshActive
	// claudeBackgroundRefreshKeepAlive 第 2 类：refresh token 最后 3 天的保活刷新
	claudeBackgroundRefreshKeepAlive
)

// claudeRefreshTokenExpiry 返回账号 refresh token 的到期时间；没有 refresh token 或没有记录期限时返回 nil。
func claudeRefreshTokenExpiry(account *Account) *time.Time {
	if account == nil || strings.TrimSpace(account.GetCredential("refresh_token")) == "" {
		return nil
	}
	return account.GetCredentialAsTime("refresh_token_expires_at")
}

// claudeRefreshTokenExpiryFixed 报告保活刷新是否已确认当前 refresh token 期限无法顺延。
func claudeRefreshTokenExpiryFixed(account *Account, expiresAt time.Time) bool {
	fixed := account.GetCredentialAsTime(claudeRefreshTokenFixedExpiryKey)
	return fixed != nil && fixed.Unix() == expiresAt.Unix()
}

// claudeBackgroundRefreshKindFor 按 D7 判断后台刷新是否要处理该账号。
func claudeBackgroundRefreshKindFor(account *Account, refreshWindow time.Duration, now time.Time) claudeBackgroundRefreshKind {
	if account == nil || strings.TrimSpace(account.GetCredential("refresh_token")) == "" {
		return claudeBackgroundRefreshNone
	}
	if rtExpiresAt := claudeRefreshTokenExpiry(account); rtExpiresAt != nil {
		remaining := rtExpiresAt.Sub(now)
		if remaining > 0 && remaining <= claudeRefreshTokenNoticeWindow && !claudeRefreshTokenExpiryFixed(account, *rtExpiresAt) {
			return claudeBackgroundRefreshKeepAlive
		}
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil || expiresAt.Sub(now) >= refreshWindow {
		return claudeBackgroundRefreshNone
	}
	if account.LastUsedAt == nil || now.Sub(*account.LastUsedAt) > claudeRefreshActiveWindow {
		return claudeBackgroundRefreshNone
	}
	return claudeBackgroundRefreshActive
}

// markClaudeRefreshTokenExtension 在刷新结果里记录 refresh token 期限能否顺延：刷新前已进入
// 最后 3 天、刷新后期限没有变晚时，记下这个期限，此后不再保活。期限变晚则什么都不写，
// 旧记录因与新期限不相等自动失效。
func markClaudeRefreshTokenExtension(account *Account, newCredentials map[string]any, now time.Time) {
	before := claudeRefreshTokenExpiry(account)
	if before == nil || newCredentials == nil {
		return
	}
	remaining := before.Sub(now)
	if remaining <= 0 || remaining > claudeRefreshTokenNoticeWindow {
		return
	}
	after := (&Account{Credentials: newCredentials}).GetCredentialAsTime("refresh_token_expires_at")
	if after == nil {
		after = before
	}
	if after.After(*before) {
		return
	}
	newCredentials[claudeRefreshTokenFixedExpiryKey] = strconv.FormatInt(after.Unix(), 10)
}

// ClaudeReauthNotice 是账号列表的「需重新授权」提示：refresh token 已过期，或已进入最后
// 3 天且保活确认期限无法顺延（D7 第 3 类）。
type ClaudeReauthNotice struct {
	// ExpiresAt refresh token 的到期时间
	ExpiresAt time.Time `json:"expires_at"`
	// DaysLeft 剩余天数，向上取整；已过期为 0
	DaysLeft int `json:"days_left"`
	// Expired refresh token 已过期
	Expired bool `json:"expired"`
}

// ClaudeReauthNoticeFor 返回账号的重新授权提示，不需要提示时返回 nil。
func ClaudeReauthNoticeFor(account *Account, now time.Time) *ClaudeReauthNotice {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return nil
	}
	expiresAt := claudeRefreshTokenExpiry(account)
	if expiresAt == nil {
		return nil
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return &ClaudeReauthNotice{ExpiresAt: *expiresAt, Expired: true}
	}
	if remaining > claudeRefreshTokenNoticeWindow || !claudeRefreshTokenExpiryFixed(account, *expiresAt) {
		return nil
	}
	days := int((remaining + 24*time.Hour - 1) / (24 * time.Hour))
	return &ClaudeReauthNotice{ExpiresAt: *expiresAt, DaysLeft: days}
}
