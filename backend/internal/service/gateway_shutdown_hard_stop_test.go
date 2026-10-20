//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/shutdownctx"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// loopbackHTTPUpstream 把网关构造的上游请求改发到本地测试服务器，并用真实的
// net/http Transport 发送：要验证的正是“上游 context 被取消时 body 读取返回
// context.Canceled”这一真实传输层行为，不能用桩 body 代替。
type loopbackHTTPUpstream struct {
	target *url.URL
	client *http.Client
}

func (u *loopbackHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req.URL.Scheme = u.target.Scheme
	req.URL.Host = u.target.Host
	req.Host = ""
	return u.client.Do(req)
}

func (u *loopbackHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

// markerStreamRecorder 在网关向客户端写出包含 marker 的内容时发信号，
// 让测试在上游事件确实被处理之后再触发断开/硬停止。
type markerStreamRecorder struct {
	*httptest.ResponseRecorder
	mu     sync.Mutex
	marker string
	seen   chan struct{}
	once   sync.Once
}

func newMarkerStreamRecorder(marker string) *markerStreamRecorder {
	return &markerStreamRecorder{ResponseRecorder: httptest.NewRecorder(), marker: marker, seen: make(chan struct{})}
}

func (w *markerStreamRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.ResponseRecorder.Write(p)
	seen := strings.Contains(w.Body.String(), w.marker)
	w.mu.Unlock()
	if seen {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *markerStreamRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

type forwardOutcome struct {
	result *ForwardResult
	err    error
}

// 关停强制阶段触发硬停止后，与请求取消解耦的上游流必须走既有的 context.Canceled
// 分支结束，并把已观测到的 usage 作为部分结果返回给 handler 提交计费；在此之前，
// 客户端断开仍不能结束这条流（断开后继续读上游以完整计费）。
func TestGatewayForwardReturnsPartialUsageWhenShutdownHardStopsDetachedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":5}}`,
		"",
		"",
	}, "\n")
	releaseUpstream := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "rid-hard-stop")
		_, _ = io.WriteString(w, upstreamSSE)
		_ = http.NewResponseController(w).Flush()
		// 模型仍在生成：流保持打开，直到网关侧断开。
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	t.Cleanup(func() {
		close(releaseUpstream)
		upstreamServer.Close()
	})
	target, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)

	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         &loopbackHTTPUpstream{target: target, client: &http.Client{Transport: transport}},
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	hardStop, fireHardStop := context.WithCancel(context.Background())
	defer fireHardStop()
	clientCtx, disconnectClient := context.WithCancel(shutdownctx.WithHardStop(context.Background(), hardStop))
	defer disconnectClient()

	rec := newMarkerStreamRecorder("message_delta")
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(clientCtx)
	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	done := make(chan forwardOutcome, 1)
	go func() {
		result, err := svc.Forward(clientCtx, c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
		done <- forwardOutcome{result: result, err: err}
	}()
	select {
	case <-rec.seen:
	case outcome := <-done:
		t.Fatalf("Forward returned before relaying the stream: %+v", outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway never relayed message_delta")
	}

	disconnectClient()
	select {
	case outcome := <-done:
		t.Fatalf("a client disconnect ended the detached upstream stream: %+v", outcome)
	case <-time.After(200 * time.Millisecond):
	}

	fireHardStop()
	var outcome forwardOutcome
	select {
	case outcome = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hard-stop did not end the detached upstream stream")
	}
	require.ErrorIs(t, outcome.err, context.Canceled)
	require.NotNil(t, outcome.result, "the partial usage must be returned so the handler submits it")
	require.True(t, outcome.result.UsageIncomplete)
	require.True(t, outcome.result.Stream)
	require.Equal(t, 11, outcome.result.Usage.InputTokens)
	require.Equal(t, 7, outcome.result.Usage.CacheReadInputTokens)
	require.Equal(t, 5, outcome.result.Usage.OutputTokens)
	require.Equal(t, "rid-hard-stop", outcome.result.RequestID)
}

func TestDetachUpstreamContextHelpersFollowShutdownHardStop(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	request, disconnect := context.WithCancel(shutdownctx.WithHardStop(context.Background(), hardStop))
	defer disconnect()

	streamCtx, releaseStream := detachStreamUpstreamContext(request, true)
	defer releaseStream()
	upstreamCtx, releaseUpstream := detachUpstreamContext(request)
	defer releaseUpstream()
	nonStreamCtx, releaseNonStream := detachStreamUpstreamContext(request, false)
	defer releaseNonStream()
	require.Equal(t, request, nonStreamCtx, "non-stream requests keep following the client context")

	// release 在发出请求前就会被调用（见各调用点），不能因此取消上游。
	releaseStream()
	releaseUpstream()
	disconnect()
	require.NoError(t, streamCtx.Err())
	require.NoError(t, upstreamCtx.Err())

	fireHardStop()
	require.ErrorIs(t, streamCtx.Err(), context.Canceled)
	require.ErrorIs(t, upstreamCtx.Err(), context.Canceled)
}
