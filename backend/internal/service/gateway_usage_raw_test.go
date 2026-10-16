//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRecordUsageRawSnapshotPreservesCacheBillingAndAmount(t *testing.T) {
	tests := []struct {
		name         string
		usage        ClaudeUsage
		force        bool
		incomplete   bool
		compensation int
	}{
		{name: "100_prompt_zero_cached_account_switch", usage: ClaudeUsage{InputTokens: 100, OutputTokens: 7}, force: true, compensation: 100},
		{name: "mixed_cached_account_switch", usage: ClaudeUsage{InputTokens: 80, OutputTokens: 7, CacheCreationInputTokens: 10, CacheReadInputTokens: 20}, force: true, compensation: 80},
		{name: "zero_input_has_no_compensation", usage: ClaudeUsage{OutputTokens: 7, CacheReadInputTokens: 40}, force: true},
		{name: "no_compensation_eligibility", usage: ClaudeUsage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 20}},
		{name: "observed_partial_still_billed", usage: ClaudeUsage{InputTokens: 100, OutputTokens: 3}, force: true, incomplete: true, compensation: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{}
			billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
			svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil)
			pricingAt := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
			result := &ForwardResult{RequestID: tt.name, Model: "claude-sonnet-4", Usage: tt.usage, UsageIncomplete: tt.incomplete}
			apiKey := &APIKey{ID: 2}
			input := &RecordUsageInput{Result: result, APIKey: apiKey, User: &User{ID: 1}, Account: &Account{ID: 3}, ForceCacheBilling: tt.force, PricingAt: pricingAt}

			// The exact pre-change billing transformation is the independent baseline.
			legacy := *result
			if tt.force && legacy.Usage.InputTokens > 0 {
				legacy.Usage.CacheReadInputTokens += legacy.Usage.InputTokens
				legacy.Usage.InputTokens = 0
			}
			wantCost := svc.calculateRecordUsageCost(context.Background(), &legacy, apiKey, result.Model, 1.1, 1.1, pricingAt)
			require.Positive(t, wantCost.TotalCost)

			for attempt := 0; attempt < 2; attempt++ {
				require.NoError(t, svc.RecordUsage(context.Background(), input))
				log := usageRepo.lastLog
				require.NotNil(t, log)
				require.Equal(t, tt.usage, result.Usage, "recording must not mutate upstream usage or double-apply compensation")
				require.Equal(t, tt.usage.InputTokens, *log.RawInputTokens)
				require.Equal(t, tt.usage.OutputTokens, *log.RawOutputTokens)
				require.Equal(t, tt.usage.CacheCreationInputTokens, *log.RawCacheCreationTokens)
				require.Equal(t, tt.usage.CacheReadInputTokens, *log.RawCacheReadTokens)
				require.Equal(t, tt.compensation, *log.CacheBillingCompensationTokens)
				require.Equal(t, tt.incomplete, log.UsageIncomplete)
				if tt.compensation > 0 {
					require.Equal(t, "sticky_session_failover", *log.CacheBillingCompensationReason)
				} else {
					require.Nil(t, log.CacheBillingCompensationReason)
				}
				require.Equal(t, legacy.Usage.InputTokens, log.InputTokens)
				require.Equal(t, legacy.Usage.CacheReadInputTokens, log.CacheReadTokens)
				require.Equal(t, wantCost.TotalCost, log.TotalCost)
				require.Equal(t, wantCost.ActualCost, log.ActualCost)
				require.Equal(t, QuantizeUsageBillingAmount(wantCost.ActualCost), billingRepo.lastCmd.BalanceCost)
				require.Equal(t, log.InputTokens, billingRepo.lastCmd.InputTokens)
				require.Equal(t, log.CacheReadTokens, billingRepo.lastCmd.CacheReadTokens)
			}
		})
	}
}

func TestRawUsageSnapshotDoesNotInventEmptyTelemetry(t *testing.T) {
	log := &UsageLog{InputTokens: 100, CacheReadTokens: 50}
	setObservedRawUsage(log, 0, 0, 0, 0)
	require.Nil(t, log.RawInputTokens)
	require.Nil(t, log.RawCacheReadTokens)
	setObservedRawUsage(log, 0, 7, 0, 0)
	require.NotNil(t, log.RawInputTokens)
	require.Zero(t, *log.RawInputTokens)
	require.Equal(t, 7, *log.RawOutputTokens)
}

func TestOpenAIRecordUsagePreservesNormalizedRawBuckets(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil, nil)
	result := &OpenAIForwardResult{RequestID: "raw_openai", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 20, CacheCreationInputTokens: 10}}
	require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: result, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3},
	}))
	log := usageRepo.lastLog
	require.Equal(t, 70, *log.RawInputTokens)
	require.Equal(t, 20, *log.RawCacheReadTokens)
	require.Equal(t, 10, *log.RawCacheCreationTokens)
	require.Equal(t, 100, *log.RawInputTokens+*log.RawCacheCreationTokens+*log.RawCacheReadTokens)
	require.Zero(t, *log.CacheBillingCompensationTokens)
	require.Equal(t, 100, result.Usage.InputTokens)
}

func TestClaudeErrorPartialUsageRemainsUnknownWithoutChangingBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	observed := ClaudeUsage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 20}
	partial := partialStreamUsageResult(c, &http.Response{Header: http.Header{}},
		&streamingResult{usage: &observed}, "claude-sonnet-4", "", time.Now(), io.ErrUnexpectedEOF)
	require.NotNil(t, partial)
	require.True(t, partial.UsageIncomplete)

	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	svc := newGatewayRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil)
	input := &RecordUsageInput{APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3}}
	baseline := *partial
	baseline.UsageIncomplete = false
	input.Result = &baseline
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	wantTotal, wantActual := usageRepo.lastLog.TotalCost, usageRepo.lastLog.ActualCost
	wantFingerprint := billingRepo.lastCmd.RequestFingerprint
	input.Result = partial
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	log := usageRepo.lastLog
	require.True(t, log.UsageIncomplete, "raw aggregate excludes incomplete snapshots even when every token field is observed")
	require.Equal(t, 100, *log.RawInputTokens)
	require.Equal(t, 7, *log.RawOutputTokens)
	require.Equal(t, 20, *log.RawCacheReadTokens)
	require.Positive(t, wantTotal)
	require.Equal(t, wantTotal, log.TotalCost)
	require.Equal(t, wantActual, log.ActualCost)
	require.Equal(t, wantFingerprint, billingRepo.lastCmd.RequestFingerprint)
}

func TestOpenAIErrorPartialUsageRemainsUnknownWithoutChangingBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil, nil)
	stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial output\"}\n\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_partial\",\"error\":{\"code\":\"invalid_request_error\",\"message\":\"stream failed\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"input_tokens_details\":{\"cached_tokens\":20}}}}\n\n"
	partial, err := svc.handleChatStreamingResponse(&http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))},
		c, &Account{ID: 3, Platform: PlatformOpenAI}, "gpt-5.1", "gpt-5.1", "gpt-5.1", time.Now(), 0)
	require.Error(t, err)
	require.NotNil(t, partial)
	require.True(t, partial.UsageIncomplete)
	require.Equal(t, 100, partial.Usage.InputTokens)
	require.Equal(t, 7, partial.Usage.OutputTokens)

	input := &OpenAIRecordUsageInput{APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3}}
	baseline := *partial
	baseline.UsageIncomplete = false
	input.Result = &baseline
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	wantTotal, wantActual := usageRepo.lastLog.TotalCost, usageRepo.lastLog.ActualCost
	wantFingerprint := billingRepo.lastCmd.RequestFingerprint
	input.Result = partial
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	log := usageRepo.lastLog
	require.True(t, log.UsageIncomplete, "error-partial usage must be persisted as unknown for complete raw metrics")
	require.Equal(t, 80, *log.RawInputTokens)
	require.Equal(t, 7, *log.RawOutputTokens)
	require.Equal(t, 20, *log.RawCacheReadTokens)
	require.Positive(t, wantTotal)
	require.Equal(t, wantTotal, log.TotalCost)
	require.Equal(t, wantActual, log.ActualCost)
	require.Equal(t, wantFingerprint, billingRepo.lastCmd.RequestFingerprint)
}

func TestRawUsageDoesNotChangeBillingFingerprint(t *testing.T) {
	params := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 0.01, ActualCost: 0.02}, User: &User{ID: 1}, APIKey: &APIKey{ID: 2}, Account: &Account{ID: 3}}
	legacy := &UsageLog{Model: "claude-sonnet-4", CacheReadTokens: 100}
	withRaw := *legacy
	input, zero, compensation := 100, 0, 100
	reason := "sticky_session_failover"
	withRaw.RawInputTokens = &input
	withRaw.RawCacheReadTokens = &zero
	withRaw.CacheBillingCompensationTokens = &compensation
	withRaw.CacheBillingCompensationReason = &reason
	oldCmd := buildUsageBillingCommand("same-request", legacy, params)
	newCmd := buildUsageBillingCommand("same-request", &withRaw, params)
	require.Equal(t, oldCmd, newCmd, "audit metadata must not invalidate existing durable money-event deduplication")
}
