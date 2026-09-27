package clashruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	externalRequestTimeout = 15 * time.Second
	externalApplyTimeout   = 60 * time.Second
	externalStartWait      = 10 * time.Second
	externalMaxBodyBytes   = 8 << 20
)

// externalRuntime drives a mihomo sidecar through its REST controller. The
// sidecar's own startup config only needs external-controller and secret;
// everything else is pushed as a payload.
type externalRuntime struct {
	base    string
	secret  string
	client  *http.Client
	ready   atomic.Bool
	mu      sync.RWMutex
	version string
}

var _ service.ClashRuntime = (*externalRuntime)(nil)

func newExternalRuntime(controllerURL, secret string) *externalRuntime {
	return &externalRuntime{
		base:   strings.TrimRight(strings.TrimSpace(controllerURL), "/"),
		secret: secret,
		client: &http.Client{Timeout: externalApplyTimeout},
	}
}

func (r *externalRuntime) Mode() string { return config.ClashPoolModeExternal }

func (r *externalRuntime) Start(ctx context.Context) error {
	deadline := time.Now().Add(externalStartWait)
	var lastErr error
	for {
		if err := r.refreshVersion(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("external clash controller not reachable: %w", lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *externalRuntime) Stop(context.Context) error {
	r.ready.Store(false)
	return nil
}

func (r *externalRuntime) Ready() bool { return r.ready.Load() }

func (r *externalRuntime) Version() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

func (r *externalRuntime) refreshVersion(ctx context.Context) error {
	var body struct {
		Version string `json:"version"`
	}
	if _, err := r.do(ctx, http.MethodGet, "/version", nil, &body, externalRequestTimeout); err != nil {
		r.ready.Store(false)
		return err
	}
	r.mu.Lock()
	r.version = body.Version
	r.mu.Unlock()
	r.ready.Store(true)
	return nil
}

func (r *externalRuntime) Apply(ctx context.Context, payload []byte) error {
	reqBody, err := json.Marshal(map[string]string{"path": "", "payload": string(payload)})
	if err != nil {
		return err
	}
	status, err := r.do(ctx, http.MethodPut, "/configs?force=true", reqBody, nil, externalApplyTimeout)
	if err != nil {
		var apiErr *controllerError
		if errors.As(err, &apiErr) && status == http.StatusBadRequest {
			return &service.ClashConfigError{Message: apiErr.Message}
		}
		return err
	}
	r.ready.Store(true)
	return nil
}

func (r *externalRuntime) HasProxy(ctx context.Context, name string) (bool, error) {
	status, err := r.do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(name), nil, nil, externalRequestTimeout)
	if status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *externalRuntime) DelayTest(ctx context.Context, proxyName, testURL string, timeout time.Duration) (time.Duration, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	query := url.Values{}
	query.Set("url", testURL)
	query.Set("timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
	var body struct {
		Delay int `json:"delay"`
	}
	path := "/proxies/" + url.PathEscape(proxyName) + "/delay?" + query.Encode()
	if _, err := r.do(ctx, http.MethodGet, path, nil, &body, timeout+5*time.Second); err != nil {
		return 0, err
	}
	return time.Duration(body.Delay) * time.Millisecond, nil
}

func (r *externalRuntime) CloseInboundConnections(ctx context.Context, listenerNames []string) (int, error) {
	if len(listenerNames) == 0 {
		return 0, nil
	}
	names := make(map[string]struct{}, len(listenerNames))
	for _, name := range listenerNames {
		names[name] = struct{}{}
	}
	var body struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				InboundName string `json:"inboundName"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if _, err := r.do(ctx, http.MethodGet, "/connections", nil, &body, externalRequestTimeout); err != nil {
		return 0, err
	}
	closed := 0
	for _, conn := range body.Connections {
		if _, ok := names[conn.Metadata.InboundName]; !ok || conn.ID == "" {
			continue
		}
		if _, err := r.do(ctx, http.MethodDelete, "/connections/"+url.PathEscape(conn.ID), nil, nil, externalRequestTimeout); err == nil {
			closed++
		}
	}
	return closed, nil
}

func (r *externalRuntime) Connections(ctx context.Context) ([]service.ClashConnection, error) {
	var body struct {
		Connections []struct {
			ID       string `json:"id"`
			Upload   int64  `json:"upload"`
			Download int64  `json:"download"`
			Start    string `json:"start"`
			Metadata struct {
				InboundName string `json:"inboundName"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if _, err := r.do(ctx, http.MethodGet, "/connections", nil, &body, externalRequestTimeout); err != nil {
		return nil, err
	}
	out := make([]service.ClashConnection, 0, len(body.Connections))
	for _, conn := range body.Connections {
		if conn.ID == "" || conn.Metadata.InboundName == "" {
			continue
		}
		// A malformed start time only costs the pre-sampler baseline check.
		start, _ := time.Parse(time.RFC3339Nano, conn.Start)
		out = append(out, service.ClashConnection{
			ID:       conn.ID,
			Inbound:  conn.Metadata.InboundName,
			Upload:   conn.Upload,
			Download: conn.Download,
			Start:    start,
		})
	}
	return out, nil
}

type controllerError struct {
	Status  int
	Message string
}

func (e *controllerError) Error() string {
	return fmt.Sprintf("clash controller returned %d: %s", e.Status, e.Message)
}

func (r *externalRuntime) do(ctx context.Context, method, path string, body []byte, out any, timeout time.Duration) (int, error) {
	if r.base == "" {
		return 0, service.ErrClashRuntimeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.secret != "" {
		req.Header.Set("Authorization", "Bearer "+r.secret)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, externalMaxBodyBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		message := strings.TrimSpace(apiErr.Message)
		if message == "" {
			message = strings.TrimSpace(string(raw))
		}
		return resp.StatusCode, &controllerError{Status: resp.StatusCode, Message: message}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode clash controller response: %w", err)
		}
	}
	return resp.StatusCode, nil
}
