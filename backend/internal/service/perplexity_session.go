package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// perplexityQuotaSnapshotExtraKey 是账号 Extra 中 Perplexity 配额快照的键。
const perplexityQuotaSnapshotExtraKey = "perplexity_quota_snapshot"

// perplexityDefaultTLSProfile 返回未绑定 profile 时的默认 TLS 指纹。
// 空字段 → dialer 使用内置抓包指纹；纯 Go TLS 是 Perplexity 反爬的强 bot
// 信号，本平台绝不裸发。
func perplexityDefaultTLSProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{Name: "perplexity-default"}
}

// doPerplexityRequest 以 fetch 风格头 + Cookie 会话 + TLS 指纹发送 Perplexity
// 网页端请求（与转发路径同一套反爬约束）。
func (s *AccountTestService) doPerplexityRequest(ctx context.Context, account *Account, method, path string) (*http.Response, error) {
	creds := perplexityCredentialsFromAccount(account)
	if strings.TrimSpace(creds.SessionToken) == "" {
		return nil, fmt.Errorf("perplexity session token is missing (credentials.api_key)")
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, creds.NormalizedBaseURL()+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build perplexity request: %w", err)
	}
	creds.SetFetchStyleHeaders(req.Header, "application/json")

	profile := perplexityDefaultTLSProfile()
	if s.tlsFPProfileService != nil {
		if p := s.tlsFPProfileService.ResolveTLSProfile(account); p != nil {
			profile = p
		}
	}
	return s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, profile)
}

// ValidatePerplexitySession 探测 Perplexity 网页会话是否存活：
// GET /api/auth/session 返回 200 且 JSON 含非空 user 对象即为有效；
// 401/403 表示会话 Cookie 已失效。
func (s *AccountTestService) ValidatePerplexitySession(ctx context.Context, account *Account) error {
	if s == nil || account == nil {
		return fmt.Errorf("perplexity session validation is not available")
	}
	resp, err := s.doPerplexityRequest(ctx, account, http.MethodGet, perplexity.SessionPath)
	if err != nil {
		return fmt.Errorf("perplexity session check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read perplexity session response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("perplexity session cookie is invalid or expired (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("perplexity session check returned HTTP %d", resp.StatusCode)
	}
	if user := gjson.GetBytes(body, "user"); !user.Exists() || !user.IsObject() || len(user.Map()) == 0 {
		return fmt.Errorf("perplexity session is not authenticated (empty user object)")
	}
	return nil
}

// perplexitySessionSummary 从 /api/auth/session 响应提取订阅挡位等账号画像。
// Perplexity 的 NextAuth session 在不同版本把挡位放在不同字段，逐一探测常见路径。
func perplexitySessionSummary(body []byte) map[string]any {
	if !gjson.ValidBytes(body) {
		return nil
	}
	summary := map[string]any{}
	if email := strings.TrimSpace(gjson.GetBytes(body, "user.email").String()); email != "" {
		summary["email"] = email
	}
	for _, path := range []string{
		"user.subscription_state", "user.subscription.tier", "user.subscription_status",
		"user.product_tier", "user.plan", "subscription_state", "subscription.tier",
	} {
		if v := strings.TrimSpace(gjson.GetBytes(body, path).String()); v != "" {
			summary["subscription_tier"] = v
			break
		}
	}
	if expires := strings.TrimSpace(gjson.GetBytes(body, "expires").String()); expires != "" {
		summary["session_expires"] = expires
	}
	return summary
}

// perplexityCreditsSummary 把 /rest/billing/credits/balance 响应压成一层标量字段
// （数值/字符串），供账号测试输出与快照展示；字段名随上游版本漂移，故不逐个硬编码。
func perplexityCreditsSummary(body []byte) map[string]any {
	if !gjson.ValidBytes(body) {
		return nil
	}
	summary := map[string]any{}
	for k, v := range gjson.GetBytes(body, "@this").Map() {
		switch v.Type {
		case gjson.Number:
			summary[k] = v.Num
		case gjson.String:
			if s := strings.TrimSpace(v.String()); s != "" && len(s) <= 64 {
				summary[k] = s
			}
		case gjson.True, gjson.False:
			summary[k] = v.Bool()
		}
	}
	if len(summary) == 0 {
		return nil
	}
	return summary
}

// FetchPerplexityAccountInfo 采集 Perplexity 账号画像：会话挡位（订阅 tier）、
// Computer 积分余额、速率配额快照，统一写入账号 Extra（键 perplexity_quota_snapshot）。
// 尽力而为：单项失败仅记日志并跳过，不影响其他项；返回已采集到的快照（可能为空）。
func (s *AccountTestService) FetchPerplexityAccountInfo(ctx context.Context, account *Account) map[string]any {
	snapshot := map[string]any{"fetched_at": time.Now().UTC().Format(time.RFC3339)}
	if s == nil || account == nil {
		return snapshot
	}

	fetchJSON := func(path string) ([]byte, int) {
		resp, err := s.doPerplexityRequest(ctx, account, http.MethodGet, path)
		if err != nil {
			logger.L().Debug("perplexity: account info fetch failed",
				zap.Int64("account_id", account.ID), zap.String("path", path), zap.Error(err))
			return nil, 0
		}
		defer func() { _ = resp.Body.Close() }()
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if readErr != nil {
			return nil, resp.StatusCode
		}
		return body, resp.StatusCode
	}

	if body, status := fetchJSON(perplexity.SessionPath); status == http.StatusOK {
		if summary := perplexitySessionSummary(body); len(summary) > 0 {
			snapshot["session"] = summary
		}
	}
	if body, status := fetchJSON(perplexity.CreditsBalancePath + "?include_credits=true"); status == http.StatusOK {
		if summary := perplexityCreditsSummary(body); len(summary) > 0 {
			snapshot["credits"] = summary
		}
	}
	if body, status := fetchJSON(perplexity.RateLimitAllPath); status == http.StatusOK && gjson.ValidBytes(body) {
		var raw map[string]any
		if json.Unmarshal(body, &raw) == nil {
			snapshot["rate_limits"] = raw
		}
	}

	if s.accountRepo != nil && account.ID > 0 {
		if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
			perplexityQuotaSnapshotExtraKey: snapshot,
		}); err != nil {
			logger.L().Debug("perplexity: persist account info snapshot failed", zap.Int64("account_id", account.ID), zap.Error(err))
		}
	}
	return snapshot
}

// perplexityAccountInfoText 把账号画像快照压成一行测试结果文本。
func perplexityAccountInfoText(snapshot map[string]any) string {
	parts := []string{"Perplexity web session is valid."}
	if sess, ok := snapshot["session"].(map[string]any); ok {
		if tier, ok := sess["subscription_tier"].(string); ok && tier != "" {
			parts = append(parts, "tier="+tier)
		}
		if email, ok := sess["email"].(string); ok && email != "" {
			parts = append(parts, "email="+email)
		}
	}
	if credits, ok := snapshot["credits"].(map[string]any); ok && len(credits) > 0 {
		keys := make([]string, 0, len(credits))
		for k := range credits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var fields []string
		for _, k := range keys {
			fields = append(fields, fmt.Sprintf("%s=%v", k, credits[k]))
		}
		parts = append(parts, "credits{"+strings.Join(fields, ", ")+"}")
	}
	if _, ok := snapshot["rate_limits"]; ok {
		parts = append(parts, "rate-limit snapshot updated")
	}
	return strings.Join(parts, " ")
}

// testPerplexityAccountConnection 是管理端账号测试入口：探测网页会话存活，
// 并尽力刷新配额快照。
func (s *AccountTestService) testPerplexityAccountConnection(c *gin.Context, account *Account, modelID string) error {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = "sonar"
	}
	testModelID = account.GetMappedModel(testModelID)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})

	ctx := c.Request.Context()
	if err := s.ValidatePerplexitySession(ctx, account); err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	snapshot := s.FetchPerplexityAccountInfo(ctx, account)
	s.sendEvent(c, TestEvent{Type: "content", Text: perplexityAccountInfoText(snapshot)})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
