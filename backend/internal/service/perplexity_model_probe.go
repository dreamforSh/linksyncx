package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	AccountTestModePerplexityModel = "perplexity_model"
	perplexityModelHealthExtraKey  = "perplexity_model_health"
	perplexityModelProbeTimeout    = 30 * time.Second
	perplexityModelProbeMaxBytes   = 8 << 20
	// perplexityModelProbeDefaultPrompt 是未指定提示词时的模型测试消息：像真实用户一样
	// 打个招呼，回复原文作为测试输出返回，便于直观判断模型是否在正常应答。
	perplexityModelProbeDefaultPrompt = "hi"
)

// PerplexityModelProbeResult records one direct, explicitly requested model
// invocation. Verified means the upstream declaration matched; it is not an
// independent identification of the underlying model implementation.
type PerplexityModelProbeResult struct {
	RequestedModel      string                   `json:"requested_model"`
	Preference          string                   `json:"preference"`
	Mode                string                   `json:"mode"`
	Status              string                   `json:"status"`
	Success             bool                     `json:"success"`
	Verified            bool                     `json:"verified"`
	Identity            perplexity.ModelIdentity `json:"identity"`
	ErrorCode           string                   `json:"error_code,omitempty"`
	ErrorMessage        string                   `json:"error_message,omitempty"`
	HTTPStatus          int                      `json:"http_status,omitempty"`
	DurationMs          int64                    `json:"duration_ms"`
	ObservedAtMs        int64                    `json:"observed_at_ms"`
	FinishedAt          time.Time                `json:"finished_at"`
	IdentityFingerprint string                   `json:"identity_fingerprint"`
	UnavailableUntil    *time.Time               `json:"unavailable_until,omitempty"`
	LastVerifiedAt      *time.Time               `json:"last_verified_at,omitempty"`
	Source              string                   `json:"source"`
	Persistence         string                   `json:"persistence"`
	ResponseText        string                   `json:"-"`
}

// PerplexityModelStateRepository merges a single model observation without
// losing peer observations or accepting stale credential/proxy generations.
type PerplexityModelStateRepository interface {
	StorePerplexityModelProbeIfUnchanged(context.Context, int64, map[string]any, *int64, PerplexityModelProbeResult) (bool, error)
	StorePerplexityCatalogIfUnchanged(context.Context, int64, map[string]any, *int64, UpstreamModelMetadataSnapshot) (bool, error)
}

func perplexityModelHealthKey(preference, mode string) string { return preference + ":" + mode }

func getPerplexityModelHealth(account *Account, preference, mode string) *PerplexityModelProbeResult {
	if account == nil {
		return nil
	}
	entries, _ := account.Extra[perplexityModelHealthExtraKey].(map[string]any)
	value := entries[perplexityModelHealthKey(preference, mode)]
	if value == nil {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result PerplexityModelProbeResult
	if json.Unmarshal(body, &result) != nil || result.Preference != preference || result.Mode != mode || result.IdentityFingerprint == "" || result.IdentityFingerprint != perplexityAccountFingerprint(account) {
		return nil
	}
	if result.FinishedAt.After(time.Now()) || time.Since(result.FinishedAt) > 48*time.Hour {
		return nil
	}
	return &result
}

func perplexityModelHealthPauseReason(ctx context.Context, account *Account, requestedModel string) string {
	if account == nil || !account.IsPerplexity() || requestedModel == "" {
		return ""
	}
	preference, mode := perplexity.ResolveModelPreference(account.GetMappedModel(requestedModel))
	if intent, ok := ctx.Value(perplexityThinkingContextKey{}).(perplexityThinkingContext); ok && intent.enabled != nil {
		opts := perplexity.DefaultRequestOptions()
		opts.Internal.ThinkingEnabled = intent.enabled
		selected, err := resolvePerplexityThinkingPreference(account, preference, opts)
		if err != nil {
			return "" // The forwarding validator reports unsupported intent explicitly.
		}
		preference = selected
	}
	return perplexityPreferenceHealthPauseReason(account, preference, mode)
}

func perplexityPreferenceHealthPauseReason(account *Account, preference, mode string) string {
	result := getPerplexityModelHealth(account, preference, mode)
	if result != nil && result.Status == "unavailable" && result.UnavailableUntil != nil && result.UnavailableUntil.After(time.Now()) {
		return "perplexity_model_unavailable"
	}
	return ""
}

func (s *AccountTestService) probePerplexityModel(ctx context.Context, account *Account, modelID, prompt, source string) (*PerplexityModelProbeResult, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, fmt.Errorf("perplexity model probe is not configured")
	}
	account, err := freshPerplexityAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	if account.GetPerplexitySessionToken() == "" {
		return nil, perplexityOptionError("the Perplexity session cookie is missing")
	}
	if modelID == "" || len(modelID) > 200 || !account.IsModelSupported(modelID) {
		return nil, perplexityOptionError("select a model allowed by this account before probing")
	}
	preference, mode, err := perplexity.ResolveModelPreferenceChecked(account.GetMappedModel(modelID))
	if err != nil {
		return nil, perplexityOptionError("%s", err.Error())
	}
	opts, err := resolvePerplexityRequestOptions(account, []byte(`{}`))
	if err != nil {
		return nil, err
	}
	if utf8.RuneCountInString(prompt) > 1024 {
		return nil, fmt.Errorf("perplexity model probe prompt must not exceed 1024 characters")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = perplexityModelProbeDefaultPrompt
	}
	if opts.Local.MaxQueryChars > 0 && utf8.RuneCountInString(prompt) > opts.Local.MaxQueryChars {
		return nil, perplexityOptionError("probe prompt exceeds the account query character limit")
	}
	if _, err := account.ProxyURLForOutbound(); err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	result := &PerplexityModelProbeResult{
		RequestedModel: modelID, Preference: preference, Mode: mode,
		Status: "failed", ObservedAtMs: started.UnixMilli(), Source: source,
		Identity:    perplexity.ModelIdentity{RequestedPreference: preference, Status: "unknown"},
		Persistence: "unavailable",
	}
	if previous := getPerplexityModelHealth(account, preference, mode); previous != nil {
		result.LastVerifiedAt = previous.LastVerifiedAt
	}
	defer func() {
		result.FinishedAt = time.Now().UTC()
		result.DurationMs = time.Since(started).Milliseconds()
		result.IdentityFingerprint = perplexityAccountFingerprint(account)
		if result.Verified {
			result.LastVerifiedAt = &result.FinishedAt
		}
		s.persistPerplexityModelProbe(ctx, account, result)
	}()
	fail := func(code, message string) (*PerplexityModelProbeResult, error) {
		result.ErrorCode, result.ErrorMessage = code, message
		return result, errors.New(message)
	}
	if bucket := perplexityExhaustedQuotaBucket(account, preference, mode, time.Now()); bucket != "" {
		return fail("rate_limited", "the selected Perplexity quota bucket is exhausted")
	}
	ctx, cancel := context.WithTimeout(ctx, perplexityModelProbeTimeout)
	defer cancel()
	body, err := perplexity.BuildAskBodyWithOptions(prompt, preference, mode, opts)
	if err != nil {
		return fail("invalid_request", "could not build the Perplexity model probe")
	}
	creds := perplexityCredentialsFromAccount(account)
	baseURL, err := s.validateUpstreamBaseURL(creds.NormalizedBaseURL())
	if err != nil {
		return fail("invalid_request", "the Perplexity base URL is not allowed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+perplexity.AskPath, bytes.NewReader(body))
	if err != nil {
		return fail("invalid_request", "could not build the Perplexity model probe request")
	}
	creds.SetFetchStyleHeaders(req.Header, "text/event-stream")
	account.ApplyHeaderOverrides(req.Header)
	profile := perplexityDefaultTLSProfile()
	if s.tlsFPProfileService != nil {
		if selected := s.tlsFPProfileService.ResolveTLSProfile(account); selected != nil {
			profile = selected
		}
	}
	resp, err := sendPerplexityAuthenticated(s.httpUpstream, s.accountRepo, account, req, profile)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return fail(perplexityProbeContextCode(ctx, "transport_error"), "perplexity model probe request failed")
	}
	if resp == nil || resp.Body == nil {
		return fail("transport_error", "perplexity model probe returned no response")
	}
	owned := &perplexityUpstreamBody{ReadCloser: resp.Body}
	defer func() { _ = owned.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = owned.Close() })
	defer stop()
	result.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorBody, _ := io.ReadAll(io.LimitReader(owned, 32<<10))
		code := perplexityProbeHTTPError(resp.StatusCode, resp.Header, errorBody)
		if code == "model_unavailable" {
			until := time.Now().Add(5 * time.Minute)
			result.Status, result.UnavailableUntil = "unavailable", &until
		}
		return fail(code, fmt.Sprintf("perplexity model probe failed (HTTP %d, %s)", resp.StatusCode, code))
	}
	limited := &io.LimitedReader{R: owned, N: perplexityModelProbeMaxBytes}
	decoder := perplexity.NewDecoder(limited)
	tracker := perplexity.NewModelIdentityTracker(preference)
	var answer perplexity.Answer
	for {
		if err := ctx.Err(); err != nil {
			return fail(perplexityProbeContextCode(ctx, "cancelled"), "perplexity model probe was cancelled or timed out")
		}
		event, err := decoder.Next()
		if err != nil {
			return fail(perplexityProbeContextCode(ctx, "invalid_stream"), "perplexity model probe did not return a complete valid answer")
		}
		rewritten := event.HasText && !event.IsDelta && !strings.HasPrefix(event.Text, answer.Content())
		delta, err := answer.Apply(event, true)
		if err != nil {
			return fail("invalid_stream", "perplexity model probe returned an invalid answer")
		}
		if rewritten {
			tracker.ResetAnswer()
		}
		tracker.Observe(event, delta)
		result.Identity = tracker.Identity()
		if event.EndOfStream {
			break
		}
	}
	if strings.TrimSpace(answer.Content()) == "" {
		return fail("empty_answer", "perplexity model probe completed without an answer")
	}
	result.Success = true
	result.Verified = result.Identity.Status == "matched"
	result.Status = result.Identity.Status
	if result.Verified {
		result.Status = "verified"
	}
	text := []rune(answer.Content())
	if len(text) > 2048 {
		text = text[:2048]
	}
	result.ResponseText = string(text)
	return result, nil
}

func perplexityProbeContextCode(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	return fallback
}

func perplexityProbeHTTPError(status int, headers http.Header, body []byte) string {
	if (status == 400 || status == 404 || status == 422) && gjson.ValidBytes(body) {
		code := firstNonEmpty(gjson.GetBytes(body, "error.code").String(), gjson.GetBytes(body, "code").String())
		switch code {
		case "model_not_found", "unsupported_model", "model_not_supported", "invalid_model":
			return "model_unavailable"
		}
	}
	class, _ := perplexity.ClassifyHTTPError(status, headers, body)
	switch class {
	case perplexity.ErrClassCloudflare:
		return "cloudflare"
	case perplexity.ErrClassRateLimited:
		return "rate_limited"
	case perplexity.ErrClassAuth:
		if status == http.StatusUnauthorized {
			return "unauthenticated"
		}
		return "forbidden"
	default:
		return "upstream_error"
	}
}

func (s *AccountTestService) persistPerplexityModelProbe(ctx context.Context, account *Account, result *PerplexityModelProbeResult) {
	repo, ok := s.accountRepo.(PerplexityModelStateRepository)
	if !ok || account.ID <= 0 {
		return
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	result.Persistence = "stored"
	applied, err := repo.StorePerplexityModelProbeIfUnchanged(persistCtx, account.ID, account.Credentials, account.ProxyID, *result)
	if err != nil {
		result.Persistence = "failed"
		logger.L().Warn("perplexity: persist model probe failed", zap.Int64("account_id", account.ID))
	} else if !applied {
		result.Persistence, result.Status, result.Verified = "stale", "stale", false
	}
}

func (s *AccountTestService) testPerplexityModelConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID, Text: "Perplexity real model probe; this consumes upstream account quota."})
	source := "manual"
	if background, _ := c.Get("scheduled_test"); background == true {
		source = "scheduled"
	}
	result, err := s.probePerplexityModel(c.Request.Context(), account, modelID, prompt, source)
	if result != nil {
		s.sendEvent(c, TestEvent{Type: "perplexity_probe", Data: result})
		if result.ResponseText != "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: result.ResponseText})
		}
	}
	if err != nil {
		message := "perplexity model probe could not be completed"
		if result != nil && result.ErrorMessage != "" {
			message = result.ErrorMessage
		} else {
			var invalid *perplexityBadRequestError
			if errors.As(err, &invalid) {
				message = invalid.msg
			}
		}
		return s.sendErrorAndEnd(c, message)
	}
	if result == nil || !result.Verified {
		s.sendEvent(c, TestEvent{Type: "status", Code: "perplexity_model_unverified", Text: "The answer completed, but the selected model was not verified by the upstream declaration."})
	}
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true, Data: result})
	return nil
}
