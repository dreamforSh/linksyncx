//go:build unit

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func antigravityCollectionContext() (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/fake-collect", nil)
	return c, recorder
}

func antigravityCollectionResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func antigravityCollectionFrames(wrapped bool) string {
	frames := []string{
		`{"responseId":"fake-response","modelVersion":"fake-model","candidates":[{"content":{"role":"model","parts":[{"text":"hello "}]}}],"usageMetadata":{"promptTokenCount":12,"cachedContentTokenCount":3}}`,
		`{"candidates":[{"content":{"parts":[{"text":"world"}]}}]}`,
		`{"candidates":[{"finishReason":"MAX_TOKENS","finishMessage":"fake limit","safetyRatings":[{"category":"fake","probability":"LOW"}]}],"usageMetadata":{"candidatesTokenCount":7,"thoughtsTokenCount":2,"candidatesTokensDetails":[{"modality":"IMAGE","tokenCount":4}]}}`,
		`{"createTime":"fake-time"}`,
	}
	var body strings.Builder
	for _, frame := range frames {
		if wrapped {
			frame = `{"response":` + frame + `}`
		}
		body.WriteString("data: " + frame + "\n\n")
	}
	return body.String()
}

func TestGeminiStreamToNonStreamingMergesTerminalMetadata(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		name := "direct"
		if wrapped {
			name = "v1internal"
		}
		t.Run(name, func(t *testing.T) {
			c, recorder := antigravityCollectionContext()
			service := &AntigravityGatewayService{}
			result, err := service.handleGeminiStreamToNonStreaming(c, antigravityCollectionResponse(antigravityCollectionFrames(wrapped)), time.Now())
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, http.StatusOK, recorder.Code)
			body := recorder.Body.String()
			require.Equal(t, "hello world", gjson.Get(body, "candidates.0.content.parts.0.text").String())
			require.Equal(t, "MAX_TOKENS", gjson.Get(body, "candidates.0.finishReason").String())
			require.Equal(t, "fake limit", gjson.Get(body, "candidates.0.finishMessage").String())
			require.Equal(t, "fake", gjson.Get(body, "candidates.0.safetyRatings.0.category").String())
			require.Equal(t, "fake-response", gjson.Get(body, "responseId").String())
			require.Equal(t, "fake-model", gjson.Get(body, "modelVersion").String())
			require.Equal(t, "fake-time", gjson.Get(body, "createTime").String())
			require.Equal(t, int64(12), gjson.Get(body, "usageMetadata.promptTokenCount").Int())
			require.Equal(t, int64(7), gjson.Get(body, "usageMetadata.candidatesTokenCount").Int())
			require.Equal(t, &ClaudeUsage{InputTokens: 9, OutputTokens: 9, CacheReadInputTokens: 3, ImageOutputTokens: 4}, result.usage)
			require.NotNil(t, result.firstTokenMs)
			require.False(t, result.usageIncomplete)
		})
	}
}

func TestCollectClaudeStreamResponseMergesTerminalMetadata(t *testing.T) {
	c, recorder := antigravityCollectionContext()
	body, result, err := (&AntigravityGatewayService{}).collectClaudeStreamResponse(c, antigravityCollectionResponse(antigravityCollectionFrames(true)), time.Now(), "fake-original-model")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, c.Writer.Written(), recorder.Body.String())
	require.Equal(t, "hello world", gjson.GetBytes(body, "content.0.text").String())
	require.Equal(t, "max_tokens", gjson.GetBytes(body, "stop_reason").String())
	require.Equal(t, "fake-response", gjson.GetBytes(body, "id").String())
	require.Equal(t, "fake-original-model", gjson.GetBytes(body, "model").String())
	require.Equal(t, int64(9), gjson.GetBytes(body, "usage.input_tokens").Int())
	require.Equal(t, int64(9), gjson.GetBytes(body, "usage.output_tokens").Int())
	require.Equal(t, int64(3), gjson.GetBytes(body, "usage.cache_read_input_tokens").Int())
	require.Equal(t, &ClaudeUsage{InputTokens: 9, OutputTokens: 9, CacheReadInputTokens: 3, ImageOutputTokens: 4}, result.usage)
	require.False(t, result.usageIncomplete)
}

func TestAntigravityNonStreamingPreservesPartOrder(t *testing.T) {
	const parts = `[{"text":"first"},{"thought":true,"text":"reason","thoughtSignature":"fake-thought"},{"functionCall":{"name":"fake_tool","args":{"value":1}},"thoughtSignature":"fake-tool"},{"inlineData":{"mimeType":"image/png","data":"ZmFrZQ=="}},{"thoughtSignature":"fake-only"},{"text":"signed","thoughtSignature":"fake-signed"},{"text":"last"}]`
	var collected []map[string]any
	require.NoError(t, json.Unmarshal([]byte(parts), &collected))
	var upstream strings.Builder
	for _, part := range collected {
		partJSON, err := json.Marshal(part)
		require.NoError(t, err)
		upstream.WriteString(`data: {"response":{"candidates":[{"content":{"parts":[` + string(partJSON) + `]}}]}}` + "\n\n")
	}
	upstream.WriteString(`data: {"response":{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n")
	c, recorder := antigravityCollectionContext()
	result, err := (&AntigravityGatewayService{}).handleGeminiStreamToNonStreaming(c, antigravityCollectionResponse(upstream.String()), time.Now())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.JSONEq(t, parts, gjson.Get(recorder.Body.String(), "candidates.0.content.parts").Raw)
}

func TestMergeCollectedPartsToResponsePreservesSignaturesAndCopies(t *testing.T) {
	const original = `{"candidates":[{"content":{"role":"model","parts":[{"text":"original"}]},"finishReason":"MAX_TOKENS"},{"content":{"parts":[{"text":"other candidate"}]}}],"usageMetadata":{"promptTokenCount":8}}`
	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(original), &response))
	var parts []map[string]any
	require.NoError(t, json.Unmarshal([]byte(`[{"text":"a"},{"text":"b"},{"text":"thought","thought":true},{"text":"signed","thoughtSignature":"fake-signature"},{"text":"c"},{"text":"","thoughtSignature":"fake-empty"},{"text":"d"},{"thoughtSignature":"fake-only"},{"text":"annotated","thought":false},{"functionCall":{"name":"fake_tool","args":{}}},{"inlineData":{"mimeType":"image/png","data":"ZmFrZQ=="}},{"text":""}]`), &parts))
	result := mergeCollectedPartsToResponse(response, parts)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.JSONEq(t, `[{"text":"ab"},{"text":"thought","thought":true},{"text":"signed","thoughtSignature":"fake-signature"},{"text":"c"},{"text":"","thoughtSignature":"fake-empty"},{"text":"d"},{"thoughtSignature":"fake-only"},{"text":"annotated","thought":false},{"functionCall":{"name":"fake_tool","args":{}}},{"inlineData":{"mimeType":"image/png","data":"ZmFrZQ=="}},{"text":""}]`, gjson.GetBytes(encoded, "candidates.0.content.parts").Raw)
	require.Equal(t, "MAX_TOKENS", gjson.GetBytes(encoded, "candidates.0.finishReason").String())
	unchanged, err := json.Marshal(response)
	require.NoError(t, err)
	require.JSONEq(t, original, string(unchanged), "part merging must not mutate the source response")
}

func TestAntigravityNonStreamingRejectsPartialWithUsage(t *testing.T) {
	const prefix = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"fake partial text"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
	cases := []struct {
		name string
		body string
	}{
		{name: "missing finish", body: prefix},
		{name: "done without finish", body: prefix + "data: [DONE]\n\n"},
		{name: "upstream error", body: prefix + `data: {"error":{"code":500,"message":"fake upstream error"}}` + "\n\n"},
		{name: "wrapped upstream error", body: prefix + `data: {"response":{"error":{"code":500}}}` + "\n\n"},
		{name: "error event", body: prefix + "event: error\ndata: {\"message\":\"fake upstream error\"}\n\n"},
		{name: "invalid JSON", body: prefix + "data: {not valid JSON}\n\n"},
		{name: "invalid response object", body: prefix + "data: null\n\n"},
		{name: "invalid parts", body: prefix + `data: {"candidates":[{"content":{"parts":[null]}}]}` + "\n\n"},
		{name: "invalid after finish", body: prefix + `data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\ndata: invalid\n\n"},
		{name: "usage only", body: `data: {"response":{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"},
		{name: "usage then content without finish", body: `data: {"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}` + "\n\n" + `data: {"candidates":[{"content":{"parts":[{"text":"fake partial text"}]}}]}` + "\n\n"},
		{name: "usage on error frame", body: `data: {"error":{"code":500},"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}` + "\n\n"},
	}
	paths := []string{"gemini", "claude collect", "claude handler"}
	for _, tc := range cases {
		for _, path := range paths {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				c, recorder := antigravityCollectionContext()
				service := &AntigravityGatewayService{}
				response := antigravityCollectionResponse(tc.body)
				var result *antigravityStreamResult
				var err error
				switch path {
				case "gemini":
					result, err = service.handleGeminiStreamToNonStreaming(c, response, time.Now())
				case "claude collect":
					var body []byte
					body, result, err = service.collectClaudeStreamResponse(c, response, time.Now(), "fake-model")
					require.Empty(t, body)
				case "claude handler":
					result, err = service.handleClaudeStreamToNonStreaming(c, response, time.Now(), "fake-model")
				}
				require.Error(t, err)
				var failover *UpstreamFailoverError
				require.False(t, errors.As(err, &failover), "observed usage must not trigger another generation")
				require.NotNil(t, result)
				require.Equal(t, &ClaudeUsage{InputTokens: 8, OutputTokens: 3}, result.usage)
				require.True(t, result.usageIncomplete)
				require.NotContains(t, recorder.Body.String(), "fake partial text")
				if path == "claude handler" {
					require.Equal(t, http.StatusBadGateway, recorder.Code)
				} else {
					require.False(t, c.Writer.Written(), "failed collection must not emit successful JSON")
				}
			})
		}
	}
}

func TestAntigravityNonStreamingEmptyFailover(t *testing.T) {
	for _, body := range []string{"", ": fake keepalive\n\n", "data: [DONE]\n\n", "data: {\"responseId\":\"fake-metadata-only\"}\n\n"} {
		for _, claude := range []bool{false, true} {
			c, _ := antigravityCollectionContext()
			service := &AntigravityGatewayService{}
			response := antigravityCollectionResponse(body)
			var result *antigravityStreamResult
			var err error
			if claude {
				_, result, err = service.collectClaudeStreamResponse(c, response, time.Now(), "fake-model")
			} else {
				result, err = service.handleGeminiStreamToNonStreaming(c, response, time.Now())
			}
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.RetryableOnSameAccount)
			require.Nil(t, result)
			require.False(t, c.Writer.Written())
		}
	}
}

type antigravityCollectionErrorReader struct {
	err error
}

func (r antigravityCollectionErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestAntigravityNonStreamingReadErrorsPreserveUsage(t *testing.T) {
	const prefix = `data: {"response":{"candidates":[{"content":{"parts":[{"text":"fake partial"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}` + "\n\n"
	for _, readErr := range []error{io.ErrUnexpectedEOF, context.Canceled, context.DeadlineExceeded} {
		t.Run(readErr.Error(), func(t *testing.T) {
			c, _ := antigravityCollectionContext()
			response := antigravityCollectionResponse("")
			response.Body = io.NopCloser(io.MultiReader(strings.NewReader(prefix), antigravityCollectionErrorReader{err: readErr}))
			body, result, err := (&AntigravityGatewayService{}).collectClaudeStreamResponse(c, response, time.Now(), "fake-model")
			require.ErrorIs(t, err, readErr)
			require.Empty(t, body)
			require.NotNil(t, result)
			require.Equal(t, &ClaudeUsage{InputTokens: 8, OutputTokens: 3}, result.usage)
			require.True(t, result.usageIncomplete)
		})
	}
}

type antigravityCollectionCloseBody struct {
	io.Reader
	closer io.Closer
	closed atomic.Bool
}

func (b *antigravityCollectionCloseBody) Close() error {
	b.closed.Store(true)
	return b.closer.Close()
}

func TestAntigravityNonStreamingTimeoutPreservesUsageAndClosesBody(t *testing.T) {
	const prefix = `data: {"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}` + "\n\n"
	reader, writer := io.Pipe()
	defer writer.Close()
	body := &antigravityCollectionCloseBody{Reader: io.MultiReader(strings.NewReader(prefix), reader), closer: reader}
	response := antigravityCollectionResponse("")
	response.Body = body
	c, _ := antigravityCollectionContext()
	service := &AntigravityGatewayService{settingService: &SettingService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 1}}}}
	_, result, err := service.collectAntigravityStreamResponse(c, response, time.Now())
	require.ErrorContains(t, err, "stream data interval timeout")
	require.NotNil(t, result)
	require.Equal(t, &ClaudeUsage{InputTokens: 8, OutputTokens: 3}, result.usage)
	require.True(t, result.usageIncomplete)
	require.True(t, body.closed.Load(), "timeout must unblock the scanner by closing its body")
}

func TestAntigravityNonStreamingRejectsIncompleteWithoutUsage(t *testing.T) {
	for _, body := range []string{
		`data: {"candidates":[{"content":{"parts":[{"text":"fake incomplete"}]}}]}` + "\n\n",
		"data: malformed JSON\n\n",
		`data: {"error":{"code":500}}` + "\n\n",
		"event: error\n\n",
	} {
		c, _ := antigravityCollectionContext()
		_, result, err := (&AntigravityGatewayService{}).collectAntigravityStreamResponse(c, antigravityCollectionResponse(body), time.Now())
		require.Error(t, err)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover), "only genuinely empty streams are eligible for empty-stream retry")
		require.NotNil(t, result)
		require.Nil(t, result.usage)
		require.True(t, result.usageIncomplete)
	}
}

func TestAntigravityNonStreamingLineTooLongPreservesUsage(t *testing.T) {
	const prefix = `data: {"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}` + "\n\n"
	c, _ := antigravityCollectionContext()
	service := &AntigravityGatewayService{settingService: &SettingService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: 128}}}}
	_, result, err := service.collectAntigravityStreamResponse(c, antigravityCollectionResponse(prefix+"data: "+strings.Repeat("x", 128<<10)), time.Now())
	require.ErrorIs(t, err, bufio.ErrTooLong)
	require.NotNil(t, result)
	require.Equal(t, &ClaudeUsage{InputTokens: 8, OutputTokens: 3}, result.usage)
	require.True(t, result.usageIncomplete)
}

func TestAntigravityNonStreamingOptionalNullMetadataDoesNotEraseState(t *testing.T) {
	const body = `data: {"responseId":"fake-response","candidates":[{"content":{"parts":[{"text":"fake text"}]} }],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}` + "\n\n" +
		`data: {"candidates":[{"content":null,"finishReason":"MAX_TOKENS"}],"usageMetadata":null}` + "\n\n" +
		`data: {"candidates":null,"usageMetadata":null}` + "\n\n"
	c, recorder := antigravityCollectionContext()
	result, err := (&AntigravityGatewayService{}).handleGeminiStreamToNonStreaming(c, antigravityCollectionResponse(body), time.Now())
	require.NoError(t, err)
	require.Equal(t, &ClaudeUsage{InputTokens: 8, OutputTokens: 3}, result.usage)
	require.Equal(t, "fake text", gjson.Get(recorder.Body.String(), "candidates.0.content.parts.0.text").String())
	require.Equal(t, "MAX_TOKENS", gjson.Get(recorder.Body.String(), "candidates.0.finishReason").String())
	require.Equal(t, int64(8), gjson.Get(recorder.Body.String(), "usageMetadata.promptTokenCount").Int())
}

func TestAntigravityNonStreamingRequiresFinishForEveryCandidate(t *testing.T) {
	const prefix = `data: {"candidates":[{"index":0,"content":{"parts":[{"text":"zero"}]}},{"index":1,"content":{"parts":[{"text":"one"}]}}],"usageMetadata":{"promptTokenCount":8}}` + "\n\n" + `data: {"candidates":[{"index":1,"finishReason":"STOP"}]}` + "\n\n"
	for _, finished := range []bool{false, true} {
		upstream := prefix
		if finished {
			upstream += `data: {"candidates":[{"index":0,"finishReason":"MAX_TOKENS"}]}` + "\n\n"
		}
		c, _ := antigravityCollectionContext()
		response, result, err := (&AntigravityGatewayService{}).collectAntigravityStreamResponse(c, antigravityCollectionResponse(upstream), time.Now())
		if !finished {
			require.ErrorContains(t, err, "finishReason")
			require.Nil(t, response)
			require.True(t, result.usageIncomplete)
			continue
		}
		require.NoError(t, err)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		require.Equal(t, "zero", gjson.GetBytes(encoded, "candidates.0.content.parts.0.text").String())
		require.Equal(t, "MAX_TOKENS", gjson.GetBytes(encoded, "candidates.0.finishReason").String())
		require.Equal(t, "one", gjson.GetBytes(encoded, "candidates.1.content.parts.0.text").String())
		require.Equal(t, "STOP", gjson.GetBytes(encoded, "candidates.1.finishReason").String())
	}
}
