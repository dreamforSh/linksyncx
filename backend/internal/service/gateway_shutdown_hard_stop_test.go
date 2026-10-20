//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/shutdownctx"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// loopbackHTTPUpstream 把网关构造的上游请求改发到本地测试服务器，并用真实的
// net/http Transport 发送：要验证的正是“上游 context 被取消时 body 读取返回
// context.Canceled”这一真实传输层行为，不能用桩 body 代替。
type loopbackHTTPUpstream struct {
	target *url.URL
	client *http.Client
}

func (u *loopbackHTTPUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	req.URL.Scheme = u.target.Scheme
	req.URL.Host = u.target.Host
	req.Host = ""
	return u.client.Do(req)
}

func (u *loopbackHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

// markerStreamRecorder 在网关向客户端写出包含 marker 的内容时发信号，
// 让测试在上游事件确实被处理之后再触发断开/硬停止。
type markerStreamRecorder struct {
	*httptest.ResponseRecorder
	mu     sync.Mutex
	marker string
	seen   chan struct{}
	once   sync.Once
}

func newMarkerStreamRecorder(marker string) *markerStreamRecorder {
	return &markerStreamRecorder{ResponseRecorder: httptest.NewRecorder(), marker: marker, seen: make(chan struct{})}
}

func (w *markerStreamRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	n, err := w.ResponseRecorder.Write(p)
	seen := strings.Contains(w.Body.String(), w.marker)
	w.mu.Unlock()
	if seen {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func (w *markerStreamRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

type forwardOutcome struct {
	result *ForwardResult
	err    error
}

// 关停强制阶段触发硬停止后，与请求取消解耦的上游流必须走既有的 context.Canceled
// 分支结束，并把已观测到的 usage 作为部分结果返回给 handler 提交计费；在此之前，
// 客户端断开仍不能结束这条流（断开后继续读上游以完整计费）。
func TestGatewayForwardReturnsPartialUsageWhenShutdownHardStopsDetachedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamSSE := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-3-5-sonnet-latest","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":7}}}`,
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
		w.Header().Set("X-Request-Id", "rid-hard-stop")
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

	cfg := &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         &loopbackHTTPUpstream{target: target, client: &http.Client{Transport: transport}},
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	hardStop, fireHardStop := context.WithCancel(context.Background())
	defer fireHardStop()
	clientCtx, disconnectClient := context.WithCancel(shutdownctx.WithHardStop(context.Background(), hardStop))
	defer disconnectClient()

	rec := newMarkerStreamRecorder("message_delta")
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(clientCtx)
	body := []byte(`{"model":"claude-3-5-sonnet-latest","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	done := make(chan forwardOutcome, 1)
	go func() {
		result, err := svc.Forward(clientCtx, c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
		done <- forwardOutcome{result: result, err: err}
	}()
	select {
	case <-rec.seen:
	case outcome := <-done:
		t.Fatalf("Forward returned before relaying the stream: %+v", outcome)
	case <-time.After(5 * time.Second):
		t.Fatal("the gateway never relayed message_delta")
	}

	disconnectClient()
	select {
	case outcome := <-done:
		t.Fatalf("a client disconnect ended the detached upstream stream: %+v", outcome)
	case <-time.After(200 * time.Millisecond):
	}

	fireHardStop()
	var outcome forwardOutcome
	select {
	case outcome = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the hard-stop did not end the detached upstream stream")
	}
	require.ErrorIs(t, outcome.err, context.Canceled)
	require.NotNil(t, outcome.result, "the partial usage must be returned so the handler submits it")
	require.True(t, outcome.result.UsageIncomplete)
	require.True(t, outcome.result.Stream)
	require.Equal(t, 11, outcome.result.Usage.InputTokens)
	require.Equal(t, 7, outcome.result.Usage.CacheReadInputTokens)
	require.Equal(t, 5, outcome.result.Usage.OutputTokens)
	require.Equal(t, "rid-hard-stop", outcome.result.RequestID)
}

// cacheTTLFlipOnSendUpstream 在网关把上游请求交给传输层的瞬间（请求体已按旧开关构建完毕）
// 翻转全局 1h TTL 注入开关，再真实发往本地上游。翻转放在 Forward 所在的 goroutine：测试
// 收到 Forward 结果后再计费，与这次写入有 happens-before，不会和 repo 的 map 读写竞争。
type cacheTTLFlipOnSendUpstream struct {
	next HTTPUpstream
	flip func()
}

func (u *cacheTTLFlipOnSendUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.flip()
	return u.next.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *cacheTTLFlipOnSendUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.flip()
	return u.next.DoWithTLS(req, proxyURL, accountID, accountConcurrency, profile)
}

// 关停强制阶段截断的流走部分 usage 计费，这时也必须沿用构建上游请求时那一次 1h TTL
// 注入决策（Forward 给部分结果补上的 cacheTTL1hInjection），不能在计费时重读全局开关：
// 开关恰好在流进行中被切换时，客户端自己要的 1h 写入会被改记成 5m 少收。
// 完整结果的同类保证由 TestGatewayForward_CacheTTL1hInjectionSnapshot_BillingMatchesSentRequest 覆盖。
func TestGatewayForwardHardStopPartialUsageKeepsCacheTTL1hSnapshot(t *testing.T) {
	cases := []struct {
		name          string
		injectAtBuild bool
		// clientTTL 是客户端自己写在 cache_control 上的 ttl（空 = 不写）。
		clientTTL     string
		wantSnapshot  cacheTTL1hInjectionSnapshot
		wantBilledTTL string
	}{
		// 网关注入的 1h 按策略归回 5m 计费；流中改写已把它记成 5m，这里主要锁住快照本身。
		{name: "injected then switched off", injectAtBuild: true, wantSnapshot: cacheTTL1hInjectionApplied, wantBilledTTL: cacheTTLTarget5m},
		// 客户端自己要的 1h 按 1h 计费；计费若重读到已打开的开关，就会被改记成 5m。
		{name: "not injected then switched on", injectAtBuild: false, clientTTL: cacheTTLTarget1h, wantSnapshot: cacheTTL1hInjectionSkipped, wantBilledTTL: cacheTTLTarget1h},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			resetGatewayForwardingCache(t)
			repo := &gatewayTTLSettingRepo{data: map[string]string{
				SettingKeyEnableAnthropicCacheTTL1hInjection: strconv.FormatBool(tc.injectAtBuild),
			}}

			releaseUpstream := make(chan struct{})
			upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sent, _ := io.ReadAll(r.Body)
				// 与真实上游一致：cache_creation 按实际发出的 TTL 归类。
				v5m, v1h := cacheTTLSnapshotCreationTokens, 0
				if gjson.GetBytes(sent, "system.0.cache_control.ttl").String() == cacheTTLTarget1h {
					v5m, v1h = 0, cacheTTLSnapshotCreationTokens
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("X-Request-Id", "rid-hard-stop-ttl")
				_, _ = io.WriteString(w, "event: message_start\n"+
					`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":3,"cache_creation_input_tokens":`+strconv.Itoa(cacheTTLSnapshotCreationTokens)+
					`,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":`+strconv.Itoa(v5m)+`,"ephemeral_1h_input_tokens":`+strconv.Itoa(v1h)+`},"output_tokens":1}}}`+"\n\n"+
					"event: message_delta\n"+
					`data: {"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":5}}`+"\n\n")
				_ = http.NewResponseController(w).Flush()
				// 模型仍在生成：流保持打开，直到网关侧被硬停止。
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

			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
			svc.cfg.Gateway.MaxLineSize = defaultMaxLineSize
			svc.responseHeaderFilter = compileResponseHeaderFilter(svc.cfg)
			svc.httpUpstream = &cacheTTLFlipOnSendUpstream{
				next: &loopbackHTTPUpstream{target: target, client: &http.Client{Transport: transport}},
				flip: func() {
					repo.data[SettingKeyEnableAnthropicCacheTTL1hInjection] = strconv.FormatBool(!tc.injectAtBuild)
					// 让缓存立即过期，下一次读取落到 repo 的新值（等价于管理员保存后刷新缓存）。
					gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
				},
			}
			svc.rateLimitService = &RateLimitService{}
			svc.settingService = NewSettingService(repo, svc.cfg)

			hardStop, fireHardStop := context.WithCancel(context.Background())
			defer fireHardStop()
			clientCtx := shutdownctx.WithHardStop(context.Background(), hardStop)

			rec := newMarkerStreamRecorder("message_delta")
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(clientCtx)
			cacheControl := `{"type":"ephemeral"}`
			if tc.clientTTL != "" {
				cacheControl = `{"type":"ephemeral","ttl":"` + tc.clientTTL + `"}`
			}
			body := `{"model":"claude-sonnet-4-6","max_tokens":64,"stream":true,` +
				`"system":[{"type":"text","text":"sys","cache_control":` + cacheControl + `}],` +
				`"messages":[{"role":"user","content":"hi"}]}`
			parsed := parseAnthropicOAuthMappingRequest(t, body)
			account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)

			done := make(chan forwardOutcome, 1)
			go func() {
				// 真实 Claude Code 客户端不走 mimicry，请求体里的 cache_control 只受 TTL 注入影响。
				result, err := svc.Forward(SetClaudeCodeClient(clientCtx, true), c, account, parsed)
				done <- forwardOutcome{result: result, err: err}
			}()
			select {
			case <-rec.seen:
			case outcome := <-done:
				t.Fatalf("Forward returned before relaying the stream: %+v", outcome)
			case <-time.After(5 * time.Second):
				t.Fatal("the gateway never relayed message_delta")
			}

			fireHardStop()
			var outcome forwardOutcome
			select {
			case outcome = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the hard-stop did not end the detached upstream stream")
			}
			require.ErrorIs(t, outcome.err, context.Canceled)
			require.NotNil(t, outcome.result, "the partial usage must be returned so the handler submits it")
			require.True(t, outcome.result.UsageIncomplete)
			require.Equal(t, tc.wantSnapshot, outcome.result.cacheTTL1hInjection, "the partial result must carry the build-time decision")

			want5m, want1h := cacheTTLSnapshotCreationTokens, 0
			if tc.wantBilledTTL == cacheTTLTarget1h {
				want5m, want1h = 0, cacheTTLSnapshotCreationTokens
			}
			require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
				Result:  outcome.result,
				APIKey:  &APIKey{ID: 501, Quota: 100},
				User:    &User{ID: 601},
				Account: account,
			}))
			require.NotNil(t, usageRepo.lastLog)
			require.Equal(t, want5m, usageRepo.lastLog.CacheCreation5mTokens, "billed 5m cache creation tokens")
			require.Equal(t, want1h, usageRepo.lastLog.CacheCreation1hTokens, "billed 1h cache creation tokens")
		})
	}
}

func TestDetachUpstreamContextHelpersFollowShutdownHardStop(t *testing.T) {
	hardStop, fireHardStop := context.WithCancel(context.Background())
	request, disconnect := context.WithCancel(shutdownctx.WithHardStop(context.Background(), hardStop))
	defer disconnect()

	streamCtx, releaseStream := detachStreamUpstreamContext(request, true)
	defer releaseStream()
	upstreamCtx, releaseUpstream := detachUpstreamContext(request)
	defer releaseUpstream()
	nonStreamCtx, releaseNonStream := detachStreamUpstreamContext(request, false)
	defer releaseNonStream()
	require.Equal(t, request, nonStreamCtx, "non-stream requests keep following the client context")

	// release 在发出请求前就会被调用（见各调用点），不能因此取消上游。
	releaseStream()
	releaseUpstream()
	disconnect()
	require.NoError(t, streamCtx.Err())
	require.NoError(t, upstreamCtx.Err())

	fireHardStop()
	require.ErrorIs(t, streamCtx.Err(), context.Canceled)
	require.ErrorIs(t, upstreamCtx.Err(), context.Canceled)
}
