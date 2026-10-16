package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/gin-gonic/gin"
)

type mcpOwnedBody struct {
	io.ReadCloser
	once sync.Once
}

func (b *mcpOwnedBody) Close() error {
	var err error
	b.once.Do(func() { err = b.ReadCloser.Close() })
	return err
}

type mcpProgress struct {
	token json.RawMessage
	count int
	last  time.Time
	seen  map[string]string
}

func (p *mcpProgress) emit(h *MCPHandler, c *gin.Context, message string) error {
	if len(p.token) == 0 || p.count >= 256 || !p.last.IsZero() && time.Since(p.last) < 100*time.Millisecond {
		return nil
	}
	p.count++
	p.last = time.Now()
	return h.writeMCPEnvelope(c, map[string]any{
		"jsonrpc": "2.0", "method": "notifications/progress",
		"params": map[string]any{"progressToken": p.token, "progress": p.count, "message": message},
	})
}

func (p *mcpProgress) observe(h *MCPHandler, c *gin.Context, updates []perplexity.SearchProgress) error {
	for _, update := range updates {
		// Reconstruct the public description from enums. Do not trust arbitrary
		// text or payload fields merely because they arrived over loopback.
		safe, ok := perplexity.SafeSearchProgress(perplexity.Step{Type: update.Type, Status: update.Status, UUID: update.ID})
		if !ok || p.seen[safe.ID] == safe.Status {
			continue
		}
		if len(p.seen) >= 128 {
			continue
		}
		p.seen[safe.ID] = safe.Status
		if err := p.emit(h, c, safe.Text); err != nil {
			return err
		}
	}
	return nil
}

func (h *MCPHandler) awaitMCPStream(c *gin.Context, ctx context.Context, key string, body []byte, ticks <-chan time.Time) (*http.Response, error) {
	type response struct {
		resp *http.Response
		err  error
	}
	ready := make(chan response)
	go func() {
		resp, err := h.loopbackStream(ctx, h.loopbackURL(), key, body)
		select {
		case ready <- response{resp: resp, err: err}:
		case <-ctx.Done():
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-ready:
			return result.resp, result.err
		case <-ticks:
			if err := writeMCPKeepalive(c); err != nil {
				return nil, err
			}
		}
	}
}

func (h *MCPHandler) streamMCPTool(c *gin.Context, id json.RawMessage, parent context.Context, key string, body []byte, token json.RawMessage) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	h.startMCPSSE(c)
	if writeMCPKeepalive(c) != nil {
		return
	}
	progress := &mcpProgress{token: token, seen: map[string]string{}}
	if progress.emit(h, c, "正在连接网关") != nil {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	resp, err := h.awaitMCPStream(c, ctx, key, body, ticker.C)
	if err != nil || resp == nil || resp.Body == nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		h.writeToolError(c, id, "Gateway stream request failed or was cancelled")
		return
	}
	owned := &mcpOwnedBody{ReadCloser: resp.Body}
	stop := context.AfterFunc(ctx, func() { _ = owned.Close() })
	defer stop()
	defer func() { _ = owned.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.writeToolError(c, id, mcpGatewayError(resp.StatusCode, nil))
		return
	}
	type nextFrame struct {
		frame perplexity.SSEFrame
		err   error
	}
	frames := make(chan nextFrame)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := perplexity.NewSSEReader(io.LimitReader(owned, mcpLoopbackMaxBodyBytes+1))
		for {
			frame, err := reader.Next()
			select {
			case frames <- nextFrame{frame: frame, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancel()
		_ = owned.Close()
		<-readerDone
	}()
	var answer strings.Builder
	stopped := false
	for {
		select {
		case <-ctx.Done():
			h.writeToolError(c, id, "MCP tool request was cancelled or timed out")
			return
		case <-ticker.C:
			if writeMCPKeepalive(c) != nil {
				return
			}
		case next := <-frames:
			if next.err != nil {
				h.writeToolError(c, id, "Gateway stream ended without a complete answer")
				return
			}
			if next.frame.Event == "error" {
				h.writeToolError(c, id, "Gateway reported a stream error")
				return
			}
			if !next.frame.HasData {
				continue
			}
			if strings.TrimSpace(next.frame.Data) == "[DONE]" {
				if !stopped || strings.TrimSpace(answer.String()) == "" {
					h.writeToolError(c, id, "Gateway stream completed without a valid text answer")
					return
				}
				h.writeResult(c, id, mcpCallToolResult{Content: []mcpToolContent{{Type: "text", Text: answer.String()}}})
				return
			}
			if err := consumeMCPChatChunk(next.frame.Data, &answer, &stopped, func(updates []perplexity.SearchProgress) error {
				return progress.observe(h, c, updates)
			}); err != nil {
				h.writeToolError(c, id, "Gateway stream failed or returned invalid data")
				return
			}
		}
	}
}

func consumeMCPChatChunk(data string, answer *strings.Builder, stopped *bool, progress func([]perplexity.SearchProgress) error) error {
	var chunk struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			Delta struct {
				Content string            `json:"content"`
				Calls   []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
			Finish *string `json:"finish_reason"`
		} `json:"choices"`
		Perplexity struct {
			Progress []perplexity.SearchProgress `json:"search_progress"`
		} `json:"perplexity"`
	}
	if !mcpJSONObject([]byte(data)) || json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Error) != 0 && string(chunk.Error) != "null" {
		return errors.New("invalid gateway stream chunk")
	}
	if len(chunk.Choices) > 1 {
		return errors.New("multiple gateway choices are unsupported")
	}
	for _, choice := range chunk.Choices {
		if len(choice.Delta.Calls) != 0 || choice.Finish != nil && *choice.Finish != "stop" {
			return errors.New("unexpected gateway tool or terminal state")
		}
		if *stopped && choice.Delta.Content != "" {
			return errors.New("text arrived after gateway completion")
		}
		if answer.Len()+len(choice.Delta.Content) > mcpLoopbackMaxBodyBytes {
			return errors.New("gateway answer exceeds its size limit")
		}
		_, _ = answer.WriteString(choice.Delta.Content)
		if choice.Finish != nil {
			*stopped = true
		}
	}
	return progress(chunk.Perplexity.Progress)
}
