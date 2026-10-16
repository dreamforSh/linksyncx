package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
)

func (s *AntigravityGatewayService) handleClaudeStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*antigravityStreamResult, error) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	writer := newAntigravityClientWriter(c.Writer, flusher, "antigravity claude")
	s.configureStreamWriter(c, writer)
	writer.beforeFirstWrite = func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)
	}
	processor := antigravity.NewStreamingProcessor(originalModel)
	var firstTokenMs *int
	result := func(incomplete bool) *antigravityStreamResult {
		usage := &ClaudeUsage{}
		mergeAntigravityCompatUsage(usage, processor.Usage())
		return &antigravityStreamResult{
			usage: usage, firstTokenMs: firstTokenMs,
			clientDisconnect: writer.Disconnected() || c.Request.Context().Err() != nil,
			usageIncomplete:  incomplete,
		}
	}
	fail := func(err error, reason string) (*antigravityStreamResult, error) {
		partial := result(true)
		if disconnect, handled := handleAntigravityStreamReadError(c, err, writer.Disconnected(), "antigravity claude"); handled {
			partial.clientDisconnect = disconnect
			return partial, nil
		}
		if !processor.MessageStartSent() && !partial.usage.hasObservedTokens() {
			return nil, antigravityCompatEmptyStreamError()
		}
		writer.Fprintf("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"upstream_error\",\"message\":%q}}\n\n", reason)
		MarkResponseCommitted(c)
		return partial, err
	}
	lines, stop, _ := s.startAntigravityCompatScanner(resp.Body)
	defer stop()
	timeout := s.antigravityCompatStreamTimeout()
	timer, timeoutCh := newAntigravityCompatTimer(timeout)
	if timer != nil {
		defer timer.Stop()
	}
	keepalive, keepaliveCh := s.newAntigravityCompatKeepaliveTicker()
	if keepalive != nil {
		defer keepalive.Stop()
	}
	for {
		select {
		case event, open := <-lines:
			if !open {
				finalEvents, _ := processor.Finish()
				if err := processor.Err(); err != nil {
					return fail(err, "upstream stream ended without a valid completion")
				}
				if !processor.MessageStartSent() {
					return fail(errors.New("upstream stream had no content"), "upstream stream had no content")
				}
				if len(finalEvents) > 0 {
					writer.Write(finalEvents)
				}
				return result(false), nil
			}
			if event.err != nil {
				return fail(fmt.Errorf("stream read error: %w", event.err), "stream_read_error")
			}
			resetAntigravityCompatTimer(timer, timeout)
			s.observeAntigravityGeminiSSELine(c, event.line)
			data := processor.ProcessLine(strings.TrimRight(event.line, "\r\n"))
			if err := processor.Err(); err != nil {
				return fail(err, "invalid upstream stream response")
			}
			if len(data) > 0 {
				if firstTokenMs == nil {
					ms := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &ms
				}
				writer.Write(data)
			}
		case <-timeoutCh:
			return fail(errors.New("stream data interval timeout"), "stream_timeout")
		case <-keepaliveCh:
			if processor.MessageStartSent() && !writer.Disconnected() {
				writer.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
			}
		}
	}
}

func handleAntigravityStreamReadError(c *gin.Context, err error, disconnected bool, prefix string) (bool, bool) {
	if disconnected || antigravityRequestCanceled(c) {
		return true, true
	}
	if c != nil && c.Request != nil && antigravityLifecycle(c.Request.Context()) != nil {
		return false, false
	}
	return handleStreamReadError(err, disconnected, prefix)
}

func antigravityRequestCanceled(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}
