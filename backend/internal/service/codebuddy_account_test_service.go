package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/gin-gonic/gin"
)

// codeBuddyUpstreamDoer adapts the shared HTTPUpstream (proxy-bound, per
// account concurrency) to the codebuddy package's HTTPDoer.
type codeBuddyUpstreamDoer struct {
	upstream HTTPUpstream
	proxyURL string
	account  *Account
}

func (d codeBuddyUpstreamDoer) Do(req *http.Request) (*http.Response, error) {
	return d.upstream.Do(req, d.proxyURL, d.account.ID, d.account.Concurrency)
}

// codeBuddyTestCredentials returns credentials carrying a usable access token,
// refreshed through the gateway's token provider when it is near expiry.
func (s *AccountTestService) codeBuddyTestCredentials(ctx context.Context, account *Account) (codebuddy.Credentials, error) {
	creds, err := account.CodeBuddyCredentials()
	if err != nil {
		return codebuddy.Credentials{}, err
	}
	if s.openaiGatewayService != nil {
		token, _, tokenErr := s.openaiGatewayService.GetAccessToken(ctx, account)
		if tokenErr != nil {
			return codebuddy.Credentials{}, tokenErr
		}
		if strings.TrimSpace(token) != "" {
			creds.AccessToken = strings.TrimSpace(token)
		}
	}
	return creds, nil
}

func (s *AccountTestService) testCodeBuddyAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	ctx := c.Request.Context()
	creds, err := s.codeBuddyTestCredentials(ctx, account)
	if err != nil {
		return s.sendErrorAndEnd(c, "CodeBuddy credentials unavailable: "+err.Error())
	}
	testModel := strings.TrimSpace(modelID)
	if testModel == "" {
		testModel = "auto"
	}
	testModel = account.GetMappedModel(testModel)
	if strings.TrimSpace(prompt) == "" {
		prompt = "hi"
	}
	payload, _ := json.Marshal(map[string]any{
		"model":    testModel,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	body, err := codebuddy.PrepareChatBody(payload, creds.Profile)
	if err != nil {
		return s.sendErrorAndEnd(c, err.Error())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codebuddy.ChatCompletionsURL(creds.Profile), bytes.NewReader(body))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create CodeBuddy request")
	}
	codebuddy.ApplyHeaders(req.Header, codebuddy.CredentialHeaders(codebuddy.IdentityOf(creds)))
	codebuddy.ApplyHeaders(req.Header, codebuddy.RequestIDHeaders(codebuddy.ConversationID(creds.Profile, "")))
	account.ApplyHeaderOverrides(req.Header)

	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return s.sendErrorAndEnd(c, "Proxy unavailable: "+err.Error())
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModel})

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("CodeBuddy request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return s.sendErrorAndEnd(c, fmt.Sprintf("CodeBuddy API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody))))
	}
	return s.processOpenAIChatCompletionsStream(c, resp.Body)
}

// fetchCodeBuddyUpstreamModels syncs the account's /v3/config catalog, stores
// it for credit metadata and returns the public model ids after the account's
// credit policy.
func (s *AccountTestService) fetchCodeBuddyUpstreamModels(ctx context.Context, account *Account) ([]string, []byte, error) {
	creds, err := s.codeBuddyTestCredentials(ctx, account)
	if err != nil {
		return nil, nil, newUpstreamModelSyncConfigError("CodeBuddy credentials unavailable", err)
	}
	if s.httpUpstream == nil {
		return nil, nil, newUpstreamModelSyncConfigError("Upstream HTTP client is not configured", nil)
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, nil, newUpstreamModelSyncConfigError("Proxy unavailable", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, codeBuddyUpstreamTimeout)
	defer cancel()
	models, body, err := codebuddy.FetchCatalog(reqCtx, codeBuddyUpstreamDoer{upstream: s.httpUpstream, proxyURL: proxyURL, account: account}, creds)
	if err != nil {
		return nil, nil, newUpstreamModelSyncUpstreamError("Failed to sync CodeBuddy model catalog", err)
	}
	if s.accountRepo != nil {
		snapshot := CodeBuddyCatalogSnapshot{SyncedAt: time.Now().UTC().Format(time.RFC3339), Models: models}
		_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{codeBuddyCatalogExtraKey: snapshot})
	}
	ids := codebuddy.CatalogIDs(codebuddy.FilterCatalog(models, account.CodeBuddyCreditPolicy()))
	if len(ids) == 0 {
		return nil, nil, newUpstreamModelSyncUpstreamError("CodeBuddy catalog returned no models for this account's credit policy", nil)
	}
	return ids, body, nil
}
