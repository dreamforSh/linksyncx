// Package clashruntime adapts mihomo cores to service.ClashRuntime: the
// in-process engine compiled into sub2api (embedded) or an external sidecar
// driven through its REST controller.
package clashruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/clashcore"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

// New returns the runtime selected by clash_pool.mode, or nil when disabled.
func New(cfg *config.Config) service.ClashRuntime {
	if cfg == nil {
		return nil
	}
	switch cfg.ClashPool.Mode {
	case config.ClashPoolModeExternal:
		return newExternalRuntime(cfg.ClashPool.External.ControllerURL, cfg.ClashPool.External.Secret)
	case config.ClashPoolModeDisabled:
		return nil
	default:
		return &embeddedRuntime{dataDir: ResolveDataDir(cfg)}
	}
}

// ResolveDataDir returns the core working directory: clash_pool.data_dir, else
// $DATA_DIR/clash, else ./data/clash.
func ResolveDataDir(cfg *config.Config) string {
	if cfg != nil && strings.TrimSpace(cfg.ClashPool.DataDir) != "" {
		return filepath.Clean(cfg.ClashPool.DataDir)
	}
	base := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if base == "" {
		base = "./data"
	}
	return filepath.Join(base, "clash")
}

type embeddedRuntime struct {
	dataDir string
	mu      sync.RWMutex
	engine  *clashcore.Engine
}

var _ service.ClashRuntime = (*embeddedRuntime)(nil)

func (r *embeddedRuntime) Mode() string { return config.ClashPoolModeEmbedded }

func (r *embeddedRuntime) Start(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.engine != nil {
		return nil
	}
	log := logger.L().With(zap.String("component", "clash.core"))
	engine, err := clashcore.Open(clashcore.Options{
		HomeDir: r.dataDir,
		Log: func(level, message string) {
			if level == "error" {
				log.Error(message)
				return
			}
			log.Warn(message)
		},
	})
	if err != nil {
		return err
	}
	r.engine = engine
	return nil
}

func (r *embeddedRuntime) Stop(context.Context) error {
	r.mu.Lock()
	engine := r.engine
	r.engine = nil
	r.mu.Unlock()
	if engine == nil {
		return nil
	}
	return engine.Close()
}

func (r *embeddedRuntime) current() (*clashcore.Engine, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.engine == nil {
		return nil, service.ErrClashRuntimeUnavailable
	}
	return r.engine, nil
}

func (r *embeddedRuntime) Ready() bool {
	_, err := r.current()
	return err == nil
}

func (r *embeddedRuntime) Version() string {
	engine, err := r.current()
	if err != nil {
		return ""
	}
	return engine.Version()
}

func (r *embeddedRuntime) Apply(_ context.Context, payload []byte) error {
	engine, err := r.current()
	if err != nil {
		return err
	}
	err = engine.Apply(payload)
	var cfgErr *clashcore.ConfigError
	if errors.As(err, &cfgErr) {
		return &service.ClashConfigError{Message: cfgErr.Error()}
	}
	return err
}

func (r *embeddedRuntime) HasProxy(_ context.Context, name string) (bool, error) {
	engine, err := r.current()
	if err != nil {
		return false, err
	}
	return engine.HasProxy(name), nil
}

func (r *embeddedRuntime) DelayTest(ctx context.Context, proxyName, testURL string, timeout time.Duration) (time.Duration, error) {
	engine, err := r.current()
	if err != nil {
		return 0, err
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return engine.DelayTest(ctx, proxyName, testURL)
}

func (r *embeddedRuntime) CloseInboundConnections(_ context.Context, listenerNames []string) (int, error) {
	engine, err := r.current()
	if err != nil {
		return 0, err
	}
	return engine.CloseInboundConnections(listenerNames), nil
}
