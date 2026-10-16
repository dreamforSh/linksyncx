package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type mcpRequestKey struct {
	KeyID   int64
	GroupID int64
	ID      string
}

type mcpInFlight struct {
	ctx      context.Context
	cancel   context.CancelFunc
	progress string
}

func mcpCanonicalID(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || len(raw) > 256 || !json.Valid(raw) {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", false
	}
	switch value := value.(type) {
	case string:
		if len(value) > 200 {
			return "", false
		}
		return "s:" + value, true
	case json.Number:
		number, err := value.Float64()
		if err != nil || number < -9007199254740991 || number > 9007199254740991 || number != float64(int64(number)) {
			return "", false
		}
		return "n:" + strconv.FormatInt(int64(number), 10), true
	default:
		return "", false
	}
}

func (h *MCPHandler) registerMCPRequest(parent context.Context, apiKey *service.APIKey, id, token json.RawMessage) (context.Context, context.CancelFunc, func(), error) {
	canonical, _ := mcpCanonicalID(id)
	progress, _ := mcpCanonicalID(token)
	key := mcpRequestKey{KeyID: apiKey.ID, GroupID: apiKey.Group.ID, ID: canonical}
	ctx, cancel := context.WithTimeout(parent, mcpLoopbackTimeout)
	entry := &mcpInFlight{ctx: ctx, cancel: cancel, progress: progress}
	h.mu.Lock()
	if h.inflight == nil {
		h.inflight = make(map[mcpRequestKey]*mcpInFlight)
	}
	for key, value := range h.inflight {
		if value.ctx.Err() != nil {
			delete(h.inflight, key)
		}
	}
	active := 0
	for other, value := range h.inflight {
		if other.KeyID == key.KeyID && other.GroupID == key.GroupID {
			active++
			if other.ID == canonical || progress != "" && value.progress == progress {
				h.mu.Unlock()
				cancel()
				return nil, nil, nil, errors.New("request id or progress token is already active")
			}
		}
	}
	if len(h.inflight) >= 1024 || active >= 32 {
		h.mu.Unlock()
		cancel()
		return nil, nil, nil, errors.New("too many active MCP requests")
	}
	h.inflight[key] = entry
	h.mu.Unlock()
	release := func() {
		h.mu.Lock()
		if h.inflight[key] == entry {
			delete(h.inflight, key)
		}
		h.mu.Unlock()
	}
	return ctx, cancel, release, nil
}

func (h *MCPHandler) cancelMCPRequest(apiKey *service.APIKey, raw json.RawMessage) {
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return
	}
	id, ok := mcpCanonicalID(params.RequestID)
	if !ok {
		return
	}
	h.mu.Lock()
	entry := h.inflight[mcpRequestKey{KeyID: apiKey.ID, GroupID: apiKey.Group.ID, ID: id}]
	h.mu.Unlock()
	if entry != nil {
		entry.cancel()
	}
}

func mcpOrigin(raw string, allowPath bool) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !allowPath && u.Path != "" && u.Path != "/" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, true
}

func (h *MCPHandler) allowMCPOrigin(c *gin.Context) bool {
	raw := c.GetHeader("Origin")
	if raw == "" {
		return true // CLI clients do not have a browser origin.
	}
	origin, ok := mcpOrigin(raw, false)
	if !ok {
		return false
	}
	allowed := []string{}
	if h.cfg != nil {
		allowed = append(allowed, h.cfg.CORS.AllowedOrigins...)
		if frontend, valid := mcpOrigin(h.cfg.Server.FrontendURL, true); valid {
			allowed = append(allowed, frontend)
		}
	}
	port := h.loopbackPort()
	allowed = append(allowed, "http://127.0.0.1:"+strconv.Itoa(port), "http://localhost:"+strconv.Itoa(port), "http://[::1]:"+strconv.Itoa(port))
	for _, configured := range allowed {
		if normalized, valid := mcpOrigin(configured, false); valid && normalized == origin {
			return true
		}
	}
	// A wildcard CORS setting or the untrusted Host header is not an MCP
	// Origin boundary; explicit browser origins must be configured.
	return false
}

func (h *MCPHandler) loopbackPort() int {
	if h.cfg != nil && h.cfg.Server.Port > 0 {
		return h.cfg.Server.Port
	}
	return 8080
}

func (h *MCPHandler) loopbackURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", h.loopbackPort())
}

func (h *MCPHandler) gatewayRequest(ctx context.Context, method, target, key string, body []byte, stream bool) (*http.Response, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(h.loopbackPort())) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "/v1/chat/completions" && u.Path != "/v1/models") {
		return nil, errors.New("invalid MCP loopback destination")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("could not build gateway request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return h.httpClient.Do(req)
}

func readMCPGatewayResponse(resp *http.Response, err error) (int, []byte, error) {
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if err != nil || resp == nil || resp.Body == nil {
		return 0, nil, errors.New("gateway request failed or was cancelled")
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, mcpLoopbackMaxBodyBytes+1))
	if readErr != nil || len(body) > mcpLoopbackMaxBodyBytes {
		return resp.StatusCode, nil, errors.New("gateway response was incomplete or exceeded its limit")
	}
	return resp.StatusCode, body, nil
}

func (h *MCPHandler) postChatCompletions(ctx context.Context, target, key string, body []byte) (int, []byte, error) {
	return readMCPGatewayResponse(h.gatewayRequest(ctx, http.MethodPost, target, key, body, false))
}

func (h *MCPHandler) getModels(ctx context.Context, target, key string) (int, []byte, error) {
	return readMCPGatewayResponse(h.gatewayRequest(ctx, http.MethodGet, target, key, nil, false))
}

func (h *MCPHandler) postStreamingChatCompletions(ctx context.Context, target, key string, body []byte) (*http.Response, error) {
	return h.gatewayRequest(ctx, http.MethodPost, target, key, body, true)
}

func (h *MCPHandler) callModelsTool(c *gin.Context, id json.RawMessage, ctx context.Context, apiKey *service.APIKey) {
	target := strings.TrimSuffix(h.loopbackURL(), "/chat/completions") + "/models"
	status, body, err := h.loopbackModels(ctx, target, apiKey.Key)
	if err != nil {
		h.writeToolError(c, id, "Could not load the current gateway model catalog")
		return
	}
	if status < 200 || status >= 300 {
		h.writeToolError(c, id, mcpGatewayError(status, body))
		return
	}
	var raw struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &raw) != nil || len(bytes.TrimSpace(raw.Data)) == 0 || bytes.TrimSpace(raw.Data)[0] != '[' {
		h.writeToolError(c, id, "Gateway returned an invalid model catalog")
		return
	}
	var response struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name,omitempty"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || len(response.Data) > 512 {
		h.writeToolError(c, id, "Gateway returned an invalid or oversized model catalog")
		return
	}
	seen := map[string]bool{}
	models := make([]map[string]string, 0, len(response.Data))
	for _, entry := range response.Data {
		if entry.ID == "" || len(entry.ID) > 200 || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		model := map[string]string{"id": entry.ID}
		if entry.DisplayName != "" && len(entry.DisplayName) <= 400 {
			model["display_name"] = entry.DisplayName
		}
		models = append(models, model)
	}
	text, _ := json.Marshal(map[string]any{
		"models": models, "source": "current_api_key_gateway_catalog",
		"notice": "这些模型名称已通过当前 Key/分组的清单规则；不代表账号权益或模型身份已经实测验证。",
	})
	h.writeResult(c, id, mcpCallToolResult{Content: []mcpToolContent{{Type: "text", Text: string(text)}}})
}

func (h *MCPHandler) startMCPSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
}

func (h *MCPHandler) writeMCPEnvelope(c *gin.Context, envelope any) error {
	if c.Request.Context().Err() != nil {
		return c.Request.Context().Err()
	}
	stream, _ := c.Get("mcp_sse")
	if stream != true {
		c.JSON(http.StatusOK, envelope)
		return nil
	}
	h.startMCPSSE(c)
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "event: message\ndata: %s\n\n", body); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}

func writeMCPKeepalive(c *gin.Context) error {
	if _, err := io.WriteString(c.Writer, ": keepalive\n\n"); err != nil {
		return err
	}
	c.Writer.Flush()
	return nil
}
