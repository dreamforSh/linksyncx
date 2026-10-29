//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// runOpenAICompatBridgeStream feeds write's SSE through the Messages or Chat
// Completions bridge and returns its result.
func runOpenAICompatBridgeStream(t *testing.T, bridge string, gateway config.GatewayConfig, account *Account, write func(w *io.PipeWriter)) (*OpenAIForwardResult, error, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	gateway.MaxLineSize = defaultMaxLineSize
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: gateway}}

	reader, writer := io.Pipe()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer func() { _ = writer.Close() }()
		write(writer)
	}()
	t.Cleanup(func() {
		_ = reader.Close()
		<-writerDone
	})

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+bridge, nil)
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: reader}
	started := time.Now()

	var result *OpenAIForwardResult
	var err error
	switch bridge {
	case "messages":
		result, err = svc.handleAnthropicStreamingResponse(resp, c, account, "grok-4.6", "grok-4.6", "grok-4.6", started)
	case "chat/completions":
		result, err = svc.handleChatStreamingResponse(resp, c, account, "grok-4.6", "grok-4.6", "grok-4.6", started, 0)
	default:
		t.Fatalf("unknown bridge %q", bridge)
	}
	return result, err, recorder
}

func TestOpenAICompatBridgesStartTTFTAtFirstOutput(t *testing.T) {
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic, expiresAt: time.Now().Add(time.Minute).UnixNano()})
	t.Cleanup(func() {
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{openAITTFTMode: OpenAITTFTModeSemantic, expiresAt: time.Now().Add(time.Minute).UnixNano()})
	})
	for _, bridge := range []string{"messages", "chat/completions"} {
		t.Run(bridge, func(t *testing.T) {
			account := &Account{ID: 7001, Name: "grok", Platform: PlatformGrok, Type: AccountTypeOAuth}
			result, err, recorder := runOpenAICompatBridgeStream(t, bridge, config.GatewayConfig{}, account, func(w *io.PipeWriter) {
				_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ttft\",\"model\":\"grok-4.6\"}}\n\n")
				time.Sleep(120 * time.Millisecond)
				_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n")
				_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ttft\",\"model\":\"grok-4.6\",\"status\":\"completed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
			})
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.FirstTokenMs, "the delta is the first token")
			require.GreaterOrEqual(t, *result.FirstTokenMs, 100, "response.created is a preamble, not a token")
			require.Contains(t, recorder.Body.String(), "hi")
		})
	}
}

func TestOpenAICompatBridgesFailOverIdleGrokStreamBeforeOutput(t *testing.T) {
	for _, bridge := range []string{"messages", "chat/completions"} {
		t.Run(bridge, func(t *testing.T) {
			account := &Account{ID: 7002, Name: "grok", Platform: PlatformGrok, Type: AccountTypeOAuth}
			hold := make(chan struct{})
			_, err, recorder := runOpenAICompatBridgeStream(t, bridge, config.GatewayConfig{StreamDataIntervalTimeout: 1}, account, func(*io.PipeWriter) {
				// A hung upstream: headers arrived, no event ever follows.
				select {
				case <-hold:
				case <-time.After(5 * time.Second):
				}
			})
			close(hold)

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.True(t, failoverErr.SafeToFailoverAfterWrite)
			require.True(t, failoverErr.RetryableOnSameAccount)
			require.Contains(t, string(failoverErr.ResponseBody), "empty_upstream")
			require.Empty(t, recorder.Body.String(), "nothing may reach the client before a failover")
		})
	}
}

func TestOpenAICompatBridgesKeepPlainIdleTimeoutForOtherPlatforms(t *testing.T) {
	account := &Account{ID: 7003, Name: "openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	hold := make(chan struct{})
	_, err, _ := runOpenAICompatBridgeStream(t, "messages", config.GatewayConfig{StreamDataIntervalTimeout: 1}, account, func(*io.PipeWriter) {
		select {
		case <-hold:
		case <-time.After(5 * time.Second):
		}
	})
	close(hold)

	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.EqualError(t, err, "stream data interval timeout")
}

func TestOpenAICompatStreamIdleTimeout(t *testing.T) {
	grok := &Account{Platform: PlatformGrok}
	openAI := &Account{Platform: PlatformOpenAI}

	disabled := &OpenAIGatewayService{cfg: &config.Config{}}
	require.Equal(t, defaultGrokStreamIdleTimeout, disabled.openAICompatStreamIdleTimeout(grok))
	require.Zero(t, disabled.openAICompatStreamIdleTimeout(openAI))

	configured := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 45}}}
	require.Equal(t, 45*time.Second, configured.openAICompatStreamIdleTimeout(grok))
	require.Equal(t, 45*time.Second, configured.openAICompatStreamIdleTimeout(openAI))
}
