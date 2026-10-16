package service

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// antigravityStreamAccumulator keeps content deltas independent of terminal
// metadata. A finish/usage-only frame must not replace previously collected parts.
// It is intentionally separate from the collectors used by other Gemini routes.
type antigravityStreamAccumulator struct {
	response          map[string]any
	candidates        []*antigravityCollectedCandidate
	candidatesByIndex map[int]*antigravityCollectedCandidate
	usageObserved     bool
	meaningful        bool
}

type antigravityCollectedCandidate struct {
	response map[string]any
	parts    []map[string]any
	finished bool
}

func (a *antigravityStreamAccumulator) add(payload []byte) (bool, error) {
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false, fmt.Errorf("invalid antigravity stream JSON: %w", err)
	}
	if envelope == nil {
		return false, errors.New("invalid antigravity stream response object")
	}

	frame := envelope
	if wrapped, ok := envelope["response"]; ok {
		var valid bool
		frame, valid = wrapped.(map[string]any)
		if !valid || frame == nil {
			return false, errors.New("invalid antigravity stream response envelope")
		}
	}
	if a.response == nil {
		a.response = make(map[string]any)
		a.candidatesByIndex = make(map[int]*antigravityCollectedCandidate)
	}

	// Some v1internal frames put response identity outside the response object.
	for _, key := range []string{"responseId", "modelVersion", "createTime"} {
		if value, ok := envelope[key]; ok {
			a.response[key] = value
		}
	}
	for key, value := range frame {
		switch key {
		case "candidates", "error":
			continue
		case "usageMetadata":
			if value == nil {
				continue
			}
			usage, ok := value.(map[string]any)
			if !ok || usage == nil {
				return false, errors.New("invalid antigravity stream usage metadata")
			}
			a.usageObserved = true
		}
		mergeAntigravityCollectField(a.response, key, value)
	}
	// Capture any usage on the error frame before returning the protocol error.
	if streamError, ok := envelope["error"]; ok && streamError != nil {
		return false, errors.New("antigravity upstream stream error")
	}
	if streamError, ok := frame["error"]; ok && streamError != nil {
		return false, errors.New("antigravity upstream stream error")
	}

	value, hasCandidates := frame["candidates"]
	if !hasCandidates || value == nil {
		return false, nil
	}
	candidates, ok := value.([]any)
	if !ok {
		return false, errors.New("invalid antigravity stream candidates")
	}
	meaningful := false
	for position, value := range candidates {
		candidate, ok := value.(map[string]any)
		if !ok || candidate == nil {
			return meaningful, errors.New("invalid antigravity stream candidate")
		}
		index := position
		if value, exists := candidate["index"]; exists {
			var valid bool
			index, valid = asInt(value)
			if !valid || index < 0 {
				return meaningful, errors.New("invalid antigravity stream candidate index")
			}
		}
		collected := a.candidatesByIndex[index]
		if collected == nil {
			collected = &antigravityCollectedCandidate{response: make(map[string]any)}
			a.candidatesByIndex[index] = collected
			a.candidates = append(a.candidates, collected)
		}
		for key, value := range candidate {
			switch key {
			case "content":
				if value == nil {
					continue
				}
				content, ok := value.(map[string]any)
				if !ok || content == nil {
					return meaningful, errors.New("invalid antigravity stream content")
				}
				metadata, _ := collected.response["content"].(map[string]any)
				if metadata == nil {
					metadata = make(map[string]any)
					collected.response["content"] = metadata
				}
				for contentKey, contentValue := range content {
					if contentKey != "parts" {
						mergeAntigravityCollectField(metadata, contentKey, contentValue)
					}
				}
				if value, exists := content["parts"]; exists && value != nil {
					parts, ok := value.([]any)
					if !ok {
						return meaningful, errors.New("invalid antigravity stream parts")
					}
					for _, value := range parts {
						part, ok := value.(map[string]any)
						if !ok || part == nil {
							return meaningful, errors.New("invalid antigravity stream part")
						}
						collected.parts = append(collected.parts, part)
						meaningful = true
					}
				}
			case "finishReason":
				if value == nil {
					continue
				}
				finishReason, ok := value.(string)
				if !ok {
					return meaningful, errors.New("invalid antigravity stream finishReason")
				}
				if strings.TrimSpace(finishReason) != "" && finishReason != "FINISH_REASON_UNSPECIFIED" {
					collected.finished = true
					collected.response[key] = value
					meaningful = true
				}
			default:
				mergeAntigravityCollectField(collected.response, key, value)
			}
		}
	}
	a.meaningful = a.meaningful || meaningful
	return meaningful, nil
}

func mergeAntigravityCollectField(dst map[string]any, key string, value any) {
	if fields, ok := value.(map[string]any); ok {
		existing, _ := dst[key].(map[string]any)
		if existing == nil {
			existing = make(map[string]any, len(fields))
			dst[key] = existing
		}
		for field, value := range fields {
			mergeAntigravityCollectField(existing, field, value)
		}
		return
	}
	dst[key] = value
}

func (a *antigravityStreamAccumulator) completed() bool {
	if len(a.candidates) == 0 {
		return false
	}
	for _, candidate := range a.candidates {
		if !candidate.finished {
			return false
		}
	}
	return true
}

func (a *antigravityStreamAccumulator) result() map[string]any {
	response := make(map[string]any, len(a.response)+1)
	for key, value := range a.response {
		response[key] = value
	}
	if len(a.candidates) == 0 {
		return response
	}
	candidates := make([]any, 0, len(a.candidates))
	for _, candidate := range a.candidates {
		response := candidate.response
		if len(candidate.parts) > 0 {
			// Reuse the part merger without losing this candidate's metadata.
			merged := mergeCollectedPartsToResponse(map[string]any{"candidates": []any{response}}, candidate.parts)
			mergedCandidates, _ := merged["candidates"].([]any)
			if len(mergedCandidates) > 0 {
				if mergedCandidate, ok := mergedCandidates[0].(map[string]any); ok {
					response = mergedCandidate
				}
			}
		}
		candidates = append(candidates, response)
	}
	response["candidates"] = candidates
	return response
}

func (a *antigravityStreamAccumulator) usage() *ClaudeUsage {
	if !a.usageObserved {
		return nil
	}
	metadata, _ := a.response["usageMetadata"].(map[string]any)
	prompt, _ := asInt(metadata["promptTokenCount"])
	cached, _ := asInt(metadata["cachedContentTokenCount"])
	output, _ := asInt(metadata["candidatesTokenCount"])
	thoughts, _ := asInt(metadata["thoughtsTokenCount"])
	imageTokens := 0
	if details, ok := metadata["candidatesTokensDetails"].([]any); ok {
		for _, value := range details {
			detail, ok := value.(map[string]any)
			if ok && detail["modality"] == "IMAGE" {
				imageTokens, _ = asInt(detail["tokenCount"])
				break
			}
		}
	}
	return &ClaudeUsage{
		InputTokens:          prompt - cached,
		OutputTokens:         output + thoughts,
		CacheReadInputTokens: cached,
		ImageOutputTokens:    imageTokens,
	}
}

// collectAntigravityStreamResponse is shared by native Gemini and Claude-compatible
// non-streaming paths. A transport EOF is successful only after finishReason; even
// [DONE] cannot turn a truncated generation into a completed response.
func (s *AntigravityGatewayService) collectAntigravityStreamResponse(c *gin.Context, resp *http.Response, startTime time.Time) (map[string]any, *antigravityStreamResult, error) {
	if upstreamResponseModelObserverFromContext(c) == nil {
		beginUpstreamResponseModelObservation(c)
	}
	maxLineSize := defaultMaxLineSize
	streamInterval := time.Duration(0)
	if s.settingService != nil && s.settingService.cfg != nil {
		if s.settingService.cfg.Gateway.MaxLineSize > 0 {
			maxLineSize = s.settingService.cfg.Gateway.MaxLineSize
		}
		streamInterval = time.Duration(s.settingService.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	scanner := bufio.NewScanner(resp.Body)
	scanBuf := getSSEScannerBuf64K()
	scanner.Buffer(scanBuf[:0], maxLineSize)
	// Closing the body on timeout/cancellation releases the blocked scanner goroutine.
	defer func() { _ = resp.Body.Close() }()
	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	defer close(done)
	var lastReadAt atomic.Int64
	lastReadAt.Store(time.Now().UnixNano())
	go func() {
		defer putSSEScannerBuf64K(scanBuf)
		defer close(events)
		send := func(event scanEvent) bool {
			select {
			case events <- event:
				return true
			case <-done:
				return false
			}
		}
		for scanner.Scan() {
			lastReadAt.Store(time.Now().UnixNano())
			if !send(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			send(scanEvent{err: err})
		}
	}()

	var intervalCh <-chan time.Time
	if streamInterval > 0 {
		ticker := time.NewTicker(streamInterval)
		defer ticker.Stop()
		intervalCh = ticker.C
	}
	var contextDone <-chan struct{}
	var contextErr func() error
	if c.Request != nil {
		readCtx := c.Request.Context()
		if lifecycle := antigravityLifecycle(readCtx); lifecycle != nil {
			readCtx = lifecycle.upstream
		}
		contextDone = readCtx.Done()
		contextErr = readCtx.Err
	}
	var accumulator antigravityStreamAccumulator
	var firstTokenMs *int
	partial := func() *antigravityStreamResult {
		result := &antigravityStreamResult{usage: accumulator.usage(), firstTokenMs: firstTokenMs, usageIncomplete: true}
		if c.Request != nil {
			result.clientDisconnect = c.Request.Context().Err() != nil
		}
		return result
	}
	errorEvent := false
	for {
		select {
		case event, ok := <-events:
			if !ok {
				if errorEvent {
					return nil, partial(), errors.New("antigravity upstream stream error event")
				}
				// Preserve the old empty-stream retry only when there is no billable
				// observation. Usage-only/truncated streams must never generate twice.
				if !accumulator.meaningful && !accumulator.usageObserved {
					return nil, nil, &UpstreamFailoverError{
						StatusCode:             http.StatusBadGateway,
						ResponseBody:           []byte(`{"error":"empty stream response from upstream"}`),
						RetryableOnSameAccount: true,
					}
				}
				if !accumulator.completed() {
					return nil, partial(), errors.New("antigravity stream ended without required finishReason")
				}
				result := partial()
				result.usageIncomplete = false
				if result.usage == nil {
					result.usage = &ClaudeUsage{}
				}
				return accumulator.result(), result, nil
			}
			if event.err != nil {
				if errors.Is(event.err, bufio.ErrTooLong) {
					logger.LegacyPrintf("service.antigravity_gateway", "SSE line too long (antigravity non-stream): max_size=%d error=%v", maxLineSize, event.err)
				}
				return nil, partial(), fmt.Errorf("read antigravity stream: %w", event.err)
			}
			line := strings.TrimRight(event.line, "\r\n")
			if line == "" {
				if errorEvent {
					return nil, partial(), errors.New("antigravity upstream stream error event")
				}
				continue
			}
			if strings.HasPrefix(line, "event:") {
				errorEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:")) == "error"
				continue
			}
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				if errorEvent {
					return nil, partial(), errors.New("antigravity upstream stream error event")
				}
				continue
			}
			s.observeAntigravityGeminiSSELine(c, event.line)
			meaningful, err := accumulator.add([]byte(payload))
			if meaningful && firstTokenMs == nil {
				ms := int(time.Since(startTime).Milliseconds())
				firstTokenMs = &ms
			}
			if err != nil {
				return nil, partial(), err
			}
			if errorEvent {
				return nil, partial(), errors.New("antigravity upstream stream error event")
			}
		case <-intervalCh:
			if time.Since(time.Unix(0, lastReadAt.Load())) < streamInterval {
				continue
			}
			return nil, partial(), errors.New("stream data interval timeout")
		case <-contextDone:
			return nil, partial(), contextErr()
		}
	}
}
