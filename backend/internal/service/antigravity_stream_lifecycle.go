package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	defaultAntigravityWriteTimeout = 15 * time.Second
	defaultAntigravityDrainTimeout = 10 * time.Second
	defaultAntigravityMaxDuration  = 30 * time.Minute
)

type antigravityStreamLifecycleKey struct{}

type antigravityStreamLifecycle struct {
	downstream context.Context
	upstream   context.Context
	cancel     context.CancelFunc
	drainAfter time.Duration
	mu         sync.Mutex
	drainTimer *time.Timer
	finished   bool
}

func newAntigravityStreamContext(parent context.Context, drainAfter, maxDuration time.Duration) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), maxDuration)
	lifecycle := &antigravityStreamLifecycle{downstream: parent, upstream: ctx, cancel: cancel, drainAfter: drainAfter}
	ctx = context.WithValue(ctx, antigravityStreamLifecycleKey{}, lifecycle)
	stop := context.AfterFunc(parent, lifecycle.startDrain)
	return ctx, func() {
		stop()
		lifecycle.mu.Lock()
		lifecycle.finished = true
		if lifecycle.drainTimer != nil {
			lifecycle.drainTimer.Stop()
		}
		lifecycle.mu.Unlock()
		cancel()
	}
}

func (l *antigravityStreamLifecycle) startDrain() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished || l.drainTimer != nil {
		return
	}
	l.drainTimer = time.AfterFunc(l.drainAfter, l.cancel)
}

func antigravityLifecycle(ctx context.Context) *antigravityStreamLifecycle {
	if ctx == nil {
		return nil
	}
	lifecycle, _ := ctx.Value(antigravityStreamLifecycleKey{}).(*antigravityStreamLifecycle)
	return lifecycle
}

func antigravityDownstreamError(ctx context.Context) error {
	if lifecycle := antigravityLifecycle(ctx); lifecycle != nil {
		if err := lifecycle.downstream.Err(); err != nil {
			return err
		}
		if err := lifecycle.upstream.Err(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

type antigravityOutboundCtx struct {
	context.Context
	upstream context.Context
}

func (c antigravityOutboundCtx) Deadline() (time.Time, bool) { return c.upstream.Deadline() }
func (c antigravityOutboundCtx) Done() <-chan struct{}       { return c.upstream.Done() }
func (c antigravityOutboundCtx) Err() error                  { return c.upstream.Err() }

// Retrying observes downstream cancellation; only the in-flight HTTP request may drain.
func antigravityOutboundContext(ctx context.Context) context.Context {
	if lifecycle := antigravityLifecycle(ctx); lifecycle != nil {
		return antigravityOutboundCtx{Context: ctx, upstream: lifecycle.upstream}
	}
	return ctx
}

func (s *AntigravityGatewayService) upstreamStreamContext(ctx context.Context, c *gin.Context) (context.Context, func()) {
	drainAfter, maxDuration := defaultAntigravityDrainTimeout, defaultAntigravityMaxDuration
	if s.settingService != nil && s.settingService.cfg != nil {
		cfg := s.settingService.cfg.Gateway
		if cfg.AntigravityStreamDrainTimeout > 0 {
			drainAfter = time.Duration(cfg.AntigravityStreamDrainTimeout) * time.Second
		}
		if cfg.AntigravityStreamMaxDuration > 0 {
			maxDuration = time.Duration(cfg.AntigravityStreamMaxDuration) * time.Second
		}
	}
	upstreamCtx, release := newAntigravityStreamContext(ctx, drainAfter, maxDuration)
	if c != nil && c.Request != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), antigravityStreamLifecycleKey{}, antigravityLifecycle(upstreamCtx)))
	}
	return context.WithValue(ctx, antigravityStreamLifecycleKey{}, antigravityLifecycle(upstreamCtx)), release
}

func doAntigravityRequest(p antigravityRetryLoopParams, req *http.Request) (*http.Response, error) {
	if err := antigravityDownstreamError(p.ctx); err != nil {
		return nil, err
	}
	return p.httpUpstream.Do(req, p.proxyURL, p.account.ID, p.account.Concurrency)
}

func (s *AntigravityGatewayService) configureStreamWriter(c *gin.Context, writer *antigravityClientWriter) {
	if s.settingService != nil && s.settingService.cfg != nil && s.settingService.cfg.Gateway.AntigravityStreamWriteTimeout > 0 {
		writer.writeTimeout = time.Duration(s.settingService.cfg.Gateway.AntigravityStreamWriteTimeout) * time.Second
	}
	if c != nil && c.Request != nil {
		if lifecycle := antigravityLifecycle(c.Request.Context()); lifecycle != nil {
			writer.onDisconnect = lifecycle.startDrain
		}
	}
}

// A failover attempt is billed only through its eventual successful result.
func partialAntigravityUsageResult(c *gin.Context, resp *http.Response, streamResult *antigravityStreamResult, model, upstreamModel string, stream bool, startTime time.Time, err error) *ForwardResult {
	if streamResult == nil || !streamResult.usage.hasObservedTokens() {
		return nil
	}
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		return nil
	}
	result := partialStreamUsageResult(c, resp, &streamingResult{
		usage: streamResult.usage, firstTokenMs: streamResult.firstTokenMs, clientDisconnect: streamResult.clientDisconnect,
	}, model, upstreamModel, startTime, err)
	if result != nil {
		result.Stream = stream
		result.UsageIncomplete = true
	}
	return result
}

func (r *ForwardResult) HasObservedUsage() bool {
	return r != nil && r.Usage.hasObservedTokens()
}
