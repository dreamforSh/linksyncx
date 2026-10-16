package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func (s *AntigravityGatewayService) handleGeminiStreamingResponse(c *gin.Context, resp *http.Response, startTime time.Time) (*antigravityStreamResult, error) {
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}
	writer := newAntigravityClientWriter(c.Writer, flusher, "antigravity gemini")
	s.configureStreamWriter(c, writer)
	writer.beforeFirstWrite = func() {
		c.Status(resp.StatusCode)
		c.Header("Content-Type", "text/event-stream; charset=utf-8")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
	}
	usage := &ClaudeUsage{}
	var firstTokenMs *int
	finished := make(map[int]bool)
	endMarker := false
	result := func(incomplete bool) *antigravityStreamResult {
		return &antigravityStreamResult{
			usage: usage, firstTokenMs: firstTokenMs,
			clientDisconnect: writer.Disconnected() || c.Request.Context().Err() != nil,
			usageIncomplete:  incomplete,
		}
	}
	fail := func(err error, reason string) (*antigravityStreamResult, error) {
		partial := result(true)
		if disconnect, handled := handleAntigravityStreamReadError(c, err, writer.Disconnected(), "antigravity gemini"); handled {
			partial.clientDisconnect = disconnect
			return partial, nil
		}
		if !c.Writer.Written() && !usage.hasObservedTokens() {
			return nil, antigravityCompatEmptyStreamError()
		}
		writer.Fprintf("event: error\ndata: {\"error\":{\"code\":502,\"status\":\"UNAVAILABLE\",\"message\":%q}}\n\n", reason)
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
				if len(finished) == 0 {
					return fail(io.ErrUnexpectedEOF, "upstream stream ended without finishReason")
				}
				for _, complete := range finished {
					if !complete {
						return fail(io.ErrUnexpectedEOF, "upstream candidate ended without finishReason")
					}
				}
				if endMarker {
					writer.Write([]byte("data: [DONE]\n\n"))
				}
				return result(false), nil
			}
			if event.err != nil {
				return fail(event.err, "stream_read_error")
			}
			resetAntigravityCompatTimer(timer, timeout)
			s.observeAntigravityGeminiSSELine(c, event.line)
			line := strings.TrimRight(event.line, "\r\n")
			if !strings.HasPrefix(line, "data:") {
				if line != "" {
					writer.Fprintf("%s\n", line)
				}
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			if payload == "[DONE]" {
				endMarker = true
				continue
			}
			if !gjson.Valid(payload) {
				return fail(errors.New("invalid upstream stream JSON"), "invalid upstream stream JSON")
			}
			outer := gjson.Parse(payload)
			outerError := outer.Get("error")
			inner, err := s.unwrapV1InternalResponse([]byte(payload))
			if err != nil {
				return fail(err, "invalid upstream stream envelope")
			}
			frame := gjson.ParseBytes(inner)
			if !frame.IsObject() {
				return fail(errors.New("invalid upstream response object"), "invalid upstream response object")
			}
			if u := extractGeminiUsage(inner); u != nil {
				usage = u
			}
			if outerError.Exists() && outerError.Type != gjson.Null {
				return fail(errors.New("upstream stream returned an error"), "upstream stream returned an error")
			}
			if value := frame.Get("error"); value.Exists() && value.Type != gjson.Null {
				return fail(errors.New("upstream stream returned an error"), "upstream stream returned an error")
			}
			meaningful := false
			for position, candidate := range frame.Get("candidates").Array() {
				index := position
				if candidate.Get("index").Exists() {
					index = int(candidate.Get("index").Int())
				}
				complete, exists := finished[index]
				if reason := candidate.Get("finishReason").String(); reason != "" && reason != "FINISH_REASON_UNSPECIFIED" {
					complete = true
				}
				if !exists || complete {
					finished[index] = complete
				}
				candidate.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
					meaningful = meaningful || part.Get("text").String() != "" || part.Get("functionCall").Exists() || part.Get("inlineData.data").String() != ""
					return !meaningful
				})
			}
			if firstTokenMs == nil && meaningful {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			writer.Fprintf("data: %s\n\n", inner)
		case <-timeoutCh:
			return fail(errors.New("stream data interval timeout"), "stream_timeout")
		case <-keepaliveCh:
			if c.Writer.Written() && !writer.Disconnected() && !downstreamRejectsSSEComments(c) {
				writer.Write([]byte(":\n\n"))
			}
		}
	}
}
