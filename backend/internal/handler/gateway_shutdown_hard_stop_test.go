//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/shutdownctx"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type shutdownHardStopUsageRepo struct {
	service.UsageLogRepository
	mu   sync.Mutex
	logs []*service.UsageLog
}

func (r *shutdownHardStopUsageRepo) Create(_ context.Context, log *service.UsageLog) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logs = append(r.logs, log)
	return true, nil
}

func (r *shutdownHardStopUsageRepo) recorded() []*service.UsageLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*service.UsageLog(nil), r.logs...)
}

// shutdownLoopbackUpstream 把网关构造的上游请求改发到本地测试服务器，并用真实的
// net/http Transport 发送：硬停止要经由真实传输层让 body 读取返回 context.Canceled，
// 桩 body 测不出这一点。
type shutdownLoopbackUpstream struct {
	target *url.URL
	client *http.Client
}

func (u *shutdownLoopbackUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req.URL.Scheme = u.target.Scheme
	req.URL.Host = u.target.Host
	req.Host = ""
	return u.client.Do(req)
}

func (u *shutdownLoopbackUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

// shutdownRelayRecorder 在网关把包含 marker 的内容写给客户端时发信号，
// 让测试在上游 usage 确实被处理之后再断开客户端、触发硬停止。
type shutdownRelayRecorder struct {
	*httptest.ResponseRecorder
	mu     sync.Mutex
	marker string
	seen   chan struct{}
	once   sync.Once
}

func (w *shutdownRelayRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.ResponseRecorder.Write(p)
	seen := strings.Contains(w.Body.String(), w.marker)
	w.mu.Unlock()
	if seen {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *shutdownRelayRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// 关停强制阶段（主服务 cancel 请求 context 的根，同时即关停硬停止）必须让与请求取消
// 解耦的上游流结束，并由 handler 把已观测到的 usage 作为不完整记录提交——否则这次
// 重启截断的流一分钱都不计。在此之前，单纯的客户端断开不能结束这条流（断开后继续
// 读完上游以完整计费），这是既有语义。
func TestGatewayMessagesSubmitsPartialUsageWhenShutdownHardStopsDetachedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_hard_stop","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`,
		"",
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		"",
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":5}}`,
		"",
		"",
	}, "\n")
	releaseUpstream := make(chan struct{})
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "rid-shutdown-hard-stop")
		_, _ = io.WriteString(w, upstreamSSE)
		_ = http.NewResponseController(w).Flush()
		// 模型仍在生成：流保持打开，直到网关侧断开。
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	t.Cleanup(func() {
		close(releaseUpstream)
		upstreamServer.Close()
	})
	target, err := url.Parse(upstreamServer.URL)
	require.NoError(t, err)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)

	group := &service.Group{ID: 19114, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1}
	account := &service.Account{
		ID: 19115, Name: "shutdown-hard-stop", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
		Credentials:   map[string]any{"access_token": "oauth-token"},
		Concurrency:   1,
		Priority:      1,
		Status:        service.StatusActive,
		Schedulable:   true,
		AccountGroups: []service.AccountGroup{{AccountID: 19115, GroupID: group.ID}},
		GroupIDs:      []int64{group.ID},
	}
	apiKey := &service.APIKey{
		ID: 19116, UserID: 19117, GroupID: &group.ID, Status: service.StatusActive, Group: group,
		User: &service.User{ID: 19117, Concurrency: 3, Balance: 100},
	}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxLineSize = 10 * 1024 * 1024
	repo := &shutdownHardStopUsageRepo{}
	snapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: []*service.Account{account}}, nil, nil, nil, nil)
	billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	pool := service.NewUsageRecordWorkerPoolWithOptions(service.UsageRecordWorkerPoolOptions{
		WorkerCount: 1, QueueSize: 8, TaskTimeout: time.Second,
	})
	t.Cleanup(pool.Stop)
	gateway := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, repo, nil, nil, nil, nil, nil, cfg, snapshot,
		nil, service.NewBillingService(cfg, nil), nil, billingCache, nil,
		&shutdownLoopbackUpstream{target: target, client: &http.Client{Transport: transport}},
		service.NewDeferredService(nil, nil, time.Hour),
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{
		gatewayService: gateway, cfg: cfg, billingCacheService: billingCache, usageRecordWorkerPool: pool,
		concurrencyHelper:        NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:       1,
		maxAccountSwitchesGemini: 1,
	}

	// 与 cmd/server 的 installHTTPDrain 一致：请求 context 派生自 forceCtx，forceCtx 同时
	// 作为关停硬停止挂在请求 context 上；强制阶段 cancel 它一次即同时取消两者。
	forceCtx, enterForcedPhase := context.WithCancel(context.Background())
	defer enterForcedPhase()
	clientCtx, disconnectClient := context.WithCancel(context.WithValue(shutdownctx.WithHardStop(forceCtx, forceCtx), ctxkey.Group, group))
	defer disconnectClient()

	rec := &shutdownRelayRecorder{ResponseRecorder: httptest.NewRecorder(), marker: "message_delta", seen: make(chan struct{})}
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body)).WithContext(clientCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "shutdown-test-client/1.0")
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: apiKey.User.Concurrency})

	handlerReturned := make(chan struct{})
	go func() {
		defer close(handlerReturned)
		h.Messages(c)
	}()
	select {
	case <-rec.seen:
	case <-handlerReturned:
		t.Fatalf("handler returned before relaying the stream: status=%d body=%s", rec.Code, rec.Body.String())
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway never relayed message_delta")
	}

	disconnectClient()
	select {
	case <-handlerReturned:
		t.Fatal("a client disconnect ended the detached upstream stream")
	case <-time.After(200 * time.Millisecond):
	}
	require.Empty(t, repo.recorded(), "usage must not be submitted while the detached stream is still draining")

	enterForcedPhase()
	select {
	case <-handlerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown hard-stop did not end the detached upstream stream")
	}
	// Cleanup 中 UsageRecordWorkerPool.Stop 会先排空队列：这里同样先停池，再检查落库。
	pool.Stop()

	logs := repo.recorded()
	require.Len(t, logs, 1, "the partial usage of the hard-stopped stream must be submitted exactly once")
	require.True(t, logs[0].UsageIncomplete)
	require.True(t, logs[0].Stream)
	require.Equal(t, account.ID, logs[0].AccountID)
	require.Equal(t, 11, logs[0].InputTokens)
	require.Equal(t, 7, logs[0].CacheReadTokens)
	require.Equal(t, 5, logs[0].OutputTokens)
	require.Equal(t, "rid-shutdown-hard-stop", logs[0].RequestID)
}
