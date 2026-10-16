//go:build unit

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type opsDeadlineAuditResponseWriter struct {
	*httptest.ResponseRecorder
	mu          sync.Mutex
	deadlines   []time.Time
	flushCalls  int
	deadlineErr error
	flushErr    error
	blockMethod string
	started     chan struct{}
	resume      chan struct{}
}

func (w *opsDeadlineAuditResponseWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	w.deadlines = append(w.deadlines, deadline)
	w.mu.Unlock()
	if w.blockMethod == "deadline" {
		close(w.started)
		<-w.resume
	}
	return w.deadlineErr
}

func (w *opsDeadlineAuditResponseWriter) FlushError() error {
	w.mu.Lock()
	w.flushCalls++
	w.mu.Unlock()
	if w.blockMethod == "flush" {
		close(w.started)
		<-w.resume
	}
	if w.flushErr != nil {
		return w.flushErr
	}
	w.ResponseRecorder.Flush()
	return nil
}

func (w *opsDeadlineAuditResponseWriter) calls() ([]time.Time, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Time(nil), w.deadlines...), w.flushCalls
}

func newOpsDeadlineAuditCapture(pool opsCaptureWriterStatePool, inner *opsDeadlineAuditResponseWriter) (*opsCaptureWriter, *gin.Context) {
	c, _ := gin.CreateTestContext(inner)
	return acquireOpsCaptureWriterFromPool(pool, c.Writer), c
}

func TestOpsCaptureWriterWriteDeadlineAndFlushErrorForward(t *testing.T) {
	inner := &opsDeadlineAuditResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	w, c := newOpsDeadlineAuditCapture(&deterministicOpsCaptureWriterStatePool{}, inner)
	defer releaseOpsCaptureWriter(w)
	controller := http.NewResponseController(w)
	deadline := time.Unix(1_800_000_000, 123)
	require.NoError(t, controller.SetWriteDeadline(deadline))
	require.NoError(t, controller.SetWriteDeadline(time.Time{}), "clearing a deadline must also reach the underlying writer")
	require.NoError(t, controller.Flush())
	deadlines, flushes := inner.calls()
	require.Equal(t, []time.Time{deadline, {}}, deadlines)
	require.Equal(t, 1, flushes)
	require.True(t, c.Writer.Written(), "FlushError must commit Gin's header state before flushing the raw writer")
	require.True(t, inner.Flushed)

	deadlineErr := errors.New("audit deadline failure")
	inner.deadlineErr = deadlineErr
	require.ErrorIs(t, controller.SetWriteDeadline(deadline), deadlineErr)
	flushErr := errors.New("audit flush failure")
	inner.flushErr = flushErr
	require.ErrorIs(t, controller.Flush(), flushErr, "FlushError must preserve the transport error rather than call Gin's error-discarding Flush")
	_, flushes = inner.calls()
	require.Equal(t, 2, flushes)
}

func TestOpsCaptureWriterWriteDeadlineReleasedAndStaleLease(t *testing.T) {
	pool := &deterministicOpsCaptureWriterStatePool{}
	firstInner := &opsDeadlineAuditResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	stale, _ := newOpsDeadlineAuditCapture(pool, firstInner)
	releaseOpsCaptureWriter(stale)
	deadline := time.Unix(1_800_000_000, 0)
	require.Error(t, stale.SetWriteDeadline(deadline))
	require.Error(t, stale.FlushError())
	deadlines, flushes := firstInner.calls()
	require.Empty(t, deadlines)
	require.Zero(t, flushes)

	currentInner := &opsDeadlineAuditResponseWriter{ResponseRecorder: httptest.NewRecorder()}
	current, c := newOpsDeadlineAuditCapture(pool, currentInner)
	defer releaseOpsCaptureWriter(current)
	require.Same(t, stale.state, current.state, "the regression must exercise an actual state-pool reuse")
	require.NotEqual(t, stale.generation, current.generation)
	require.Error(t, http.NewResponseController(stale).SetWriteDeadline(deadline))
	require.Error(t, http.NewResponseController(stale).Flush())
	require.False(t, c.Writer.Written(), "stale FlushError must not commit the new request's headers")
	deadlines, flushes = currentInner.calls()
	require.Empty(t, deadlines)
	require.Zero(t, flushes)

	// A late release must not invalidate or return the current lease to the pool.
	releaseOpsCaptureWriter(stale)
	require.NoError(t, current.SetWriteDeadline(deadline))
	require.NoError(t, current.FlushError())
	deadlines, flushes = currentInner.calls()
	require.Equal(t, []time.Time{deadline}, deadlines)
	require.Equal(t, 1, flushes)

	var nilWriter *opsCaptureWriter
	require.Error(t, nilWriter.SetWriteDeadline(deadline))
	require.Error(t, nilWriter.FlushError())
}

func TestOpsCaptureWriterWriteDeadlineDelegatedCallsHoldLeaseUntilReturn(t *testing.T) {
	for _, method := range []string{"deadline", "flush"} {
		t.Run(method, func(t *testing.T) {
			pool := &deterministicOpsCaptureWriterStatePool{}
			inner := &opsDeadlineAuditResponseWriter{
				ResponseRecorder: httptest.NewRecorder(), blockMethod: method,
				started: make(chan struct{}), resume: make(chan struct{}),
			}
			var resumeOnce sync.Once
			resume := func() { resumeOnce.Do(func() { close(inner.resume) }) }
			t.Cleanup(resume)
			w, _ := newOpsDeadlineAuditCapture(pool, inner)
			operationDone := make(chan error, 1)
			go func() {
				if method == "deadline" {
					operationDone <- w.SetWriteDeadline(time.Now().Add(time.Second))
				} else {
					operationDone <- w.FlushError()
				}
			}()
			select {
			case <-inner.started:
			case <-time.After(2 * time.Second):
				t.Fatal("delegated operation did not start")
			}
			require.True(t, w.state.mu.TryLock(), "network calls must not hold the capture-state mutex")
			w.state.mu.Unlock()
			released := make(chan struct{})
			go func() { releaseOpsCaptureWriter(w); close(released) }()
			require.Eventually(t, func() bool {
				w.state.mu.RLock()
				defer w.state.mu.RUnlock()
				return w.state.generation != w.generation
			}, time.Second, time.Millisecond, "release must invalidate the handle before waiting for its delegated call")
			select {
			case <-released:
				t.Fatal("capture state was released while the delegated operation was still using the old response writer")
			default:
			}
			require.Error(t, w.SetWriteDeadline(time.Time{}), "release must reject new operations immediately")
			require.Error(t, w.FlushError())
			resume()
			select {
			case err := <-operationDone:
				require.NoError(t, err)
			case <-time.After(2 * time.Second):
				t.Fatal("delegated operation did not return")
			}
			select {
			case <-released:
			case <-time.After(2 * time.Second):
				t.Fatal("release did not finish after the delegated operation returned")
			}
			require.Len(t, pool.states, 1)
			deadlines, flushes := inner.calls()
			if method == "deadline" {
				require.Len(t, deadlines, 1)
				require.Zero(t, flushes)
			} else {
				require.Empty(t, deadlines)
				require.Equal(t, 1, flushes)
			}
		})
	}
}
