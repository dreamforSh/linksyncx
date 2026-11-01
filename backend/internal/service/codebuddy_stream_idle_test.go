//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStreamIdleBodyCutsOffSilentRead(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	body := newStreamIdleBody(reader, 50*time.Millisecond)

	started := time.Now()
	_, err := body.Read(make([]byte, 8))
	var idleErr *upstreamStreamIdleError
	require.ErrorAs(t, err, &idleErr)
	require.GreaterOrEqual(t, time.Since(started), 50*time.Millisecond)
	require.NotErrorIs(t, err, context.Canceled, "a stall must not read as a client abort")
	_, writeErr := writer.Write([]byte("x"))
	require.ErrorIs(t, writeErr, io.ErrClosedPipe, "the wrapped body is closed")
	require.NoError(t, body.Close())
}

func TestStreamIdleBodyIgnoresTimeOutsideRead(t *testing.T) {
	reader, writer := io.Pipe()
	body := newStreamIdleBody(reader, 50*time.Millisecond)
	defer func() { _ = body.Close() }()
	go func() {
		_, _ = writer.Write([]byte("a"))
		_, _ = writer.Write([]byte("b"))
		_ = writer.Close()
	}()

	buf := make([]byte, 1)
	_, err := body.Read(buf)
	require.NoError(t, err)
	// The relay is busy elsewhere (a slow client) for several idle periods
	// while the upstream has the next byte ready.
	time.Sleep(200 * time.Millisecond)
	n, err := body.Read(buf)
	require.NoError(t, err)
	require.Equal(t, "b", string(buf[:n]))
	_, err = body.Read(buf)
	require.ErrorIs(t, err, io.EOF)
}

func TestStreamIdleBodyKeepsTricklingStreamAlive(t *testing.T) {
	reader, writer := io.Pipe()
	body := newStreamIdleBody(reader, 60*time.Millisecond)
	defer func() { _ = body.Close() }()
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(20 * time.Millisecond)
			_, _ = writer.Write([]byte{'x'})
		}
		_ = writer.Close()
	}()

	data, err := io.ReadAll(body)
	require.NoError(t, err, "each byte arrives well inside the limit")
	require.Equal(t, strings.Repeat("x", 10), string(data))
}

func TestStreamIdleBodyCloseStopsTimer(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	body := newStreamIdleBody(reader, 20*time.Millisecond)
	require.NoError(t, body.Close())
	time.Sleep(60 * time.Millisecond)

	_, err := body.Read(make([]byte, 1))
	require.ErrorIs(t, err, io.ErrClosedPipe)
	var idleErr *upstreamStreamIdleError
	require.False(t, errors.As(err, &idleErr), "a closed body never times out")
}

func TestStreamIdleBodyLeavesNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		reader, writer := io.Pipe()
		body := newStreamIdleBody(reader, 5*time.Millisecond)
		_, err := body.Read(make([]byte, 1))
		var idleErr *upstreamStreamIdleError
		require.ErrorAs(t, err, &idleErr)
		require.NoError(t, body.Close())
		_ = writer.Close()
	}
	require.Eventually(t, func() bool { return runtime.NumGoroutine() <= before+2 }, time.Second, 10*time.Millisecond)
}

func TestCodeBuddyStreamIdleTimeoutFollowsGatewaySetting(t *testing.T) {
	require.Equal(t, defaultCodeBuddyStreamIdleTimeout, (&OpenAIGatewayService{cfg: &config.Config{}}).codeBuddyStreamIdleTimeout())
	configured := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamDataIntervalTimeout: 45}}}
	require.Equal(t, 45*time.Second, configured.codeBuddyStreamIdleTimeout())

	code, message := classifyOpenAIUpstreamStreamReadError(&upstreamStreamIdleError{idle: time.Second})
	require.Equal(t, OpenAIUpstreamStreamTimeoutCode, code)
	require.Equal(t, openAIUpstreamStreamTimeoutMessage, message)
}

// newCodeBuddyIdleContext is newCodeBuddyTestContext without gin.SetMode,
// which writes a plain global and races in parallel tests; go test already
// runs gin in test mode.
func newCodeBuddyIdleContext(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, rec
}

// codeBuddyIdleRepo records the cooldowns a stalled CodeBuddy stream writes.
type codeBuddyIdleRepo struct {
	AccountRepository
	mu        sync.Mutex
	cooldowns []codeBuddyIdleCooldown
}

type codeBuddyIdleCooldown struct {
	accountID int64
	until     time.Time
	reason    string
}

func (r *codeBuddyIdleRepo) SetTempUnschedulable(_ context.Context, id int64, until time.Time, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldowns = append(r.cooldowns, codeBuddyIdleCooldown{accountID: id, until: until, reason: reason})
	return nil
}

func (r *codeBuddyIdleRepo) UpdateExtra(context.Context, int64, map[string]any) error { return nil }

func (r *codeBuddyIdleRepo) recorded() []codeBuddyIdleCooldown {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]codeBuddyIdleCooldown(nil), r.cooldowns...)
}

func (r *codeBuddyIdleRepo) requireCooledOnce(t *testing.T) {
	t.Helper()
	cooldowns := r.recorded()
	require.Len(t, cooldowns, 1)
	require.Equal(t, int64(77), cooldowns[0].accountID)
	require.Equal(t, "codebuddy stream idle timeout", cooldowns[0].reason)
	require.WithinDuration(t, time.Now().Add(codeBuddyStreamIdleCooldown), cooldowns[0].until, 30*time.Second)
}

// codeBuddyIdleTestService returns a gateway with a 1s stream idle limit
// whose upstream answers 200 with an SSE body fed by feed. The body stays open
// after feed returns, so a feed that never closes it is an upstream that
// stalls for good.
func codeBuddyIdleTestService(t *testing.T, feed func(w *io.PipeWriter)) (*OpenAIGatewayService, *codeBuddyIdleRepo, *httpUpstreamRecorder) {
	t.Helper()
	reader, writer := io.Pipe()
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		feed(writer)
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		<-fed
	})
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamDataIntervalTimeout = 1
	repo := &codeBuddyIdleRepo{}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-idle"}},
		Body:       reader,
	}}
	return &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, accountRepo: repo}, repo, upstream
}

const (
	codeBuddyIdleContentChunk = "data: {\"id\":\"c1\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hel\"}}]}\n\n"
	codeBuddyIdleStreamTail   = "data: {\"id\":\"c1\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"model\":\"glm-5.2\",\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9}}\n\n" +
		"data: [DONE]\n\n"
)

type codeBuddyIdleEndpoint struct {
	name     string
	path     string
	body     string
	forward  func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error)
	terminal string // what a stream that finished cleanly ends with
	// stalled asserts the client-side ending of a stream that stalled after output.
	stalled func(t *testing.T, out string)
}

var codeBuddyIdleEndpoints = []codeBuddyIdleEndpoint{
	{
		name: "chat",
		path: "/v1/chat/completions",
		body: `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
			return svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
		},
		terminal: "data: [DONE]",
		stalled: func(t *testing.T, out string) {
			// The handler appends the error frame from the typed error.
			require.Contains(t, out, `"content":"hel"`)
		},
	},
	{
		name: "messages",
		path: "/v1/messages",
		body: `{"model":"glm-5.2","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
			return svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
		},
		terminal: "event: message_stop",
		stalled: func(t *testing.T, out string) {
			require.Contains(t, out, `"text":"hel"`)
			require.True(t, strings.HasSuffix(out, "event: error\ndata: {\"error\":{\"message\":\"Upstream response stream timed out\",\"type\":\"stream_timeout\"},\"type\":\"error\"}\n\n"), out)
		},
	},
	{
		name: "responses",
		path: "/v1/responses",
		body: `{"model":"glm-5.2","stream":true,"input":"hi"}`,
		forward: func(svc *OpenAIGatewayService, c *gin.Context, account *Account, body []byte) (*OpenAIForwardResult, error) {
			return svc.Forward(context.Background(), c, account, body)
		},
		terminal: "response.completed",
		stalled: func(t *testing.T, out string) {
			require.Contains(t, out, `"delta":"hel"`)
			events := strings.Split(strings.TrimSpace(out), "\n\n")
			_, data, ok := strings.Cut(events[len(events)-1], "data: ")
			require.True(t, ok, out)
			last := gjson.Parse(data)
			require.Equal(t, "response.failed", last.Get("type").String(), out)
			require.Equal(t, "failed", last.Get("response.status").String())
			require.Equal(t, OpenAIUpstreamStreamTimeoutCode, last.Get("response.error.code").String())
			require.NotEmpty(t, last.Get("response.id").String())
		},
	},
}

func TestCodeBuddyStreamIdleFailsOverBeforeOutput(t *testing.T) {
	for _, ep := range codeBuddyIdleEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			// Headers arrived and the upstream never sends a byte.
			svc, repo, upstream := codeBuddyIdleTestService(t, func(*io.PipeWriter) {})
			c, rec := newCodeBuddyIdleContext(ep.path, []byte(ep.body))

			result, err := ep.forward(svc, c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), []byte(ep.body))

			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Nil(t, result)
			require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
			require.False(t, failoverErr.RetryableOnSameAccount, "the stalled account is cooling down")
			require.Equal(t, OpenAIUpstreamStreamTimeoutCode, gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
			require.Equal(t, "rid-idle", failoverErr.ResponseHeaders.Get("x-request-id"))
			require.Empty(t, rec.Body.String(), "nothing may reach the client before a failover")
			require.False(t, c.Writer.Written())
			repo.requireCooledOnce(t)
			require.Len(t, upstream.requests, 1)
		})
	}
}

func TestCodeBuddyStreamIdleEndsStartedStreamWithTypedError(t *testing.T) {
	for _, ep := range codeBuddyIdleEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			svc, repo, _ := codeBuddyIdleTestService(t, func(w *io.PipeWriter) {
				_, _ = io.WriteString(w, codeBuddyIdleContentChunk)
			})
			c, rec := newCodeBuddyIdleContext(ep.path, []byte(ep.body))

			result, err := ep.forward(svc, c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), []byte(ep.body))

			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr), "output already reached the client")
			code, message, ok := OpenAIUpstreamStreamReadErrorDetails(err)
			require.True(t, ok, "typed upstream stream error: %v", err)
			require.Equal(t, OpenAIUpstreamStreamTimeoutCode, code)
			require.Equal(t, openAIUpstreamStreamTimeoutMessage, message)
			require.NotErrorIs(t, err, context.Canceled)
			require.NotNil(t, result)
			require.True(t, result.UsageIncomplete)
			require.True(t, result.Stream)

			out := rec.Body.String()
			require.NotContains(t, out, ep.terminal, "a stall must not look like a clean end")
			ep.stalled(t, out)
			if ep.name != "chat" {
				require.True(t, IsResponseCommitted(c), "the bridge wrote the terminal event")
			}
			repo.requireCooledOnce(t)
		})
	}
}

func TestCodeBuddyStreamIdleLeavesHealthyStreamsAlone(t *testing.T) {
	for _, ep := range codeBuddyIdleEndpoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Parallel()
			// Gaps shorter than the limit add up to more than it.
			svc, repo, _ := codeBuddyIdleTestService(t, func(w *io.PipeWriter) {
				for i := 0; i < 3; i++ {
					if _, err := io.WriteString(w, codeBuddyIdleContentChunk); err != nil {
						return
					}
					time.Sleep(450 * time.Millisecond)
				}
				_, _ = io.WriteString(w, codeBuddyIdleStreamTail)
				_ = w.Close()
			})
			c, rec := newCodeBuddyIdleContext(ep.path, []byte(ep.body))

			result, err := ep.forward(svc, c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), []byte(ep.body))

			require.NoError(t, err)
			require.NotNil(t, result)
			require.False(t, result.UsageIncomplete)
			require.Equal(t, 7, result.Usage.InputTokens)
			require.Equal(t, 2, result.Usage.OutputTokens)
			require.Contains(t, rec.Body.String(), ep.terminal)
			require.Empty(t, repo.recorded())
		})
	}
}

func TestCodeBuddyRawChatStreamFinishesWhenUpstreamLingersAfterDone(t *testing.T) {
	t.Parallel()
	// Everything arrived, but the upstream keeps the stream open.
	svc, repo, _ := codeBuddyIdleTestService(t, func(w *io.PipeWriter) {
		_, _ = io.WriteString(w, codeBuddyIdleContentChunk+codeBuddyIdleStreamTail)
	})
	body := []byte(codeBuddyIdleEndpoints[0].body)
	c, rec := newCodeBuddyIdleContext("/v1/chat/completions", body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.UsageIncomplete)
	require.Equal(t, 2, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Empty(t, repo.recorded(), "a finished generation is not a stall")
}

func TestCodeBuddyNonStreamIdleFailsOver(t *testing.T) {
	t.Parallel()
	svc, repo, _ := codeBuddyIdleTestService(t, func(w *io.PipeWriter) {
		_, _ = io.WriteString(w, codeBuddyIdleContentChunk)
	})
	body := []byte(`{"model":"glm-5.2","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyIdleContext("/v1/chat/completions", body)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, codeBuddyTestAccount(codebuddy.ProfileCNCLI), body, "", "")

	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Nil(t, result)
	require.Equal(t, OpenAIUpstreamStreamTimeoutCode, gjson.GetBytes(failoverErr.ResponseBody, "error.code").String())
	require.Empty(t, rec.Body.String())
	repo.requireCooledOnce(t)
}

func TestRawChatStreamIdleLimitIsCodeBuddyOnly(t *testing.T) {
	t.Parallel()
	reader, writer := io.Pipe()
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		// Longer than the configured limit before the first byte.
		time.Sleep(1500 * time.Millisecond)
		_, _ = io.WriteString(writer, codeBuddyIdleContentChunk+codeBuddyIdleStreamTail)
		_ = writer.Close()
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		<-fed
	})
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamDataIntervalTimeout = 1
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       reader,
	}}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}
	body := []byte(`{"model":"gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	c, rec := newCodeBuddyIdleContext("/v1/chat/completions", body)

	result, err := svc.forwardAsRawChatCompletions(context.Background(), c, rawChatCompletionsTestAccount(), body, "")

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
}
