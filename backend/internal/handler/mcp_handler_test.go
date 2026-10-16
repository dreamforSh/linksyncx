//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type mcpTestRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newMCPTestContext(body, platform string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(3)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      1,
		Key:     "sk-test-mcp",
		UserID:  7,
		GroupID: &groupID,
		User:    &service.User{ID: 7},
		Group: &service.Group{
			ID:       groupID,
			Platform: platform,
			Status:   service.StatusActive,
		},
	})
	return c, rec
}

func newMCPTestHandler() *MCPHandler {
	return NewMCPHandler(&config.Config{}, BuildInfo{Version: "test"})
}

func serveMCP(t *testing.T, h *MCPHandler, body, platform string) (*httptest.ResponseRecorder, mcpTestRPCResponse) {
	t.Helper()
	c, rec := newMCPTestContext(body, platform)
	h.HandleMCP(c)
	var resp mcpTestRPCResponse
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	}
	return rec, resp
}

func TestMCPHandlerInitialize(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)
	require.JSONEq(t, `1`, string(resp.ID))

	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.Equal(t, "2025-03-26", result.ProtocolVersion)
	require.NotNil(t, result.Capabilities.Tools)
	require.Equal(t, "sub2api-perplexity", result.ServerInfo.Name)
	require.Equal(t, "test", result.ServerInfo.Version)
}

func TestMCPHandlerToolsList(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `{"jsonrpc":"2.0","id":"abc","method":"tools/list"}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)
	require.JSONEq(t, `"abc"`, string(resp.ID))

	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.Len(t, result.Tools, 4)
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
		require.NotEmpty(t, tool.Description)
		require.Equal(t, "object", tool.InputSchema["type"])
	}
	require.Equal(t, []string{"perplexity_ask", "perplexity_search", "perplexity_research", "perplexity_models"}, names)
}

func TestMCPHandlerUnknownMethod(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `{"jsonrpc":"2.0","id":9,"method":"resources/list"}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, resp.Error)
	require.Equal(t, -32601, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "resources/list")
	require.JSONEq(t, `9`, string(resp.ID))
}

func TestMCPHandlerNonPerplexityGroupRejected(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `{"jsonrpc":"2.0","id":2,"method":"initialize"}`, service.PlatformOpenAI)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, resp.Error)
	require.Equal(t, -32601, resp.Error.Code)
	require.Contains(t, resp.Error.Message, "perplexity")
}

func TestMCPHandlerNotificationAccepted(t *testing.T) {
	// notifications/initialized（无 id）→ 202 空响应
	rec := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	h := newMCPTestHandler()
	h.HandleMCP(c)
	require.Equal(t, http.StatusAccepted, rec.Code)
	require.Empty(t, rec.Body.String())
}

func TestMCPHandlerBatchRejected(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, resp.Error)
	require.Equal(t, -32600, resp.Error.Code)
}

func TestMCPHandlerPing(t *testing.T) {
	rec, resp := serveMCP(t, newMCPTestHandler(), `{"jsonrpc":"2.0","id":5,"method":"ping"}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)
	require.JSONEq(t, `{}`, string(resp.Result))
}

func TestMCPHandlerModelsToolStatic(t *testing.T) {
	h := newMCPTestHandler()
	h.loopbackCC = func(context.Context, string, string, []byte) (int, []byte, error) {
		t.Fatal("perplexity_models must not hit the chat completions loopback")
		return 0, nil, nil
	}
	rec, resp := serveMCP(t, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"perplexity_models","arguments":{}}}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)

	var result mcpCallToolResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	require.Equal(t, "text", result.Content[0].Type)
	for _, model := range []string{"sonar", "sonar-pro", "sonar-reasoning", "sonar-deep-research", "sonar-computer", "sonar-pro-computer"} {
		require.Contains(t, result.Content[0].Text, model)
	}
}

func TestMCPHandlerToolCallAskLoopback(t *testing.T) {
	h := newMCPTestHandler()
	var gotURL, gotKey string
	var gotBody []byte
	h.loopbackCC = func(_ context.Context, url, apiKey string, body []byte) (int, []byte, error) {
		gotURL, gotKey, gotBody = url, apiKey, body
		return http.StatusOK, []byte(`{"choices":[{"message":{"role":"assistant","content":"Answer text","tool_calls":[{"function":{"name":"search","arguments":"{\"q\":\"x\"}"}}]}}]}`), nil
	}
	rec, resp := serveMCP(t, h, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"perplexity_ask","arguments":{"query":"hello world"}}}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)

	require.Contains(t, gotURL, "/v1/chat/completions")
	require.Equal(t, "sk-test-mcp", gotKey)
	var ccReq struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(gotBody, &ccReq))
	require.Equal(t, "sonar", ccReq.Model)
	require.False(t, ccReq.Stream)
	require.Len(t, ccReq.Messages, 1)
	require.Equal(t, "user", ccReq.Messages[0].Role)
	require.Equal(t, "hello world", ccReq.Messages[0].Content)

	var result mcpCallToolResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	require.Equal(t, "Answer text\n🔧 search {\"q\":\"x\"}", result.Content[0].Text)
}

func TestMCPHandlerToolCallSearchModelOverride(t *testing.T) {
	h := newMCPTestHandler()
	var gotBody []byte
	h.loopbackCC = func(_ context.Context, _ string, _ string, body []byte) (int, []byte, error) {
		gotBody = body
		return http.StatusOK, []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`), nil
	}
	_, resp := serveMCP(t, h, `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"perplexity_search","arguments":{"query":"q","model":"sonar-reasoning"}}}`, service.PlatformPerplexity)
	require.Nil(t, resp.Error)
	require.Contains(t, string(gotBody), `"model":"sonar-reasoning"`)

	var result mcpCallToolResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.False(t, result.IsError)
	require.Equal(t, "ok", result.Content[0].Text)
}

func TestMCPHandlerToolCallUpstreamError(t *testing.T) {
	h := newMCPTestHandler()
	h.loopbackCC = func(context.Context, string, string, []byte) (int, []byte, error) {
		return http.StatusInternalServerError, []byte(`{"error":{"message":"no accounts available"}}`), nil
	}
	rec, resp := serveMCP(t, h, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"perplexity_research","arguments":{"query":"deep topic"}}}`, service.PlatformPerplexity)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Nil(t, resp.Error)

	var result mcpCallToolResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.True(t, result.IsError)
	require.Len(t, result.Content, 1)
	require.Contains(t, result.Content[0].Text, "no accounts available")
	require.Contains(t, result.Content[0].Text, "500")
}

func TestMCPHandlerToolCallMissingQuery(t *testing.T) {
	h := newMCPTestHandler()
	h.loopbackCC = func(context.Context, string, string, []byte) (int, []byte, error) {
		t.Fatal("loopback must not be called when query is missing")
		return 0, nil, nil
	}
	_, resp := serveMCP(t, h, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"perplexity_ask","arguments":{}}}`, service.PlatformPerplexity)
	require.Nil(t, resp.Error)
	var result mcpCallToolResult
	require.NoError(t, json.Unmarshal(resp.Result, &result))
	require.True(t, result.IsError)
	require.Contains(t, result.Content[0].Text, "query")
}
