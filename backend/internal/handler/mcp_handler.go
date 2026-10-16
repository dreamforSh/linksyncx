package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	mcpProtocolVersion      = "2025-03-26"
	mcpServerName           = "linksyncx-perplexity"
	mcpLoopbackTimeout      = 30 * time.Minute
	mcpLoopbackMaxBodyBytes = 32 << 20
	mcpMaxRequestBytes      = 1 << 20
	mcpErrParseError        = -32700
	mcpErrInvalidRequest    = -32600
	mcpErrMethodNotFound    = -32601
	mcpErrInvalidParams     = -32602
)

type mcpJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type mcpJSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type mcpJSONRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *mcpJSONRPCError `json:"error,omitempty"`
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpCallToolResult struct {
	Content []mcpToolContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

type mcpCallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}

type mcpToolArguments struct {
	Query     string   `json:"query"`
	Model     string   `json:"model"`
	Sources   []string `json:"sources"`
	Language  *string  `json:"language"`
	Timezone  *string  `json:"timezone"`
	Incognito *bool    `json:"is_incognito"`
}

type mcpLoopbackCall func(context.Context, string, string, []byte) (int, []byte, error)

// MCPHandler keeps the authenticated gateway as its only execution and billing
// path. In-flight cancellation is identity-scoped and local to this instance.
type MCPHandler struct {
	cfg            *config.Config
	version        string
	httpClient     *http.Client
	loopbackCC     mcpLoopbackCall
	loopbackModels func(context.Context, string, string) (int, []byte, error)
	loopbackStream func(context.Context, string, string, []byte) (*http.Response, error)
	mu             sync.Mutex
	inflight       map[mcpRequestKey]*mcpInFlight
}

func NewMCPHandler(cfg *config.Config, buildInfo BuildInfo) *MCPHandler {
	version := strings.TrimSpace(buildInfo.Version)
	if version == "" {
		version = "1"
	}
	transport := &http.Transport{}
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	}
	transport.Proxy = nil
	h := &MCPHandler{
		cfg: cfg, version: version, inflight: make(map[mcpRequestKey]*mcpInFlight),
		httpClient: &http.Client{Transport: transport, Timeout: mcpLoopbackTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	h.loopbackCC = h.postChatCompletions
	h.loopbackModels = h.getModels
	h.loopbackStream = h.postStreamingChatCompletions
	return h
}

func (h *MCPHandler) HandleMCP(c *gin.Context) {
	if !h.allowMCPOrigin(c) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	if header := c.GetHeader("MCP-Protocol-Version"); header != "" && header != mcpProtocolVersion {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "Unsupported MCP protocol version"})
		return
	}
	if contentType := c.GetHeader("Content-Type"); contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || mediaType != "application/json" {
			c.AbortWithStatus(http.StatusUnsupportedMediaType)
			return
		}
	}
	accept := strings.ToLower(c.GetHeader("Accept"))
	if accept != "" && !strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/event-stream") && !strings.Contains(accept, "*/*") {
		c.AbortWithStatus(http.StatusNotAcceptable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, mcpMaxRequestBytes+1))
	if err != nil || len(body) > mcpMaxRequestBytes {
		c.AbortWithStatus(http.StatusRequestEntityTooLarge)
		return
	}
	var req mcpJSONRPCRequest
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Expected one JSON-RPC object")
		return
	}
	if json.Unmarshal(trimmed, &req) != nil {
		h.writeError(c, json.RawMessage("null"), mcpErrParseError, "Failed to parse JSON-RPC request")
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" || req.Params != nil && !mcpJSONObject(req.Params) {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Invalid JSON-RPC request")
		return
	}
	if apiKey.Group == nil || apiKey.Group.Platform != service.PlatformPerplexity {
		h.writeError(c, mcpResponseID(req.ID), mcpErrMethodNotFound, "MCP requires an API key bound to a perplexity group")
		return
	}
	if strings.HasPrefix(req.Method, "notifications/") {
		if len(req.ID) != 0 {
			h.writeError(c, mcpResponseID(req.ID), mcpErrInvalidRequest, "Notifications must not have an id")
			return
		}
		if req.Method == "notifications/cancelled" {
			h.cancelMCPRequest(apiKey, req.Params)
		}
		c.Status(http.StatusAccepted)
		c.Writer.WriteHeaderNow()
		return
	}
	if _, valid := mcpCanonicalID(req.ID); !valid {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Request id must be a bounded string or safe integer")
		return
	}
	c.Set("mcp_sse", strings.Contains(accept, "text/event-stream"))
	switch req.Method {
	case "initialize":
		if len(req.Params) != 0 {
			var initialize struct {
				ProtocolVersion *string `json:"protocolVersion"`
			}
			if json.Unmarshal(req.Params, &initialize) != nil || initialize.ProtocolVersion != nil && (*initialize.ProtocolVersion == "" || len(*initialize.ProtocolVersion) > 64) {
				h.writeError(c, req.ID, mcpErrInvalidParams, "Invalid initialize protocol version")
				return
			}
		}
		h.writeResult(c, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": mcpServerName, "version": h.version},
		})
	case "ping":
		h.writeResult(c, req.ID, map[string]any{})
	case "tools/list":
		h.writeResult(c, req.ID, map[string]any{"tools": mcpPerplexityTools()})
	case "tools/call":
		h.handleToolsCall(c, req, apiKey)
	default:
		h.writeError(c, req.ID, mcpErrMethodNotFound, "Method not found")
	}
}

func (h *MCPHandler) HandleMCPUnsupported(c *gin.Context) {
	if !h.allowMCPOrigin(c) {
		c.AbortWithStatus(http.StatusForbidden)
		return
	}
	c.Header("Allow", "POST")
	c.Status(http.StatusMethodNotAllowed)
	c.Writer.WriteHeaderNow()
}

func (h *MCPHandler) handleToolsCall(c *gin.Context, req mcpJSONRPCRequest, apiKey *service.APIKey) {
	var params mcpCallToolParams
	if json.Unmarshal(req.Params, &params) != nil || params.Name == "" {
		h.writeError(c, req.ID, mcpErrInvalidParams, "Invalid tools/call params")
		return
	}
	if len(params.Meta.ProgressToken) != 0 {
		if _, valid := mcpCanonicalID(params.Meta.ProgressToken); !valid {
			h.writeError(c, req.ID, mcpErrInvalidParams, "Invalid progress token")
			return
		}
	}
	ctx, cancel, release, err := h.registerMCPRequest(c.Request.Context(), apiKey, req.ID, params.Meta.ProgressToken)
	if err != nil {
		h.writeError(c, req.ID, mcpErrInvalidRequest, err.Error())
		return
	}
	defer cancel()
	defer release()
	switch params.Name {
	case "perplexity_models":
		if params.Arguments != nil && !mcpEmptyObject(params.Arguments) {
			h.writeToolError(c, req.ID, "perplexity_models accepts no arguments")
			return
		}
		h.callModelsTool(c, req.ID, ctx, apiKey)
	case "perplexity_ask", "perplexity_search", "perplexity_reason", "perplexity_research":
		h.callPerplexityTool(c, req, apiKey, params, ctx)
	default:
		h.writeToolError(c, req.ID, "Unknown tool")
	}
}

func (h *MCPHandler) callPerplexityTool(c *gin.Context, req mcpJSONRPCRequest, apiKey *service.APIKey, params mcpCallToolParams, ctx context.Context) {
	var args mcpToolArguments
	decoder := json.NewDecoder(bytes.NewReader(params.Arguments))
	decoder.DisallowUnknownFields()
	if !mcpJSONObject(params.Arguments) || decoder.Decode(&args) != nil {
		h.writeToolError(c, req.ID, "Invalid or unsupported tool arguments")
		return
	}
	var rawArguments map[string]json.RawMessage
	_ = json.Unmarshal(params.Arguments, &rawArguments)
	for _, value := range rawArguments {
		if string(bytes.TrimSpace(value)) == "null" {
			h.writeToolError(c, req.ID, "Tool arguments must not be null")
			return
		}
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		h.writeToolError(c, req.ID, "Missing required argument: query")
		return
	}
	model := "sonar"
	switch params.Name {
	case "perplexity_search":
		model = "sonar-pro"
	case "perplexity_reason":
		model = "sonar-reasoning"
	case "perplexity_research":
		model = "sonar-deep-research"
	}
	if args.Model != "" {
		if params.Name != "perplexity_search" && params.Name != "perplexity_reason" {
			h.writeToolError(c, req.ID, "Only search and reason support model overrides")
			return
		}
		if len(args.Model) > 200 || strings.TrimSpace(args.Model) == "" {
			h.writeToolError(c, req.ID, "Invalid model")
			return
		}
		model = strings.TrimSpace(args.Model)
	}
	opts := perplexity.DefaultRequestOptions()
	extension := map[string]any{}
	if args.Sources != nil {
		opts.Sources, extension["sources"] = args.Sources, args.Sources
	}
	if args.Language != nil {
		opts.Language, extension["language"] = *args.Language, *args.Language
	}
	if args.Timezone != nil {
		opts.Timezone, extension["timezone"] = *args.Timezone, *args.Timezone
	}
	if args.Incognito != nil {
		extension["is_incognito"] = *args.Incognito
	}
	if err := opts.Normalize(); err != nil {
		h.writeToolError(c, req.ID, err.Error())
		return
	}
	stream, _ := c.Get("mcp_sse")
	ccRequest := map[string]any{"model": model, "messages": []map[string]any{{"role": "user", "content": query}}, "stream": stream == true}
	if params.Name == "perplexity_reason" {
		ccRequest["thinking"] = true
	}
	if len(extension) > 0 {
		ccRequest["perplexity"] = extension
	}
	body, _ := json.Marshal(ccRequest)
	if stream == true {
		h.streamMCPTool(c, req.ID, ctx, apiKey.Key, body, params.Meta.ProgressToken)
		return
	}
	status, data, err := h.loopbackCC(ctx, h.loopbackURL(), apiKey.Key, body)
	if err != nil {
		h.writeToolError(c, req.ID, "Gateway tool request failed or was cancelled")
		return
	}
	if status < 200 || status >= 300 {
		h.writeToolError(c, req.ID, mcpGatewayError(status, data))
		return
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content string            `json:"content"`
				Calls   []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Choices) != 1 || strings.TrimSpace(response.Choices[0].Message.Content) == "" || len(response.Choices[0].Message.Calls) != 0 {
		h.writeToolError(c, req.ID, "Gateway did not return a valid text answer")
		return
	}
	h.writeResult(c, req.ID, &mcpCallToolResult{Content: []mcpToolContent{{Type: "text", Text: response.Choices[0].Message.Content}}})
}

func (h *MCPHandler) writeResult(c *gin.Context, id json.RawMessage, result any) {
	_ = h.writeMCPEnvelope(c, mcpJSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (h *MCPHandler) writeError(c *gin.Context, id json.RawMessage, code int, message string) {
	_ = h.writeMCPEnvelope(c, mcpJSONRPCResponse{JSONRPC: "2.0", ID: id, Error: &mcpJSONRPCError{Code: code, Message: message}})
}

func (h *MCPHandler) writeToolError(c *gin.Context, id json.RawMessage, message string) {
	h.writeResult(c, id, &mcpCallToolResult{Content: []mcpToolContent{{Type: "text", Text: message}}, IsError: true})
}

func mcpPerplexityTools() []mcpTool {
	tools := make([]mcpTool, 0, 5)
	for _, spec := range []struct{ name, description string }{
		{"perplexity_ask", "Quick Perplexity search answer"},
		{"perplexity_search", "Perplexity Pro Search with citations"},
		{"perplexity_reason", "Perplexity reasoning-model search; search progress is not private chain of thought"},
		{"perplexity_research", "Perplexity Deep Research report"},
		{"perplexity_models", "List model names allowed by the current API key and group; this is not an entitlement test"},
	} {
		properties := map[string]any{}
		schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if spec.name != "perplexity_models" {
			properties["query"] = map[string]any{"type": "string", "minLength": 1}
			properties["sources"] = map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string", "enum": []string{"web", "scholar", "social"}}}
			properties["language"] = map[string]any{"type": "string", "description": "BCP 47 language tag"}
			properties["timezone"] = map[string]any{"type": "string", "description": "IANA timezone"}
			properties["is_incognito"] = map[string]any{"type": "boolean"}
			schema["required"] = []string{"query"}
			if spec.name == "perplexity_search" || spec.name == "perplexity_reason" {
				properties["model"] = map[string]any{"type": "string", "description": "An allowed model name from perplexity_models"}
			}
		}
		tools = append(tools, mcpTool{Name: spec.name, Description: spec.description, InputSchema: schema})
	}
	return tools
}

func mcpEmptyObject(raw []byte) bool {
	if !mcpJSONObject(raw) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && len(object) == 0
}

func mcpJSONObject(raw []byte) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(trimmed)
}

func mcpResponseID(id json.RawMessage) json.RawMessage {
	if _, ok := mcpCanonicalID(id); !ok {
		return json.RawMessage("null")
	}
	return id
}

func mcpGatewayError(status int, _ []byte) string {
	return fmt.Sprintf("Gateway tool request failed (HTTP %d); inspect gateway diagnostics for details", status)
}
