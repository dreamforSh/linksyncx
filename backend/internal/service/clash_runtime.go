package service

import (
	"context"
	"errors"
	"time"
)

// ClashConfigError reports a configuration the core refused. The running
// configuration is unchanged when it is returned.
type ClashConfigError struct {
	Message string
}

func (e *ClashConfigError) Error() string { return "clash core rejected configuration: " + e.Message }

// ErrClashRuntimeUnavailable is returned when no core is running.
var ErrClashRuntimeUnavailable = errors.New("clash core is unavailable")

// ClashRuntime drives one mihomo core: the in-process engine (embedded) or an
// external sidecar reached through its REST controller.
type ClashRuntime interface {
	Mode() string
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
	Version() string
	// Apply replaces the running configuration. It returns *ClashConfigError
	// when the core rejects the payload.
	Apply(ctx context.Context, payload []byte) error
	// HasProxy reports whether the running configuration defines name; used
	// with the configuration marker to detect drift (e.g. sidecar restarts).
	HasProxy(ctx context.Context, name string) (bool, error)
	DelayTest(ctx context.Context, proxyName, testURL string, timeout time.Duration) (time.Duration, error)
	// CloseInboundConnections drops established connections accepted by the
	// named listeners, so disabled nodes stop carrying traffic immediately.
	CloseInboundConnections(ctx context.Context, listenerNames []string) (int, error)
}
