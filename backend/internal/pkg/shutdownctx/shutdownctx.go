// Package shutdownctx lets upstream contexts that are deliberately detached from
// the client request still stop when the process shuts down.
//
// 网关把上游流与请求 context 解耦（客户端断开后继续读上游，以拿到完整 usage 计费），
// 所以关停强制阶段取消请求 context 对它们无效：handler 一直挂到进程退出，部分 usage
// 也随之丢失。主服务把关停的“硬停止”信号挂在每个请求 context 上，Detach 出来的上游
// context 只响应这个信号，客户端断开的语义保持不变。
package shutdownctx

import (
	"context"
	"time"
)

type hardStopKey struct{}

// WithHardStop attaches the process shutdown hard-stop signal to ctx. Contexts
// later derived with Detach from ctx (or from its descendants) are cancelled
// when hardStop is done. hardStop should carry no request-scoped values: a
// detached context falls back to it for keys the request does not have.
func WithHardStop(ctx, hardStop context.Context) context.Context {
	if hardStop == nil {
		return ctx
	}
	return context.WithValue(ctx, hardStopKey{}, hardStop)
}

// Detach returns a context that keeps the values of ctx but not its
// cancellation or deadline, like context.WithoutCancel. When ctx carries a
// hard-stop signal from WithHardStop, the returned context is cancelled with it.
func Detach(ctx context.Context) context.Context {
	detached := context.WithoutCancel(ctx)
	hardStop, _ := ctx.Value(hardStopKey{}).(context.Context)
	if hardStop == nil {
		return detached
	}
	return hardStopContext{values: detached, hardStop: hardStop}
}

// hardStopContext 取值来自已解耦的请求 context，取消只来自硬停止。
// 不为每个请求注册 AfterFunc：剥离请求取消后的上游 context 没有明确的结束时机
// （release 在发出请求前就会被调用），逐个注册会在长生命周期的硬停止上无限累积。
type hardStopContext struct {
	values   context.Context
	hardStop context.Context
}

func (c hardStopContext) Deadline() (time.Time, bool) { return c.hardStop.Deadline() }
func (c hardStopContext) Done() <-chan struct{}       { return c.hardStop.Done() }
func (c hardStopContext) Err() error                  { return c.hardStop.Err() }

// Value 先查请求的值；查不到时回落到硬停止 context。标准库靠 Value 取内部的
// cancelCtx 键找可挂靠的父 cancelCtx，而 WithoutCancel 会屏蔽这个键：不回落的话，
// 中间隔着一层 WithValue（如 httptrace）再派生 WithCancel 时，标准库既找不到父
// cancelCtx、valueCtx 也不实现 AfterFunc，只能为每条上游流起一个监听 goroutine。
// 回落后子 context 直接登记在硬停止的 children 上，结束时自动摘除。
// 硬停止 context 应不携带业务值（主服务传入的 forceCtx 直接派生自 Background），
// 回落因此不会让它的值冒充请求的值。
func (c hardStopContext) Value(key any) any {
	if value := c.values.Value(key); value != nil {
		return value
	}
	return c.hardStop.Value(key)
}

// AfterFunc 兜底硬停止不是标准 cancelCtx 链的情况：context.WithCancel/WithTimeout 从它
// 派生子 context 时直接挂到硬停止上（子 context 结束时自动注销），而不是为每个子
// context 起一个监听 goroutine。
func (c hardStopContext) AfterFunc(f func()) func() bool {
	return context.AfterFunc(c.hardStop, f)
}

func (c hardStopContext) String() string {
	return "shutdownctx.Detach"
}
