//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runAnthropicRelayForTest(t *testing.T, stream, originalModel, mappedModel string, rewrite *ToolNameRewrite) (string, *streamingResult, error) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		rateLimitService: &RateLimitService{},
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if rewrite != nil {
		c.Set(toolNameRewriteKey, rewrite)
	}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}
	result, err := svc.handleStreamingResponse(context.Background(), resp, c, &Account{ID: 1}, time.Now(), originalModel, mappedModel, false)
	return rec.Body.String(), result, err
}

func TestHandleStreamingResponse_NormalizesAndForwardsEvents(t *testing.T) {
	const stop = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	tests := []struct {
		name    string
		stream  string
		want    string
		wantErr string
	}{
		{
			name:   "缺少 event 行时按 type 补全，data 前缀统一加空格",
			stream: "data:{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" + stop,
			want:   "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" + stop,
		},
		{
			name:   "CRLF、空白分隔行与注释事件",
			stream: ": keepalive\r\n\r\nevent: ping\r\ndata: {\"type\": \"ping\"}\r\n \r\n" + stop,
			want:   ": keepalive\n\nevent: ping\ndata: {\"type\": \"ping\"}\n\n" + stop,
		},
		{
			name:   "非 JSON 对象的 data 原样透传",
			stream: "event: content_block_delta\ndata: {\"type\":\n\ndata: 123\n\n" + stop,
			want:   "event: content_block_delta\ndata: {\"type\":\n\ndata: 123\n\n" + stop,
		},
		{
			name:    "结尾没有空行的不完整事件不会写出",
			stream:  "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}",
			want:    "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n",
			wantErr: "missing terminal event",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _, err := runAnthropicRelayForTest(t, tt.stream, "m", "m", nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, body)
		})
	}
}

func TestHandleStreamingResponse_RewritesMessageStartModelAndUsage(t *testing.T) {
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4-5-20250929\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	body, result, err := runAnthropicRelayForTest(t, stream, "claude-sonnet-4-5", "claude-sonnet-4-5-20250929", nil)
	require.NoError(t, err)
	require.Equal(t, "event: message_start\ndata: {\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}},\"type\":\"message_start\"}\n\n"+
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":9}}\n\n"+
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", body)
	require.Equal(t, 3, result.usage.InputTokens)
	require.Equal(t, 9, result.usage.OutputTokens)
}

func TestHandleStreamingResponse_RestoresToolNames(t *testing.T) {
	stream := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"fetch_file\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"see cc_sess_list\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	body, _, err := runAnthropicRelayForTest(t, stream, "m", "m", &ToolNameRewrite{ReverseOrdered: [][2]string{{"fetch_file", "Read"}}})
	require.NoError(t, err)
	require.Contains(t, body, `"name":"Read"`)
	require.Contains(t, body, `"text":"see sessions_list"`)
	require.NotContains(t, body, "fetch_file")
}

func TestRestoreToolNamesInBytes_NoMatchDoesNotAllocate(t *testing.T) {
	chunk := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"plain streamed text without any rewritten tool names\"}}\n\n")
	rw := &ToolNameRewrite{ReverseOrdered: [][2]string{{"fetch_file", "Read"}}}
	allocs := testing.AllocsPerRun(100, func() {
		_ = restoreToolNamesInBytes(chunk, rw)
	})
	require.Zero(t, allocs)
}
