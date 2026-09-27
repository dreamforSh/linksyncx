package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	defaultClaudeUsageURL   = "https://api.anthropic.com/api/oauth/usage"
	defaultClaudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	// 重置领取接口挂在 /api/organizations/{org_uuid}/ 下，而不是 /api/oauth/。
	defaultClaudeOrganizationsURL = "https://api.anthropic.com/api/organizations"

	claudeUsageRequestTimeout = 30 * time.Second
	// 与 Claude Code 领取重置时的 25s 超时一致：领取不可重复，宁可多等也不提前放弃。
	claudeResetClaimTimeout = 25 * time.Second
	claudeProfileTimeout    = 15 * time.Second
	claudeAPIMaxErrorBody   = 4 << 10
)

// 默认 User-Agent 跟随运行期生效的 CLI 版本（真实客户端 2.1.280 的
// /api/oauth/usage UA 形如 claude-code/2.1.280）；指纹缓存命中时优先用账号指纹 UA。
func defaultUsageUserAgent() string {
	return "claude-code/" + claude.EffectiveCLIVersion()
}

type claudeUsageService struct {
	usageURL          string
	profileURL        string
	organizationsURL  string
	allowPrivateHosts bool
	httpUpstream      service.HTTPUpstream
}

func newClaudeUsageService(httpUpstream service.HTTPUpstream) *claudeUsageService {
	return &claudeUsageService{
		usageURL:         defaultClaudeUsageURL,
		profileURL:       defaultClaudeProfileURL,
		organizationsURL: defaultClaudeOrganizationsURL,
		httpUpstream:     httpUpstream,
	}
}

// NewClaudeUsageFetcher 创建 Claude 用量获取服务
// httpUpstream: 可选，如果提供则支持 TLS 指纹伪装
func NewClaudeUsageFetcher(httpUpstream service.HTTPUpstream) service.ClaudeUsageFetcher {
	return newClaudeUsageService(httpUpstream)
}

// NewClaudeOAuthAPIClient 创建 Claude OAuth 账号侧接口（profile、重置领取）的客户端，
// 与用量获取共用请求头、TLS 指纹与代理处理。
func NewClaudeOAuthAPIClient(httpUpstream service.HTTPUpstream) service.ClaudeOAuthAPIClient {
	return newClaudeUsageService(httpUpstream)
}

// FetchUsage 简单版本，不支持 TLS 指纹（向后兼容）
func (s *claudeUsageService) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*service.ClaudeUsageResponse, error) {
	return s.FetchUsageWithOptions(ctx, &service.ClaudeUsageFetchOptions{
		AccessToken: accessToken,
		ProxyURL:    proxyURL,
	})
}

// FetchUsageWithOptions 完整版本，支持 TLS 指纹和自定义 User-Agent
func (s *claudeUsageService) FetchUsageWithOptions(ctx context.Context, opts *service.ClaudeUsageFetchOptions) (*service.ClaudeUsageResponse, error) {
	if opts == nil {
		return nil, fmt.Errorf("options is nil")
	}

	target := s.usageURL
	if query := strings.TrimSpace(opts.Query); query != "" {
		separator := "?"
		if strings.Contains(target, "?") {
			separator = "&"
		}
		target += separator + query
	}

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	setClaudeOAuthHeaders(req, opts)

	resp, err := s.do(req, opts, claudeUsageRequestTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		msg := fmt.Sprintf("API returned status %d: %s", resp.StatusCode, string(body))
		return nil, infraerrors.New(http.StatusInternalServerError, "UPSTREAM_ERROR", msg)
	}

	var usageResp service.ClaudeUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		return nil, fmt.Errorf("decode response failed: %w", err)
	}

	return &usageResp, nil
}

// FetchProfile 读取 GET /api/oauth/profile 的原始响应（订阅类型、限额档位、组织 UUID）。
func (s *claudeUsageService) FetchProfile(ctx context.Context, opts *service.ClaudeUsageFetchOptions) ([]byte, error) {
	if opts == nil {
		return nil, fmt.Errorf("options is nil")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.profileURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	setClaudeOAuthHeaders(req, opts)
	req.Header.Set("Cache-Control", "no-cache")
	return s.doJSON(req, opts, claudeProfileTimeout)
}

// ClaimRateLimitReset 调用 POST /api/organizations/{org}/reset_rate_limits 领取一次重置，
// 返回 2xx 响应体；领取结果（reset / already_used / not_limited ...）由调用方解析。
func (s *claudeUsageService) ClaimRateLimitReset(ctx context.Context, opts *service.ClaudeUsageFetchOptions, orgUUID string, claim *service.ClaudeRateLimitResetRequest) ([]byte, error) {
	if opts == nil || claim == nil {
		return nil, fmt.Errorf("options is nil")
	}
	orgUUID = strings.TrimSpace(orgUUID)
	if orgUUID == "" {
		return nil, fmt.Errorf("organization uuid is required")
	}
	payload, err := json.Marshal(claim)
	if err != nil {
		return nil, fmt.Errorf("encode request failed: %w", err)
	}
	target := strings.TrimRight(s.organizationsURL, "/") + "/" + url.PathEscape(orgUUID) + "/reset_rate_limits"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request failed: %w", err)
	}
	setClaudeOAuthHeaders(req, opts)
	return s.doJSON(req, opts, claudeResetClaimTimeout)
}

// setClaudeOAuthHeaders 设置与抓包一致的请求头（不设置 Accept-Encoding，让 Go 自动处理压缩）。
func setClaudeOAuthHeaders(req *http.Request, opts *service.ClaudeUsageFetchOptions) {
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.AccessToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	// 设置 User-Agent（优先使用缓存的 Fingerprint，否则使用默认值）
	userAgent := defaultUsageUserAgent()
	if opts.Fingerprint != nil && opts.Fingerprint.UserAgent != "" {
		userAgent = opts.Fingerprint.UserAgent
	}
	req.Header.Set("User-Agent", userAgent)
}

// do 发送请求：有 TLS Profile 且有 HTTPUpstream 时走 DoWithTLS，否则走普通 HTTP 客户端。
func (s *claudeUsageService) do(req *http.Request, opts *service.ClaudeUsageFetchOptions, timeout time.Duration) (*http.Response, error) {
	if opts.TLSProfile != nil && s.httpUpstream != nil {
		ctx, cancel := context.WithTimeout(req.Context(), timeout)
		resp, err := s.httpUpstream.DoWithTLS(req.WithContext(ctx), opts.ProxyURL, opts.AccountID, 0, opts.TLSProfile)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("request with TLS fingerprint failed: %w", err)
		}
		resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
		return resp, nil
	}

	// 不启用 TLS 指纹，使用普通 HTTP 客户端
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           opts.ProxyURL,
		Timeout:            timeout,
		ValidateResolvedIP: true,
		AllowPrivateHosts:  s.allowPrivateHosts,
	})
	if err != nil {
		return nil, fmt.Errorf("create http client failed: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

// doJSON 发送请求并返回 2xx 响应体；非 2xx 映射为带状态语义的业务错误。
// 上游 401/403 不能原样透传：后台前端会把 401 当成管理员登录失效。
func (s *claudeUsageService) doJSON(req *http.Request, opts *service.ClaudeUsageFetchOptions, timeout time.Duration) ([]byte, error) {
	resp, err := s.do(req, opts, timeout)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR", err.Error())
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if readErr != nil {
			return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR", fmt.Sprintf("read response failed: %v", readErr))
		}
		return body, nil
	}

	detail := strings.TrimSpace(string(body))
	if len(detail) > claudeAPIMaxErrorBody {
		detail = detail[:claudeAPIMaxErrorBody]
	}
	msg := fmt.Sprintf("upstream returned status %d", resp.StatusCode)
	if detail != "" {
		msg += ": " + detail
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_AUTH_FAILED", msg)
	case http.StatusTooManyRequests:
		return nil, infraerrors.New(http.StatusTooManyRequests, "CLAUDE_UPSTREAM_RATE_LIMITED", msg)
	default:
		return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR", msg)
	}
}
