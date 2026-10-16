package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// 本文件实现一个极简 MCP（Model Context Protocol）服务端，挂在 POST /mcp，
// 走 Streamable HTTP 的「单 POST 端点 + 纯 JSON 响应」子集（无 SSE）。
//
// 它把 Perplexity 平台能力暴露为 MCP tools；tools/call 不做任何自有调度，
// 而是把请求折叠成 OpenAI Chat Completions 后回环 POST 到本机的
// /v1/chat/completions（携带客户端同一把 API Key），完整复用鉴权、计费、
// 账号池调度与故障转移链路。
const (
	mcpProtocolVersion = "2025-03-26"
	mcpServerName      = "sub2api-perplexity"

	mcpLoopbackTimeout      = 5 * time.Minute
	mcpLoopbackMaxBodyBytes = 32 << 20 // 回环响应体读取上限（32MB），防内存放大
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
}

type mcpToolArguments struct {
	Query string `json:"query"`
	Model string `json:"model"`
}

// mcpLoopbackCall 执行到本机 /v1/chat/completions 的回环 POST。
// 独立成字段是为了让单测可以打桩，不必真的起 HTTP 服务。
type mcpLoopbackCall func(ctx context.Context, url, apiKey string, body []byte) (status int, respBody []byte, err error)

// MCPHandler 服务 POST /mcp。
type MCPHandler struct {
	cfg        *config.Config
	version    string
	httpClient *http.Client
	loopbackCC mcpLoopbackCall
}

func NewMCPHandler(cfg *config.Config, buildInfo BuildInfo) *MCPHandler {
	version := strings.TrimSpace(buildInfo.Version)
	if version == "" {
		version = "1"
	}
	h := &MCPHandler{
		cfg:        cfg,
		version:    version,
		httpClient: &http.Client{Timeout: mcpLoopbackTimeout},
	}
	h.loopbackCC = h.postChatCompletions
	return h
}

// HandleMCP 处理 POST /mcp 的 JSON-RPC 2.0 请求。
func (h *MCPHandler) HandleMCP(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Failed to read request body")
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Empty request body")
		return
	}
	// MVP：批量请求（数组）整体拒绝。
	if trimmed[0] == '[' {
		h.writeError(c, json.RawMessage("null"), mcpErrInvalidRequest, "Batch requests are not supported")
		return
	}

	var req mcpJSONRPCRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		h.writeError(c, json.RawMessage("null"), mcpErrParseError, "Failed to parse JSON-RPC request")
		return
	}

	// MCP notifications（notifications/* 或无 id）→ 202，无响应体。
	// gin 的 c.Status 只记录状态码，不写底层 header；无响应体时必须显式 flush。
	if len(req.ID) == 0 || string(req.ID) == "null" || strings.HasPrefix(req.Method, "notifications/") {
		c.Status(http.StatusAccepted)
		c.Writer.WriteHeaderNow()
		return
	}

	// 平台门禁：本端点只服务绑定 perplexity 平台分组的 Key。
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.Group == nil || apiKey.Group.Platform != service.PlatformPerplexity {
		h.writeError(c, req.ID, mcpErrMethodNotFound,
			"MCP endpoint requires an API key whose group belongs to the perplexity platform")
		return
	}

	switch req.Method {
	case "initialize":
		h.writeResult(c, req.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": mcpServerName, "version": h.version},
		})
	case "ping":
		h.writeResult(c, req.ID, map[string]any{})
	case "tools/list":
		h.writeResult(c, req.ID, map[string]any{"tools": mcpPerplexityTools()})
	case "tools/call":
		h.handleToolsCall(c, req, apiKey)
	default:
		h.writeError(c, req.ID, mcpErrMethodNotFound, fmt.Sprintf("Method not found: %s", req.Method))
	}
}

func (h *MCPHandler) handleToolsCall(c *gin.Context, req mcpJSONRPCRequest, apiKey *service.APIKey) {
	var params mcpCallToolParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			h.writeError(c, req.ID, mcpErrInvalidParams, "Invalid tools/call params")
			return
		}
	}
	switch params.Name {
	case "perplexity_models":
		h.writeResult(c, req.ID, &mcpCallToolResult{
			Content: []mcpToolContent{{Type: "text", Text: mcpPerplexityModelsText()}},
		})
	case "perplexity_ask":
		h.callPerplexityTool(c, req, apiKey, params.Name, params.Arguments, "sonar", false)
	case "perplexity_search":
		h.callPerplexityTool(c, req, apiKey, params.Name, params.Arguments, "sonar-pro", true)
	case "perplexity_research":
		h.callPerplexityTool(c, req, apiKey, params.Name, params.Arguments, "sonar-deep-research", false)
	default:
		h.writeResult(c, req.ID, &mcpCallToolResult{
			Content: []mcpToolContent{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", params.Name)}},
			IsError: true,
		})
	}
}

// callPerplexityTool 把工具调用折叠为 CC 请求并回环到 /v1/chat/completions。
// allowModelOverride 仅 perplexity_search 为 true：arguments.model 非空时透传为 CC model。
func (h *MCPHandler) callPerplexityTool(c *gin.Context, req mcpJSONRPCRequest, apiKey *service.APIKey, toolName string, rawArgs json.RawMessage, defaultModel string, allowModelOverride bool) {
	var args mcpToolArguments
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			h.writeToolError(c, req.ID, "Invalid tool arguments: "+err.Error())
			return
		}
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		h.writeToolError(c, req.ID, "Missing required argument: query")
		return
	}
	model := defaultModel
	if allowModelOverride {
		if override := strings.TrimSpace(args.Model); override != "" {
			model = override
		}
	}

	ccBody, err := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]any{{"role": "user", "content": query}},
		"stream":   false,
	})
	if err != nil {
		h.writeToolError(c, req.ID, "Failed to build chat completions request")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), mcpLoopbackTimeout)
	defer cancel()
	status, respBody, err := h.loopbackCC(ctx, h.loopbackURL(), apiKey.Key, ccBody)
	if err != nil {
		logger.L().Warn("mcp: loopback chat completions request failed",
			zap.String("tool", toolName), zap.String("model", model), zap.Error(err))
		h.writeToolError(c, req.ID, "Loopback chat completions request failed: "+err.Error())
		return
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		h.writeToolError(c, req.ID,
			fmt.Sprintf("Upstream chat completions failed (HTTP %d): %s", status, strings.TrimSpace(string(respBody))))
		return
	}

	var ccResp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &ccResp); err != nil || len(ccResp.Choices) == 0 {
		h.writeToolError(c, req.ID, "Invalid chat completions response from upstream")
		return
	}

	text := ccResp.Choices[0].Message.Content
	// 上游 agent 的步骤轨迹（若存在）以 tool_calls 形式返回，追加为 🔧 前缀的文本行。
	for _, call := range ccResp.Choices[0].Message.ToolCalls {
		if text != "" {
			text += "\n"
		}
		text += fmt.Sprintf("🔧 %s %s", call.Function.Name, call.Function.Arguments)
	}
	h.writeResult(c, req.ID, &mcpCallToolResult{
		Content: []mcpToolContent{{Type: "text", Text: text}},
	})
}

// postChatCompletions 是回环调用的默认实现：POST CC 请求到本机网关，
// 以客户端同一把 API Key 鉴权，从而复用鉴权/计费/调度/换号全链路。
func (h *MCPHandler) postChatCompletions(ctx context.Context, url, apiKey string, body []byte) (int, []byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := h.httpClient.Do(httpReq)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, mcpLoopbackMaxBodyBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, respBody, nil
}

func (h *MCPHandler) loopbackURL() string {
	port := 8080
	if h.cfg != nil && h.cfg.Server.Port > 0 {
		port = h.cfg.Server.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d/v1/chat/completions", port)
}

func (h *MCPHandler) writeResult(c *gin.Context, id json.RawMessage, result any) {
	c.JSON(http.StatusOK, mcpJSONRPCResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (h *MCPHandler) writeError(c *gin.Context, id json.RawMessage, code int, message string) {
	c.JSON(http.StatusOK, mcpJSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcpJSONRPCError{Code: code, Message: message},
	})
}

func (h *MCPHandler) writeToolError(c *gin.Context, id json.RawMessage, message string) {
	h.writeResult(c, id, &mcpCallToolResult{
		Content: []mcpToolContent{{Type: "text", Text: message}},
		IsError: true,
	})
}

func mcpPerplexityTools() []mcpTool {
	queryProperty := map[string]any{"type": "string", "description": "The question or research topic"}
	return []mcpTool{
		{
			Name:        "perplexity_ask",
			Description: "Quick answer with Perplexity auto search",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": queryProperty},
				"required":   []string{"query"},
			},
		},
		{
			Name:        "perplexity_search",
			Description: "Pro Search: multi-step web research with citations",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": queryProperty,
					"model": map[string]any{"type": "string", "description": "Optional model override (default: sonar-pro), e.g. sonar-reasoning, sonar-computer"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "perplexity_research",
			Description: "Deep Research report",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": queryProperty},
				"required":   []string{"query"},
			},
		},
		{
			Name:        "perplexity_models",
			Description: "List the available Perplexity model aliases",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func mcpPerplexityModelsText() string {
	return `Available Perplexity model aliases (usable as "model" on /v1/chat/completions, and as the optional "model" argument of perplexity_search):
- sonar — Quick answers with automatic search (fast default)
- sonar-pro — Pro Search: multi-step web research with citations
- sonar-reasoning — Reasoning-focused answers with visible reasoning
- sonar-deep-research — Deep Research: long-form report generation
- sonar-computer — Computer-use agent tasks
- sonar-pro-computer — Pro-tier computer-use agent tasks`
}
