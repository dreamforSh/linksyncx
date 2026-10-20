package shutdownctx

import (
	"context"
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
