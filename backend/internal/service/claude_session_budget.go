package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultClaudeMaxSessions 是 Anthropic OAuth / setup-token 账号未单独配置 max_sessions 时，
	// 同一账号同时活跃的上游会话预算。
	DefaultClaudeMaxSessions = 5
	// MaxClaudeDefaultMaxSessions 是系统默认会话预算允许填写的上限。
	MaxClaudeDefaultMaxSessions = 1000

	claudeDefaultMaxSessionsCacheTTL  = 60 * time.Second
	claudeDefaultMaxSessionsErrorTTL  = 5 * time.Second
	claudeDefaultMaxSessionsDBTimeout = 5 * time.Second
)

type cachedClaudeDefaultMaxSessions struct {
	value     int
	expiresAt int64
}

// ClaudeSessionBudget 返回账号同时活跃的上游会话预算，0 表示不限。只对 Anthropic OAuth /
// setup-token 账号生效：
//   - 单会话模式（session_id_masking_enabled）：1，同一时刻只接一个下游对话；
//   - 账号单独配置了 max_sessions：用账号的值；
//   - 否则用系统默认预算 defaultBudget（≤ 0 表示不限）。
func ClaudeSessionBudget(account *Account, defaultBudget int) int {
	if account == nil || !account.IsAnthropicOAuthOrSetupToken() {
		return 0
	}
	if account.IsSessionIDMaskingEnabled() {
		return 1
	}
	if explicit := account.GetMaxSessions(); explicit > 0 {
		return explicit
	}
	if defaultBudget > 0 {
		return defaultBudget
	}
	return 0
}

// GetClaudeDefaultMaxSessions 返回 Claude 账号的系统默认会话预算（0 = 不限）。调度热路径逐账号
// 调用：进程内缓存 60 秒，并发未命中合并为一次读库；读库失败时用内置默认值。
func (s *SettingService) GetClaudeDefaultMaxSessions(ctx context.Context) int {
	if s == nil || s.settingRepo == nil {
		return DefaultClaudeMaxSessions
	}
	if cached, ok := s.claudeDefaultMaxSessionsCache.Load().(*cachedClaudeDefaultMaxSessions); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.value
		}
	}
	result, _, _ := s.claudeDefaultMaxSessionsSF.Do(SettingKeyClaudeDefaultMaxSessions, func() (any, error) {
		if cached, ok := s.claudeDefaultMaxSessionsCache.Load().(*cachedClaudeDefaultMaxSessions); ok && cached != nil {
			if time.Now().UnixNano() < cached.expiresAt {
				return cached.value, nil
			}
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claudeDefaultMaxSessionsDBTimeout)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyClaudeDefaultMaxSessions)
		ttl := claudeDefaultMaxSessionsCacheTTL
		value := DefaultClaudeMaxSessions
		switch {
		case err == nil:
			value = parseClaudeDefaultMaxSessions(raw)
		case errors.Is(err, ErrSettingNotFound):
		default:
			slog.Warn("failed to get claude default max sessions, falling back to the built-in default", "error", err)
			ttl = claudeDefaultMaxSessionsErrorTTL
		}
		s.storeClaudeDefaultMaxSessions(value, ttl)
		return value, nil
	})
	if value, ok := result.(int); ok {
		return value
	}
	return DefaultClaudeMaxSessions
}

func (s *SettingService) storeClaudeDefaultMaxSessions(value int, ttl time.Duration) {
	s.claudeDefaultMaxSessionsCache.Store(&cachedClaudeDefaultMaxSessions{
		value:     value,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}

// parseClaudeDefaultMaxSessions 解析存储值；缺省或非法时用内置默认值。
func parseClaudeDefaultMaxSessions(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultClaudeMaxSessions
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil || validateClaudeDefaultMaxSessions(value) != nil {
		return DefaultClaudeMaxSessions
	}
	return value
}

func validateClaudeDefaultMaxSessions(value int) error {
	if value < 0 || value > MaxClaudeDefaultMaxSessions {
		return fmt.Errorf("claude_default_max_sessions must be between 0 and %d", MaxClaudeDefaultMaxSessions)
	}
	return nil
}

// claudeSessionBudget 返回账号在本实例上生效的会话预算（见 ClaudeSessionBudget）。
func (s *GatewayService) claudeSessionBudget(ctx context.Context, account *Account) int {
	if s == nil {
		return ClaudeSessionBudget(account, DefaultClaudeMaxSessions)
	}
	return ClaudeSessionBudget(account, s.settingService.GetClaudeDefaultMaxSessions(ctx))
}
