//go:build unit

package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type antigravityLifecycleTestUpstream struct {
	HTTPUpstream
	do func(*http.Request) (*http.Response, error)
}

func (u *antigravityLifecycleTestUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.do(req)
}

func antigravityLifecycleTestRetryContext(parent, upstream context.Context) context.Context {
	return context.WithValue(parent, antigravityStreamLifecycleKey{}, antigravityLifecycle(upstream))
}

func antigravityLifecycleTestWaitDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("stream lifecycle did not cancel within its bounded lifetime")
	}
}

func TestAntigravityStreamLifecycleOutboundPreservesLateContextValues(t *testing.T) {
	type markerKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(t.Context(), markerKey{}, "request-value"))
	defer cancelParent()
	upstream, release := newAntigravityStreamContext(parent, time.Second, 5*time.Second)
	defer release()
	retryCtx := antigravityLifecycleTestRetryContext(parent, upstream)
	// These values are deliberately added after the lifecycle was created.
	retryCtx = WithHTTPUpstreamProfile(retryCtx, HTTPUpstreamProfileLongStream)
	retryCtx = antigravity.WithDeviceIdentity(retryCtx, "linux", "arm64", "LINUX_ARM64")
	outbound := antigravityOutboundContext(retryCtx)
	req, err := antigravity.NewAPIRequestWithURL(outbound, "http://127.0.0.1", "streamGenerateContent", "fake-token", []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, HTTPUpstreamProfileLongStream, HTTPUpstreamProfileFromContext(req.Context()))
	require.Equal(t, "LINUX_ARM64", antigravity.DevicePlatformFromContext(req.Context()))
	require.Equal(t, "request-value", req.Context().Value(markerKey{}))
	require.Contains(t, req.Header.Get("User-Agent"), "linux/arm64")
	require.Contains(t, req.Header.Get("Client-Metadata"), `"platform":"LINUX"`)
	deadline, ok := req.Context().Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(5*time.Second), deadline, time.Second)

	cancelParent()
	require.ErrorIs(t, retryCtx.Err(), context.Canceled)
	require.ErrorIs(t, antigravityDownstreamError(retryCtx), context.Canceled)
	require.NoError(t, req.Context().Err(), "only the in-flight request may survive downstream cancellation")
	release()
	antigravityLifecycleTestWaitDone(t, req.Context())
	require.ErrorIs(t, req.Context().Err(), context.Canceled)
}

func TestAntigravityStreamLifecycleStopsNewAttemptsImmediately(t *testing.T) {
	for _, mode := range []string{"cancel_before_loop", "cancel_after_request_build", "cancel_on_first_failure"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			upstreamCtx, release := newAntigravityStreamContext(parent, time.Second, 5*time.Second)
			defer release()
			retryCtx := antigravityLifecycleTestRetryContext(parent, upstreamCtx)
			var calls int
			upstream := &antigravityLifecycleTestUpstream{do: func(req *http.Request) (*http.Response, error) {
				calls++
				require.NoError(t, req.Context().Err())
				cancelParent()
				return nil, errors.New("local transport failure")
			}}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(retryCtx)
			params := antigravityRetryLoopParams{
				ctx: retryCtx, c: c, account: newAntigravityCompatAccount(AccountTypeOAuth),
				httpUpstream: upstream, accessToken: "fake-token", action: "streamGenerateContent", body: []byte(`{}`),
			}
			started := time.Now()
			var err error
			if mode == "cancel_after_request_build" {
				req, buildErr := antigravity.NewAPIRequestWithURL(antigravityOutboundContext(retryCtx), "http://127.0.0.1", params.action, params.accessToken, params.body)
				require.NoError(t, buildErr)
				cancelParent()
				_, err = doAntigravityRequest(params, req)
			} else {
				if mode == "cancel_before_loop" {
					cancelParent()
				}
				_, err = (&AntigravityGatewayService{}).antigravityRetryLoop(params)
			}
			require.ErrorIs(t, err, context.Canceled)
			wantCalls := 0
			if mode == "cancel_on_first_failure" {
				wantCalls = 1
			}
			require.Equal(t, wantCalls, calls, "cancelled downstream must not initiate another upstream attempt")
			require.Less(t, time.Since(started), 750*time.Millisecond, "must not wait for a retry backoff or the drain deadline")
			require.NoError(t, upstreamCtx.Err(), "retry cancellation must not consume the in-flight drain window")
		})
	}
}

func TestAntigravityStreamLifecycleShortDrainAndMaximumDuration(t *testing.T) {
	t.Run("drain_timeout", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()
		ctx, release := newAntigravityStreamContext(parent, 20*time.Millisecond, 5*time.Second)
		defer release()
		cancelParent()
		antigravityLifecycleTestWaitDone(t, ctx)
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	})
	t.Run("maximum_duration_without_disconnect", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(t.Context())
		defer cancelParent()
		ctx, release := newAntigravityStreamContext(parent, time.Second, 20*time.Millisecond)
		defer release()
		antigravityLifecycleTestWaitDone(t, ctx)
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
		require.NoError(t, parent.Err())
		require.ErrorIs(t, antigravityDownstreamError(antigravityLifecycleTestRetryContext(parent, ctx)), context.DeadlineExceeded)
	})
	t.Run("parent_cancelled_before_registration", func(t *testing.T) {
		parent, cancelParent := context.WithCancel(t.Context())
		cancelParent()
		ctx, release := newAntigravityStreamContext(parent, 20*time.Millisecond, time.Hour)
		defer release()
		antigravityLifecycleTestWaitDone(t, ctx)
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	})
}

func TestAntigravityStreamLifecycleServiceContextLimits(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "defaults"
		cfg := &config.Config{}
		wantDrain, wantMaximum := defaultAntigravityDrainTimeout, defaultAntigravityMaxDuration
		if configured {
			name = "configured"
			cfg.Gateway.AntigravityStreamDrainTimeout = 2
			cfg.Gateway.AntigravityStreamMaxDuration = 4
			wantDrain, wantMaximum = 2*time.Second, 4*time.Second
		}
		t.Run(name, func(t *testing.T) {
			parent, cancelParent := context.WithCancel(t.Context())
			defer cancelParent()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/audit", nil).WithContext(parent)
			svc := newAntigravityTestService(cfg)
			ctx, release := svc.upstreamStreamContext(parent, c)
			defer release()
			lifecycle := antigravityLifecycle(ctx)
			require.NotNil(t, lifecycle)
			require.Same(t, lifecycle, antigravityLifecycle(c.Request.Context()))
			require.Equal(t, wantDrain, lifecycle.drainAfter)
			deadline, ok := antigravityOutboundContext(ctx).Deadline()
			require.True(t, ok)
			require.WithinDuration(t, time.Now().Add(wantMaximum), deadline, time.Second)
			cancelParent()
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			require.NoError(t, antigravityOutboundContext(ctx).Err())
		})
	}
}

// This parent exposes AfterFunc registrations so cleanup is checked without
// flaky process-wide goroutine-count assertions or waiting for an hour timer.
type antigravityLifecycleTrackedParent struct {
	mu        sync.Mutex
	done      chan struct{}
	next      int
	callbacks map[int]func()
}

func (*antigravityLifecycleTrackedParent) Deadline() (time.Time, bool) { return time.Time{}, false }
func (p *antigravityLifecycleTrackedParent) Done() <-chan struct{}     { return p.done }
func (*antigravityLifecycleTrackedParent) Err() error                  { return nil }
func (*antigravityLifecycleTrackedParent) Value(any) any               { return nil }
func (p *antigravityLifecycleTrackedParent) AfterFunc(f func()) func() bool {
	p.mu.Lock()
	id := p.next
	p.next++
	p.callbacks[id] = f
	p.mu.Unlock()
	return func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		_, exists := p.callbacks[id]
		delete(p.callbacks, id)
		return exists
	}
}

func TestAntigravityStreamLifecycleReleaseUnregistersAndRejectsLateDrain(t *testing.T) {
	parent := &antigravityLifecycleTrackedParent{done: make(chan struct{}), callbacks: make(map[int]func())}
	for i := 0; i < 128; i++ {
		ctx, release := newAntigravityStreamContext(parent, time.Hour, time.Hour)
		parent.mu.Lock()
		registered := len(parent.callbacks)
		parent.mu.Unlock()
		require.Equal(t, 1, registered)
		release()
		release()
		lifecycle := antigravityLifecycle(ctx)
		lifecycle.startDrain()
		lifecycle.mu.Lock()
		finished, timer := lifecycle.finished, lifecycle.drainTimer
		lifecycle.mu.Unlock()
		require.True(t, finished)
		require.Nil(t, timer, "late disconnect must not register an hour-long drain callback after release")
		require.ErrorIs(t, ctx.Err(), context.Canceled)
		parent.mu.Lock()
		remaining := len(parent.callbacks)
		parent.mu.Unlock()
		require.Zero(t, remaining, "release must unregister the parent cancellation callback")
	}
}

func TestAntigravityStreamLifecycleCancellationReleaseRace(t *testing.T) {
	for i := 0; i < 128; i++ {
		parent, cancelParent := context.WithCancel(t.Context())
		if i%2 == 0 {
			cancelParent()
		}
		ctx, release := newAntigravityStreamContext(parent, time.Hour, time.Hour)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); cancelParent() }()
		go func() { defer wg.Done(); release() }()
		wg.Wait()
		lifecycle := antigravityLifecycle(ctx)
		lifecycle.startDrain()
		lifecycle.mu.Lock()
		finished, timer := lifecycle.finished, lifecycle.drainTimer
		lifecycle.mu.Unlock()
		require.True(t, finished)
		if timer != nil {
			require.False(t, timer.Stop(), "release must already have stopped the drain timer")
		}
		require.ErrorIs(t, ctx.Err(), context.Canceled)
	}
}

type antigravityLifecycleTrackedBody struct {
	io.ReadCloser
	once   sync.Once
	closed chan struct{}
}

func (b *antigravityLifecycleTrackedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { close(b.closed) })
	return err
}

func TestAntigravityStreamLifecycleRealHTTPDisconnectAndBounds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	protocols := []struct {
		name    string
		forward func(*AntigravityGatewayService, *gin.Context, *Account) (*ForwardResult, error)
	}{
		{"messages", func(s *AntigravityGatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return s.Forward(c.Request.Context(), c, a, []byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), false)
		}},
		{"gemini", func(s *AntigravityGatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return s.ForwardGemini(c.Request.Context(), c, a, "gemini-3.1-pro-high", "streamGenerateContent", true, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), false)
		}},
		{"chat", func(s *AntigravityGatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return s.ForwardAsChatCompletions(c.Request.Context(), c, a, []byte(`{"model":"gemini-3.1-pro-high","stream":true,"messages":[{"role":"user","content":"hello"}]}`), nil)
		}},
		{"responses", func(s *AntigravityGatewayService, c *gin.Context, a *Account) (*ForwardResult, error) {
			return s.ForwardAsResponses(c.Request.Context(), c, a, []byte(`{"model":"gemini-3.1-pro-high","stream":true,"input":"hello"}`), nil)
		}},
	}
	for _, protocol := range protocols {
		for _, mode := range []string{"late_usage_after_real_disconnect", "drain_timeout", "maximum_duration"} {
			t.Run(protocol.name+"/"+mode, func(t *testing.T) {
				releaseTail := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseTail) }) }
				upstreamCancelled := make(chan struct{})
				local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"lifecycle-local\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":1}}}\n\n")
					_ = http.NewResponseController(w).Flush()
					select {
					case <-r.Context().Done():
						close(upstreamCancelled)
					case <-releaseTail:
						_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"lifecycle-local\",\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":15}}}\n\n")
					}
				}))
				t.Cleanup(local.Close)
				t.Cleanup(local.CloseClientConnections)
				t.Cleanup(release)
				target, err := url.Parse(local.URL)
				require.NoError(t, err)
				client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
				t.Cleanup(client.CloseIdleConnections)
				bodyClosed := make(chan struct{})
				requestContexts := make(chan context.Context, 1)
				var calls atomic.Int32
				upstream := &antigravityLifecycleTestUpstream{do: func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					requestContexts <- req.Context()
					// Rewrite before touching the network. No request can reach Google,
					// and the real HTTP transport receives the unmodified request context.
					localReq := req.Clone(req.Context())
					localReq.URL.Scheme, localReq.URL.Host = target.Scheme, target.Host
					localReq.Host = target.Host
					resp, requestErr := client.Do(localReq)
					if requestErr == nil {
						resp.Body = &antigravityLifecycleTrackedBody{ReadCloser: resp.Body, closed: bodyClosed}
					}
					return resp, requestErr
				}}
				cfg := config.GatewayConfig{MaxLineSize: defaultMaxLineSize, AntigravityStreamDrainTimeout: 1, AntigravityStreamMaxDuration: 4}
				if mode == "maximum_duration" {
					cfg.AntigravityStreamDrainTimeout, cfg.AntigravityStreamMaxDuration = 4, 1
				}
				svc := newAntigravityCompatService(cfg, upstream)
				type outcome struct {
					result        *ForwardResult
					err           error
					downstreamErr error
				}
				outcomes := make(chan outcome, 1)
				downstreamContexts := make(chan context.Context, 1)
				engine := gin.New()
				engine.POST("/audit", func(c *gin.Context) {
					originalContext := c.Request.Context()
					downstreamContexts <- originalContext
					result, forwardErr := protocol.forward(svc, c, newAntigravityCompatAccount(AccountTypeOAuth))
					outcomes <- outcome{result: result, err: forwardErr, downstreamErr: originalContext.Err()}
				})
				downstream := httptest.NewServer(engine)
				t.Cleanup(downstream.Close)
				t.Cleanup(downstream.CloseClientConnections)
				downstreamClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
				t.Cleanup(downstreamClient.CloseIdleConnections)
				response, err := downstreamClient.Post(downstream.URL+"/audit", "application/json", strings.NewReader(`{}`))
				require.NoError(t, err)
				t.Cleanup(func() { _ = response.Body.Close() })
				_, err = bufio.NewReader(response.Body).ReadString('\n')
				require.NoError(t, err)
				originalContext := <-downstreamContexts
				outboundContext := <-requestContexts
				if mode != "maximum_duration" {
					require.NoError(t, response.Body.Close())
					antigravityLifecycleTestWaitDone(t, originalContext)
					require.NoError(t, outboundContext.Err(), "real downstream disconnect must not cancel the existing HTTP stream immediately")
				}
				if mode == "late_usage_after_real_disconnect" {
					release()
				}
				var got outcome
				select {
				case got = <-outcomes:
				case <-time.After(4 * time.Second):
					t.Fatal("Forward did not exit within its stream lifecycle limit")
				}
				require.NotNil(t, got.result, "already observed usage must survive bounded stream termination: %v", got.err)
				require.Equal(t, 8, got.result.Usage.InputTokens)
				if mode == "late_usage_after_real_disconnect" {
					require.NoError(t, got.err)
					require.Equal(t, 15, got.result.Usage.OutputTokens)
					require.False(t, got.result.UsageIncomplete, "fully drained late usage remains complete despite downstream cancellation")
					require.True(t, got.result.ClientDisconnect)
				} else {
					require.Equal(t, 1, got.result.Usage.OutputTokens)
					require.True(t, got.result.UsageIncomplete, "timeout must not label the old usage snapshot as final")
					select {
					case <-upstreamCancelled:
					case <-time.After(time.Second):
						t.Fatal("bounded termination did not cancel the actual HTTP response")
					}
				}
				if mode == "maximum_duration" {
					require.NoError(t, got.downstreamErr, "maximum duration must work even when the downstream is still connected")
				} else {
					require.ErrorIs(t, got.downstreamErr, context.Canceled)
				}
				select {
				case <-bodyClosed:
				case <-time.After(time.Second):
					t.Fatal("Forward returned without closing its upstream Body")
				}
				require.Equal(t, int32(1), calls.Load(), "disconnect or lifecycle expiry must not launch another upstream request")
			})
		}
	}
}

func TestAntigravityStreamLifecycleNonStreamingDrain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, protocol := range []string{"gemini", "messages", "chat", "responses"} {
		for _, mode := range []string{"late_usage_after_real_disconnect", "drain_timeout", "maximum_duration"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				firstFlushed := make(chan struct{})
				releaseTail := make(chan struct{})
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(releaseTail) }) }
				upstreamCancelled := make(chan struct{})
				local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"nonstream-lifecycle-local\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":1}}}\n\n")
					_ = http.NewResponseController(w).Flush()
					close(firstFlushed)
					select {
					case <-r.Context().Done():
						close(upstreamCancelled)
					case <-releaseTail:
						_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"nonstream-lifecycle-local\",\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":8,\"candidatesTokenCount\":15}}}\n\n")
					}
				}))
				t.Cleanup(local.Close)
				t.Cleanup(local.CloseClientConnections)
				t.Cleanup(release)
				target, err := url.Parse(local.URL)
				require.NoError(t, err)
				client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
				t.Cleanup(client.CloseIdleConnections)
				bodyClosed := make(chan struct{})
				outboundContexts := make(chan context.Context, 1)
				var calls atomic.Int32
				upstream := &antigravityLifecycleTestUpstream{do: func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					outboundContexts <- req.Context()
					localReq := req.Clone(req.Context())
					localReq.URL.Scheme, localReq.URL.Host = target.Scheme, target.Host
					localReq.Host = target.Host
					resp, requestErr := client.Do(localReq)
					if requestErr == nil {
						resp.Body = &antigravityLifecycleTrackedBody{ReadCloser: resp.Body, closed: bodyClosed}
					}
					return resp, requestErr
				}}
				cfg := config.GatewayConfig{MaxLineSize: defaultMaxLineSize, AntigravityStreamDrainTimeout: 1, AntigravityStreamMaxDuration: 4}
				if mode == "maximum_duration" {
					cfg.AntigravityStreamDrainTimeout, cfg.AntigravityStreamMaxDuration = 4, 1
				}
				svc := newAntigravityCompatService(cfg, upstream)
				type outcome struct {
					result        *ForwardResult
					err           error
					downstreamErr error
				}
				outcomes := make(chan outcome, 1)
				downstreamContexts := make(chan context.Context, 1)
				engine := gin.New()
				engine.POST("/audit-nonstream", func(c *gin.Context) {
					// Real gateway handlers parse the inbound body before forwarding.
					// Reaching EOF also lets net/http observe a later TCP disconnect
					// while this non-streaming handler has not written a response yet.
					_, _ = io.Copy(io.Discard, c.Request.Body)
					originalContext := c.Request.Context()
					downstreamContexts <- originalContext
					account := newAntigravityCompatAccount(AccountTypeOAuth)
					var result *ForwardResult
					var forwardErr error
					switch protocol {
					case "gemini":
						result, forwardErr = svc.ForwardGemini(c.Request.Context(), c, account, "gemini-3.1-pro-high", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`), false)
					case "messages":
						result, forwardErr = svc.Forward(c.Request.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","stream":false,"max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`), false)
					case "chat":
						result, forwardErr = svc.ForwardAsChatCompletions(c.Request.Context(), c, account, []byte(`{"model":"gemini-3.1-pro-high","stream":false,"messages":[{"role":"user","content":"hello"}]}`), nil)
					default:
						result, forwardErr = svc.ForwardAsResponses(c.Request.Context(), c, account, []byte(`{"model":"gemini-3.1-pro-high","stream":false,"input":"hello"}`), nil)
					}
					outcomes <- outcome{result: result, err: forwardErr, downstreamErr: originalContext.Err()}
				})
				downstream := httptest.NewServer(engine)
				t.Cleanup(downstream.Close)
				t.Cleanup(downstream.CloseClientConnections)
				downstreamClient := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
				t.Cleanup(downstreamClient.CloseIdleConnections)
				clientCtx, cancelClient := context.WithCancel(t.Context())
				t.Cleanup(cancelClient)
				req, err := http.NewRequestWithContext(clientCtx, http.MethodPost, downstream.URL+"/audit-nonstream", strings.NewReader(`{}`))
				require.NoError(t, err)
				clientResults := make(chan error, 1)
				go func() {
					resp, requestErr := downstreamClient.Do(req)
					if resp != nil {
						_, _ = io.Copy(io.Discard, resp.Body)
						_ = resp.Body.Close()
					}
					clientResults <- requestErr
				}()
				select {
				case <-firstFlushed:
				case <-time.After(3 * time.Second):
					t.Fatal("local upstream never flushed the initial non-streaming collector input")
				}
				originalContext := <-downstreamContexts
				outboundContext := <-outboundContexts
				if mode != "maximum_duration" {
					// There is intentionally no downstream response to read yet:
					// cancelling this real HTTP request closes the waiting client.
					cancelClient()
					antigravityLifecycleTestWaitDone(t, originalContext)
					require.NoError(t, outboundContext.Err())
				}
				if mode == "late_usage_after_real_disconnect" {
					select {
					case early := <-outcomes:
						t.Fatalf("collector exited on downstream cancellation before its drain grace: result=%+v err=%v", early.result, early.err)
					case <-time.After(20 * time.Millisecond):
					}
					require.NoError(t, outboundContext.Err())
					release()
				}
				var got outcome
				select {
				case got = <-outcomes:
				case <-time.After(4 * time.Second):
					t.Fatal("non-streaming collector did not exit within its lifecycle bound")
				}
				require.NotNil(t, got.result, "observed collector usage must survive bounded termination: %v", got.err)
				require.False(t, got.result.Stream)
				require.Equal(t, 8, got.result.Usage.InputTokens)
				if mode == "late_usage_after_real_disconnect" {
					require.NoError(t, got.err)
					require.Equal(t, 15, got.result.Usage.OutputTokens)
					require.False(t, got.result.UsageIncomplete)
					require.True(t, got.result.ClientDisconnect)
				} else {
					require.Equal(t, 1, got.result.Usage.OutputTokens)
					require.True(t, got.result.UsageIncomplete)
					select {
					case <-upstreamCancelled:
					case <-time.After(time.Second):
						t.Fatal("collector timeout did not cancel the actual upstream request")
					}
				}
				if mode == "maximum_duration" {
					require.NoError(t, got.downstreamErr)
				} else {
					require.ErrorIs(t, got.downstreamErr, context.Canceled)
				}
				select {
				case <-bodyClosed:
				case <-time.After(time.Second):
					t.Fatal("non-streaming Forward returned without closing the upstream Body")
				}
				select {
				case clientErr := <-clientResults:
					if mode != "maximum_duration" {
						require.ErrorIs(t, clientErr, context.Canceled)
					}
				case <-time.After(time.Second):
					t.Fatal("downstream HTTP client did not exit")
				}
				require.Equal(t, int32(1), calls.Load())
			})
		}
	}
}
