//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 本文件覆盖"全局 1h cache TTL 注入"开关的请求级快照：构建上游请求、改写响应 usage、
// 计费三处必须使用同一次决策。开关来自进程级设置缓存，管理员切换或设置读取失败（缓存
// 回落默认值 5s）都可能落在请求构建与计费之间；各处若各自实时读取，发出去的是网关注入
// 的 1h 却按 1h 计费（应归回 5m），或客户端自己要的 1h 被改写成 5m 少收。

const cacheTTLSnapshotCreationTokens = 1000

// cacheTTLFlipUpstream 在收到上游请求的瞬间翻转开关，模拟"请求已按旧值构建并发出，
// 响应处理与计费读到新值"的窗口；响应 usage 按实际发出的 TTL 归类，与真实上游一致。
type cacheTTLFlipUpstream struct {
	stream   bool
	flip     func()
	lastBody []byte
}

func (u *cacheTTLFlipUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req != nil && req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		u.lastBody = b
		req.Body = io.NopCloser(bytes.NewReader(b))
	}
	if u.flip != nil {
		u.flip()
	}

	v5m, v1h := cacheTTLSnapshotCreationTokens, 0
	if gjson.GetBytes(u.lastBody, "system.0.cache_control.ttl").String() == cacheTTLTarget1h {
		v5m, v1h = 0, cacheTTLSnapshotCreationTokens
	}
	usage := `{"input_tokens":3,"cache_creation_input_tokens":` + strconv.Itoa(cacheTTLSnapshotCreationTokens) +
		`,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":` + strconv.Itoa(v5m) +
		`,"ephemeral_1h_input_tokens":` + strconv.Itoa(v1h) + `},"output_tokens":2}`

	if !u.stream {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"rid-cache-ttl-snapshot"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",` +
				`"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":` + usage + `}`)),
		}, nil
	}
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"usage":` + usage + "}}\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"rid-cache-ttl-snapshot"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}, nil
}

func (u *cacheTTLFlipUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGatewayForward_CacheTTL1hInjectionSnapshot_BillingMatchesSentRequest(t *testing.T) {
	cases := []struct {
		name          string
		injectAtBuild bool
		// clientTTL 是客户端自己写在 cache_control 上的 ttl（空 = 不写）。
		clientTTL     string
		wantSentTTL   string
		wantBilledTTL string
	}{
		// 构建时开关开：网关把 ttl 强制写成 1h，按策略计费归回 5m；开关随后被关掉也不能按 1h 收。
		{name: "injected then switched off", injectAtBuild: true, wantSentTTL: cacheTTLTarget1h, wantBilledTTL: cacheTTLTarget5m},
		// 构建时开关关：1h 是客户端自己要的，按 1h 计费；开关随后被打开也不能改写成 5m。
		{name: "not injected then switched on", injectAtBuild: false, clientTTL: cacheTTLTarget1h, wantSentTTL: cacheTTLTarget1h, wantBilledTTL: cacheTTLTarget1h},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+"/stream="+strconv.FormatBool(stream), func(t *testing.T) {
				resetGatewayForwardingCache(t)
				repo := &gatewayTTLSettingRepo{data: map[string]string{
					SettingKeyEnableAnthropicCacheTTL1hInjection: strconv.FormatBool(tc.injectAtBuild),
				}}
				upstream := &cacheTTLFlipUpstream{stream: stream}
				upstream.flip = func() {
					repo.data[SettingKeyEnableAnthropicCacheTTL1hInjection] = strconv.FormatBool(!tc.injectAtBuild)
					// 让缓存立即过期，下一次读取落到 repo 的新值（等价于管理员保存后刷新缓存）。
					gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
				}

				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				svc := newGatewayRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
				svc.cfg.Gateway.MaxLineSize = defaultMaxLineSize
				svc.responseHeaderFilter = compileResponseHeaderFilter(svc.cfg)
				svc.httpUpstream = upstream
				svc.rateLimitService = &RateLimitService{}
				svc.settingService = NewSettingService(repo, svc.cfg)

				cacheControl := `{"type":"ephemeral"}`
				if tc.clientTTL != "" {
					cacheControl = `{"type":"ephemeral","ttl":"` + tc.clientTTL + `"}`
				}
				body := `{"model":"claude-sonnet-4-6","max_tokens":64,"stream":` + strconv.FormatBool(stream) +
					`,"system":[{"type":"text","text":"sys","cache_control":` + cacheControl + `}],` +
					`"messages":[{"role":"user","content":"hi"}]}`
				c, rec := newAnthropicOAuthMappingTestContext("/v1/messages")
				parsed := parseAnthropicOAuthMappingRequest(t, body)
				account := newAnthropicOAuthMappingAccount(AccountTypeOAuth, nil)

				// 真实 Claude Code 客户端不走 mimicry，请求体里的 cache_control 只受 TTL 注入影响。
				result, err := svc.Forward(SetClaudeCodeClient(context.Background(), true), c, account, parsed)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Equal(t, tc.wantSentTTL, gjson.GetBytes(upstream.lastBody, "system.0.cache_control.ttl").String())

				want5m, want1h := cacheTTLSnapshotCreationTokens, 0
				if tc.wantBilledTTL == cacheTTLTarget1h {
					want5m, want1h = 0, cacheTTLSnapshotCreationTokens
				}

				require.NoError(t, svc.RecordUsage(context.Background(), &RecordUsageInput{
					Result:  result,
					APIKey:  &APIKey{ID: 501, Quota: 100},
					User:    &User{ID: 601},
					Account: account,
				}))
				require.NotNil(t, usageRepo.lastLog)
				require.Equal(t, want5m, usageRepo.lastLog.CacheCreation5mTokens, "billed 5m cache creation tokens")
				require.Equal(t, want1h, usageRepo.lastLog.CacheCreation1hTokens, "billed 1h cache creation tokens")

				// 客户端看到的 usage 与计费口径一致。
				clientBody := rec.Body.String()
				require.Contains(t, clientBody, `"ephemeral_5m_input_tokens":`+strconv.Itoa(want5m))
				require.Contains(t, clientBody, `"ephemeral_1h_input_tokens":`+strconv.Itoa(want1h))
			})
		}
	}
}
