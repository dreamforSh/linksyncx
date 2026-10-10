//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type reproUpstreamCall struct {
	accountID   int64
	sessionHdr  string
	metadataUID string
	concurrency int
	// concurrencyStream 是读取流式响应体时的并发数
	concurrencyStream int
}

type reproUpstream struct {
	mu       sync.Mutex
	calls    []reproUpstreamCall
	concSvc  *service.ConcurrencyService
	accounts []int64
}

func (u *reproUpstream) record(req *http.Request, accountID int64) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	counts, _ := u.concSvc.GetAccountConcurrencyBatch(context.Background(), u.accounts)
	u.mu.Lock()
	u.calls = append(u.calls, reproUpstreamCall{
		accountID:   accountID,
		sessionHdr:  req.Header.Get("X-Claude-Code-Session-Id"),
		metadataUID: gjson.GetBytes(body, "metadata.user_id").String(),
		concurrency: counts[accountID],
	})
	u.mu.Unlock()
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	idx := len(u.calls) - 1
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       &reproStreamBody{r: strings.NewReader(sse), u: u, idx: idx, accountID: accountID},
		Request:    req,
	}, nil
}

type reproStreamBody struct {
	r         io.Reader
	u         *reproUpstream
	idx       int
	accountID int64
	sampled   bool
}

func (b *reproStreamBody) Read(p []byte) (int, error) {
	if !b.sampled {
		b.sampled = true
		counts, _ := b.u.concSvc.GetAccountConcurrencyBatch(context.Background(), b.u.accounts)
		b.u.mu.Lock()
		b.u.calls[b.idx].concurrencyStream = counts[b.accountID]
		b.u.mu.Unlock()
	}
	return b.r.Read(p)
}
func (b *reproStreamBody) Close() error { return nil }

func (u *reproUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	return u.record(req, accountID)
}
func (u *reproUpstream) DoWithTLS(req *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.record(req, accountID)
}

func TestReproSessionAndConcurrency(t *testing.T)      { reproRun(t, true) }
func TestReproSessionAndConcurrencyNonCC(t *testing.T) { reproRun(t, false) }

func reproRun(t *testing.T, cc bool) {
	gin.SetMode(gin.TestMode)
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	groupID := int64(2001)
	group := &service.Group{ID: groupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	mk := func(id int64) *service.Account {
		return &service.Account{
			ID: id, Name: "acc", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "tok"},
			Extra:       map[string]any{"account_uuid": "11111111-2222-3333-4444-55555555555" + string(rune('0'+id%10))},
			Concurrency: 5, Priority: 1, Status: service.StatusActive, Schedulable: true,
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
			GroupIDs:      []int64{groupID},
		}
	}
	accounts := []*service.Account{mk(1001), mk(1002), mk(1003)}

	concCache := repository.NewConcurrencyCache(rdb, 15, 0)
	concSvc := service.NewConcurrencyService(concCache)
	identity := service.NewIdentityService(repository.NewIdentityCache(rdb))
	upstream := &reproUpstream{concSvc: concSvc, accounts: []int64{1001, 1002, 1003}}

	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
	gwSvc := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil,
		repository.NewGatewayCache(rdb), cfg, schedulerSnapshot, concSvc,
		nil, nil, nil, identity, upstream, nil, nil,
		repository.NewSessionLimitCache(rdb, 5), repository.NewRPMCache(rdb),
		nil, service.NewSettingService(&oauthCaptchaSettingRepo{values: map[string]string{}}, cfg), nil, nil, nil, nil, nil, nil,
	)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	defer billingCacheSvc.Stop()
	h := &GatewayHandler{
		gatewayService:           gwSvc,
		billingCacheService:      billingCacheSvc,
		concurrencyHelper:        NewConcurrencyHelper(concSvc, SSEPingFormatClaude, 0),
		maxAccountSwitches:       3,
		maxAccountSwitchesGemini: 1,
		cfg:                      cfg,
	}

	send := func(body string) int {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		if cc {
			req.Header.Set("User-Agent", "claude-cli/2.1.293 (external, cli)")
			req.Header.Set("X-Claude-Code-Session-Id", "c0ffee00-1111-4222-8333-444455556666")
			req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
		} else {
			req.Header.Set("User-Agent", "Anthropic/Python 0.40.0")
		}
		req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
		c.Request = req
		apiKey := &service.APIKey{ID: 3001, UserID: 4001, GroupID: &groupID, Status: service.StatusActive,
			User: &service.User{ID: 4001, Concurrency: 10, Balance: 100}, Group: group}
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
		h.Messages(c)
		if rec.Code != 200 {
			t.Logf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Code
	}

	uid := `{\"device_id\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"account_uuid\":\"\",\"session_id\":\"c0ffee00-1111-4222-8333-444455556666\"}`
	sys := `[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.293.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}]`
	body1 := `{"model":"claude-sonnet-4-5","max_tokens":256,"stream":true,"system":` + sys + `,"metadata":{"user_id":"` + uid + `"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	body2 := `{"model":"claude-sonnet-4-5","max_tokens":256,"stream":true,"system":` + sys + `,"metadata":{"user_id":"` + uid + `"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"text","text":"again"}]}]}`

	if !cc {
		body1 = `{"model":"claude-sonnet-4-5","max_tokens":256,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
		body2 = `{"model":"claude-sonnet-4-5","max_tokens":256,"stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":[{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"text","text":"again"}]}]}`
	}
	require.Equal(t, 200, send(body1))
	require.Equal(t, 200, send(body2))
	require.Equal(t, 200, send(body2))

	for i, call := range upstream.calls {
		t.Logf("call %d: account=%d stream_conc=%d concurrency_during=%d session_hdr=%s metadata=%s", i, call.accountID, call.concurrencyStream, call.concurrency, call.sessionHdr, call.metadataUID)
	}
}
