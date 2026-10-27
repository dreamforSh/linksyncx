package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	codeBuddyChatEndpoint = "/v2/chat/completions"
	// codeBuddyModelBlockCooldown parks a model the account cannot serve
	// (11102 "service info not found").
	codeBuddyModelBlockCooldown = 6 * time.Hour
	// codeBuddyCreditsExhaustedCooldown parks an account whose balance ran out
	// until the next billing probe can confirm a refill.
	codeBuddyCreditsExhaustedCooldown = time.Hour
)

// codeBuddyCCTarget resolves the bearer token and chat URL for the CC paths.
func (s *OpenAIGatewayService) codeBuddyCCTarget(ctx context.Context, account *Account) (string, string, error) {
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", "", fmt.Errorf("account %d missing access_token", account.ID)
	}
	return token, codebuddy.ChatCompletionsURL(account.CodeBuddyProfile()), nil
}

// sendCodeBuddyUpstreamRequest is the CodeBuddy branch of
// sendCCUpstreamRequest. The upstream only streams, so the body is always
// sent with stream=true; for a non-stream client the SSE is folded back into
// one chat.completion JSON before the caller's buffered path reads it. Only
// the official client identity is sent: no client header is passed through.
func (s *OpenAIGatewayService) sendCodeBuddyUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	clientStream bool,
	bearerToken string,
) (*http.Response, error) {
	creds, err := account.CodeBuddyCredentials()
	if err != nil {
		return nil, fmt.Errorf("invalid codebuddy credentials: %w", err)
	}
	creds.AccessToken = bearerToken
	upstreamModel := gjson.GetBytes(body, "model").String()
	if denial := codeBuddyCreditPolicyDenial(account, codebuddy.UpstreamModel(upstreamModel, creds.Profile)); denial != "" {
		MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
		writeCodeBuddyClientError(c, http.StatusForbidden, "permission_error", denial)
		return nil, fmt.Errorf("codebuddy credit policy denied model %s", upstreamModel)
	}
	prepared, err := codebuddy.PrepareChatBody(body, creds.Profile)
	if err != nil {
		var reqErr *codebuddy.RequestError
		if errors.As(err, &reqErr) {
			writeCodeBuddyClientError(c, http.StatusBadRequest, "invalid_request_error", reqErr.Message)
		}
		return nil, err
	}

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	targetURL := codebuddy.ChatCompletionsURL(creds.Profile)
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(prepared))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	SetActualOpenAIUpstreamEndpoint(c, codeBuddyChatEndpoint)
	upstreamReq = upstreamReq.WithContext(WithHTTPUpstreamProfile(upstreamReq.Context(), HTTPUpstreamProfileLongStream))
	codebuddy.ApplyHeaders(upstreamReq.Header, codebuddy.CredentialHeaders(codebuddy.IdentityOf(creds)))
	conversationID := codebuddy.ConversationID(creds.Profile, codeBuddySessionKey(c, account, prepared))
	codebuddy.ApplyHeaders(upstreamReq.Header, codebuddy.RequestIDHeaders(conversationID))
	account.ApplyHeaderOverrides(upstreamReq.Header)

	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, proxyErr, false)
	}
	resp, err := s.doOpenAIUpstream(upstreamReq, proxyURL, account)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return resp, nil
	}
	s.recordCodeBuddyCreditsUsage(ctx, account, gjson.GetBytes(prepared, "model").String())
	if clientStream {
		return resp, nil
	}
	return s.aggregateCodeBuddyStream(resp), nil
}

// aggregateCodeBuddyStream swaps the SSE body for an aggregated JSON body. An
// error frame inside the stream becomes an HTTP error response so the
// caller's error path (failover, client formatting) handles it.
func (s *OpenAIGatewayService) aggregateCodeBuddyStream(resp *http.Response) *http.Response {
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	aggregated, err := codebuddy.AggregateChatStream(resp.Body, maxLineSize)
	_ = resp.Body.Close()
	resp.Header = resp.Header.Clone()
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	if err != nil {
		status := http.StatusBadGateway
		body := []byte(`{"error":{"type":"upstream_error","message":"CodeBuddy upstream stream ended without a usable response"}}`)
		var streamErr *codebuddy.StreamError
		if errors.As(err, &streamErr) {
			body = streamErr.Body
			if codebuddy.IsContentFilterError(body) {
				status = http.StatusBadRequest
			}
		}
		resp.StatusCode = status
		resp.Status = fmt.Sprintf("%d %s", status, http.StatusText(status))
		resp.Header.Set("Content-Type", "application/json")
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Body = io.NopCloser(bytes.NewReader(aggregated))
	return resp
}

// codeBuddySessionKey keeps X-Conversation-ID stable across the turns of one
// conversation: an explicit client session id when present, else a hash of
// the system prompt plus the first user message. The key is scoped to the
// account so two accounts never share a conversation.
func codeBuddySessionKey(c *gin.Context, account *Account, body []byte) string {
	if explicit := explicitOpenAIHeaderSessionID(c); explicit != "" {
		return fmt.Sprintf("%d:h:%s", account.ID, explicit)
	}
	var system strings.Builder
	firstUser := ""
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		role := message.Get("role").String()
		if role == "system" && firstUser == "" {
			_, _ = system.WriteString(codeBuddyMessageText(message.Get("content")))
			continue
		}
		if role == "user" {
			firstUser = codeBuddyMessageText(message.Get("content"))
			break
		}
	}
	if system.Len() == 0 && firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(system.String() + "\x00" + firstUser))
	return fmt.Sprintf("%d:c:%s", account.ID, hex.EncodeToString(sum[:16]))
}

func codeBuddyMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var b strings.Builder
	for _, part := range content.Array() {
		if part.Get("type").String() == "text" {
			if b.Len() > 0 {
				_ = b.WriteByte('\n')
			}
			_, _ = b.WriteString(part.Get("text").String())
		}
	}
	return b.String()
}

// codeBuddyAnthropicReasoningEffort keeps the Messages bridge's stand-in
// "medium" (used when output_config names no effort) away from DeepSeek, whose
// unnamed default is max; thinking.type=disabled turns thinking off.
func codeBuddyAnthropicReasoningEffort(req *apicompat.AnthropicRequest, upstreamModel, convertedEffort string) string {
	if req == nil || !codebuddy.IsDeepSeekModel(upstreamModel) {
		return convertedEffort
	}
	if req.Thinking != nil && strings.EqualFold(strings.TrimSpace(req.Thinking.Type), "disabled") {
		return "none"
	}
	if req.OutputConfig == nil || strings.TrimSpace(req.OutputConfig.Effort) == "" {
		return ""
	}
	return convertedEffort
}

// codeBuddyOutboundReasoningEffort mirrors codebuddy.PrepareChatBody's DeepSeek
// effort rewrite so usage logs record the level the upstream actually ran.
func codeBuddyOutboundReasoningEffort(upstreamModel string, requested *string) *string {
	if !codebuddy.IsDeepSeekModel(upstreamModel) {
		return requested
	}
	raw := ""
	if requested != nil {
		raw = *requested
	}
	effort, send := codebuddy.DeepSeekReasoningEffort(raw)
	if !send {
		return nil
	}
	return &effort
}

func writeCodeBuddyClientError(c *gin.Context, status int, errType, message string) {
	if c == nil || c.Writer == nil || c.Writer.Written() {
		return
	}
	if c.Request != nil && c.Request.URL != nil && strings.HasSuffix(c.Request.URL.Path, "/messages") {
		MarkResponseCommitted(c)
		writeAnthropicError(c, status, errType, message)
		return
	}
	writeChatCompletionsError(c, status, errType, message)
}

// handleCodeBuddyUpstreamError applies CodeBuddy's error semantics before the
// generic OpenAI handling. decided=false defers to the generic path (401/403
// token invalidation, 5xx failover, admin rules).
func (s *OpenAIGatewayService) handleCodeBuddyUpstreamError(
	ctx context.Context,
	account *Account,
	statusCode int,
	headers http.Header,
	body []byte,
	upstreamModel string,
) (decided bool, failover bool) {
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	switch {
	case codebuddy.IsContentFilterError(body):
		// A refusal about the prompt says nothing about the account.
		return true, false
	case codebuddy.CreditsExhausted(statusCode, body):
		s.markCodeBuddyCreditsExhausted(stateCtx, account)
		s.setCodeBuddyRateLimited(stateCtx, account, time.Now().Add(codeBuddyCreditsExhaustedCooldown), "codebuddy_credits_exhausted")
		return true, true
	case statusCode == http.StatusTooManyRequests:
		s.setCodeBuddyRateLimited(stateCtx, account, codebuddy.RateLimitResetAt(headers, body, time.Now()), "429")
		return true, true
	case codebuddy.ModelNotServable(statusCode, body):
		if s.accountRepo != nil && upstreamModel != "" {
			modelKey := modelRateLimitKeyForUpstreamModelNotFound(stateCtx, account, upstreamModel)
			resetAt := time.Now().Add(codeBuddyModelBlockCooldown)
			if err := s.accountRepo.SetModelRateLimit(stateCtx, account.ID, modelKey, resetAt, "codebuddy_model_not_servable"); err != nil {
				logger.L().Warn("codebuddy model block failed", zap.Int64("account_id", account.ID), zap.String("model", modelKey), zap.Error(err))
			}
		}
		return true, true
	}
	return false, false
}

func (s *OpenAIGatewayService) setCodeBuddyRateLimited(ctx context.Context, account *Account, resetAt time.Time, reason string) {
	if s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.SetRateLimited(ctx, account.ID, resetAt); err != nil {
		logger.L().Warn("codebuddy rate limit set failed", zap.Int64("account_id", account.ID), zap.Error(err))
		return
	}
	logger.L().Info("codebuddy account rate limited",
		zap.Int64("account_id", account.ID),
		zap.String("reason", reason),
		zap.Time("reset_at", resetAt),
	)
}

// recordCodeBuddyCreditsUsage folds an accepted request into the account's
// credit tally and draws the same amount out of the cached balance, so the
// admin UI tracks burn without probing billing on every request.
func (s *OpenAIGatewayService) recordCodeBuddyCreditsUsage(ctx context.Context, account *Account, upstreamModel string) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	credits, priced := codeBuddyRequestCredits(account, upstreamModel)
	updates := map[string]any{
		codeBuddyCreditsUsageExtraKey: account.GetCodeBuddyCreditsUsage().Charge(credits, priced, time.Now()),
	}
	if credits > 0 {
		if snapshot := account.GetCodeBuddyCredits(); snapshot != nil {
			snapshot.Deduct(credits)
			updates[codeBuddyCreditsExtraKey] = snapshot
		}
	}
	s.persistCodeBuddyExtra(ctx, account, updates)
}

// markCodeBuddyCreditsExhausted records an upstream refusal for lack of
// credits; a cached positive balance would otherwise look healthy.
func (s *OpenAIGatewayService) markCodeBuddyCreditsExhausted(ctx context.Context, account *Account) {
	snapshot := account.GetCodeBuddyCredits()
	if snapshot == nil {
		snapshot = &codebuddy.CreditsSnapshot{}
	}
	snapshot.Credits = 0
	snapshot.Exhausted = true
	snapshot.Estimated = true
	for i := range snapshot.Segments {
		snapshot.Segments[i].Remaining = 0
	}
	snapshot.SoonestExpiry = nil
	logger.L().Warn("codebuddy upstream reported exhausted credits", zap.Int64("account_id", account.ID))
	s.persistCodeBuddyExtra(ctx, account, map[string]any{codeBuddyCreditsExtraKey: snapshot})
}

// persistCodeBuddyExtra merges updates into the stored Extra in the
// background: the write sits in front of the first upstream byte and must not
// add latency, and losing a tally update must never fail a request the
// upstream already served. The in-memory account is left untouched because
// scheduler snapshots may share it across concurrent requests.
func (s *OpenAIGatewayService) persistCodeBuddyExtra(ctx context.Context, account *Account, updates map[string]any) {
	if len(updates) == 0 || s.accountRepo == nil {
		return
	}
	accountID := account.ID
	stateCtx, cancel := openAIAccountStateContext(ctx)
	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				logger.L().Error("codebuddy extra update panicked", zap.Int64("account_id", accountID), zap.Any("recover", r))
			}
		}()
		if err := s.accountRepo.UpdateExtra(stateCtx, accountID, updates); err != nil {
			logger.L().Debug("codebuddy extra update failed", zap.Int64("account_id", accountID), zap.Error(err))
		}
	}()
}
