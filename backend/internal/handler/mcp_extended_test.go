//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMCPExtendedReasonAndSearchOptions(t *testing.T) {
	h := newMCPTestHandler()
	var forwarded []byte
	h.loopbackCC = func(_ context.Context, _ string, _ string, body []byte) (int, []byte, error) {
		forwarded = body
		return 200, []byte(`{"choices":[{"message":{"content":"answer"}}]}`), nil
	}
	_, response := serveMCP(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"perplexity_reason","arguments":{"query":"question","sources":["scholar"],"language":"zh-CN","timezone":"Asia/Shanghai","is_incognito":false}}}`, service.PlatformPerplexity)
	require.Nil(t, response.Error)
	var request map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(forwarded, &request))
	require.JSONEq(t, `"sonar-reasoning"`, string(request["model"]))
	require.JSONEq(t, `true`, string(request["thinking"]))
	require.JSONEq(t, `{"sources":["scholar"],"language":"zh-CN","timezone":"Asia/Shanghai","is_incognito":false}`, string(request["perplexity"]))
	for _, arguments := range []string{
		`{"query":"q","sources":[]}`, `{"query":"q","sources":null}`, `{"query":"q","sources":["private"]}`,
		`{"query":"q","language":"bad_tag"}`, `{"query":"q","timezone":"Local"}`, `{"query":"q","unexpected":true}`,
	} {
		forwarded = nil
		_, response = serveMCP(t, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"perplexity_search","arguments":`+arguments+`}}`, service.PlatformPerplexity)
		var result mcpCallToolResult
		require.NoError(t, json.Unmarshal(response.Result, &result))
		require.True(t, result.IsError, arguments)
		require.Nil(t, forwarded)
	}
}

func TestMCPExtendedOriginAndProtocolValidation(t *testing.T) {
	h := newMCPTestHandler()
	h.cfg.CORS.AllowedOrigins = []string{"*", "https://console.example"}
	for _, tc := range []struct {
		origin string
		status int
	}{
		{"https://attacker.example", 403}, {"null", 403}, {"https://console.example", 200}, {"", 200},
	} {
		c, rec := newMCPTestContext(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, service.PlatformPerplexity)
		c.Request.Host = "attacker.example"
		c.Request.Header.Set("Origin", tc.origin)
		h.HandleMCP(c)
		require.Equal(t, tc.status, rec.Code)
	}
	for _, body := range []string{
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","id":{},"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`, `{"jsonrpc":"2.0","method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":[]}`,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":123}}`,
	} {
		_, response := serveMCP(t, h, body, service.PlatformPerplexity)
		require.NotNil(t, response.Error, body)
	}
	c, rec := newMCPTestContext(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, service.PlatformPerplexity)
	c.Request.Header.Set("MCP-Protocol-Version", "unsupported")
	h.HandleMCP(c)
	require.Equal(t, 400, rec.Code)
}

func TestMCPExtendedCancellationIsKeyAndGroupScoped(t *testing.T) {
	h := newMCPTestHandler()
	started := make(chan struct{})
	h.loopbackCC = func(ctx context.Context, _ string, _ string, _ []byte) (int, []byte, error) {
		close(started)
		<-ctx.Done()
		return 0, nil, ctx.Err()
	}
	call, result := newMCPTestContext(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"perplexity_ask","arguments":{"query":"q"}}}`, service.PlatformPerplexity)
	wrongKey, wrongResponse := newMCPTestContext(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`, service.PlatformPerplexity)
	other, _ := middleware.GetAPIKeyFromContext(wrongKey)
	other.ID, other.Key = 2, "sk-other-key"
	wrongGroup, groupResponse := newMCPTestContext(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`, service.PlatformPerplexity)
	groupKey, _ := middleware.GetAPIKeyFromContext(wrongGroup)
	groupKey.Group.ID = 4
	rightKey, cancelResponse := newMCPTestContext(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`, service.PlatformPerplexity)
	done := make(chan struct{})
	go func() { h.HandleMCP(call); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("tool did not start")
	}
	h.HandleMCP(wrongKey)
	h.HandleMCP(wrongGroup)
	require.Equal(t, 202, wrongResponse.Code)
	require.Equal(t, 202, groupResponse.Code)
	h.mu.Lock()
	active := h.inflight[mcpRequestKey{KeyID: 1, GroupID: 3, ID: "n:7"}]
	h.mu.Unlock()
	require.NotNil(t, active)
	require.NoError(t, active.ctx.Err())
	h.HandleMCP(rightKey)
	require.Equal(t, 202, cancelResponse.Code)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not terminate the tool")
	}
	require.Contains(t, result.Body.String(), `"isError":true`)
	h.mu.Lock()
	require.Empty(t, h.inflight)
	h.mu.Unlock()
}

func TestMCPExtendedSSEProgressAndFinalResult(t *testing.T) {
	h := newMCPTestHandler()
	var closes atomic.Int32
	stream := `data: {"choices":[{"delta":{"content":"hello"}}],"perplexity":{"search_progress":[{"id":"step","type":"SEARCH_WEB","status":"DONE","text":"private-query-secret"}]}}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":" world"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	h.loopbackStream = func(_ context.Context, _ string, key string, body []byte) (*http.Response, error) {
		require.Equal(t, "sk-test-mcp", key)
		require.Contains(t, string(body), `"stream":true`)
		return &http.Response{StatusCode: 200, Body: &mcpCountedBody{Reader: strings.NewReader(stream), closed: &closes}}, nil
	}
	c, rec := newMCPTestContext(`{"jsonrpc":"2.0","id":"stream-1","method":"tools/call","params":{"name":"perplexity_search","arguments":{"query":"q"},"_meta":{"progressToken":"token-1"}}}`, service.PlatformPerplexity)
	c.Request.Header.Set("Accept", "application/json, text/event-stream")
	h.HandleMCP(c)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, rec.Body.String(), `"method":"notifications/progress"`)
	require.Contains(t, rec.Body.String(), `"progressToken":"token-1"`)
	require.Contains(t, rec.Body.String(), "hello world")
	require.Contains(t, rec.Body.String(), `"id":"stream-1"`)
	require.NotContains(t, rec.Body.String(), "private-query-secret")
	require.NotContains(t, rec.Body.String(), `"total"`)
	require.Equal(t, int32(1), closes.Load())
}

type mcpCountedBody struct {
	io.Reader
	closed *atomic.Int32
}

func (b *mcpCountedBody) Close() error { b.closed.Add(1); return nil }

func TestMCPExtendedSSEFailureNeverPublishesPartialAnswer(t *testing.T) {
	for _, terminal := range []string{"", "event: error\ndata: {\"error\":\"private-secret\"}\n\n", "data: [DONE]\n\n"} {
		h := newMCPTestHandler()
		h.loopbackStream = func(context.Context, string, string, []byte) (*http.Response, error) {
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"partial-secret-answer\"}}]}\n\n" + terminal
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		c, rec := newMCPTestContext(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"perplexity_ask","arguments":{"query":"q"}}}`, service.PlatformPerplexity)
		c.Request.Header.Set("Accept", "text/event-stream")
		h.HandleMCP(c)
		require.Contains(t, rec.Body.String(), `"isError":true`)
		require.NotContains(t, rec.Body.String(), "partial-secret-answer")
		require.NotContains(t, rec.Body.String(), "private-secret")
	}
}

func TestMCPExtendedLoopbackNeverRedirectsOrUsesEnvironmentProxy(t *testing.T) {
	var leaked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer synthetic-mcp-key", r.Header.Get("Authorization"))
		w.Header().Set("Location", other.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	h := newMCPTestHandler()
	h.cfg.Server.Port = server.Listener.Addr().(*net.TCPAddr).Port
	t.Cleanup(h.httpClient.CloseIdleConnections)
	require.Nil(t, h.httpClient.Transport.(*http.Transport).Proxy)
	status, _, err := h.postChatCompletions(context.Background(), h.loopbackURL(), "synthetic-mcp-key", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, 302, status)
	require.Zero(t, leaked.Load())
	_, _, err = h.postChatCompletions(context.Background(), other.URL, "synthetic-mcp-key", []byte(`{}`))
	require.Error(t, err)
	require.Zero(t, leaked.Load())
}

type mcpBlockingPipeBody struct {
	*io.PipeReader
	closes atomic.Int32
	closed chan struct{}
}

func (b *mcpBlockingPipeBody) Close() error {
	err := b.PipeReader.Close()
	if b.closes.Add(1) == 1 {
		close(b.closed)
	}
	return err
}

func TestMCPExtendedSSECancellationClosesBlockedBody(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		h := newMCPTestHandler()
		reader, writer := io.Pipe()
		defer func() { _ = writer.Close() }()
		body := &mcpBlockingPipeBody{PipeReader: reader, closed: make(chan struct{})}
		entered := make(chan struct{})
		h.loopbackStream = func(context.Context, string, string, []byte) (*http.Response, error) {
			close(entered)
			return &http.Response{StatusCode: 200, Body: body}, nil
		}
		call, _ := newMCPTestContext(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"perplexity_search","arguments":{"query":"q"}}}`, service.PlatformPerplexity)
		call.Request.Header.Set("Accept", "text/event-stream")
		ctx, cancel := context.WithCancel(call.Request.Context())
		call.Request = call.Request.WithContext(ctx)
		notification, rec := newMCPTestContext(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":8}}`, service.PlatformPerplexity)
		done := make(chan struct{})
		go func() { h.HandleMCP(call); close(done) }()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("stream did not start")
		}
		if disconnect {
			cancel()
		} else {
			h.HandleMCP(notification)
			require.Equal(t, 202, rec.Code)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("stream did not cancel")
		}
		select {
		case <-body.closed:
		case <-time.After(3 * time.Second):
			t.Fatal("stream body was not closed")
		}
		require.Equal(t, int32(1), body.closes.Load())
		h.mu.Lock()
		require.Empty(t, h.inflight)
		h.mu.Unlock()
		cancel()
	}
}

func TestMCPExtendedActiveRequestsAreBoundedAndIDsRemainTyped(t *testing.T) {
	h := newMCPTestHandler()
	c, _ := newMCPTestContext(`{}`, service.PlatformPerplexity)
	key, _ := middleware.GetAPIKeyFromContext(c)
	for index := 0; index < 32; index++ {
		raw, _ := json.Marshal(index)
		_, cancel, release, err := h.registerMCPRequest(context.Background(), key, raw, nil)
		require.NoError(t, err)
		t.Cleanup(cancel)
		t.Cleanup(release)
	}
	_, _, _, err := h.registerMCPRequest(context.Background(), key, json.RawMessage(`100`), nil)
	require.Error(t, err)
	stringID, _ := mcpCanonicalID(json.RawMessage(`"1"`))
	numberID, _ := mcpCanonicalID(json.RawMessage(`1`))
	require.NotEqual(t, stringID, numberID)
	for _, raw := range []string{`null`, `{}`, `true`, `1.5`, `9007199254740992`, `1 true`} {
		_, valid := mcpCanonicalID(json.RawMessage(raw))
		require.False(t, valid, raw)
	}
}
