//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParsedRequestReplaceBody_SameSliceSkipsReparse(t *testing.T) {
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"messages":[]}`)), PlatformAnthropic)
	require.NoError(t, err)

	// 人为改写派生字段：同一切片不触发重解析，字段保持不变即说明跳过了解析。
	parsed.Model = "sentinel"
	require.NoError(t, parsed.ReplaceBody(parsed.Body.Bytes()))
	require.Equal(t, "sentinel", parsed.Model)

	// 内容相同但底层数组不同，仍按新请求体重新解析。
	copied := append([]byte(nil), parsed.Body.Bytes()...)
	require.NoError(t, parsed.ReplaceBody(copied))
	require.Equal(t, "claude-sonnet-4-5", parsed.Model)
	require.True(t, parsed.Stream)
}

func TestParsedRequestReplaceBody_FailedRefreshIsNotShortCircuited(t *testing.T) {
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[]}`)), PlatformAnthropic)
	require.NoError(t, err)

	invalid := []byte(`{"model":`)
	require.Error(t, parsed.ReplaceBody(invalid))
	require.Error(t, parsed.ReplaceBody(invalid), "解析失败后同一切片仍需重新校验")
}

func TestParsedRequestCloneForBody_SameSliceReusesDerivedState(t *testing.T) {
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	parsed.Model = "sentinel"

	clone, err := parsed.CloneForBody(parsed.Body.Bytes())
	require.NoError(t, err)
	require.Equal(t, "sentinel", clone.Model)
	require.Equal(t, parsed.MessagesRaw(), clone.MessagesRaw())

	clone, err = parsed.CloneForBody([]byte(`{"model":"claude-opus-4-6","messages":[]}`))
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-6", clone.Model)
}

type firstWriteHookWriter struct {
	header       http.Header
	wrote        bool
	onFirstWrite func()
}

func (w *firstWriteHookWriter) Header() http.Header { return w.header }

func (w *firstWriteHookWriter) WriteHeader(int) {}

func (w *firstWriteHookWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		if w.onFirstWrite != nil {
			w.onFirstWrite()
		}
	}
	return len(p), nil
}

func (w *firstWriteHookWriter) Flush() {}

func TestGatewayForward_SyncsAcceptedWireBodyAfterFirstClientByte(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-sonnet-4-5-20250929\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")),
	}}
	svc := newAnthropicOAuthMappingGatewayService(upstream)
	// 指纹服务会改写 metadata.user_id，让上游实际接受的 wire body 与客户端 body 不同。
	svc.identityService = NewIdentityService(&identityCacheStub{})
	account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)
	account.Extra = map[string]any{"account_uuid": "99999999-8888-7777-6666-555555555555"}

	parsed := parseAnthropicOAuthMappingRequest(t, `{"model":"claude-sonnet-4-5-20250929","stream":true,"max_tokens":16,`+
		`"metadata":{"user_id":"{\"device_id\":\"7f3c2b1a9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b\",\"account_uuid\":\"\",\"session_id\":\"0b6f8a52-4c1e-4f7a-9a3d-2e5b7c9d1f3a\"}"},`+
		`"messages":[{"role":"user","content":"hi"}]}`)
	clientBody := string(parsed.Body.Bytes())

	var bodyAtFirstByte string
	writer := &firstWriteHookWriter{header: http.Header{}, onFirstWrite: func() {
		bodyAtFirstByte = string(parsed.Body.Bytes())
	}}
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")

	_, err := svc.Forward(context.Background(), c, account, parsed)
	require.NoError(t, err)
	require.NotEqual(t, clientBody, string(upstream.lastBody), "前置条件：wire body 与客户端 body 不同")
	require.Equal(t, clientBody, bodyAtFirstByte, "wire body 同步不应发生在客户端首字节之前")
	require.Equal(t, string(upstream.lastBody), string(parsed.Body.Bytes()), "Forward 返回前应同步为上游接受的 wire body")
}
