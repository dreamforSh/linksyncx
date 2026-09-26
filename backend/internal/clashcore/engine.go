// Package clashcore embeds the mihomo (Clash.Meta) core in-process so a single
// sub2api deployment ships its own proxy kernel.
//
// mihomo keeps process-wide global state (tunnel, inbound listeners, resolver,
// log fan-out), so at most one Engine may be open per process.
package clashcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/common/observable"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	mlog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

const mihomoModulePath = "github.com/metacubex/mihomo"

var (
	ErrEngineOpen     = errors.New("clashcore: an engine is already open in this process")
	ErrEngineClosed   = errors.New("clashcore: engine closed")
	ErrProxyNotFound  = errors.New("clashcore: proxy not found")
	errApplyPanicked  = errors.New("clashcore: apply panicked")
	engineOpen        atomic.Bool
	emptyConfigYAML   = []byte("mode: rule\nlog-level: silent\nallow-lan: false\nprofile:\n  store-selected: false\n  store-fake-ip: false\nrules:\n  - MATCH,REJECT\n")
	logBurstPerSecond = 20
)

// LogFunc receives mihomo warnings and errors. level is "warn" or "error".
type LogFunc func(level, message string)

// Options configures an Engine.
type Options struct {
	// HomeDir is mihomo's working directory (cache and asset files). It is
	// created with mode 0700 when missing.
	HomeDir string
	// Log receives rate-limited mihomo warnings and errors. Info and debug
	// events are dropped because mihomo logs every proxied connection.
	Log LogFunc
}

// ConfigError reports a configuration mihomo refused to parse. The running
// configuration is left untouched when Apply returns it.
type ConfigError struct {
	Err error
}

func (e *ConfigError) Error() string { return e.Err.Error() }
func (e *ConfigError) Unwrap() error { return e.Err }

// Engine is the in-process mihomo core.
type Engine struct {
	mu      sync.Mutex
	closed  bool
	opts    Options
	sub     observable.Subscription[mlog.Event]
	logDone chan struct{}
	version string
}

// Open starts the in-process core with an empty configuration.
func Open(opts Options) (*Engine, error) {
	if !engineOpen.CompareAndSwap(false, true) {
		return nil, ErrEngineOpen
	}
	if opts.HomeDir != "" {
		if err := os.MkdirAll(opts.HomeDir, 0o700); err != nil {
			engineOpen.Store(false)
			return nil, fmt.Errorf("clashcore: create home dir: %w", err)
		}
		C.SetHomeDir(opts.HomeDir)
	}
	// mihomo mirrors its log events to logrus on stdout; keep stdout clean and
	// forward through the subscription instead.
	mlog.SetLevel(mlog.SILENT)

	e := &Engine{
		opts:    opts,
		sub:     mlog.Subscribe(),
		logDone: make(chan struct{}),
		version: moduleVersion(),
	}
	go e.pumpLogs()

	if err := e.Apply(emptyConfigYAML); err != nil {
		_ = e.Close()
		return nil, err
	}
	return e, nil
}

// Version reports the linked mihomo module version.
func (e *Engine) Version() string { return e.version }

// Validate parses payload without applying it.
func (e *Engine) Validate(payload []byte) error {
	if _, err := executor.ParseWithBytes(payload); err != nil {
		return &ConfigError{Err: err}
	}
	return nil
}

// Apply parses payload and hot-reloads it. Inbound listeners whose
// configuration did not change keep running, so established connections
// survive. mihomo briefly suspends the tunnel while applying, so callers should
// only apply when the configuration actually changed.
func (e *Engine) Apply(payload []byte) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrEngineClosed
	}
	cfg, parseErr := executor.ParseWithBytes(payload)
	if parseErr != nil {
		return &ConfigError{Err: parseErr}
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errApplyPanicked, r)
		}
	}()
	executor.ApplyConfig(cfg, true)
	// ApplyConfig resets the level from the payload; keep stdout silent.
	mlog.SetLevel(mlog.SILENT)
	return nil
}

// DelayTest measures a HEAD request to testURL through the named proxy.
func (e *Engine) DelayTest(ctx context.Context, proxyName, testURL string) (time.Duration, error) {
	if e.isClosed() {
		return 0, ErrEngineClosed
	}
	proxy, ok := tunnel.Proxies()[proxyName]
	if !ok {
		return 0, ErrProxyNotFound
	}
	ms, err := proxy.URLTest(ctx, testURL, nil)
	if err != nil {
		return 0, err
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// HasProxy reports whether the running configuration defines name (proxy or group).
func (e *Engine) HasProxy(name string) bool {
	if e.isClosed() {
		return false
	}
	_, ok := tunnel.Proxies()[name]
	return ok
}

// CloseInboundConnections closes tracked connections accepted by the named
// inbound listeners and returns how many were closed.
func (e *Engine) CloseInboundConnections(inboundNames []string) int {
	if len(inboundNames) == 0 {
		return 0
	}
	names := make(map[string]struct{}, len(inboundNames))
	for _, name := range inboundNames {
		names[name] = struct{}{}
	}
	closed := 0
	statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
		info := c.Info()
		if info == nil || info.Metadata == nil {
			return true
		}
		if _, ok := names[info.Metadata.InName]; ok {
			_ = c.Close()
			closed++
		}
		return true
	})
	return closed
}

// Close stops every listener and releases the engine slot.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	if cfg, err := executor.ParseWithBytes(emptyConfigYAML); err == nil {
		func() {
			defer func() { _ = recover() }()
			executor.ApplyConfig(cfg, true)
		}()
	}
	e.closed = true
	e.mu.Unlock()

	mlog.UnSubscribe(e.sub)
	<-e.logDone
	engineOpen.Store(false)
	return nil
}

func (e *Engine) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

func (e *Engine) pumpLogs() {
	defer close(e.logDone)
	var (
		windowStart time.Time
		emitted     int
		dropped     int
	)
	for event := range e.sub {
		if e.opts.Log == nil {
			continue
		}
		level := ""
		switch event.LogLevel {
		case mlog.WARNING:
			level = "warn"
		case mlog.ERROR:
			level = "error"
		default:
			continue
		}
		now := time.Now()
		if now.Sub(windowStart) >= time.Second {
			if dropped > 0 {
				e.opts.Log("warn", fmt.Sprintf("mihomo: %d log lines dropped by rate limit", dropped))
			}
			windowStart, emitted, dropped = now, 0, 0
		}
		if emitted >= logBurstPerSecond {
			dropped++
			continue
		}
		emitted++
		e.opts.Log(level, event.Payload)
	}
}

func moduleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == mihomoModulePath {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return "unknown"
}
