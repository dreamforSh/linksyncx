package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ClashProfileStats aggregates node states of a subscription.
type ClashProfileStats struct {
	Total     int `json:"total"`
	Active    int `json:"active"`
	Healthy   int `json:"healthy"`
	Unhealthy int `json:"unhealthy"`
	Missing   int `json:"missing"`
	Invalid   int `json:"invalid"`
	Disabled  int `json:"disabled"`
	// Bound counts nodes backing at least one account.
	Bound int `json:"bound"`
	// BoundAccounts counts distinct non-shadow accounts using the profile's nodes.
	BoundAccounts int `json:"bound_accounts"`
}

// ClashProfile is a Clash subscription. The URL is only ever returned masked.
type ClashProfile struct {
	ID                     int64             `json:"id"`
	Name                   string            `json:"name"`
	URLMasked              string            `json:"url_masked"`
	UserAgent              string            `json:"user_agent"`
	Enabled                bool              `json:"enabled"`
	RefreshIntervalMinutes int               `json:"refresh_interval_minutes"`
	IncludePattern         string            `json:"include_pattern"`
	ExcludePattern         string            `json:"exclude_pattern"`
	FetchProxyID           *int64            `json:"fetch_proxy_id"`
	Notes                  string            `json:"notes"`
	LastRefreshAt          *time.Time        `json:"last_refresh_at"`
	LastRefreshStatus      string            `json:"last_refresh_status"`
	LastRefreshError       string            `json:"last_refresh_error"`
	LastFormat             string            `json:"last_format"`
	UploadBytes            int64             `json:"upload_bytes"`
	DownloadBytes          int64             `json:"download_bytes"`
	TotalBytes             int64             `json:"total_bytes"`
	ExpireAt               *time.Time        `json:"expire_at"`
	NodeCount              int               `json:"node_count"`
	Stats                  ClashProfileStats `json:"stats"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

func ClashProfileFromService(p *service.ClashProfileSummary) *ClashProfile {
	if p == nil {
		return nil
	}
	return &ClashProfile{
		ID:                     p.ID,
		Name:                   p.Name,
		URLMasked:              p.URLMasked,
		UserAgent:              p.UserAgent,
		Enabled:                p.Enabled,
		RefreshIntervalMinutes: p.RefreshIntervalMinutes,
		IncludePattern:         p.IncludePattern,
		ExcludePattern:         p.ExcludePattern,
		FetchProxyID:           p.FetchProxyID,
		Notes:                  p.Notes,
		LastRefreshAt:          p.LastRefreshAt,
		LastRefreshStatus:      p.LastRefreshStatus,
		LastRefreshError:       p.LastRefreshError,
		LastFormat:             p.LastFormat,
		UploadBytes:            p.UploadBytes,
		DownloadBytes:          p.DownloadBytes,
		TotalBytes:             p.TotalBytes,
		ExpireAt:               p.ExpireAt,
		NodeCount:              p.NodeCount,
		Stats: ClashProfileStats{
			Total: p.Stats.Total, Active: p.Stats.Active, Healthy: p.Stats.Healthy, Unhealthy: p.Stats.Unhealthy,
			Missing: p.Stats.Missing, Invalid: p.Stats.Invalid, Disabled: p.Stats.Disabled, Bound: p.Stats.Bound,
			BoundAccounts: p.Stats.BoundAccounts,
		},
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

// ClashBoundAccount is an account using a node's exit.
type ClashBoundAccount struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	IsShadow bool   `json:"is_shadow"`
}

func clashAccountsFromService(accounts []service.ClashBoundAccount) []ClashBoundAccount {
	out := make([]ClashBoundAccount, 0, len(accounts))
	for _, acc := range accounts {
		out = append(out, ClashBoundAccount{ID: acc.ID, Name: acc.Name, Platform: acc.Platform, IsShadow: acc.ParentAccountID != nil})
	}
	return out
}

// ClashPlatformChecks reports AI platform reachability through an exit.
type ClashPlatformChecks struct {
	CheckedAt *time.Time        `json:"checked_at"`
	Results   map[string]string `json:"results"`
}

func clashPlatformChecks(c service.ClashPlatformChecks) ClashPlatformChecks {
	results := c.Results
	if results == nil {
		results = map[string]string{}
	}
	return ClashPlatformChecks{CheckedAt: c.CheckedAt, Results: results}
}

// ClashNode is one subscription node and its managed proxy.
type ClashNode struct {
	ID                  int64               `json:"id"`
	ProfileID           int64               `json:"profile_id"`
	ProfileName         string              `json:"profile_name"`
	Name                string              `json:"name"`
	Type                string              `json:"type"`
	Server              string              `json:"server"`
	ServerPort          int                 `json:"server_port"`
	Status              string              `json:"status"`
	StatusReason        string              `json:"status_reason"`
	MissingSince        *time.Time          `json:"missing_since"`
	ListenPort          int                 `json:"listen_port"`
	ProxyID             int64               `json:"proxy_id"`
	HealthStatus        string              `json:"health_status"`
	LatencyMs           *int                `json:"latency_ms"`
	ConsecutiveFailures int                 `json:"consecutive_failures"`
	LastCheckedAt       *time.Time          `json:"last_checked_at"`
	LastCheckError      string              `json:"last_check_error"`
	ExitIP              string              `json:"exit_ip"`
	ExitCountry         string              `json:"exit_country"`
	ExitCountryCode     string              `json:"exit_country_code"`
	ExitRegion          string              `json:"exit_region"`
	ExitCity            string              `json:"exit_city"`
	ExitStatus          string              `json:"exit_status"`
	ExitPendingIP       string              `json:"exit_pending_ip"`
	ExitCheckedAt       *time.Time          `json:"exit_checked_at"`
	ExitChangedAt       *time.Time          `json:"exit_changed_at"`
	PlatformChecks      ClashPlatformChecks `json:"platform_checks"`
	Available           bool                `json:"available"`
	UnavailableReason   string              `json:"unavailable_reason"`
	Accounts            []ClashBoundAccount `json:"accounts"`
	CreatedAt           time.Time           `json:"created_at"`
	UpdatedAt           time.Time           `json:"updated_at"`
}

func ClashNodeFromService(v *service.ClashNodeView) *ClashNode {
	if v == nil {
		return nil
	}
	return &ClashNode{
		ID:                  v.ID,
		ProfileID:           v.ProfileID,
		ProfileName:         v.ProfileName,
		Name:                v.Name,
		Type:                v.Type,
		Server:              v.Server,
		ServerPort:          v.ServerPort,
		Status:              v.Status,
		StatusReason:        v.StatusReason,
		MissingSince:        v.MissingSince,
		ListenPort:          v.ListenPort,
		ProxyID:             v.ProxyID,
		HealthStatus:        v.HealthStatus,
		LatencyMs:           v.LatencyMs,
		ConsecutiveFailures: v.ConsecutiveFailures,
		LastCheckedAt:       v.LastCheckedAt,
		LastCheckError:      v.LastCheckError,
		ExitIP:              v.ExitIP,
		ExitCountry:         v.ExitCountry,
		ExitCountryCode:     v.ExitCountryCode,
		ExitRegion:          v.ExitRegion,
		ExitCity:            v.ExitCity,
		ExitStatus:          v.ExitStatus,
		ExitPendingIP:       v.ExitPendingIP,
		ExitCheckedAt:       v.ExitCheckedAt,
		ExitChangedAt:       v.ExitChangedAt,
		PlatformChecks:      clashPlatformChecks(v.PlatformChecks),
		Available:           v.Available(),
		UnavailableReason:   v.UnavailableReason(),
		Accounts:            clashAccountsFromService(v.Accounts),
		CreatedAt:           v.CreatedAt,
		UpdatedAt:           v.UpdatedAt,
	}
}

// ClashExitOption is one exit offered by the account proxy selector.
type ClashExitOption struct {
	ProxyID           int64               `json:"proxy_id"`
	NodeID            int64               `json:"node_id"`
	ProfileID         int64               `json:"profile_id"`
	ProfileName       string              `json:"profile_name"`
	NodeName          string              `json:"node_name"`
	Type              string              `json:"type"`
	Status            string              `json:"status"`
	HealthStatus      string              `json:"health_status"`
	LatencyMs         *int                `json:"latency_ms"`
	ExitIP            string              `json:"exit_ip"`
	ExitCountry       string              `json:"exit_country"`
	ExitCountryCode   string              `json:"exit_country_code"`
	ExitCity          string              `json:"exit_city"`
	ExitStatus        string              `json:"exit_status"`
	ExitPendingIP     string              `json:"exit_pending_ip"`
	ExitKey           string              `json:"exit_key"`
	Available         bool                `json:"available"`
	UnavailableReason string              `json:"unavailable_reason"`
	Occupants         []ClashBoundAccount `json:"occupants"`
	PlatformChecks    ClashPlatformChecks `json:"platform_checks"`
}

// ClashExitList is the selector payload.
type ClashExitList struct {
	MaxAccountsPerExit       int               `json:"max_accounts_per_exit"`
	AllowUnprobedExitBinding bool              `json:"allow_unprobed_exit_binding"`
	Exits                    []ClashExitOption `json:"exits"`
}

func ClashExitListFromService(list *service.ClashExitList) *ClashExitList {
	if list == nil {
		return &ClashExitList{Exits: []ClashExitOption{}}
	}
	out := &ClashExitList{
		MaxAccountsPerExit:       list.MaxAccountsPerExit,
		AllowUnprobedExitBinding: list.AllowUnprobedExitBinding,
		Exits:                    make([]ClashExitOption, 0, len(list.Exits)),
	}
	for _, e := range list.Exits {
		out.Exits = append(out.Exits, ClashExitOption{
			ProxyID:           e.ProxyID,
			NodeID:            e.NodeID,
			ProfileID:         e.ProfileID,
			ProfileName:       e.ProfileName,
			NodeName:          e.NodeName,
			Type:              e.Type,
			Status:            e.Status,
			HealthStatus:      e.HealthStatus,
			LatencyMs:         e.LatencyMs,
			ExitIP:            e.ExitIP,
			ExitCountry:       e.ExitCountry,
			ExitCountryCode:   e.ExitCountryCode,
			ExitCity:          e.ExitCity,
			ExitStatus:        e.ExitStatus,
			ExitPendingIP:     e.ExitPendingIP,
			ExitKey:           e.ExitKey,
			Available:         e.Available,
			UnavailableReason: e.UnavailableReason,
			Occupants:         clashAccountsFromService(e.Occupants),
			PlatformChecks:    clashPlatformChecks(e.PlatformChecks),
		})
	}
	return out
}

// ClashInstanceStatus is one instance's local core state.
type ClashInstanceStatus struct {
	InstanceID       string     `json:"instance_id"`
	Mode             string     `json:"mode"`
	Ready            bool       `json:"ready"`
	Version          string     `json:"version"`
	ConfigHash       string     `json:"config_hash"`
	Listeners        int        `json:"listeners"`
	ListenerFailures int        `json:"listener_failures"`
	LastAppliedAt    *time.Time `json:"last_applied_at"`
	LastError        string     `json:"last_error"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func clashInstance(s service.ClashInstanceStatus) ClashInstanceStatus {
	out := ClashInstanceStatus{
		InstanceID:       s.InstanceID,
		Mode:             s.Mode,
		Ready:            s.Ready,
		Version:          s.Version,
		ConfigHash:       s.ConfigHash,
		Listeners:        s.Listeners,
		ListenerFailures: s.ListenerFails,
		LastError:        s.LastError,
		UpdatedAt:        s.UpdatedAt,
	}
	if !s.LastAppliedAt.IsZero() {
		applied := s.LastAppliedAt
		out.LastAppliedAt = &applied
	}
	return out
}

// ClashRuntimeStatus is the admin view of the pool runtime.
type ClashRuntimeStatus struct {
	Mode      string                `json:"mode"`
	Enabled   bool                  `json:"enabled"`
	Local     ClashInstanceStatus   `json:"local"`
	Instances []ClashInstanceStatus `json:"instances"`
}

func ClashRuntimeStatusFromService(s service.ClashRuntimeStatus) *ClashRuntimeStatus {
	out := &ClashRuntimeStatus{Mode: s.Mode, Enabled: s.Enabled, Local: clashInstance(s.Local), Instances: make([]ClashInstanceStatus, 0, len(s.Instances))}
	for _, instance := range s.Instances {
		out.Instances = append(out.Instances, clashInstance(instance))
	}
	return out
}
