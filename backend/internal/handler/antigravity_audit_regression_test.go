//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type antigravityAuditUsageRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *antigravityAuditUsageRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return true, nil
}

func (r *antigravityAuditUsageRepo) recorded() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

type antigravityAuditReadError struct{}

func (antigravityAuditReadError) Read([]byte) (int, error) {
	return 0, errors.New("audit upstream body interrupted")
}

type antigravityAuditUpstream struct {
	service.HTTPUpstream
	partial  bool
	requests [][]byte
	tokens   []string
}

func (u *antigravityAuditUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.requests = append(u.requests, body)
	u.tokens = append(u.tokens, req.Header.Get("Authorization"))
	// This fake implements the transport boundary itself: no HTTP client, DNS,
	// Google request, or credential lookup is involved.
	frame := `{"response":{"responseId":"audit-response","candidates":[{"content":{"role":"model","parts":[{"text":"Audit answer"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"totalTokenCount":16}}}`
	frames := "data: " + frame + "\n\n"
	var reader io.Reader = strings.NewReader(frames)
	if u.partial {
		reader = io.MultiReader(reader, antigravityAuditReadError{})
	} else {
		frames += "data: " + `{"response":{"responseId":"audit-response","candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"totalTokenCount":16}}}` + "\n\n"
		reader = strings.NewReader(frames)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"antigravity-audit-request"}},
		Body:       io.NopCloser(reader),
		Request:    req,
	}, nil
}

func newAntigravityAuditHandler(t *testing.T, upstream *antigravityAuditUpstream) (*GatewayHandler, *service.APIKey, *service.Account, *antigravityAuditUsageRepo) {
	t.Helper()
	group := &service.Group{ID: 19014, Hydrated: true, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{
		ID: 19015, Name: "audit-fake-antigravity", Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "audit-fake-access-token", "project_id": "audit-fake-project",
			"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
			"model_mapping": map[string]any{"gemini-2.5-pro": "gemini-2.5-pro"},
		},
		Extra:         map[string]any{"mixed_scheduling": true},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: 19015, GroupID: group.ID}},
	}
	apiKey := &service.APIKey{
		ID: 19016, UserID: 19017, GroupID: &group.ID, Status: service.StatusActive, Group: group,
		User: &service.User{ID: 19017, Concurrency: 3, Balance: 100},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	repo := &antigravityAuditUsageRepo{}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 8, TaskTimeout: time.Second,
	})
	t.Cleanup(pool.Stop)
	gateway := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, repo, nil, nil, nil, nil, nil, cfg, snapshot,
		nil, service.NewBillingService(cfg, nil), nil, billingCache, nil, nil,
		service.NewDeferredService(nil, nil, time.Hour),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{
		gatewayService: gateway, cfg: cfg, billingCacheService: billingCache, usageRecordWorkerPool: pool,
		concurrencyHelper:         NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:        1,
		maxAccountSwitchesGemini:  1,
		antigravityGatewayService: service.NewAntigravityGatewayService(nil, nil, nil, service.NewAntigravityTokenProvider(nil, nil, nil), nil, upstream, nil, nil),
	}
	return h, apiKey, account, repo
}

func antigravityAuditContext(t *testing.T, apiKey *service.APIKey, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "audit-fake-client/1.0")
	c.Request.Header.Set("session_id", "audit-client-session")
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, apiKey.Group))
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: apiKey.User.Concurrency})
	return c, rec
}

func TestAntigravityAuditMessagesGeminiMixedProtocol(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/partial=%t", stream, partial), func(t *testing.T) {
				upstream := &antigravityAuditUpstream{partial: partial}
				h, apiKey, account, repo := newAntigravityAuditHandler(t, upstream)
				body := []byte(fmt.Sprintf(`{"model":"gemini-2.5-pro","stream":%t,"max_tokens":64,"messages":[{"role":"user","content":"Audit greeting"}]}`, stream))
				c, rec := antigravityAuditContext(t, apiKey, body)
				h.Messages(c)
				h.usageRecordWorkerPool.Stop()

				selected, ok := c.Get(opsAccountIDKey)
				require.True(t, ok)
				require.Equal(t, account.ID, selected, "the real handler must select the mixed Antigravity account from the Gemini group")
				require.Len(t, upstream.requests, 1, "a delivered/partial response must never launch another upstream attempt")
				payload := upstream.requests[0]
				require.True(t, gjson.GetBytes(payload, "request.contents").IsArray(), string(payload))
				require.False(t, gjson.GetBytes(payload, "messages").Exists(), "Anthropic messages must be converted before reaching Antigravity")
				require.False(t, gjson.GetBytes(payload, "request.messages").Exists())
				require.Contains(t, gjson.GetBytes(payload, "request.contents").Raw, "Audit greeting")
				require.Equal(t, []string{"Bearer audit-fake-access-token"}, upstream.tokens)

				if stream {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
					require.Contains(t, rec.Body.String(), "event: message_start")
					require.Contains(t, rec.Body.String(), "event: content_block_delta")
					require.Contains(t, rec.Body.String(), "Audit answer")
					if !partial {
						require.Contains(t, rec.Body.String(), "event: message_stop")
					}
				} else if !partial {
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					var response map[string]any
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
					require.Equal(t, "message", response["type"])
					require.Equal(t, "assistant", response["role"])
					require.Equal(t, "gemini-2.5-pro", response["model"])
					require.Contains(t, rec.Body.String(), "Audit answer")
					require.NotContains(t, response, "candidates")
				} else {
					require.GreaterOrEqual(t, rec.Code, http.StatusInternalServerError, rec.Body.String())
					require.Equal(t, "error", gjson.GetBytes(rec.Body.Bytes(), "type").String())
				}
				require.NotContains(t, rec.Body.String(), `"candidates"`, "the Messages endpoint must never leak a Gemini response envelope")
				require.NotContains(t, rec.Body.String(), `"usageMetadata"`)
				logs := repo.recorded()
				require.Len(t, logs, 1, "normal and partial paths must each submit usage exactly once, never both partial and success billing")
				require.Equal(t, 11, logs[0].InputTokens)
				require.Equal(t, 5, logs[0].OutputTokens)
				require.Equal(t, partial, logs[0].UsageIncomplete)
				require.Equal(t, uint64(1), h.usageRecordWorkerPool.Stats().SubmittedTasks)
				require.Zero(t, h.usageRecordWorkerPool.Stats().FailedTasks)
			})
		}
	}
}

func TestAntigravityAuditPartialUsageSubmission(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*service.RecordUsageInput) *service.RecordUsageInput
		err    error
		want   int
	}{
		{name: "positive usage and interruption", err: io.ErrUnexpectedEOF, want: 1},
		{name: "positive usage and disconnect", err: context.Canceled, want: 1},
		{name: "nil input", mutate: func(*service.RecordUsageInput) *service.RecordUsageInput { return nil }, err: io.ErrUnexpectedEOF},
		{name: "nil result", mutate: func(in *service.RecordUsageInput) *service.RecordUsageInput { in.Result = nil; return in }, err: io.ErrUnexpectedEOF},
		{name: "zero usage", mutate: func(in *service.RecordUsageInput) *service.RecordUsageInput {
			in.Result.Usage = service.ClaudeUsage{}
			return in
		}, err: io.ErrUnexpectedEOF},
		{name: "nil account", mutate: func(in *service.RecordUsageInput) *service.RecordUsageInput { in.Account = nil; return in }, err: io.ErrUnexpectedEOF},
		{name: "nil api key", mutate: func(in *service.RecordUsageInput) *service.RecordUsageInput { in.APIKey = nil; return in }, err: io.ErrUnexpectedEOF},
		{name: "other platform", mutate: func(in *service.RecordUsageInput) *service.RecordUsageInput {
			in.Account.Platform = service.PlatformGemini
			return in
		}, err: io.ErrUnexpectedEOF},
		{name: "failover is billed by eventual result", err: &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}},
		{name: "wrapped failover is not billed twice", err: fmt.Errorf("wrapped: %w", &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, apiKey, account, repo := newAntigravityAuditHandler(t, &antigravityAuditUpstream{})
			body := []byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"partial"}]}`)
			c, _ := antigravityAuditContext(t, apiKey, body)
			input := &service.RecordUsageInput{
				Result: &service.ForwardResult{
					RequestID: "audit-partial-request", Model: "gemini-2.5-pro", Stream: true, UsageIncomplete: true,
					Usage: service.ClaudeUsage{InputTokens: 11, OutputTokens: 5},
				},
				APIKey: apiKey, Account: account,
			}
			if tt.mutate != nil {
				input = tt.mutate(input)
			}
			require.NotPanics(t, func() { h.recordPartialAntigravityUsage(c, tt.err, body, input) })
			h.usageRecordWorkerPool.Stop()
			require.Equal(t, uint64(tt.want), h.usageRecordWorkerPool.Stats().SubmittedTasks)
			require.Zero(t, h.usageRecordWorkerPool.Stats().FailedTasks)
			logs := repo.recorded()
			require.Len(t, logs, tt.want)
			if tt.want == 1 {
				require.Same(t, apiKey.User, input.User)
				require.Equal(t, service.PlatformGemini, input.QuotaPlatform)
				require.Equal(t, "/v1/messages", input.InboundEndpoint)
				require.Equal(t, "audit-fake-client/1.0", input.UserAgent)
				require.Equal(t, "audit-client-session", input.SessionID)
				require.Equal(t, service.HashUsageRequestPayload(body), input.RequestPayloadHash)
				require.Equal(t, account.ID, logs[0].AccountID)
				require.Equal(t, 11, logs[0].InputTokens)
				require.Equal(t, 5, logs[0].OutputTokens)
				require.True(t, logs[0].UsageIncomplete)
			}
		})
	}
}
