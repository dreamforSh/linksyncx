package service

import (
	"context"
	"encoding/json"
	"errors"
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
	if s == nil || s.httpUpstream == nil || account == nil {
		return nil, fmt.Errorf("perplexity account info sender is not configured")
	}
	creds := perplexityCredentialsFromAccount(account)
	if strings.TrimSpace(creds.SessionToken) == "" {
		return nil, fmt.Errorf("perplexity session token is missing (credentials.api_key)")
	}
	if _, err := account.ProxyURLForOutbound(); err != nil {
		return nil, err
	}
	baseURL, err := s.validateUpstreamBaseURL(creds.NormalizedBaseURL())
	if err != nil {
		return nil, fmt.Errorf("invalid perplexity base URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+path, nil)
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
	return sendPerplexityAuthenticated(s.httpUpstream, s.accountRepo, account, req, profile)
}

// ValidatePerplexitySession 探测 Perplexity 网页会话是否存活：
// GET /api/auth/session 返回 200 且 JSON 含非空 user 对象即为有效。
// 401 / 空 user 明确失效；403 可能是站点拒绝，不能据此捏造过期状态。
func (s *AccountTestService) ValidatePerplexitySession(ctx context.Context, account *Account) error {
	if s == nil || account == nil {
		return fmt.Errorf("perplexity session validation is not available")
	}
	profile := perplexityProfileFromSnapshot(s.refreshPerplexityAccountInfo(ctx, account, []string{"session"}))
	if profile.SessionValid != nil && *profile.SessionValid {
		return nil
	}
	if message := profile.Sections["session"].Error; message != "" {
		return errors.New(message)
	}
	return fmt.Errorf("perplexity session could not be verified")
}

func (s *AccountTestService) fetchPerplexityJSON(ctx context.Context, account *Account, path string) ([]byte, int, error) {
	resp, err := s.doPerplexityRequest(ctx, account, http.MethodGet, path)
	if err != nil {
		if errors.Is(err, ErrAccountProxyUnavailable) {
			return nil, 0, fmt.Errorf("perplexity assigned proxy is unavailable: %w", ErrAccountProxyUnavailable)
		}
		// Transport errors can embed proxy credentials. Keep API-facing errors
		// deliberately generic and never include raw upstream bodies or cookies.
		return nil, 0, fmt.Errorf("perplexity request failed")
	}
	if resp == nil || resp.Body == nil {
		return nil, 0, fmt.Errorf("perplexity response is empty")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read perplexity response failed")
	}
	if len(body) > 1<<20 {
		return nil, resp.StatusCode, fmt.Errorf("perplexity response exceeds size limit")
	}
	return body, resp.StatusCode, nil
}

func validatePerplexitySessionResponse(body []byte, status int) (bool, error) {
	if status == http.StatusUnauthorized {
		return true, fmt.Errorf("perplexity session is unauthenticated or expired (HTTP %d)", status)
	}
	if status == http.StatusForbidden {
		return false, fmt.Errorf("perplexity session verification was forbidden (HTTP 403)")
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("perplexity session check returned HTTP %d", status)
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return false, fmt.Errorf("perplexity session response is not a JSON object")
	}
	if user := gjson.GetBytes(body, "user"); !user.IsObject() || len(user.Map()) == 0 {
		return true, fmt.Errorf("perplexity session is unauthenticated (empty user object)")
	}
	return false, nil
}

func (s *AccountTestService) updatePerplexitySessionHealth(ctx context.Context, account *Account, valid bool) {
	if s.accountRepo == nil || account.ID <= 0 {
		return
	}
	var err error
	if !valid {
		err = s.accountRepo.SetError(ctx, account.ID, PerplexityInvalidSessionMessage)
	} else if account.Status == StatusError && strings.HasPrefix(account.ErrorMessage, "perplexity session is unauthenticated") {
		// A valid session only recovers this platform's own authentication error;
		// it must not clear unrelated quota, policy, or administrator failures.
		err = s.accountRepo.ClearError(ctx, account.ID)
	}
	if err != nil {
		logger.L().Debug("perplexity: persist session health failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
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

// FetchPerplexityAccountInfo refreshes independent profile sections while retaining
// each failed section's last-good data and timestamp. A collection attempt is not
// a successful refresh of all sections.
func (s *AccountTestService) FetchPerplexityAccountInfo(ctx context.Context, account *Account) map[string]any {
	return s.refreshPerplexityAccountInfo(ctx, account, perplexityInfoSections[:])
}

func (s *AccountTestService) refreshPerplexityAccountInfo(ctx context.Context, account *Account, sections []string) map[string]any {
	started := time.Now().UTC()
	var previous any
	if account != nil {
		previous = account.Extra[perplexityQuotaSnapshotExtraKey]
	}
	if s == nil {
		profile := perplexityProfileFromSnapshot(previous)
		profile.SessionValid = nil
		for _, name := range sections {
			profile.failSection(name, "perplexity account info sender is not configured", started)
		}
		return profile.persistedSnapshot()
	}
	fresh, loadErr := freshPerplexityAccount(ctx, s.accountRepo, account)
	if loadErr == nil {
		account = fresh
		previous = account.Extra[perplexityQuotaSnapshotExtraKey]
	}
	profile := perplexityProfileFromSnapshot(previous)
	fingerprint := perplexityAccountFingerprint(account)
	if profile.IdentityFingerprint != "" && profile.IdentityFingerprint != fingerprint {
		profile = perplexityProfileFromSnapshot(nil)
	} else if profile.IdentityFingerprint == "" {
		// Old observations have no credential generation. Keep them visible as
		// last-good data, but do not promote them to fresh scheduling evidence.
		for name, section := range profile.Sections {
			section.Status = "stale"
			profile.Sections[name] = section
		}
	}
	profile.AttemptedAt = &started
	profile.ObservedAtMs = started.UnixMilli()
	if loadErr != nil {
		profile.SessionValid = nil
		for _, name := range sections {
			profile.failSection(name, "current perplexity account could not be loaded", started)
		}
		return profile.persistedSnapshot()
	}
	_, conditional := s.accountRepo.(PerplexityAccountStateRepository)
	var observedSessionValid *bool
	allSuccessful := true
	for _, name := range sections {
		path := perplexity.SessionPath
		switch name {
		case "session":
			profile.SessionValid = nil
		case "credits":
			path = perplexity.CreditsBalancePath + "?include_credits=true"
		case "rate_limits":
			path = perplexity.RateLimitAllPath
		default:
			continue
		}
		attemptedAt := time.Now().UTC()
		body, status, err := s.fetchPerplexityJSON(ctx, account, path)
		var data map[string]any
		if err == nil && name == "session" {
			var invalid bool
			invalid, err = validatePerplexitySessionResponse(body, status)
			if invalid {
				valid := false
				profile.SessionValid, observedSessionValid = &valid, &valid
				if !conditional {
					s.updatePerplexitySessionHealth(ctx, account, false)
				}
			} else if err == nil {
				valid := true
				profile.SessionValid, observedSessionValid = &valid, &valid
				if !conditional {
					s.updatePerplexitySessionHealth(ctx, account, true)
				}
				data = perplexitySessionSummary(body)
			}
		} else if err == nil {
			if status != http.StatusOK {
				err = fmt.Errorf("perplexity %s check returned HTTP %d", name, status)
			} else if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
				err = fmt.Errorf("perplexity %s response is not a JSON object", name)
			} else if name == "credits" {
				data = perplexityCreditsSummary(body)
				if data == nil {
					err = fmt.Errorf("perplexity credits response contains no balance fields")
				}
			} else if json.Unmarshal(body, &data) != nil {
				err = fmt.Errorf("perplexity rate limits response is not valid JSON")
			}
		}
		if err != nil {
			allSuccessful = false
			profile.failSection(name, err.Error(), attemptedAt)
			continue
		}
		fetchedAt := time.Now().UTC()
		profile.Snapshot[name] = data
		profile.Sections[name] = PerplexityUsageSection{Status: "success", FetchedAt: &fetchedAt, AttemptedAt: &attemptedAt}
	}
	if allSuccessful && len(sections) == len(perplexityInfoSections) {
		fetchedAt := time.Now().UTC()
		profile.FetchedAt = &fetchedAt
	}
	profile.IdentityFingerprint = perplexityAccountFingerprint(account)
	snapshot := profile.persistedSnapshot()
	if s.accountRepo == nil || account.ID <= 0 {
		return snapshot
	}
	if repo, ok := s.accountRepo.(PerplexityAccountStateRepository); ok {
		applied, err := repo.ApplyPerplexityProfileIfUnchanged(ctx, account.ID, account.Credentials, account.ProxyID, PerplexityProfileUpdate{
			Snapshot: snapshot, SessionValid: observedSessionValid, ObservedAtMs: started.UnixMilli(),
		})
		if err != nil {
			logger.L().Warn("perplexity: persist account info snapshot failed", zap.Int64("account_id", account.ID))
		} else if !applied {
			// A newer observation or an administrator edit won the race. Return
			// that identity's data rather than labeling the old response current.
			latest, readErr := s.accountRepo.GetByID(ctx, account.ID)
			if readErr == nil && latest != nil {
				current := perplexityProfileFromSnapshot(latest.Extra[perplexityQuotaSnapshotExtraKey])
				if current.IdentityFingerprint == perplexityAccountFingerprint(latest) {
					return current.persistedSnapshot()
				}
			}
			unknown := perplexityProfileFromSnapshot(nil)
			for _, name := range perplexityInfoSections {
				unknown.failSection(name, "perplexity account changed during refresh", started)
			}
			return unknown.persistedSnapshot()
		}
	} else if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{perplexityQuotaSnapshotExtraKey: snapshot}); err != nil {
		logger.L().Debug("perplexity: persist account info snapshot failed", zap.Int64("account_id", account.ID))
	}
	return snapshot
}

// perplexityAccountInfoText 把账号画像快照压成一行测试结果文本。
func perplexityAccountInfoText(snapshot map[string]any) string {
	parts := []string{"Perplexity web session is valid."}
	profile := perplexityProfileFromSnapshot(snapshot)
	if _, hasSections := snapshot["sections"]; hasSections {
		switch {
		case profile.SessionValid == nil:
			parts[0] = "Perplexity web session could not be verified."
		case !*profile.SessionValid:
			parts[0] = "Perplexity web session is unauthenticated or expired."
		}
	}
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
		if section, present := profile.Sections["rate_limits"]; snapshot["sections"] != nil && present && section.Status != "success" {
			parts = append(parts, "rate-limit snapshot retained (last-good)")
		} else {
			parts = append(parts, "rate-limit snapshot updated")
		}
	}
	for _, name := range perplexityInfoSections {
		if section := profile.Sections[name]; section.Error != "" {
			parts = append(parts, name+": "+section.Error)
		}
	}
	return strings.Join(parts, " ")
}

// testPerplexityAccountConnection 是管理端账号测试入口：探测网页会话存活，
// 并尽力刷新配额快照。
func (s *AccountTestService) testPerplexityAccountConnection(c *gin.Context, account *Account, _ string) error {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Text: "Perplexity session validation only; the selected model is not invoked."})

	snapshot := s.FetchPerplexityAccountInfo(c.Request.Context(), account)
	profile := perplexityProfileFromSnapshot(snapshot)
	if profile.SessionValid == nil || !*profile.SessionValid {
		message := profile.Sections["session"].Error
		if message == "" {
			message = "perplexity session could not be verified"
		}
		return s.sendErrorAndEnd(c, message)
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: perplexityAccountInfoText(snapshot)})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
