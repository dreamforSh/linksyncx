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
// when hardStop is done.
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
func (c hardStopContext) Value(key any) any           { return c.values.Value(key) }

// AfterFunc 让 context.WithCancel/WithTimeout 从它派生子 context 时直接挂到硬停止上
// （子 context 结束时自动注销），而不是为每个子 context 起一个监听 goroutine。
func (c hardStopContext) AfterFunc(f func()) func() bool {
	return context.AfterFunc(c.hardStop, f)
}

func (c hardStopContext) String() string {
	return "shutdownctx.Detach"
}
