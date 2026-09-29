package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCRSShutdownTimeoutConfiguration(t *testing.T) {
	for input, want := range map[string]time.Duration{"": 5 * time.Second, "540": 540 * time.Second, " 30 ": 30 * time.Second} {
		got, err := parseShutdownTimeout(input)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, input := range []string{"0", "-1", "garbage", "1.5", "9223372036854775807"} {
		_, err := parseShutdownTimeout(input)
		require.Error(t, err)
	}
}

type drainResult struct {
	drained bool
	err     error
}

func startDrainTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	installHTTPDrain(server.Config)
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func waitForDrainSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stream state")
	}
}

func drainAsync(server *http.Server, grace, forced time.Duration) <-chan drainResult {
	result := make(chan drainResult, 1)
	go func() {
		drained, err := drainHTTPServer(server, grace, forced)
		result <- drainResult{drained, err}
	}()
	return result
}

func TestCRSShutdownWaitsForActiveStream(t *testing.T) {
	started, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseStream := sync.OnceFunc(func() { close(release) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-release
		_, _ = io.WriteString(w, "data: done\n\n")
		close(settled)
	})
	t.Cleanup(releaseStream)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	waitForDrainSignal(t, started)
	done := drainAsync(server.Config, time.Second, time.Second)
	select {
	case result := <-done:
		t.Fatalf("shutdown returned before active stream settled: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	releaseStream()
	result := <-done
	require.True(t, result.drained)
	require.NoError(t, result.err)
	waitForDrainSignal(t, settled)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\ndata: done\n\n", string(body))
}

func TestCRSShutdownCancelsStreamBeforeWaitingForSettlement(t *testing.T) {
	cancelled, settle, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishSettlement := sync.OnceFunc(func() { close(settle) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
		<-settle
		close(settled)
	})
	t.Cleanup(finishSettlement)
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	done := drainAsync(server.Config, 20*time.Millisecond, time.Second)
	waitForDrainSignal(t, cancelled)
	select {
	case result := <-done:
		t.Fatalf("shutdown returned before cancelled stream settled: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}
	finishSettlement()
	result := <-done
	require.True(t, result.drained)
	require.ErrorIs(t, result.err, context.DeadlineExceeded)
	waitForDrainSignal(t, settled)
}

func TestCRSShutdownBoundsStuckStreamWithoutPermittingDependencyCleanup(t *testing.T) {
	release := make(chan struct{})
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-release // Model an upstream stream detached from the request context.
	})
	t.Cleanup(func() { close(release) })
	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()
	started := time.Now()
	drained, err := drainHTTPServer(server.Config, 20*time.Millisecond, 20*time.Millisecond)
	require.False(t, drained, "active handlers must keep database/cache cleanup disabled")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
}

func TestCRSShutdownTracksHijackedHandlerThroughSettlement(t *testing.T) {
	hijacked, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finishSettlement := sync.OnceFunc(func() { close(release) })
	server := startDrainTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
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
	defer resp.Body.Close()
	waitForDrainSignal(t, hijacked)
	done := drainAsync(server.Config, 20*time.Millisecond, time.Second)
	waitForDrainSignal(t, cancelled)
	select {
	case result := <-done:
		t.Fatalf("shutdown ignored live hijacked handler: %+v", result)
	default:
	}
	finishSettlement()
	result := <-done
	require.True(t, result.drained)
	require.ErrorIs(t, result.err, context.DeadlineExceeded)
}

func TestCRSShutdownRejectsNewRequests(t *testing.T) {
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("new request reached handler during drain")
	})}
	installHTTPDrain(server)
	server.Handler.(*httpDrain).beginDrain()
	recorder := httptest.NewRecorder()
	server.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	drained, err := drainHTTPServer(server, time.Second, time.Second)
	require.True(t, drained)
	require.NoError(t, err)
}

func TestCRSShutdownRequiresHandlerTracking(t *testing.T) {
	drained, err := drainHTTPServer(&http.Server{}, time.Second, time.Second)
	require.False(t, drained)
	require.ErrorContains(t, err, "tracking is not installed")
}

func TestCRSCleanupHasBoundedWait(t *testing.T) {
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
