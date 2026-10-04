//go:build unit

package service

import (
	"context"
	"encoding/json"
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

type anthropicSSEFieldCase struct {
	name string
	data string
	fast bool
}

func anthropicSSEFieldCases() []anthropicSSEFieldCase {
	esc := jsonUnicodeEscape
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	return []anthropicSSEFieldCase{
		{"常规 delta，字符串里的数字加 e 不影响快速路径", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"1e5 v2E9 toolu_01E9"}}`, true},
		{"重复 type：事件类型取最后一个，终止判断取第一个", `{"type":"message_stop","type":"content_block_delta","index":1}`, true},
		{"重复 index 与 content_block 内重复 type", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","type":"text"},"index":2}`, true},
		{"字段类型不符", `{"type":"content_block_delta","delta":{"type":"text_delta"},"delta":"str","index":"1"}`, true},
		{"message 与 model 都重复", `{"type":"message_start","message":{"model":"a","model":"b"},"message":{"model":"c","model":"d"}}`, true},
		{"转义键名与转义值", `{"typ` + esc("0065") + `":"message_` + esc("0073") + `top","ind` + esc("0065") + `x":3}`, true},
		{"孤立代理后跟转义", `{"type":"content_block_delta` + esc("d800", "0041") + `","message":{"model":"m` + esc("dc00") + `"}}`, true},
		{"非法 UTF-8", "{\"type\":\"content_block_delta\xff\",\"message\":{\"model\":\"m\xed\xa0\x80\"}}", true},
		{"小数与负零下标", `{"type":"content_block_stop","index":3.0,"v":-0}`, true},
		{"溢出的指数", `{"type":"message_stop","v":1e400}`, false},
		{"溢出的长整数", `{"type":"message_stop","v":` + strings.Repeat("9", 400) + `}`, false},
		{"不溢出的指数也保守走慢路径", `{"type":"message_stop","type":"ping","v":1e2}`, false},
		{"超过嵌套上限", `{"type":"message_stop","v":` + deep + `}`, false},
		{"超大载荷", `{"type":"message_stop","v":"` + strings.Repeat("x", jsonDepthSafeMaxBytes) + `"}`, false},
		{"null", `null`, false},
		{"非对象", `[{"type":"message_stop"}]`, false},
		{"非法 JSON", `{"type":"message_stop"`, false},
	}
}

// requireAnthropicSSEFieldsExact 断言：走快速路径的输入 json.Unmarshal 必然成功，且读出的
// 字段与解码成 map 后读取的结果完全一致。
func requireAnthropicSSEFieldsExact(t *testing.T, data string) {
	t.Helper()
	if !anthropicSSEFastPathSafe(data) {
		return
	}
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(data), &event), "fast path accepted %.120q", data)
	require.Equal(t, anthropicSSEEventFieldsFromMap(event, data), parseAnthropicSSEEventFields(data), "%.120q", data)
}

func TestAnthropicSSEEventFields_FastPathMatchesMapDecode(t *testing.T) {
	for _, tc := range anthropicSSEFieldCases() {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.fast, anthropicSSEFastPathSafe(tc.data))
			requireAnthropicSSEFieldsExact(t, tc.data)
		})
	}
}

func FuzzAnthropicSSEEventFields(f *testing.F) {
	for _, tc := range anthropicSSEFieldCases() {
		if len(tc.data) < 2048 {
			f.Add(tc.data)
		}
	}
	f.Fuzz(func(t *testing.T, data string) {
		requireAnthropicSSEFieldsExact(t, data)
	})
}
