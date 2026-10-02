package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// 代理过期回退策略。曾经的 "direct"（过期后把账号改投直连）已移除（迁移 251）：
// 分配了代理 / Clash 出口的账号任何情况下都不得静默转为直连，否则泄漏网关出口 IP。
// 过期后要么保持绑定原代理（none，fail-closed），要么改投到指定的备用代理（proxy）。
const (
	FallbackModeNone  = "none"
	FallbackModeProxy = "proxy"
)

// IsValidProxyFallbackMode 报告 mode 是否为受支持的过期回退策略（none / proxy）。
func IsValidProxyFallbackMode(mode string) bool {
	return mode == FallbackModeNone || mode == FallbackModeProxy
}

// Proxy sources. Clash-managed proxies are materialized from Clash subscription
// nodes; their host, port and credentials never change once created.
const (
	ProxySourceManual = "manual"
	ProxySourceClash  = "clash"
	// ProxySourceAll disables the source filter when listing.
	ProxySourceAll = "all"
)

type Proxy struct {
	ID             int64
	Name           string
	Protocol       string
	Host           string
	Port           int
	Username       string
	Password       string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ExpiresAt      *time.Time
	FallbackMode   string
	BackupProxyID  *int64
	ExpiryWarnDays int
	// Source is ProxySourceManual or ProxySourceClash; empty means manual
	// (older scheduler snapshots predate the field).
	Source string
}

// IsClashManaged reports whether the proxy is owned by a Clash subscription node.
func (p *Proxy) IsClashManaged() bool {
	return p != nil && p.Source == ProxySourceClash
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

// IsExpired 报告代理是否已过期（基于 expires_at，与 status 无关）。
func (p *Proxy) IsExpired(now time.Time) bool {
	return p.ExpiresAt != nil && !p.ExpiresAt.After(now)
}

func (p *Proxy) URL() string {
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

// resolveProxyURLByID 按代理 ID 回查 repo 得到出站代理 URL，语义与 Account.ProxyURLForOutbound
// 一致（fail-closed）：
//
//   - proxyID 为 nil → ("", nil)：未指定代理，直连是预期。
//   - repo 缺失 / 查询失败 / 代理不存在 / URL 为空 → 包装 ErrAccountProxyUnavailable 的错误，
//     调用方绝不能回退直连（否则泄漏网关真实出口 IP）。
//
// 用于只持有代理 ID 的出站路径：OAuth 授权流程（管理员为待建账号选定的代理）、
// token 刷新与 privacy 设置等按 account.ProxyID 回查 repo 的后台调用。
func resolveProxyURLByID(ctx context.Context, repo ProxyRepository, proxyID *int64) (string, error) {
	if proxyID == nil {
		return "", nil
	}
	if repo == nil {
		return "", fmt.Errorf("%w: proxy %d cannot be resolved without a proxy repository", ErrAccountProxyUnavailable, *proxyID)
	}
	proxy, err := repo.GetByID(ctx, *proxyID)
	if err != nil {
		return "", fmt.Errorf("%w: proxy %d lookup failed: %w", ErrAccountProxyUnavailable, *proxyID, err)
	}
	if proxy == nil {
		return "", fmt.Errorf("%w: proxy %d not found", ErrAccountProxyUnavailable, *proxyID)
	}
	return (&Account{ProxyID: proxyID, Proxy: proxy}).ProxyURLForOutbound()
}

// accountProxyURLWithRepo 返回账号出站代理 URL：优先用已加载且与 ProxyID 一致的 Proxy 关系，
// 缺失 / 错配时回查 repo；两者都拿不到时 fail-closed 返回错误，绝不回退直连。
func accountProxyURLWithRepo(ctx context.Context, repo ProxyRepository, account *Account) (string, error) {
	if account == nil || account.ProxyID == nil {
		return "", nil
	}
	if proxyURL, err := account.ProxyURLForOutbound(); err == nil {
		return proxyURL, nil
	}
	return resolveProxyURLByID(ctx, repo, account.ProxyID)
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}

// accountProxyURL distinguishes intentional direct routing from a broken binding.
func accountProxyURL(account *Account) (string, error) {
	if account == nil {
		return "", fmt.Errorf("account is required")
	}
	return account.ProxyURLForOutbound()
}
