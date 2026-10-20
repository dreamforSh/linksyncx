package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Exit change policies: what happens when a node's egress IP changes in place.
const (
	ClashExitChangePause  = "pause"
	ClashExitChangeAccept = "accept"
)

const clashDefaultUserAgent = "clash.meta"

// clashPoolSettingsVersion 2 introduced unlimited exit sharing (0).
const clashPoolSettingsVersion = 2

// ClashPoolSettings are runtime tunables stored in the settings table so every
// instance shares them and admins can change them without a restart.
type ClashPoolSettings struct {
	// MaxAccountsPerExit caps accounts sharing one egress IP; 0 = unlimited
	// (default), 1 = exclusive.
	MaxAccountsPerExit int `json:"max_accounts_per_exit"`
	// SettingsVersion marks the stored schema. Version 1 (absent) predates
	// unlimited sharing, when 1 was both the minimum and the default.
	SettingsVersion int `json:"settings_version,omitempty"`
	// AllowUnprobedExitBinding allows binding nodes whose egress IP is unknown.
	AllowUnprobedExitBinding bool `json:"allow_unprobed_exit_binding"`
	// PauseAutomaticProbes 暂停后台的定时探测，供按流量计费的机场、节点很多或不想让出口定期
	// 访问 AI 平台的部署使用。用「暂停」的反向命名，是为了让零值就等于继续探测：全新安装、
	// 没有该字段的旧记录、不带该字段的自建脚本 PUT、存储值校验失败和读取出错的回退，都天然
	// 继续探测，读写和校验路径不需要任何特判。探测在这里承担安全职责（出口变更暂停、本机 IP
	// 自查、故障暂停与恢复），所以暂停时仍保留一部分探测，见 clashPausedProbeViews。
	PauseAutomaticProbes bool   `json:"pause_automatic_probes"`
	HealthTestURL        string `json:"health_test_url"`
	HealthTimeoutMs      int    `json:"health_timeout_ms"`
	// BoundCheckIntervalSeconds applies to nodes backing at least one account.
	BoundCheckIntervalSeconds   int `json:"bound_check_interval_seconds"`
	UnboundCheckIntervalSeconds int `json:"unbound_check_interval_seconds"`
	// FailureThreshold consecutive failures mark a node unhealthy;
	// RecoveryThreshold consecutive successes mark it healthy again.
	FailureThreshold         int  `json:"failure_threshold"`
	RecoveryThreshold        int  `json:"recovery_threshold"`
	ExitProbeIntervalMinutes int  `json:"exit_probe_interval_minutes"`
	ExitProbePerMinute       int  `json:"exit_probe_per_minute"`
	PlatformChecksEnabled    bool `json:"platform_checks_enabled"`
	// ExitChangePolicy decides whether an in-place egress change pauses bound
	// accounts until an admin accepts it.
	ExitChangePolicy     string `json:"exit_change_policy"`
	PauseTTLMinutes      int    `json:"pause_ttl_minutes"`
	MissingRetentionDays int    `json:"missing_retention_days"`
	// DropProtectionPercent skips a refresh when the node count drops by more
	// than this percentage (0 disables the guard).
	DropProtectionPercent int    `json:"drop_protection_percent"`
	DefaultUserAgent      string `json:"default_user_agent"`
}

func defaultClashPoolSettings() *ClashPoolSettings {
	return &ClashPoolSettings{
		MaxAccountsPerExit:          0,
		SettingsVersion:             clashPoolSettingsVersion,
		HealthTestURL:               "https://www.gstatic.com/generate_204",
		HealthTimeoutMs:             5000,
		BoundCheckIntervalSeconds:   60,
		UnboundCheckIntervalSeconds: 600,
		FailureThreshold:            3,
		RecoveryThreshold:           2,
		ExitProbeIntervalMinutes:    720,
		ExitProbePerMinute:          30,
		PlatformChecksEnabled:       true,
		ExitChangePolicy:            ClashExitChangePause,
		PauseTTLMinutes:             30,
		MissingRetentionDays:        7,
		DropProtectionPercent:       50,
		DefaultUserAgent:            clashDefaultUserAgent,
	}
}

// GetClashPoolSettings returns stored settings merged over defaults.
func (s *SettingService) GetClashPoolSettings(ctx context.Context) (*ClashPoolSettings, error) {
	defaults := defaultClashPoolSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyClashPoolSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return nil, fmt.Errorf("get clash pool settings: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return defaults, nil
	}
	settings := *defaults
	settings.SettingsVersion = 0
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("parse clash pool settings: %w", err)
	}
	if settings.SettingsVersion < clashPoolSettingsVersion {
		// Before version 2 the limit could not go below 1 and defaulted to 1,
		// so a stored 1 is the old forced exclusivity, not an explicit choice.
		if settings.MaxAccountsPerExit == 1 {
			settings.MaxAccountsPerExit = 0
		}
		settings.SettingsVersion = clashPoolSettingsVersion
	}
	if err := validateClashPoolSettings(&settings); err != nil {
		// A stored value that no longer validates must not stall the pool.
		return defaults, nil
	}
	return &settings, nil
}

// SetClashPoolSettings validates and stores settings.
func (s *SettingService) SetClashPoolSettings(ctx context.Context, settings *ClashPoolSettings) error {
	if s == nil || s.settingRepo == nil {
		return infraerrors.ServiceUnavailable("CLASH_SETTINGS_UNAVAILABLE", "settings storage is unavailable")
	}
	if settings == nil {
		return infraerrors.BadRequest("INVALID_CLASH_POOL_SETTINGS", "settings cannot be nil")
	}
	if err := validateClashPoolSettings(settings); err != nil {
		return err
	}
	settings.SettingsVersion = clashPoolSettingsVersion
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal clash pool settings: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyClashPoolSettings, string(data))
}

func validateClashPoolSettings(s *ClashPoolSettings) error {
	s.HealthTestURL = strings.TrimSpace(s.HealthTestURL)
	s.ExitChangePolicy = strings.ToLower(strings.TrimSpace(s.ExitChangePolicy))
	s.DefaultUserAgent = strings.TrimSpace(s.DefaultUserAgent)
	if s.DefaultUserAgent == "" {
		s.DefaultUserAgent = clashDefaultUserAgent
	}
	checks := []struct {
		name     string
		value    int
		min, max int
	}{
		{"max_accounts_per_exit", s.MaxAccountsPerExit, 0, 100},
		{"health_timeout_ms", s.HealthTimeoutMs, 1000, 30000},
		{"bound_check_interval_seconds", s.BoundCheckIntervalSeconds, 30, 3600},
		{"unbound_check_interval_seconds", s.UnboundCheckIntervalSeconds, 60, 86400},
		{"failure_threshold", s.FailureThreshold, 1, 20},
		{"recovery_threshold", s.RecoveryThreshold, 1, 20},
		{"exit_probe_interval_minutes", s.ExitProbeIntervalMinutes, 30, 10080},
		{"exit_probe_per_minute", s.ExitProbePerMinute, 1, 600},
		{"pause_ttl_minutes", s.PauseTTLMinutes, 15, 240},
		{"missing_retention_days", s.MissingRetentionDays, 1, 365},
		{"drop_protection_percent", s.DropProtectionPercent, 0, 100},
	}
	for _, check := range checks {
		if check.value < check.min || check.value > check.max {
			return infraerrors.BadRequest("INVALID_CLASH_POOL_SETTINGS",
				fmt.Sprintf("%s must be between %d and %d", check.name, check.min, check.max))
		}
	}
	if s.ExitChangePolicy != ClashExitChangePause && s.ExitChangePolicy != ClashExitChangeAccept {
		return infraerrors.BadRequest("INVALID_CLASH_POOL_SETTINGS", "exit_change_policy must be pause or accept")
	}
	parsed, err := url.Parse(s.HealthTestURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return infraerrors.BadRequest("INVALID_CLASH_POOL_SETTINGS", "health_test_url must be an http(s) URL")
	}
	if len(s.DefaultUserAgent) > 200 {
		return infraerrors.BadRequest("INVALID_CLASH_POOL_SETTINGS", "default_user_agent is too long")
	}
	return nil
}
