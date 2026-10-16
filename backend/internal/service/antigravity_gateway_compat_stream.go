package service

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

type antigravityCompatStreamAdapter interface {
	Emit(*apicompat.AnthropicStreamEvent, *antigravityClientWriter)
	Finalize(*antigravityClientWriter)
	WriteError(*antigravityClientWriter, string)
}

type antigravityChatStreamAdapter struct {
	anthropicState *apicompat.AnthropicEventToResponsesState
	chatState      *apicompat.ResponsesEventToChatState
}

func newAntigravityChatStreamAdapter(model string, includeUsage bool) *antigravityChatStreamAdapter {
	anthropicState := apicompat.NewAnthropicEventToResponsesState()
	anthropicState.Model = model
	anthropicState.DeltaOnly = true
	chatState := apicompat.NewResponsesEventToChatState()
	chatState.Model = model
	chatState.IncludeUsage = includeUsage
	return &antigravityChatStreamAdapter{
		anthropicState: anthropicState,
		chatState:      chatState,
	}
}

func (a *antigravityChatStreamAdapter) Emit(event *apicompat.AnthropicStreamEvent, writer *antigravityClientWriter) {
	for _, responseEvent := range apicompat.AnthropicEventToResponsesEvents(event, a.anthropicState) {
		a.emitResponseEvent(&responseEvent, writer)
	}
}

func (a *antigravityChatStreamAdapter) Finalize(writer *antigravityClientWriter) {
	for _, responseEvent := range apicompat.FinalizeAnthropicResponsesStream(a.anthropicState) {
		a.emitResponseEvent(&responseEvent, writer)
	}
	for _, chunk := range apicompat.FinalizeResponsesChatStream(a.chatState) {
		if data, err := apicompat.ChatChunkToSSE(chunk); err == nil {
			writer.Write([]byte(data))
		}
	}
	writer.Write([]byte("data: [DONE]\n\n"))
}

func (a *antigravityChatStreamAdapter) WriteError(writer *antigravityClientWriter, reason string) {
	writer.Fprintf("data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\"}}\n\n", reason)
}

func (a *antigravityChatStreamAdapter) emitResponseEvent(event *apicompat.ResponsesStreamEvent, writer *antigravityClientWriter) {
	for _, chunk := range apicompat.ResponsesEventToChatChunks(event, a.chatState) {
		if data, err := apicompat.ChatChunkToSSE(chunk); err == nil {
			writer.Write([]byte(data))
		}
	}
}

type antigravityResponsesStreamAdapter struct {
	anthropicState *apicompat.AnthropicEventToResponsesState
}

func newAntigravityResponsesStreamAdapter(model string) *antigravityResponsesStreamAdapter {
	state := apicompat.NewAnthropicEventToResponsesState()
	state.Model = model
	return &antigravityResponsesStreamAdapter{anthropicState: state}
}

func (a *antigravityResponsesStreamAdapter) Emit(event *apicompat.AnthropicStreamEvent, writer *antigravityClientWriter) {
	for _, responseEvent := range apicompat.AnthropicEventToResponsesEvents(event, a.anthropicState) {
		a.emitResponseEvent(responseEvent, writer)
	}
}

func (a *antigravityResponsesStreamAdapter) Finalize(writer *antigravityClientWriter) {
	for _, responseEvent := range apicompat.FinalizeAnthropicResponsesStream(a.anthropicState) {
		a.emitResponseEvent(responseEvent, writer)
	}
}

func (a *antigravityResponsesStreamAdapter) WriteError(writer *antigravityClientWriter, reason string) {
	if a.anthropicState.CompletedSent {
		return
	}
	state := a.anthropicState
	state.CompletedSent = true
	event := apicompat.ResponsesStreamEvent{
		Type: "response.failed", SequenceNumber: state.SequenceNumber,
		Response: &apicompat.ResponsesResponse{
			ID: state.ResponseID, Object: "response", CreatedAt: state.Created, Model: state.Model,
			Status: "failed", Output: []apicompat.ResponsesOutput{},
			Error: &apicompat.ResponsesError{Code: "upstream_error", Message: reason},
		},
	}
	state.SequenceNumber++
	a.emitResponseEvent(event, writer)
}

func (a *antigravityResponsesStreamAdapter) emitResponseEvent(event apicompat.ResponsesStreamEvent, writer *antigravityClientWriter) {
	if data, err := apicompat.ResponsesEventToSSE(event); err == nil {
		writer.Write([]byte(data))
	}
}

type antigravityCompatScanEvent struct {
	line string
	err  error
}

type antigravityCompatStreamSession struct {
	processor      *antigravity.StreamingProcessor
	adapter        antigravityCompatStreamAdapter
	writer         *antigravityClientWriter
	usage          *ClaudeUsage
	pendingEvents  []apicompat.AnthropicStreamEvent
	firstTokenMs   *int
	startTime      time.Time
	meaningfulData bool
}

func newAntigravityCompatStreamSession(
	model string,
	startTime time.Time,
	adapter antigravityCompatStreamAdapter,
	writer *antigravityClientWriter,
) *antigravityCompatStreamSession {
	return &antigravityCompatStreamSession{
		processor: antigravity.NewStreamingProcessor(model),
		adapter:   adapter,
		writer:    writer,
		usage:     &ClaudeUsage{},
		startTime: startTime,
	}
}

func (s *antigravityCompatStreamSession) consume(line string) {
	for _, event := range s.processor.ProcessLineEvents(line) {
		s.emitOrBuffer(event)
	}
}

func (s *antigravityCompatStreamSession) hasMeaningfulData() bool {
	return s.meaningfulData
}

func (s *antigravityCompatStreamSession) finish() (*antigravityStreamResult, error) {
	events, _ := s.processor.FinishEvents()
	if err := s.processor.Err(); err != nil {
		return s.collectResult(s.writer.Disconnected()), err
	}
	for _, event := range events {
		s.emitOrBuffer(event)
	}
	if !s.hasMeaningfulData() {
		return s.collectResult(s.writer.Disconnected()), errors.New("upstream stream had no content")
	}
	s.adapter.Finalize(s.writer)
	return s.result(s.writer.Disconnected()), nil
}

func (s *antigravityCompatStreamSession) collectResult(clientDisconnect bool) *antigravityStreamResult {
	result := s.result(clientDisconnect)
	result.usageIncomplete = true
	return result
}

func (s *antigravityCompatStreamSession) result(clientDisconnect bool) *antigravityStreamResult {
	mergeAntigravityCompatUsage(s.usage, s.processor.Usage())
	return &antigravityStreamResult{
		usage: s.usage, firstTokenMs: s.firstTokenMs, clientDisconnect: clientDisconnect,
	}
}

func (s *antigravityCompatStreamSession) emitOrBuffer(event apicompat.AnthropicStreamEvent) {
	if s.meaningfulData {
		s.adapter.Emit(&event, s.writer)
		return
	}

	s.pendingEvents = append(s.pendingEvents, event)
	if !isMeaningfulAntigravityCompatEvent(&event) {
		return
	}

	s.meaningfulData = true
	ms := int(time.Since(s.startTime).Milliseconds())
	s.firstTokenMs = &ms
	for i := range s.pendingEvents {
		s.adapter.Emit(&s.pendingEvents[i], s.writer)
	}
	s.pendingEvents = nil
}

func isMeaningfulAntigravityCompatEvent(event *apicompat.AnthropicStreamEvent) bool {
	if event == nil {
		return false
	}
	if event.Type == "message_stop" {
		return true
	}
	if event.ContentBlock != nil {
		block := event.ContentBlock
		return block.Type == "tool_use" ||
			block.Text != "" ||
			block.Thinking != "" ||
			block.Signature != "" ||
			block.Source != nil
	}
	if event.Delta != nil {
		delta := event.Delta
		return delta.Text != "" ||
			delta.PartialJSON != "" ||
			delta.Thinking != "" ||
			delta.Signature != "" ||
			delta.StopReason != ""
	}
	return false
}

func mergeAntigravityCompatUsage(dst *ClaudeUsage, src *antigravity.ClaudeUsage) {
	if dst == nil || src == nil {
		return
	}
	dst.InputTokens = src.InputTokens
	dst.OutputTokens = src.OutputTokens
	dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	dst.CacheReadInputTokens = src.CacheReadInputTokens
	dst.ImageOutputTokens = src.ImageOutputTokens
}

func (s *AntigravityGatewayService) handleAntigravityCompatStream(
	c *gin.Context,
	resp *http.Response,
	startTime time.Time,
	originalModel string,
	adapter antigravityCompatStreamAdapter,
	prefix string,
) (*antigravityStreamResult, error) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	writer := newAntigravityClientWriter(c.Writer, flusher, prefix)
	s.configureStreamWriter(c, writer)
	writer.beforeFirstWrite = func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
	}
	session := newAntigravityCompatStreamSession(originalModel, startTime, adapter, writer)
	events, stopScanner, maxLineSize := s.startAntigravityCompatScanner(resp.Body)
	defer stopScanner()

	timeout := s.antigravityCompatStreamTimeout()
	timeoutTimer, timeoutCh := newAntigravityCompatTimer(timeout)
	if timeoutTimer != nil {
		defer timeoutTimer.Stop()
	}
	keepaliveTicker, keepaliveCh := s.newAntigravityCompatKeepaliveTicker()
	if keepaliveTicker != nil {
		defer keepaliveTicker.Stop()
	}

	for {
		select {
		case event, open := <-events:
			if !open {
				result, err := session.finish()
				if err != nil {
					return s.handleAntigravityCompatReadError(c, session, err, maxLineSize, prefix)
				}
				result.clientDisconnect = result.clientDisconnect || antigravityRequestCanceled(c)
				return result, nil
			}
			if event.err != nil {
				return s.handleAntigravityCompatReadError(c, session, event.err, maxLineSize, prefix)
			}
			resetAntigravityCompatTimer(timeoutTimer, timeout)
			s.observeAntigravityGeminiSSELine(c, event.line)
			session.consume(event.line)
			if err := session.processor.Err(); err != nil {
				return s.handleAntigravityCompatReadError(c, session, err, maxLineSize, prefix)
			}

		case <-timeoutCh:
			if writer.Disconnected() {
				return session.collectResult(true), nil
			}
			return s.handleAntigravityCompatReadError(c, session, errors.New("stream data interval timeout"), maxLineSize, prefix)

		case <-keepaliveCh:
			if session.hasMeaningfulData() && !writer.Disconnected() {
				writer.Write([]byte(": ping\n\n"))
			}
		}
	}
}

func (s *AntigravityGatewayService) startAntigravityCompatScanner(
	body io.Reader,
) (<-chan antigravityCompatScanEvent, func(), int) {
	maxLineSize := defaultMaxLineSize
	if s.settingService != nil && s.settingService.cfg != nil && s.settingService.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
	}
	scanner := bufio.NewScanner(body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)

	events := make(chan antigravityCompatScanEvent, 16)
	done := make(chan struct{})
	go func() {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		send := func(event antigravityCompatScanEvent) bool {
			select {
			case events <- event:
				return true
			case <-done:
				return false
			}
		}
		for scanner.Scan() {
			if !send(antigravityCompatScanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			send(antigravityCompatScanEvent{err: err})
		}
	}()
	return events, func() { close(done) }, maxLineSize
}

func (s *AntigravityGatewayService) antigravityCompatStreamTimeout() time.Duration {
	if s.settingService == nil || s.settingService.cfg == nil {
		return 0
	}
	return time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
}

func (s *AntigravityGatewayService) newAntigravityCompatKeepaliveTicker() (*time.Ticker, <-chan time.Time) {
	if s.settingService == nil || s.settingService.cfg == nil {
		return nil, nil
	}
	interval := time.Duration(s.settingService.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	if interval <= 0 {
		return nil, nil
	}
	ticker := time.NewTicker(interval)
	return ticker, ticker.C
}

func newAntigravityCompatTimer(timeout time.Duration) (*time.Timer, <-chan time.Time) {
	if timeout <= 0 {
		return nil, nil
	}
	timer := time.NewTimer(timeout)
	return timer, timer.C
}

func resetAntigravityCompatTimer(timer *time.Timer, timeout time.Duration) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(timeout)
}

func (s *AntigravityGatewayService) handleAntigravityCompatReadError(
	c *gin.Context,
	session *antigravityCompatStreamSession,
	err error,
	maxLineSize int,
	prefix string,
) (*antigravityStreamResult, error) {
	partial := session.collectResult(session.writer.Disconnected())
	if disconnect, handled := handleAntigravityStreamReadError(c, err, session.writer.Disconnected(), prefix); handled {
		partial.clientDisconnect = disconnect
		return partial, nil
	}
	if !session.hasMeaningfulData() && !partial.usage.hasObservedTokens() {
		return nil, antigravityCompatEmptyStreamError()
	}
	reason := "stream_read_error"
	if errors.Is(err, bufio.ErrTooLong) {
		logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (%s): max_size=%d error=%v", prefix, maxLineSize, err)
		reason = "response_too_large"
	}
	if !session.hasMeaningfulData() {
		return partial, s.writeAntigravityCompatError(c, http.StatusBadGateway, "upstream_error", "Invalid or incomplete upstream stream")
	}
	writeAntigravityCompatStreamError(c, session.adapter, session.writer, reason)
	return partial, fmt.Errorf("stream read error: %w", err)
}

func writeAntigravityCompatStreamError(
	c *gin.Context,
	adapter antigravityCompatStreamAdapter,
	writer *antigravityClientWriter,
	reason string,
) {
	adapter.WriteError(writer, reason)
	MarkResponseCommitted(c)
}

func antigravityCompatEmptyStreamError() error {
	logger.LegacyPrintf("service.antigravity_gateway", "Empty Antigravity compatibility stream, triggering failover")
	return &UpstreamFailoverError{
		StatusCode:             http.StatusBadGateway,
		ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
		RetryableOnSameAccount: true,
	}
}

func (s *AntigravityGatewayService) handleChatCompletionsStreamingFromAntigravity(
	c *gin.Context,
	resp *http.Response,
	startTime time.Time,
	originalModel string,
	includeUsage bool,
) (*antigravityStreamResult, error) {
	return s.handleAntigravityCompatStream(
		c,
		resp,
		startTime,
		originalModel,
		newAntigravityChatStreamAdapter(originalModel, includeUsage),
		"antigravity chat completions stream",
	)
}

func (s *AntigravityGatewayService) handleResponsesStreamingFromAntigravity(
	c *gin.Context,
	resp *http.Response,
	startTime time.Time,
	originalModel string,
) (*antigravityStreamResult, error) {
	return s.handleAntigravityCompatStream(
		c,
		resp,
		startTime,
		originalModel,
		newAntigravityResponsesStreamAdapter(originalModel),
		"antigravity responses stream",
	)
}
