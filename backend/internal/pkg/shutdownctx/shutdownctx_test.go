package shutdownctx

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testKey string

func requireDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("context was not cancelled")
	}
}

func requireNotDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatalf("context cancelled unexpectedly: %v", ctx.Err())
	default:
	}
}

func TestDetachIgnoresClientCancellationButFollowsHardStop(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	defer fireHardStop()
	base := WithHardStop(context.WithValue(context.Background(), testKey("k"), "v"), hardStop)
	request, clientDisconnect := context.WithTimeout(base, time.Hour)

	upstream := Detach(request)
	clientDisconnect()
	requireNotDone(t, upstream)
	require.NoError(t, upstream.Err())
	require.Equal(t, "v", upstream.Value(testKey("k")), "request values must survive detaching")
	_, hasDeadline := upstream.Deadline()
	require.False(t, hasDeadline, "the request deadline must not leak into the detached context")

	fireHardStop()
	requireDone(t, upstream)
	require.ErrorIs(t, upstream.Err(), context.Canceled)
	require.ErrorIs(t, context.Cause(upstream), context.Canceled)
}

func TestDetachedChildrenAreCancelledByHardStop(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	defer fireHardStop()
	upstream := Detach(WithHardStop(context.Background(), hardStop))

	child, cancelChild := context.WithCancel(upstream)
	defer cancelChild()
	timed, cancelTimed := context.WithTimeout(upstream, time.Hour)
	defer cancelTimed()
	finished, cancelFinished := context.WithCancel(upstream)
	cancelFinished() // 已结束的子 context 不受后续硬停止影响，也不应残留注册

	fireHardStop()
	requireDone(t, child)
	requireDone(t, timed)
	require.ErrorIs(t, child.Err(), context.Canceled)
	require.ErrorIs(t, timed.Err(), context.Canceled)
	require.ErrorIs(t, finished.Err(), context.Canceled)
}

// Transport 等会在解耦后的 context 上再套一层 WithValue（如 httptrace）后派生
// WithCancel；这条路径不能为每条上游流多起一个监听硬停止的 goroutine。
func TestDetachedChildrenBehindValueLayerNeedNoWatcherGoroutine(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	defer fireHardStop()
	upstream := Detach(WithHardStop(context.Background(), hardStop))
	traced := context.WithValue(upstream, testKey("trace"), "on")

	const streams = 200
	before := runtime.NumGoroutine()
	children := make([]context.Context, 0, streams)
	cancels := make([]context.CancelFunc, 0, streams)
	for i := 0; i < streams; i++ {
		child, cancel := context.WithCancel(traced)
		children = append(children, child)
		cancels = append(cancels, cancel)
	}
	defer func() {
		for _, cancel := range cancels {
			cancel()
		}
	}()
	grown := runtime.NumGoroutine() - before
	require.Less(t, grown, streams/4, "every detached stream behind a value layer started a watcher goroutine")
	require.Equal(t, "on", children[0].Value(testKey("trace")))

	// 一半正常结束，其余由硬停止取消。
	for _, cancel := range cancels[:streams/2] {
		cancel()
	}
	requireNotDone(t, children[streams-1])
	fireHardStop()
	for _, child := range children {
		requireDone(t, child)
		require.ErrorIs(t, child.Err(), context.Canceled)
	}
}

func TestDetachPrefersRequestValuesOverHardStopValues(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.WithValue(context.Background(), testKey("k"), "hard-stop"))
	defer fireHardStop()
	request := context.WithValue(context.Background(), testKey("k"), "request")
	upstream := Detach(WithHardStop(request, hardStop))

	require.Equal(t, "request", upstream.Value(testKey("k")))
}

func TestDetachWithoutHardStopMatchesWithoutCancel(t *testing.T) {
	request, clientDisconnect := context.WithCancel(context.WithValue(context.Background(), testKey("k"), "v"))
	upstream := Detach(request)
	clientDisconnect()

	require.Nil(t, upstream.Done(), "without a hard-stop the context must never be cancelled")
	require.NoError(t, upstream.Err())
	require.Equal(t, "v", upstream.Value(testKey("k")))
}

func TestDetachingTwiceKeepsHardStop(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	upstream := Detach(Detach(WithHardStop(context.Background(), hardStop)))

	fireHardStop()
	requireDone(t, upstream)
}

func TestWithHardStopIgnoresNilSignal(t *testing.T) {
	base := context.Background()
	require.Equal(t, base, WithHardStop(base, nil))
}
