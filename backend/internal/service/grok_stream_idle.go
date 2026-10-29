package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Default Grok stream idle when gateway.stream_data_interval_timeout is 0.
// Long enough for slow thinking models, short enough to release hung sockets.
const defaultGrokStreamIdleTimeout = 180 * time.Second

// Shorter cool after a Grok stream-idle failure so the account can re-enter soon
// but is not immediately re-picked in a tight failover loop.
const grokStreamIdleCooldown = 2 * time.Minute

// resolveGrokStreamIdleTimeout returns the effective upstream-read idle timeout
// for Grok streams. Prefers the global gateway setting when positive; otherwise
// applies a Grok-only default so hung SSE bodies still fail over.
func resolveGrokStreamIdleTimeout(cfgStreamIntervalSec int) time.Duration {
	if cfgStreamIntervalSec > 0 {
		return time.Duration(cfgStreamIntervalSec) * time.Second
	}
	return defaultGrokStreamIdleTimeout
}

// openAICompatStreamIdleTimeout is the upstream-read idle limit of the Chat
// Completions and Messages bridges over Responses streams. Grok always gets
// one, matching the native Responses path, so a hung Grok stream is cut off
// even when the global gateway setting is disabled.
func (s *OpenAIGatewayService) openAICompatStreamIdleTimeout(account *Account) time.Duration {
	cfgSec := 0
	if s != nil && s.cfg != nil {
		cfgSec = s.cfg.Gateway.StreamDataIntervalTimeout
	}
	if account != nil && account.IsGrok() {
		return resolveGrokStreamIdleTimeout(cfgSec)
	}
	if cfgSec > 0 {
		return time.Duration(cfgSec) * time.Second
	}
	return 0
}

// openAICompatBridgeContext is the request context of a bridge stream, or
// Background for contexts built without a request.
func openAICompatBridgeContext(c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return context.Background()
	}
	return c.Request.Context()
}

// grokStreamIdleBridgeFailover applies the native Responses path's Grok idle
// handling in a bridge: cool the account briefly, and fail over when nothing
// has reached the client yet. It returns nil for other platforms and for
// streams that already wrote output.
func (s *OpenAIGatewayService) grokStreamIdleBridgeFailover(ctx context.Context, account *Account, idle time.Duration, outputStarted bool) *UpstreamFailoverError {
	if account == nil || !account.IsGrok() {
		return nil
	}
	s.tempUnscheduleGrok(ctx, account, grokStreamIdleCooldown, "grok stream idle timeout")
	if outputStarted {
		return nil
	}
	return grokStreamIdleFailoverError(account, idle)
}

// grokStreamIdleFailoverError builds a pre-commit/handler-visible failover so
// the gateway can switch OAuth accounts after a hung Grok upstream stream.
func grokStreamIdleFailoverError(account *Account, idle time.Duration) *UpstreamFailoverError {
	msg := fmt.Sprintf("Grok stream idle timeout after %s with no upstream data", idle.Round(time.Second))
	return &UpstreamFailoverError{
		StatusCode:               502,
		ResponseBody:             []byte(`{"error":{"code":"empty_upstream","message":"` + strings.ReplaceAll(msg, `"`, `'`) + `"}}`),
		SafeToFailoverAfterWrite: true,
		// An idle upstream stream is transient and should get the configured
		// same-account retry budget before switching credentials. This applies
		// to both pooled and dedicated Grok accounts; the handler still enforces
		// the request's retry limit.
		RetryableOnSameAccount: account != nil && account.Platform == PlatformGrok,
		RequestScopedTransient: true,
		SameAccountRetryMax:    1,
		// Permit at most one same-account replay after the idle failure. The
		// deadline is anchored at failure time, so a hung stream cannot consume
		// the normal three-attempt budget before failover.
		SameAccountRetryDeadline: time.Now().Add(idle),
	}
}
