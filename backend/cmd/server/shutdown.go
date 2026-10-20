package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/shutdownctx"
	"github.com/Wei-Shaw/sub2api/internal/pkg/sysutil"
)

const (
	// forcedDrainTimeout 是宽限期结束、取消全部请求 context（并硬停止解耦的上游流）之后，
	// 再等 handler 收尾的时长：被截断的流要提交部分 usage，非流式请求与 WebSocket 要返回。
	// 不经 shutdownctx 解耦的后台等待仍可能不响应，所以只给一个短窗口，不让它拖长重启。
	forcedDrainTimeout = 5 * time.Second
	// applicationCleanupTimeout 限定依赖清理（app.Cleanup）的等待：清理卡住时进程仍要
	// 退出交给 systemd/Docker 拉起，而不是挂着一个监听已关闭、什么都不服务的进程。
	applicationCleanupTimeout = 30 * time.Second
)

type requestConnKey struct{}

// shutdownTrigger 汇总两类关停请求：SIGINT/SIGTERM，以及进程内重启（管理后台的
// “重启”按钮、在线更新/回滚后的重启）。两者必须走同一条排空 + Cleanup 路径，
// 否则进程内重启的 os.Exit 会丢掉 usage 记录池、计费缓存与配额 flusher 中的计费。
type shutdownTrigger struct {
	signals    chan os.Signal
	restart    chan struct{}
	stopped    chan struct{}
	stopOnce   sync.Once
	unregister func()
}

// newShutdownTrigger 开始监听终止信号并向 sysutil 注册进程内重启；
// 必须在开始接收请求之前调用，否则早到的重启请求会退化成直接退出。
func newShutdownTrigger() *shutdownTrigger {
	trigger := newShutdownTriggerWith(make(chan os.Signal, 1))
	signal.Notify(trigger.signals, syscall.SIGINT, syscall.SIGTERM)
	return trigger
}

func newShutdownTriggerWith(signals chan os.Signal) *shutdownTrigger {
	trigger := &shutdownTrigger{
		signals: signals,
		restart: make(chan struct{}, 1),
		stopped: make(chan struct{}),
	}
	trigger.unregister = sysutil.RegisterGracefulRestart(trigger.requestRestart)
	return trigger
}

func (t *shutdownTrigger) requestRestart() {
	select {
	case t.restart <- struct{}{}:
	default: // 已有未处理的重启请求，合并
	}
}

// wait 阻塞到第一个关停请求，返回用于日志的原因。
func (t *shutdownTrigger) wait() string {
	select {
	case sig := <-t.signals:
		// 第一个信号之后立即恢复默认处理：排空可能持续数十秒，第二次 Ctrl+C/SIGTERM
		// 必须能直接结束进程。docker/systemd 只发一次 SIGTERM，随后是 SIGKILL，不受影响。
		signal.Stop(t.signals)
		return "signal " + sig.String()
	case <-t.restart:
		// 进程内重启时继续监听信号：排空期间到来的第一个 SIGTERM（如 systemctl stop）
		// 不应跳过排空与 Cleanup，收到后再恢复默认处理，下一个信号即可强制结束。
		go func() {
			select {
			case <-t.signals:
				signal.Stop(t.signals)
			case <-t.stopped:
			}
		}()
		return "in-app restart requested"
	}
}

// stop 注销进程内重启与信号监听。
func (t *shutdownTrigger) stop() {
	t.stopOnce.Do(func() {
		t.unregister()
		signal.Stop(t.signals)
		close(t.stopped)
	})
}

// httpDrain 统计正在执行的 handler，让关停流程能等它们真正返回后再拆依赖。
//
// 刻意不包装 ResponseWriter：gin 的 Flush/Hijack 依赖底层 writer 的接口断言，
// 包一层就会让 SSE 刷新和 WebSocket 升级失效。
type httpDrain struct {
	next http.Handler

	mu       sync.Mutex
	stopping bool
	active   int
	done     chan struct{}
	// hijacked 记录已被 hijack（WebSocket 等）的连接：http.Server.Shutdown 不跟踪它们，
	// 只能由排空流程自己关闭。
	hijacked map[net.Conn]struct{}

	// forceCtx 是所有请求 context 的根：宽限期结束时 cancel 一次即取消全部进行中的请求，
	// 并对与请求取消解耦的上游流发出关停硬停止。
	forceCtx context.Context
	cancel   context.CancelFunc
}

// installHTTPDrain 给 server 装上 handler 排空跟踪，必须在 ListenAndServe 之前调用。
// 已有的 BaseContext/ConnContext/ConnState 钩子会被保留并先于排空逻辑执行。
func installHTTPDrain(server *http.Server) {
	forceCtx, cancel := context.WithCancel(context.Background())
	next := server.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	d := &httpDrain{
		next:     next,
		done:     make(chan struct{}),
		hijacked: make(map[net.Conn]struct{}),
		forceCtx: forceCtx,
		cancel:   cancel,
	}
	server.Handler = d

	// 请求 context 统一派生自 forceCtx（HTTP/1 与 h2c 的请求 context 都源自 BaseContext）：
	// 强制阶段一次 cancel 就能取消所有请求，不必给每个请求单独注册 AfterFunc——那样
	// 每个请求都要两次争抢 forceCtx 上的同一把锁。
	//
	// forceCtx 同时作为关停硬停止挂在请求 context 上：网关刻意与请求取消解耦的上游流
	// （shutdownctx.Detach）只响应它，强制阶段以 context.Canceled 结束并提交已观测到的
	// 部分 usage，而不是挂到进程退出、计费全部丢失。
	previousBase := server.BaseContext
	server.BaseContext = func(listener net.Listener) context.Context {
		if previousBase == nil {
			return shutdownctx.WithHardStop(forceCtx, forceCtx)
		}
		base, cancelBase := context.WithCancel(previousBase(listener))
		context.AfterFunc(forceCtx, cancelBase)
		return shutdownctx.WithHardStop(base, forceCtx)
	}

	previousConnContext := server.ConnContext
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousConnContext != nil {
			ctx = previousConnContext(ctx, conn)
		}
		// handler 返回时据此把（可能被 hijack 的）连接从 hijacked 中移除。
		return context.WithValue(ctx, requestConnKey{}, conn)
	}

	previousState := server.ConnState
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		if previousState != nil {
			previousState(conn, state)
		}
		// 只有 hijack 需要加锁：New/Active/Idle/Closed 每个连接都会触发多次，
		// 在热路径上抢全局锁不值得。StateHijacked 是终态，之后不会再有 StateClosed。
		if state != http.StateHijacked {
			return
		}
		d.mu.Lock()
		d.hijacked[conn] = struct{}{}
		closeConn := d.stopping
		d.mu.Unlock()
		if closeConn {
			// 排空开始后才完成的 WebSocket 升级同样按 T=0 策略立即关闭。
			_ = conn.Close()
		}
	}
}

func (d *httpDrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		// 监听在关停开始时已关闭，能走到这里的只有既有连接上的新请求。
		// Connection: close 让 HTTP/1 客户端换连接重试，HTTP/2 下会触发平滑 GOAWAY。
		w.Header().Set("Connection", "close")
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	d.active++
	d.mu.Unlock()
	// defer 保证 handler panic（net/http 会兜底 recover）时计数同样归还。
	defer d.finish(r)
	d.next.ServeHTTP(w, r)
}

func (d *httpDrain) finish(r *http.Request) {
	conn, _ := r.Context().Value(requestConnKey{}).(net.Conn)
	d.mu.Lock()
	defer d.mu.Unlock()
	if conn != nil {
		delete(d.hijacked, conn)
	}
	d.active--
	if d.stopping && d.active == 0 {
		close(d.done)
	}
}

// beginDrain 进入排空状态：拒绝新请求，并在 T=0 关闭所有 hijack 连接。
//
// WebSocket（Codex/Responses WS、OpenAI live、grok audio、ops WS）不享受宽限：
// 空闲的 Codex WS 客户端会一直挂着连接，若等到宽限结束再关，每次重启都要
// 完整等满宽限（期间监听已关闭，新请求全部被拒）。只有 HTTP/SSE 请求等宽限。
func (d *httpDrain) beginDrain() {
	d.mu.Lock()
	if !d.stopping {
		d.stopping = true
		if d.active == 0 {
			close(d.done)
		}
	}
	d.mu.Unlock()
	d.closeHijacked()
}

func (d *httpDrain) closeHijacked() {
	d.mu.Lock()
	conns := make([]net.Conn, 0, len(d.hijacked))
	for conn := range d.hijacked {
		conns = append(conns, conn)
	}
	d.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (d *httpDrain) activeHandlers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active
}

// drainHTTPServer 关闭监听并等待 handler 返回：先给 grace 让请求自然结束；超时后
// 取消所有请求 context（含关停硬停止）、关闭全部连接，再最多等 forceWait。
//
// drained=true 表示所有被跟踪的 handler 都已返回；否则 remaining 是仍在运行的数量
// （既不看请求取消、也不经 shutdownctx 响应硬停止的等待）。err 非 nil 说明宽限已
// 耗尽或 Shutdown 本身出错，即使 drained=true 也可能非 nil。
func drainHTTPServer(server *http.Server, grace, forceWait time.Duration) (drained bool, remaining int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	d, ok := server.Handler.(*httpDrain)
	if !ok {
		// 没装跟踪时至少保证监听被关闭，不能让依赖清理与仍在服务的 server 并行。
		return false, 0, errors.Join(errors.New("HTTP request drain tracking is not installed"), server.Shutdown(ctx))
	}
	d.beginDrain()
	err = server.Shutdown(ctx)
	select {
	case <-d.done:
		return true, 0, err
	case <-ctx.Done():
	}

	// 强制阶段：取消所有请求 context（含 hijack 请求）并硬停止解耦的上游流，断开全部连接。
	d.cancel()
	_ = server.Close()
	d.closeHijacked()
	timer := time.NewTimer(forceWait)
	defer timer.Stop()
	select {
	case <-d.done:
		return true, 0, ctx.Err()
	case <-timer.C:
		remaining = d.activeHandlers()
		return false, remaining, errors.Join(ctx.Err(), errors.New("request handlers remain active after forced close"))
	}
}

type shutdownTimings struct {
	grace   time.Duration
	forced  time.Duration
	cleanup time.Duration
}

type shutdownReport struct {
	drained   bool
	remaining int
	cleanedUp bool
}

// runShutdown 是主服务收到关停请求后的完整序列：排空 HTTP handler，再执行有界的依赖清理。
func runShutdown(server *http.Server, grace time.Duration, cleanup func()) shutdownReport {
	return runShutdownWith(server, shutdownTimings{
		grace:   grace,
		forced:  forcedDrainTimeout,
		cleanup: applicationCleanupTimeout,
	}, cleanup)
}

func runShutdownWith(server *http.Server, timings shutdownTimings, cleanup func()) shutdownReport {
	drained, remaining, err := drainHTTPServer(server, timings.grace, timings.forced)
	if err != nil {
		log.Printf("HTTP drain finished with error (drained=%t, handlers still running=%d): %v", drained, remaining, err)
	}
	// 无论 handler 是否排空都必须执行 Cleanup：它是 usage 记录池、计费缓存写队列和
	// 配额 flusher 落盘的唯一入口。仍未返回的 handler 不响应取消，进程随后照样退出，
	// 它们的计费本就保不住；跳过 Cleanup 只会再丢掉已完成请求的计费。
	cleanedUp := cleanupWithin(cleanup, timings.cleanup)
	if !cleanedUp {
		log.Printf("Application cleanup exceeded %s; exiting with pending work", timings.cleanup)
	}
	return shutdownReport{drained: drained, remaining: remaining, cleanedUp: cleanedUp}
}

// cleanupWithin 运行 cleanup，最多等待 timeout；超时后放弃等待（cleanup 继续在后台运行）。
func cleanupWithin(cleanup func(), timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		defer close(done)
		cleanup()
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
