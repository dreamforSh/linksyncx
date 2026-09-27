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

// ClashConnection is one connection tracked by the core. The byte counters are
// cumulative since the core accepted it; the core forgets them on close.
type ClashConnection struct {
	ID string
	// Inbound is the name of the listener that accepted the connection.
	Inbound  string
	Upload   int64
	Download int64
	Start    time.Time
}

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
	// Connections lists the open connections accepted by named listeners,
	// used to sample per-node traffic.
	Connections(ctx context.Context) ([]ClashConnection, error)
}
