//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const proxyBindingSwitchResponse = `{"id":"resp_proxy_switch","object":"response","model":"gpt-5.1","status":"completed",` +
	`"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],` +
	`"usage":{"input_tokens":1,"output_tokens":1}}`

// proxyBindingSwitchUpstream 记录每次真正出站的账号，并一律返回成功响应（sse 为真时按
// Responses SSE 返回：Chat Completions 经 OAuth 转发时上游总是流式）。
type proxyBindingSwitchUpstream struct {
	service.HTTPUpstream
	sse        bool
	mu         sync.Mutex
	accountIDs []int64
}

func (u *proxyBindingSwitchUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	u.mu.Unlock()
	if u.sse {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(bytes.NewBufferString("event: response.completed\n" +
				`data: {"type":"response.completed","response":` + proxyBindingSwitchResponse + "}\n\n")),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(proxyBindingSwitchResponse)),
	}, nil
}

func (u *proxyBindingSwitchUpstream) calls() []int64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int64(nil), u.accountIDs...)
}

// brokenBindingAccountPool：首选账号 1 绑定失效（由 bind 决定是 URL 无效还是关系缺失），账号 2 健康。
func brokenBindingAccountPool(bind func(*service.Account)) []service.Account {
	accounts := []service.Account{
		{ID: 1, Name: "broken-binding", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Priority: 0,
			Credentials: map[string]any{"access_token": "token-1"}},
		{ID: 2, Name: "healthy", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"access_token": "token-2"}},
	}
	bind(&accounts[0])
	return accounts
}

func brokenBindingCases() []struct {
	name string
	bind func(*service.Account)
} {
	proxyID := int64(9)
	return []struct {
		name string
		bind func(*service.Account)
	}{
		{name: "invalid url", bind: func(a *service.Account) {
			id := proxyID
			a.ProxyID = &id
			a.Proxy = &service.Proxy{ID: id, Name: "typo-proxy", Protocol: "http", Host: "bad host", Port: 8080}
		}},
		{name: "missing relation", bind: func(a *service.Account) {
			id := proxyID
			a.ProxyID = &id
			a.Proxy = nil
		}},
	}
}

// 首选账号的代理绑定失效时，请求必须换到健康账号完成（fail-closed：失效账号一次都不出站），
// 而不是在第一个账号上以终态 502 结束——proxy-never-direct 对数据面的要求是“拒绝直连且换号”。
func TestOpenAIGatewayHandlerResponses_BrokenProxyBindingSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range brokenBindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &proxyBindingSwitchUpstream{}
			handler := newOpenAIResponsesFailoverTestHandlerWithAccounts(t, upstream, brokenBindingAccountPool(tc.bind))
			c, rec := newOpenAIResponsesFailoverTestContext(t, nil)

			handler.Responses(c)

			require.Equal(t, []int64{2}, upstream.calls(), "the broken account must never reach upstream; the healthy one serves the request")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "completed", gjson.GetBytes(rec.Body.Bytes(), "status").String())
		})
	}
}

// Chat Completions 入口走同一个 OpenAIGatewayService，但是独立的 handler 换号循环。
func TestOpenAIGatewayHandlerChatCompletions_BrokenProxyBindingSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range brokenBindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &proxyBindingSwitchUpstream{sse: true}
			handler := newOpenAIResponsesFailoverTestHandlerWithAccounts(t, upstream, brokenBindingAccountPool(tc.bind))
			c, rec := newOpenAIChatCompletionsProxyBindingContext()

			handler.ChatCompletions(c)

			require.Equal(t, []int64{2}, upstream.calls(), "the broken account must never reach upstream; the healthy one serves the request")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, "ok", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
		})
	}
}

func newOpenAIChatCompletionsProxyBindingContext() (*gin.Context, *httptest.ResponseRecorder) {
	groupID := int64(3133)
	body := []byte(`{"model":"gpt-5.1","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID:      99,
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		User:    &service.User{ID: 100},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	return c, rec
}
