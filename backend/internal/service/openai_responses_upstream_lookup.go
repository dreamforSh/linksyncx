package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Responses created on OpenAI API-key accounts are stored by the upstream. The
// gateway only remembers which account produced each one (the sticky response
// binding) and who may use it (the HTTP owner binding), both for the sticky
// response TTL, so retrieval, deletion and input_items listing go back to that
// account while those bindings last.

// ErrOpenAIResponsesLookupUpstream marks a lookup the upstream could not serve.
var ErrOpenAIResponsesLookupUpstream = errors.New("openai responses lookup: upstream unavailable")

// ForwardOpenAIResponsesLookup relays GET/DELETE /v1/responses/{id} and GET
// /v1/responses/{id}/input_items (suffix "" or "/input_items") with the
// client's query to the API-key account that created the response. It reports
// false without writing when the gateway cannot place the response, so the
// caller answers 404.
func (s *OpenAIGatewayService) ForwardOpenAIResponsesLookup(ctx context.Context, c *gin.Context, groupID int64, responseID string, userID, apiKeyID int64, method, suffix string) (bool, error) {
	responseID = strings.TrimSpace(responseID)
	if s == nil || c == nil || !isSafeUpstreamPathSegment(responseID) {
		return false, nil
	}
	owned, err := s.ValidateOpenAIHTTPResponseOwner(ctx, groupID, responseID, userID, apiKeyID)
	if err != nil || !owned {
		return false, err
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return false, nil
	}
	accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
	if err != nil || accountID <= 0 {
		return false, err
	}
	account, err := s.openAIResponsesLookupAccount(ctx, accountID)
	if err != nil || account == nil || !account.IsOpenAIApiKey() {
		return false, err
	}

	targetURL := openaiPlatformAPIURL
	if baseURL := account.GetOpenAIBaseURL(); baseURL != "" {
		validatedURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return false, err
		}
		targetURL = buildOpenAIResponsesURLForPlatform(account.Platform, validatedURL)
	}
	targetURL += "/" + url.PathEscape(responseID) + suffix
	if c.Request != nil && c.Request.URL != nil && c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
	if err != nil {
		return false, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return false, err
	}
	authHeaders, err := s.buildOpenAIAuthenticationHeaders(ctx, account, token)
	if err != nil {
		return false, err
	}
	for key, values := range authHeaders {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	accept := "application/json"
	if c.Request != nil && strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream") {
		accept = "text/event-stream"
	}
	req.Header.Set("accept", accept)
	if customUA := account.GetOpenAIUserAgent(); customUA != "" {
		req.Header.Set("user-agent", customUA)
	}
	account.ApplyHeaderOverrides(req.Header)

	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return false, err
	}
	resp, err := s.doOpenAIUpstream(req, proxyURL, account)
	if err != nil {
		return false, errors.Join(ErrOpenAIResponsesLookupUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The key's own failures are the operator's problem, not the caller's.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= http.StatusInternalServerError {
		logger.L().Warn("openai.responses_lookup_upstream_failed",
			zap.Int64("account_id", account.ID),
			zap.Int("status", resp.StatusCode),
			zap.String("method", method),
		)
		return false, ErrOpenAIResponsesLookupUpstream
	}
	responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(strings.ToLower(contentType), "text/event-stream") {
		// Retrieving a background response with stream=true replays its events.
		c.Writer.Header().Set("Content-Type", contentType)
		c.Writer.WriteHeader(resp.StatusCode)
		reader := bufio.NewReader(resp.Body)
		buf := make([]byte, 32<<10)
		for {
			n, readErr := reader.Read(buf)
			if n > 0 {
				if _, writeErr := c.Writer.Write(buf[:n]); writeErr != nil {
					return true, nil
				}
				c.Writer.Flush()
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					logger.L().Warn("openai.responses_lookup_stream_interrupted", zap.Int64("account_id", account.ID), zap.Error(readErr))
				}
				return true, nil
			}
		}
	}
	body, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return true, nil
	}
	if contentType == "" {
		contentType = "application/json"
	}
	c.Data(resp.StatusCode, contentType, body)
	if method == http.MethodDelete && resp.StatusCode == http.StatusOK && suffix == "" {
		_ = store.DeleteResponseAccount(ctx, groupID, responseID)
	}
	return true, nil
}

// openAIResponsesLookupAccount loads the bound account without the scheduling
// gates: a rate-limited key can still read back what it stored.
func (s *OpenAIGatewayService) openAIResponsesLookupAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	if s.accountRepo != nil {
		return s.accountRepo.GetByID(ctx, accountID)
	}
	return nil, nil
}
