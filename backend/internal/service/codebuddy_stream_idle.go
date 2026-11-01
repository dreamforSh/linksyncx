package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// defaultCodeBuddyStreamIdleTimeout is the CodeBuddy upstream-read idle limit
// when gateway.stream_data_interval_timeout is 0. The upstream streams
// reasoning_content deltas while a model thinks and sends no heartbeat, so the
// only long silence of a healthy stream is queueing or prefill before the
// first chunk. 300s is the largest value the setting accepts and the per-read
// limit codebuddy2api runs against the same upstream: it leaves slow prefill
// alone and still releases a stream that will never resume.
const defaultCodeBuddyStreamIdleTimeout = 300 * time.Second

// codeBuddyStreamIdleCooldown keeps the failover of a stalled stream from
// landing on the same account right away.
const codeBuddyStreamIdleCooldown = 2 * time.Minute

// codeBuddyStreamIdleTimeout returns the upstream-read idle limit of CodeBuddy
// streams: the gateway setting when positive, else the CodeBuddy default. The
// upstream only streams, even for non-stream clients, so without a limit a
// stalled stream holds the client and the account until the client gives up.
func (s *OpenAIGatewayService) codeBuddyStreamIdleTimeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		return time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	return defaultCodeBuddyStreamIdleTimeout
}

// upstreamStreamIdleError is the read error of a stream the idle limit cut
// off. It wraps nothing on purpose: closing the body cancels the upstream
// request, and a context.Canceled cause would read as a client abort.
type upstreamStreamIdleError struct {
	idle time.Duration
}

func (e *upstreamStreamIdleError) Error() string {
	return fmt.Sprintf("upstream stream idle for %s", e.idle)
}

// streamIdleBody closes the wrapped body once a single Read has waited idle
// for upstream bytes, which unblocks a synchronous reader such as
// bufio.Scanner; that Read then fails with *upstreamStreamIdleError. Only time
// spent inside Read counts, so a client that is slow to take the relayed
// bytes never reads as a silent upstream. Reads just stamp their start: the
// timer fires once per idle period and re-arms itself for what is left, so
// the body runs no goroutine of its own and nothing outlives Close.
type streamIdleBody struct {
	body io.ReadCloser
	idle time.Duration
	// readStarted is when the Read in progress began, as a monotonic offset
	// from base plus one; 0 between reads.
	base        time.Time
	readStarted atomic.Int64
	timedOut    atomic.Bool

	mu     sync.Mutex
	timer  *time.Timer
	closed bool

	closeOnce sync.Once
	closeErr  error
}

// newStreamIdleBody wraps body with an idle limit; idle <= 0 leaves it as is.
func newStreamIdleBody(body io.ReadCloser, idle time.Duration) io.ReadCloser {
	if body == nil || idle <= 0 {
		return body
	}
	b := &streamIdleBody{body: body, idle: idle, base: time.Now()}
	b.mu.Lock()
	b.timer = time.AfterFunc(idle, b.expire)
	b.mu.Unlock()
	return b
}

func (b *streamIdleBody) Read(p []byte) (int, error) {
	b.readStarted.Store(int64(time.Since(b.base)) + 1)
	n, err := b.body.Read(p)
	b.readStarted.Store(0)
	if err != nil && !errors.Is(err, io.EOF) && b.timedOut.Load() {
		err = &upstreamStreamIdleError{idle: b.idle}
	}
	return n, err
}

func (b *streamIdleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	return b.closeBody()
}

func (b *streamIdleBody) expire() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	waited := time.Duration(0)
	if started := b.readStarted.Load(); started != 0 {
		waited = time.Since(b.base) - time.Duration(started-1)
	}
	if waited < b.idle {
		b.timer.Reset(b.idle - waited)
		b.mu.Unlock()
		return
	}
	b.timedOut.Store(true)
	b.mu.Unlock()
	// The upstream transport aborts the blocked Read once its request is
	// cancelled, so this close returns as soon as that Read has seen it.
	_ = b.closeBody()
}

func (b *streamIdleBody) closeBody() error {
	b.closeOnce.Do(func() { b.closeErr = b.body.Close() })
	return b.closeErr
}

// noteCodeBuddyStreamIdle handles a CodeBuddy stream the idle limit cut off
// before it finished: the account cools down briefly, the gateway's
// stream-timeout policy sees the timeout, and ops records the stall. failover
// tells whether the caller is about to switch accounts (nothing reached the
// client) or to end a stream that already started with an error.
func (s *OpenAIGatewayService) noteCodeBuddyStreamIdle(c *gin.Context, account *Account, model, requestID string, idleErr *upstreamStreamIdleError, failover bool) {
	if account == nil || idleErr == nil {
		return
	}
	logger.L().Warn("codebuddy upstream stream idle timeout",
		zap.Int64("account_id", account.ID),
		zap.String("model", model),
		zap.String("request_id", requestID),
		zap.Duration("idle", idleErr.idle),
		zap.Bool("failover", failover),
	)
	stateCtx, cancel := openAIAccountStateContext(openAICompatBridgeContext(c))
	defer cancel()
	if s.rateLimitService != nil {
		s.rateLimitService.HandleStreamTimeout(stateCtx, account, model)
	}
	s.tempUnscheduleCodeBuddy(stateCtx, account, codeBuddyStreamIdleCooldown, "codebuddy stream idle timeout")

	if c == nil {
		return
	}
	kind := "http_error"
	if failover {
		kind = "failover"
	}
	message := "CodeBuddy upstream stream sent nothing for " + idleErr.idle.String()
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(requestID),
		Kind:               kind,
		Message:            message,
	})
}

// newCodeBuddyStreamIdleFailoverError lets the handler retry a stream that
// stalled before any byte reached the client on another account; the stalled
// account is already cooling down, so it is not retried in place.
func newCodeBuddyStreamIdleFailoverError(requestID string, idleErr *upstreamStreamIdleError) *UpstreamFailoverError {
	headers := http.Header{}
	if id := strings.TrimSpace(requestID); id != "" {
		headers.Set("x-request-id", id)
	}
	return &UpstreamFailoverError{
		StatusCode:      http.StatusBadGateway,
		ResponseBody:    openAIRawStreamTruncatedErrorBody(idleErr),
		ResponseHeaders: headers,
	}
}

// codeBuddyBridgeStreamIdle handles a stall of the CodeBuddy stream behind a
// Messages or Responses bridge; any other scanErr returns two nils. A stall
// before any client output returns the failover. After output it returns the
// typed stream error, and the caller ends the stream with its protocol's
// terminal error event.
func (s *OpenAIGatewayService) codeBuddyBridgeStreamIdle(c *gin.Context, account *Account, model, requestID string, scanErr error) (*UpstreamFailoverError, error) {
	var idleErr *upstreamStreamIdleError
	if !account.IsCodeBuddy() || !errors.As(scanErr, &idleErr) {
		return nil, nil
	}
	outputStarted := c != nil && c.Writer != nil && c.Writer.Written()
	s.noteCodeBuddyStreamIdle(c, account, model, requestID, idleErr, !outputStarted)
	if !outputStarted {
		return newCodeBuddyStreamIdleFailoverError(requestID, idleErr), nil
	}
	// The bridge writes the terminal event itself, so the handler appends no
	// second one and the failure still counts towards the SLA.
	MarkOpsStreamFailure(c, "upstream_error", OpenAIUpstreamStreamTimeoutCode, openAIUpstreamStreamTimeoutMessage, http.StatusBadGateway)
	MarkResponseCommitted(c)
	return nil, newOpenAIUpstreamStreamReadError(idleErr)
}

// writeCodeBuddyAnthropicStreamTimeout ends a started Messages stream with
// the error event the native Claude path sends for a stream timeout.
func writeCodeBuddyAnthropicStreamTimeout(c *gin.Context) {
	_, _ = fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE(OpenAIUpstreamStreamTimeoutCode, openAIUpstreamStreamTimeoutMessage))
	c.Writer.Flush()
}

// codeBuddyResponsesStreamTimeout is the response.failed event that ends a
// started Responses stream after a stall.
func codeBuddyResponsesStreamTimeout(state *apicompat.ChatCompletionsToResponsesStreamState) apicompat.ResponsesStreamEvent {
	state.CompletedSent = true
	event := apicompat.ResponsesStreamEvent{
		Type: "response.failed", SequenceNumber: state.SequenceNumber,
		Response: &apicompat.ResponsesResponse{
			ID: state.ResponseID, Object: "response", CreatedAt: state.Created, Model: state.Model,
			Status: "failed", Output: []apicompat.ResponsesOutput{},
			Error: &apicompat.ResponsesError{Code: OpenAIUpstreamStreamTimeoutCode, Message: openAIUpstreamStreamTimeoutMessage},
		},
	}
	state.SequenceNumber++
	return event
}

// tempUnscheduleCodeBuddy persists a short cooldown, which the scheduler
// snapshot picks up from the account row; the write never shortens a longer
// cooldown already in place. CodeBuddy has no in-memory runtime block
// (BlockAccountScheduling only covers OpenAI and Grok).
func (s *OpenAIGatewayService) tempUnscheduleCodeBuddy(ctx context.Context, account *Account, cooldown time.Duration, reason string) {
	if s == nil || account == nil || s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, time.Now().Add(cooldown), reason); err != nil {
		logger.L().Warn("codebuddy temp unschedule failed", zap.Int64("account_id", account.ID), zap.Error(err))
	}
}
