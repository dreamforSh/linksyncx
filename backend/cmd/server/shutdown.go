package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

func parseShutdownTimeout(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 5 * time.Second, nil
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
		return 0, fmt.Errorf("SHUTDOWN_TIMEOUT_SECONDS must be a positive integer duration")
	}
	return time.Duration(seconds) * time.Second, nil
}

const (
	forcedDrainTimeout        = 20 * time.Second
	applicationCleanupTimeout = 30 * time.Second
)

type requestConnKey struct{}

// Track handler completion without wrapping ResponseWriter's streaming interfaces.
type httpDrain struct {
	next     http.Handler
	mu       sync.Mutex
	stopping bool
	active   int
	done     chan struct{}
	hijacked map[net.Conn]struct{}
	forceCtx context.Context
	cancel   context.CancelFunc
}

func installHTTPDrain(server *http.Server) {
	forceCtx, cancel := context.WithCancel(context.Background())
	next := server.Handler
	if next == nil {
		next = http.DefaultServeMux
	}
	d := &httpDrain{next: next, done: make(chan struct{}), hijacked: make(map[net.Conn]struct{}), forceCtx: forceCtx, cancel: cancel}
	server.Handler = d
	previousContext, previousState := server.ConnContext, server.ConnState
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousContext != nil {
			ctx = previousContext(ctx, conn)
		}
		return context.WithValue(ctx, requestConnKey{}, conn)
	}
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		if previousState != nil {
			previousState(conn, state)
		}
		d.mu.Lock()
		closeConn := false
		switch state {
		case http.StateHijacked:
			d.hijacked[conn] = struct{}{}
			closeConn = d.stopping
		case http.StateClosed:
			delete(d.hijacked, conn)
		}
		d.mu.Unlock()
		if closeConn {
			_ = conn.Close()
		}
	}
}

func (d *httpDrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		w.Header().Set("Connection", "close")
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	d.active++
	d.mu.Unlock()
	ctx, cancel := context.WithCancel(r.Context())
	stopCancel := context.AfterFunc(d.forceCtx, cancel)
	defer func() { stopCancel(); cancel() }()
	r = r.WithContext(ctx)
	defer func() {
		d.mu.Lock()
		if conn, ok := r.Context().Value(requestConnKey{}).(net.Conn); ok {
			delete(d.hijacked, conn)
		}
		d.active--
		if d.stopping && d.active == 0 {
			close(d.done)
		}
		d.mu.Unlock()
	}()
	d.next.ServeHTTP(w, r)
}

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

func shutdownHTTPServer(server *http.Server, timeout time.Duration) (bool, error) {
	return drainHTTPServer(server, timeout, forcedDrainTimeout)
}

// Only a completed handler drain permits closing its database/cache dependencies.
func drainHTTPServer(server *http.Server, timeout, forceWait time.Duration) (bool, error) {
	d, ok := server.Handler.(*httpDrain)
	if !ok {
		return false, errors.New("HTTP request drain tracking is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	d.beginDrain()
	err := server.Shutdown(ctx)
	select {
	case <-d.done:
		return true, err
	case <-ctx.Done():
		d.cancel()
		_ = server.Close()
		d.closeHijacked()
	}
	timer := time.NewTimer(forceWait)
	defer timer.Stop()
	select {
	case <-d.done:
		return true, ctx.Err()
	case <-timer.C:
		return false, errors.Join(ctx.Err(), errors.New("request handlers remain active after forced close"))
	}
}

func cleanupWithin(cleanup func(), timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { cleanup(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
