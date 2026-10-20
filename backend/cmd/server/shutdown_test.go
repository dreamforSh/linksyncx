package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/shutdownctx"
	"github.com/Wei-Shaw/sub2api/internal/pkg/sysutil"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
)

type drainOutcome struct {
	drained   bool
	remaining int
	err       error
}

func startDrainTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	installHTTPDrain(server.Config)
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// startH2CDrainTestServer 复刻 ProvideHTTPServer 的 h2c 配置（http2.ConfigureServer +
// Protocols 打开 UnencryptedHTTP2），再像 main 一样在其后安装排空跟踪。
func startH2CDrainTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	require.NoError(t, http2.ConfigureServer(server.Config, &http2.Server{}))
	serverProtocols := new(http.Protocols)
	serverProtocols.SetHTTP1(true)
	serverProtocols.SetUnencryptedHTTP2(true)
	server.Config.Protocols = serverProtocols
	installHTTPDrain(server.Config)
	server.Start()
	t.Cleanup(server.Close)

	clientProtocols := new(http.Protocols)
	clientProtocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: clientProtocols}
	t.Cleanup(transport.CloseIdleConnections)
	return server, &http.Client{Transport: transport}
}

// startDrainTestServerCountingConns 与 startDrainTestServer 相同，另返回 http.Server 已登记
// （ConnState=StateNew）的连接数：测试据此确认原始 TCP 连接确实进入了 Serve 的跟踪，
// 而不是还停在 Accept 之前。
func startDrainTestServerCountingConns(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var accepted atomic.Int32
	server := httptest.NewUnstartedServer(handler)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			accepted.Add(1)
		}
	}
	installHTTPDrain(server.Config)
	server.Start()
	t.Cleanup(server.Close)
	return server, &accepted
}

func requireConnClosedByServer(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := conn.Read(make([]byte, 64))
	require.Zero(t, n)
	require.Error(t, err)
	var netErr net.Error
	require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "the connection must be closed, not left hanging: %v", err)
}

func drainOf(t *testing.T, server *http.Server) *httpDrain {
	t.Helper()
	drain, ok := server.Handler.(*httpDrain)
	require.True(t, ok, "drain tracking must be installed")
	return drain
}

func waitForDrainSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream state")
	}
}

func drainAsync(server *http.Server, grace, forced time.Duration) <-chan drainOutcome {
	result := make(chan drainOutcome, 1)
	go func() {
		drained, remaining, err := drainHTTPServer(server, grace, forced)
		result <- drainOutcome{drained: drained, remaining: remaining, err: err}
	}()
	return result
}

func receiveDrainOutcome(t *testing.T, result <-chan drainOutcome) drainOutcome {
	t.Helper()
	select {
	case outcome := <-result:
		return outcome
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not return")
		return drainOutcome{}
	}
}

func requireDrainStillRunning(t *testing.T, result <-chan drainOutcome) {
	t.Helper()
	select {
	case outcome := <-result:
		t.Fatalf("drain returned while a tracked handler was still running: %+v", outcome)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHTTPDrainWaitsForActiveStreamToFinish(t *testing.T) {
	started, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush stream: %v", err)
			return
		}
		close(started)
		<-release
		_, _ = io.WriteString(w, "data: done\n\n")
		close(settled)
	})
	t.Cleanup(releaseStream)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)

	done := drainAsync(server.Config, time.Second, time.Second)
	requireDrainStillRunning(t, done)
	releaseStream()
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
	require.Zero(t, outcome.remaining)
	require.NoError(t, outcome.err)
	waitForDrainSignal(t, settled)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\ndata: done\n\n", string(body), "the in-flight stream must complete during the grace")
}

func TestHTTPDrainCancelsRequestContextsAfterGraceThenWaitsForHandlers(t *testing.T) {
	cancelled, settle, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishSettlement := sync.OnceFunc(func() { close(settle) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush stream: %v", err)
			return
		}
		<-r.Context().Done()
		close(cancelled)
		// 模拟取消后仍需完成的结算（例如提交部分 usage）。
		<-settle
		close(settled)
	})
	t.Cleanup(finishSettlement)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	done := drainAsync(server.Config, 20*time.Millisecond, time.Second)
	waitForDrainSignal(t, cancelled)
	requireDrainStillRunning(t, done)
	finishSettlement()
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
	require.ErrorIs(t, outcome.err, context.DeadlineExceeded)
	waitForDrainSignal(t, settled)
}

func TestHTTPDrainReportsStuckDetachedHandlerWithinForcedWait(t *testing.T) {
	release := make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush stream: %v", err)
			return
		}
		<-release // 模拟既不看请求取消、也不经 shutdownctx 响应硬停止的等待。
	})
	// 必须先于 server.Close 执行（t.Cleanup 后进先出），否则 Close 会等这个 handler。
	t.Cleanup(func() { close(release) })
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	started := time.Now()
	drained, remaining, err := drainHTTPServer(server.Config, 20*time.Millisecond, 20*time.Millisecond)
	require.False(t, drained)
	require.Equal(t, 1, remaining, "the stuck handler must be reported for the shutdown log")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second, "grace + forced wait must bound the drain")
}

func TestHTTPDrainTracksHijackedHandlerUntilItReturns(t *testing.T) {
	hijacked, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishSettlement := sync.OnceFunc(func() { close(release) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
		_ = rw.Flush()
		close(hijacked)
		<-r.Context().Done()
		close(cancelled)
		<-release
	})
	t.Cleanup(finishSettlement)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, hijacked)

	done := drainAsync(server.Config, 20*time.Millisecond, time.Second)
	waitForDrainSignal(t, cancelled)
	requireDrainStillRunning(t, done)
	finishSettlement()
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
	require.ErrorIs(t, outcome.err, context.DeadlineExceeded)
}

// 用户决策：WebSocket 会话在排空开始（T=0）即断开，只有 HTTP/SSE 享受宽限。
// 一个停在两轮之间的空闲 WS 会话不能把重启拖满整个宽限。
func TestHTTPDrainClosesIdleWebSocketAtDrainStart(t *testing.T) {
	accepted, handlerReturned := make(chan struct{}), make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		close(accepted)
		// 空闲会话：handler 阻塞在读下一轮请求上，且不看请求 context。
		_, _, _ = conn.Read(context.Background())
	})

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelDial()
	client, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	waitForDrainSignal(t, accepted)

	const grace = 5 * time.Second
	started := time.Now()
	outcome := receiveDrainOutcome(t, drainAsync(server.Config, grace, time.Second))
	require.True(t, outcome.drained)
	require.NoError(t, outcome.err, "the drain must finish inside the grace")
	require.Less(t, time.Since(started), time.Second, "an idle WebSocket session must not hold the drain for the grace")
	waitForDrainSignal(t, handlerReturned)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRead()
	_, _, err = client.Read(readCtx)
	require.Error(t, err, "the client connection must be closed by the drain")
}

func TestHTTPDrainClosesConnectionsHijackedAfterDrainStarted(t *testing.T) {
	entered, proceed, handlerReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerReturned)
		close(entered)
		<-proceed
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		// 连接在 hijack 时已被排空流程关闭，这次写入不应到达客户端。
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		_ = rw.Flush()
	})

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()
	_, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: drain.test\r\n\r\n")
	require.NoError(t, err)
	waitForDrainSignal(t, entered)

	drain := drainOf(t, server.Config)
	done := drainAsync(server.Config, time.Second, time.Second)
	require.Eventually(t, func() bool {
		drain.mu.Lock()
		defer drain.mu.Unlock()
		return drain.stopping
	}, 2*time.Second, time.Millisecond)
	close(proceed)
	waitForDrainSignal(t, handlerReturned)

	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err := conn.Read(make([]byte, 64))
	require.Zero(t, n, "a connection hijacked during the drain must not be served")
	require.Error(t, err)
	var netErr net.Error
	require.False(t, errors.As(err, &netErr) && netErr.Timeout(), "the connection must be closed, not left hanging: %v", err)
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
}

func TestHTTPDrainRejectsRequestsArrivingDuringDrain(t *testing.T) {
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("new request reached handler during drain")
	})}
	installHTTPDrain(server)
	drainOf(t, server).beginDrain()

	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "close", recorder.Header().Get("Connection"))
	require.NotContains(t, strings.ToLower(recorder.Body.String()), "sub2api", "the 503 body must not name the gateway")

	drained, remaining, err := drainHTTPServer(server, time.Second, time.Second)
	require.True(t, drained)
	require.Zero(t, remaining)
	require.NoError(t, err)
}

// 只建了 TCP、从未发完请求头的连接（浏览器预连接、四层探活、请求头发到一半）不能拖住
// 排空：net/http 的 Shutdown 要等 StateNew 连接存在满 5 秒才把它当空闲关掉（Issue 22682），
// 即使一个 handler 都没在跑，每次关停/后台重启也会白等约 5 秒，期间监听已关闭、什么都不服务。
func TestHTTPDrainDoesNotWaitForConnectionsThatNeverSentARequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		sent string
	}{
		{name: "silent"},
		{name: "partial header", sent: "GET / HTTP/1.1\r\nHost: drain.test\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, accepted := startDrainTestServerCountingConns(t, func(http.ResponseWriter, *http.Request) {
				t.Error("no request should reach the handler")
			})
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			require.NoError(t, err)
			defer func() { _ = conn.Close() }()
			if tc.sent != "" {
				_, err = io.WriteString(conn, tc.sent)
				require.NoError(t, err)
			}
			require.Eventually(t, func() bool { return accepted.Load() == 1 }, 2*time.Second, time.Millisecond)

			// 宽限远大于 5 秒：排空若仍按 StateNew 规则轮询，会在约 5 秒后才返回而不是到期。
			started := time.Now()
			drained, remaining, err := drainHTTPServer(server.Config, 10*time.Second, time.Second)
			elapsed := time.Since(started)
			require.True(t, drained)
			require.Zero(t, remaining)
			require.NoError(t, err)
			require.Less(t, elapsed, time.Second, "a connection that never sent a request must not hold the drain")
			requireConnClosedByServer(t, conn)
		})
	}
}

// 关掉“从未发过请求”的连接不能波及刚返回的 handler：d.done 在我们的 ServeHTTP 返回时
// 就关闭，net/http 随后才在 finishRequest 里刷出缓冲的响应尾部和 chunk 结束标记，所以排空
// 仍要等 Shutdown 确认这些连接收尾完毕，而不是一见 d.done 就 server.Close()。
func TestHTTPDrainDeliversResponseTailWhileClosingSilentConnections(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	server, accepted := startDrainTestServerCountingConns(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		_ = http.NewResponseController(w).Flush()
		close(started)
		<-release
		// 不 Flush：这部分只在 handler 返回后由 net/http 刷出。
		_, _ = io.WriteString(w, "data: tail\n\n")
	})
	t.Cleanup(releaseStream)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)

	silent, err := net.Dial("tcp", server.Listener.Addr().String())
	require.NoError(t, err)
	defer func() { _ = silent.Close() }()
	require.Eventually(t, func() bool { return accepted.Load() == 2 }, 2*time.Second, time.Millisecond)

	begin := time.Now()
	done := drainAsync(server.Config, 10*time.Second, time.Second)
	requireDrainStillRunning(t, done)
	releaseStream()
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
	require.NoError(t, outcome.err)
	require.Less(t, time.Since(begin), time.Second, "the silent connection must not hold the drain once the stream finished")
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "the response tail flushed after the handler returned must not be cut")
	require.Equal(t, "data: first\n\ndata: tail\n\n", string(body))
	requireConnClosedByServer(t, silent)
}

func TestHTTPDrainWithoutTrackerStillClosesServer(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	// 先完成一次请求，确保 Serve 已在跟踪监听（否则 Shutdown 时监听可能尚未登记）。
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	drained, remaining, err := drainHTTPServer(server.Config, time.Second, time.Second)
	require.False(t, drained)
	require.Zero(t, remaining)
	require.ErrorContains(t, err, "tracking is not installed")

	_, err = net.DialTimeout("tcp", server.Listener.Addr().String(), time.Second)
	require.Error(t, err, "the listener must be closed even without drain tracking")
}

func TestHTTPDrainOverH2CWaitsForInFlightStream(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	server, client := startH2CDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush stream: %v", err)
			return
		}
		close(started)
		<-release
		_, _ = io.WriteString(w, "data: done\n\n")
	})
	t.Cleanup(releaseStream)
	resp, err := client.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, 2, resp.ProtoMajor, "the request must actually use h2c")
	waitForDrainSignal(t, started)

	done := drainAsync(server.Config, 2*time.Second, time.Second)
	requireDrainStillRunning(t, done)
	releaseStream()
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained)
	require.NoError(t, outcome.err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\ndata: done\n\n", string(body), "the h2c stream must complete after GOAWAY")
}

// 强制阶段对请求的取消依赖“请求 context 派生自 forceCtx”（BaseContext）。这里绕开
// Shutdown/Close 直接触发 forceCtx：否则 server.Close 断开连接本身也会取消请求，
// 测不出派生关系是否成立。
func TestHTTPDrainForceCancelReachesRequestContexts(t *testing.T) {
	newHandler := func(started, cancelled chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_ = http.NewResponseController(w).Flush()
			close(started)
			<-r.Context().Done()
			close(cancelled)
		}
	}
	for _, tc := range []struct {
		name       string
		start      func(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client)
		protoMajor int
	}{
		{
			name: "http1",
			start: func(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client) {
				return startDrainTestServer(t, handler), http.DefaultClient
			},
			protoMajor: 1,
		},
		{name: "h2c", start: startH2CDrainTestServer, protoMajor: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, cancelled := make(chan struct{}), make(chan struct{})
			server, client := tc.start(t, newHandler(started, cancelled))
			resp, err := client.Get(server.URL)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, tc.protoMajor, resp.ProtoMajor)
			waitForDrainSignal(t, started)

			drainOf(t, server.Config).cancel()
			waitForDrainSignal(t, cancelled)
		})
	}
}

type drainHookKey string

func TestInstallHTTPDrainPreservesExistingServerHooks(t *testing.T) {
	var sawNewState atomic.Bool
	started, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(drainHookKey("base")) != "base-value" || r.Context().Value(drainHookKey("conn")) != "conn-value" {
			t.Error("request context lost values from the existing BaseContext/ConnContext hooks")
		}
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	server.Config.BaseContext = func(net.Listener) context.Context {
		return context.WithValue(context.Background(), drainHookKey("base"), "base-value")
	}
	server.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, drainHookKey("conn"), "conn-value")
	}
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			sawNewState.Store(true)
		}
	}
	installHTTPDrain(server.Config)
	server.Start()
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)
	require.True(t, sawNewState.Load(), "the existing ConnState hook must still run")

	// 已有 BaseContext 时请求 context 不再直接派生自 forceCtx，取消要靠 AfterFunc 转接。
	drainOf(t, server.Config).cancel()
	waitForDrainSignal(t, cancelled)
}

func TestRunShutdownRunsCleanupEvenWhenHandlersRemain(t *testing.T) {
	release := make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		<-release
	})
	t.Cleanup(func() { close(release) })
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var cleanups atomic.Int32
	report := runShutdownWith(server.Config, shutdownTimings{
		grace:   20 * time.Millisecond,
		forced:  20 * time.Millisecond,
		cleanup: time.Second,
	}, func() { cleanups.Add(1) })
	require.False(t, report.drained)
	require.Equal(t, 1, report.remaining)
	require.True(t, report.cleanedUp)
	require.Equal(t, int32(1), cleanups.Load(), "usage/billing/quota flushes in Cleanup must run even when a stream is still detached")
}

func TestCleanupWithinStopsWaitingAfterTimeout(t *testing.T) {
	require.True(t, cleanupWithin(func() {}, time.Second))
	release, finished := make(chan struct{}), make(chan struct{})
	defer close(release)
	require.False(t, cleanupWithin(func() { <-release; close(finished) }, 20*time.Millisecond))
	select {
	case <-finished:
		t.Fatal("blocked cleanup unexpectedly finished")
	default:
	}
}

// 管理后台“重启”（sysutil.RestartService → RequestRestart）必须走与 SIGTERM 相同的
// 排空 + Cleanup 路径，而不是 os.Exit 直接丢掉进行中的流与未落盘的计费。
func TestInAppRestartRequestDrivesGracefulShutdown(t *testing.T) {
	trigger := newShutdownTriggerWith(make(chan os.Signal, 1))
	t.Cleanup(trigger.stop)

	started, release := make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		_ = http.NewResponseController(w).Flush()
		close(started)
		<-release
		_, _ = io.WriteString(w, "data: done\n\n")
	})
	t.Cleanup(releaseStream)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)

	var cleanups atomic.Int32
	reasons := make(chan string, 1)
	reports := make(chan shutdownReport, 1)
	go func() {
		reasons <- trigger.wait()
		reports <- runShutdownWith(server.Config, shutdownTimings{
			grace:   2 * time.Second,
			forced:  time.Second,
			cleanup: time.Second,
		}, func() { cleanups.Add(1) })
	}()

	sysutil.RequestRestart()
	select {
	case reason := <-reasons:
		require.Equal(t, "in-app restart requested", reason)
	case <-time.After(2 * time.Second):
		t.Fatal("restart request did not reach the shutdown trigger (would have fallen back to os.Exit)")
	}
	drain := drainOf(t, server.Config)
	require.Eventually(t, func() bool {
		drain.mu.Lock()
		defer drain.mu.Unlock()
		return drain.stopping
	}, 2*time.Second, time.Millisecond, "restart must start the HTTP drain")
	require.Zero(t, cleanups.Load(), "cleanup must wait for the in-flight stream")

	releaseStream()
	var report shutdownReport
	select {
	case report = <-reports:
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown did not finish")
	}
	require.True(t, report.drained)
	require.True(t, report.cleanedUp)
	require.Equal(t, int32(1), cleanups.Load())
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\ndata: done\n\n", string(body))
}

func TestShutdownTriggerReturnsOnFirstSignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	trigger := newShutdownTriggerWith(signals)
	t.Cleanup(trigger.stop)

	signals <- syscall.SIGTERM
	require.Equal(t, "signal "+syscall.SIGTERM.String(), trigger.wait())
}

func TestShutdownTriggerMergesRepeatedRestartRequests(t *testing.T) {
	trigger := newShutdownTriggerWith(make(chan os.Signal, 1))
	t.Cleanup(trigger.stop)

	for range 3 {
		sysutil.RequestRestart() // 不得阻塞调用方（handler 的后台 goroutine）
	}
	require.Equal(t, "in-app restart requested", trigger.wait())
	require.Empty(t, trigger.restart)
}

// 网关的流式上游刻意与请求取消解耦（shutdownctx.Detach）。强制阶段必须通过关停硬停止
// 结束它们，让 handler 走部分 usage 计费分支返回，而不是把排空拖到 drained=false。
// 宽限期内则绝不能触发：那段时间正是留给流自然结束、拿到完整 usage 的。
func TestHTTPDrainHardStopsDetachedUpstreamsOnlyInForcedPhase(t *testing.T) {
	started, settled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var upstreamErr atomic.Value
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		upstream := shutdownctx.Detach(r.Context())
		close(started)
		select {
		case <-upstream.Done():
			upstreamErr.Store(upstream.Err())
		case <-release: // 硬停止没接上时让测试失败而不是挂住
		}
		close(settled)
	})
	t.Cleanup(func() { close(release) })
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)

	const grace = 300 * time.Millisecond
	done := drainAsync(server.Config, grace, 2*time.Second)
	select {
	case <-settled:
		t.Fatal("the hard-stop fired during the grace, before in-flight streams could finish on their own")
	case <-time.After(grace / 2):
	}
	outcome := receiveDrainOutcome(t, done)
	require.True(t, outcome.drained, "a detached upstream must end in the forced phase instead of holding the drain")
	require.Zero(t, outcome.remaining)
	require.ErrorIs(t, outcome.err, context.DeadlineExceeded, "the handler may only return after the grace expired")
	waitForDrainSignal(t, settled)
	require.Equal(t, context.Canceled, upstreamErr.Load(), "the stream loop's context.Canceled branch bills partial usage")
}

func TestDetachedUpstreamSurvivesClientDisconnectUntilHardStop(t *testing.T) {
	started, requestCancelled, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		upstream := shutdownctx.Detach(r.Context())
		close(started)
		<-r.Context().Done()
		if upstream.Err() != nil {
			t.Error("a client disconnect must not cancel the detached upstream (it is drained for billing)")
		}
		close(requestCancelled)
		select {
		case <-upstream.Done():
		case <-release: // 硬停止没接上时让测试失败而不是挂住
		}
		close(settled)
	})
	t.Cleanup(func() { close(release) })
	clientCtx, disconnect := context.WithCancel(context.Background())
	defer disconnect()
	req, err := http.NewRequestWithContext(clientCtx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	waitForDrainSignal(t, started)

	disconnect()
	waitForDrainSignal(t, requestCancelled)
	select {
	case <-settled:
		t.Fatal("the detached upstream ended on client disconnect")
	case <-time.After(50 * time.Millisecond):
	}

	outcome := receiveDrainOutcome(t, drainAsync(server.Config, 20*time.Millisecond, 2*time.Second))
	require.True(t, outcome.drained)
	waitForDrainSignal(t, settled)
}
