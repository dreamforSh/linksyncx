package codebuddy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func fakeJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestProfileForAuth(t *testing.T) {
	t.Parallel()
	cnIssuer := fakeJWT(map[string]any{"iss": "https://copilot.tencent.com/auth/realms/copilot"})
	intlIssuer := fakeJWT(map[string]any{"iss": "https://www.workbuddy.ai/auth/realms/copilot"})

	cases := []struct {
		domain, token, want string
	}{
		{"", "", ProfileCNCLI},
		{"www.codebuddy.cn", cnIssuer, ProfileCNCLI},
		{"www.workbuddy.cn", cnIssuer, ProfileCNWork},
		{"www.codebuddy.ai", "", ProfileIntlCLI},
		{"", intlIssuer, ProfileIntlWork},
		{"https://www.workbuddy.ai", intlIssuer, ProfileIntlWork},
	}
	for _, tc := range cases {
		got, err := ProfileForAuth(tc.domain, tc.token)
		require.NoError(t, err, tc.domain)
		require.Equal(t, tc.want, got, tc.domain)
	}

	_, err := ProfileForAuth("www.codebuddy.cn", intlIssuer)
	require.ErrorContains(t, err, "conflicting credential sites")
	_, err = ProfileForAuth("www.codebuddy.ai", intlIssuer)
	require.ErrorContains(t, err, "conflicting credential products")
	_, err = ProfileForAuth("evil.example.com", "")
	require.Error(t, err)
	_, err = ProfileForAuth("https://www.codebuddy.cn:8443", "")
	require.Error(t, err)
}

func TestEndpoints(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://copilot.tencent.com/v2/chat/completions", ChatCompletionsURL(ProfileCNCLI))
	require.Equal(t, "https://www.workbuddy.cn/v2/plugin/auth/token/refresh", RefreshURL(ProfileCNWork))
	require.Equal(t, "https://www.codebuddy.ai/v3/config", ConfigURL(ProfileIntlCLI))
	require.Equal(t, "https://www.codebuddy.cn", BillingHost(ProfileCNCLI))
	require.Equal(t, "https://www.workbuddy.ai", BillingHost(ProfileIntlWork))
}

func TestHeaders(t *testing.T) {
	t.Parallel()
	id := Identity{Profile: ProfileIntlWork, AccessToken: "tok", Domain: "www.workbuddy.ai", UID: "u1", EnterpriseID: "e1"}
	chat := CredentialHeaders(id)
	require.Equal(t, "WorkBuddy/5.5.2 WorkBuddy AI/5.5.2 CLI/2.137.1", chat["User-Agent"])
	require.Equal(t, "Bearer tok", chat["Authorization"])
	require.Equal(t, "e1", chat["X-Tenant-Id"])
	require.Equal(t, "craft", chat["X-Agent-Intent"])
	require.Equal(t, "www.workbuddy.ai", chat["X-Domain"])

	catalog := CatalogHeaders(id)
	require.NotContains(t, catalog, "x-stainless-os")
	require.NotContains(t, catalog, "X-Agent-Intent")
	require.NotContains(t, catalog, "x-client-platform", "WorkBuddy must not request the CLI catalog")
	require.Equal(t, "close", catalog["Connection"])

	cli := CatalogHeaders(Identity{Profile: ProfileCNCLI, AccessToken: "tok"})
	require.Equal(t, "cli", cli["x-client-platform"])
	require.Equal(t, CLIUserAgent, cli["User-Agent"])
	require.Equal(t, DefaultDomain, cli["X-Domain"])

	refresh := RefreshHeaders(id, "rt")
	require.Equal(t, "rt", refresh["X-Refresh-Token"])
	require.Equal(t, "plugin", refresh["X-Auth-Refresh-Source"])
	require.Equal(t, "Bearer tok", refresh["Authorization"])

	for _, headers := range []map[string]string{chat, catalog, refresh, OAuthHeaders(), WebHeaders(BillingHost(id.Profile), id)} {
		for key, value := range headers {
			require.NotContains(t, strings.ToLower(key+value), "codebuddy2api")
			require.NotContains(t, strings.ToLower(key+value), "sub2api")
		}
	}
}

func TestConversationAndRequestIDs(t *testing.T) {
	t.Parallel()
	a := ConversationID(ProfileCNCLI, "key")
	require.Equal(t, a, ConversationID(ProfileCNCLI, "key"))
	require.NotEqual(t, a, ConversationID(ProfileIntlCLI, "key"))
	require.Len(t, a, 36)
	require.NotEqual(t, ConversationID(ProfileCNCLI, ""), ConversationID(ProfileCNCLI, ""))

	headers := RequestIDHeaders(a)
	require.Equal(t, a, headers["X-Conversation-ID"])
	require.Equal(t, headers["X-Request-ID"], headers["X-Conversation-Message-ID"])
	require.Equal(t, headers["X-Root-Request-ID"], headers["X-B3-TraceId"])
	require.True(t, strings.HasPrefix(headers["traceparent"], "00-"+headers["X-Trace-ID"]+"-"))
}

func TestPrepareChatBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"auto","store":true,"metadata":{"a":1},"stream":false,"messages":[` +
		`{"role":"user","content":"a<b>&c"},{"role":"developer","content":"be brief"}],` +
		`"tools":[{"type":"function","function":{"name":"f"}},{"type":"function","function":{"name":"g"}}],` +
		`"tool_choice":{"type":"function","function":{"name":"g"}}}`)
	out, err := PrepareChatBody(body, ProfileCNCLI)
	require.NoError(t, err)
	require.Contains(t, string(out), `a<b>&c`, "no HTML escaping")

	var got map[string]any
	require.NoError(t, json.Unmarshal(out, &got))
	require.NotContains(t, got, "store")
	require.NotContains(t, got, "metadata")
	require.Equal(t, true, got["stream"])
	require.Equal(t, map[string]any{"include_usage": true}, got["stream_options"])
	require.Equal(t, "auto", got["model"])
	require.Equal(t, "required", got["tool_choice"])
	require.Len(t, got["tools"], 1)
	require.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, "be brief", gjson.GetBytes(out, "messages.0.content").String())
	require.Equal(t, "user", gjson.GetBytes(out, "messages.1.role").String())

	out, err = PrepareChatBody([]byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`), ProfileIntlCLI)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, IntlAutoModel, got["model"])
	require.Equal(t, DefaultSystemMessage, gjson.GetBytes(out, "messages.0.content").String())

	_, err = PrepareChatBody([]byte(`{"model":"m","messages":[]}`), ProfileCNCLI)
	var reqErr *RequestError
	require.ErrorAs(t, err, &reqErr)
	_, err = PrepareChatBody([]byte(`{"model":"m","n":2,"messages":[{"role":"user","content":"x"}]}`), ProfileCNCLI)
	require.ErrorAs(t, err, &reqErr)
	_, err = PrepareChatBody([]byte(`{"model":"m","tool_choice":{"type":"function","function":{"name":"x"}},"messages":[{"role":"user","content":"x"}]}`), ProfileCNCLI)
	require.ErrorAs(t, err, &reqErr)
}

func TestPrepareChatBodyMergesIntlImageRuns(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},` +
		`{"role":"user","content":"look"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]},` +
		`{"role":"assistant","content":"ok"},{"role":"user","content":"a"},{"role":"user","content":"b"}]}`)
	out, err := PrepareChatBody(body, ProfileIntlWork)
	require.NoError(t, err)
	var got struct {
		Messages []map[string]any `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Len(t, got.Messages, 5, "only the image run merges")
	require.Len(t, gjson.GetBytes(out, "messages.1.content").Array(), 3)
	require.Equal(t, "\n\n", gjson.GetBytes(out, "messages.1.content.1.text").String())

	domestic, err := PrepareChatBody(body, ProfileCNCLI)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(domestic, &got))
	require.Len(t, got.Messages, 6)
}

func TestAggregateChatStream(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		`data: {"id":"c1","model":"glm-5.2","created":7,"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call_","type":"function","function":{"name":"get_","arguments":"{\"a\""}}]}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"weather","arguments":":1}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`,
		`data: [DONE]`,
	}, "\n\n")
	out, err := AggregateChatStream(strings.NewReader(stream), 0)
	require.NoError(t, err)
	var got struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage map[string]int `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(out, &got))
	require.Equal(t, "chat.completion", got.Object)
	require.Equal(t, "glm-5.2", got.Model)
	require.Equal(t, "Hello", got.Choices[0].Message.Content)
	require.Equal(t, "think", got.Choices[0].Message.ReasoningContent)
	require.Equal(t, "call_1", got.Choices[0].Message.ToolCalls[0].ID)
	require.Equal(t, "get_weather", got.Choices[0].Message.ToolCalls[0].Function.Name)
	require.Equal(t, `{"a":1}`, got.Choices[0].Message.ToolCalls[0].Function.Arguments)
	require.Equal(t, "tool_calls", got.Choices[0].FinishReason)
	require.Equal(t, 8, got.Usage["total_tokens"])

	_, err = AggregateChatStream(strings.NewReader(`data: {"error":{"message":"boom","code":"x"}}`+"\n\n"), 0)
	var streamErr *StreamError
	require.ErrorAs(t, err, &streamErr)
	require.JSONEq(t, `{"error":{"message":"boom","code":"x"}}`, string(streamErr.Body))

	_, err = AggregateChatStream(strings.NewReader(""), 0)
	require.Error(t, err)
}

func TestErrorClassification(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", "30")
	require.Equal(t, now.Add(30*time.Second), RateLimitResetAt(h, nil, now))
	reset := RateLimitResetAt(nil, []byte(`{"msg":"limit reached, resets at 2026-10-19 00:00:00 UTC+8"}`), now)
	require.Equal(t, time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC), reset.UTC())
	require.Equal(t, now.Add(DefaultRateLimitCooldown), RateLimitResetAt(nil, []byte(`{}`), now))
	h.Set("Retry-After", "999999")
	require.Equal(t, now.Add(MaxRateLimitCooldown), RateLimitResetAt(h, nil, now))

	require.True(t, ModelNotServable(400, []byte(`{"code":11102,"msg":"service info not found"}`)))
	require.True(t, ModelNotServable(404, []byte(`{"error":{"message":"model foo is not supported"}}`)))
	require.False(t, ModelNotServable(500, []byte(`{"code":11102}`)))
	require.False(t, ModelNotServable(400, []byte(`{"msg":"bad request"}`)))

	require.True(t, IsContentFilterError([]byte(`{"error":{"code":"content_filter"}}`)))
	require.True(t, IsContentFilterText(" 抱歉，系统检测到您当前输入的信息存在敏感内容, 我无法响应您的请求，请检查后重新输入。"))
	require.False(t, IsContentFilterError([]byte(`{"error":{"message":"rate limited"}}`)))

	require.True(t, CreditsExhausted(403, []byte(`{"msg":"积分不足"}`)))
	require.False(t, CreditsExhausted(200, []byte(`积分不足`)))
}

func TestErrorMessage(t *testing.T) {
	t.Parallel()
	// Top-level {code, msg} — the shape the generic extractor misses.
	require.Equal(t, "service info not found (code 11102)",
		ErrorMessage([]byte(`{"code":11102,"msg":"service info not found"}`)))
	// Nested error envelope with message alias.
	require.Equal(t, "model foo is not supported (code content_filter)",
		ErrorMessage([]byte(`{"error":{"code":"content_filter","message":"model foo is not supported"}}`)))
	// msg only.
	require.Equal(t, "bad request", ErrorMessage([]byte(`{"msg":"bad request"}`)))
	// code only (errCode alias).
	require.Equal(t, "CodeBuddy upstream error (code 40001)",
		ErrorMessage([]byte(`{"errCode":"40001"}`)))
	// Neither code nor message → empty, so callers fall back.
	require.Equal(t, "", ErrorMessage([]byte(`{"foo":"bar"}`)))
	require.Equal(t, "", ErrorMessage([]byte(`not json`)))
}

func TestParseCredentialsOfficialInfo(t *testing.T) {
	t.Parallel()
	token := fakeJWT(map[string]any{"iss": "https://copilot.tencent.com/auth/realms/copilot"})
	raw := map[string]any{
		"account": map[string]any{"uid": float64(12345), "nickname": "n", "type": "ultimate", "enterpriseId": "ent"},
		"auth": map[string]any{
			"accessToken": token, "refreshToken": "rt", "domain": "www.workbuddy.cn",
			"expiresAt": float64(1_900_000_000_000),
		},
	}
	creds, err := ParseCredentials(raw)
	require.NoError(t, err)
	require.Equal(t, "12345", creds.UID)
	require.Equal(t, ProfileCNWork, creds.Profile)
	require.Equal(t, SiteCN, creds.Site)
	require.Equal(t, ProductWorkBuddy, creds.Product)
	require.Equal(t, int64(1_900_000_000), creds.ExpiresAt)
	require.True(t, creds.IsEnterprise())

	stored := creds.ToMap()
	require.Equal(t, "2030-03-17T17:46:40Z", stored[KeyExpiresAt])
	again, err := ParseCredentials(stored)
	require.NoError(t, err)
	require.Equal(t, creds.ExpiresAt, again.ExpiresAt)
	require.Equal(t, creds.Profile, again.Profile)

	_, err = ParseCredentials(map[string]any{"auth": map[string]any{"accessToken": map[string]any{"enc": "x"}}, "account": map[string]any{"uid": "1"}})
	require.Error(t, err)
	_, err = ParseCredentials(map[string]any{"access_token": "t", "uid": "1", "domain": "evil.example.com"})
	require.Error(t, err)
}

func TestParseCatalog(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"code":0,"data":{
		"models":[
			{"id":"glm-5.2","name":"GLM","credits":"x0 credits","supportsToolCall":true},
			{"id":"kimi","credits":"x1.5","supportsToolCall":true,"aliases":["kimi-latest"]},
			{"id":"notools","supportsToolCall":false},
			{"id":"off","disabled":true,"supportsToolCall":true},
			{"id":"wb-only","supportsToolCall":true}],
		"agents":[{"name":"cli","models":["glm-5.2","kimi-latest","notools","off"]},
		          {"name":"craft","tags":["default"],"models":[{"id":"wb-only"}]}],
		"availableModels":[]}}`)
	cli, err := ParseCatalog(payload, ProductCLI)
	require.NoError(t, err)
	require.Equal(t, []string{"glm-5.2", "kimi"}, CatalogIDs(cli))
	price, ok := ModelMultiplier(cli, "kimi-latest")
	require.True(t, ok)
	require.Equal(t, 1.5, price)
	require.Equal(t, []string{"glm-5.2"}, CatalogIDs(FilterCatalog(cli, "zero")))

	wb, err := ParseCatalog(payload, ProductWorkBuddy)
	require.NoError(t, err)
	require.Equal(t, []string{"wb-only"}, CatalogIDs(wb))

	require.Equal(t, []string{"auto"}, CatalogIDs([]CatalogModel{{ID: IntlAutoModel}}))
}

func TestParseEnterpriseUsage(t *testing.T) {
	t.Parallel()
	snapshot, ok := ParseEnterpriseUsage(map[string]any{"code": float64(0), "data": map[string]any{
		"credit": 4.07, "limitNum": float64(2000), "cycleResetTime": "2026-10-22 00:00:00"}})
	require.True(t, ok)
	require.Equal(t, 1995.93, snapshot.Credits)
	require.Equal(t, 2000.0, snapshot.Total)
	require.NotNil(t, snapshot.ResetAt)

	unlimited, ok := ParseEnterpriseUsage(map[string]any{"data": map[string]any{"data": map[string]any{"limitNum": float64(-1)}}})
	require.True(t, ok)
	require.True(t, unlimited.Unlimited)
	unlimited.Deduct(5)
	require.False(t, unlimited.Estimated)
}

type recordedRequest struct {
	method, url string
	header      http.Header
}

type fakeDoer struct {
	requests  []recordedRequest
	responses map[string]string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, recordedRequest{method: req.Method, url: req.URL.String(), header: req.Header.Clone()})
	body := `{"code":1,"msg":"pending"}`
	for prefix, resp := range f.responses {
		if strings.HasPrefix(req.URL.String(), prefix) {
			body = resp
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

func TestOAuthFlow(t *testing.T) {
	t.Parallel()
	token := fakeJWT(map[string]any{"iss": "https://www.codebuddy.ai/auth/realms/copilot"})
	doer := &fakeDoer{responses: map[string]string{
		"https://www.codebuddy.ai/v2/plugin/auth/state":    `{"code":0,"data":{"state":"st","authUrl":"https://www.codebuddy.ai/login?state=st"}}`,
		"https://www.codebuddy.ai/v2/plugin/auth/token":    `{"code":0,"data":{"accessToken":"` + token + `","refreshToken":"rt","domain":"www.codebuddy.ai","expiresIn":3600}}`,
		"https://www.codebuddy.ai/v2/plugin/login/account": `{"code":0,"data":{"uid":"u9","nickname":"nick"}}`,
	}}
	m := NewOAuthManager()
	start, err := m.Start(context.Background(), LoginSiteIntlCodeBuddy, doer)
	require.NoError(t, err)
	require.Equal(t, "https://www.codebuddy.ai/login?state=st", start.VerificationURI)
	require.Equal(t, "https://www.codebuddy.ai/v2/plugin/auth/state?platform=CLI", doer.requests[0].url)
	require.Equal(t, CLIUserAgent, doer.requests[0].header.Get("User-Agent"))

	poll := m.Poll(context.Background(), start.LoginID)
	require.True(t, poll.Done)
	require.Empty(t, poll.Error)
	require.Equal(t, ProfileIntlCLI, poll.Profile)
	require.Equal(t, "Bearer "+token, doer.requests[2].header.Get("Authorization"))
	require.Equal(t, "www.codebuddy.ai", doer.requests[2].header.Get("X-Domain"))

	creds, err := m.Take(start.LoginID)
	require.NoError(t, err)
	require.Equal(t, "u9", creds.UID)
	require.Greater(t, creds.ExpiresAt, time.Now().Unix())
	_, err = m.Take(start.LoginID)
	require.Error(t, err)

	cnDoer := &fakeDoer{responses: map[string]string{
		"https://www.codebuddy.cn/v2/plugin/auth/state": `{"code":0,"data":{"state":"s2"}}`,
	}}
	cnStart, err := m.Start(context.Background(), LoginSiteCN, cnDoer)
	require.NoError(t, err)
	require.Equal(t, "https://www.codebuddy.cn/v2/plugin/auth/state?platform=workbuddy", cnDoer.requests[0].url)
	require.Equal(t, "https://www.codebuddy.cn/login?state=s2", cnStart.VerificationURI)
	require.False(t, m.Poll(context.Background(), cnStart.LoginID).Done)
}

func TestRefresh(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: map[string]string{
		"https://copilot.tencent.com/v2/plugin/auth/token/refresh": `{"code":0,"data":{"accessToken":"new","refreshToken":"rt2","expiresIn":7200,"domain":"www.codebuddy.cn"}}`,
	}}
	result, err := Refresh(context.Background(), doer, Credentials{Profile: ProfileCNCLI, AccessToken: "old", RefreshToken: "rt", UID: "u", Domain: "www.codebuddy.cn"})
	require.NoError(t, err)
	require.Equal(t, "new", result.AccessToken)
	require.Equal(t, "rt2", result.RefreshToken)
	require.InDelta(t, time.Now().Unix()+7200, result.ExpiresAt, 5)
	sent := doer.requests[0].header
	require.Equal(t, "rt", sent.Get("X-Refresh-Token"))
	require.Equal(t, "Bearer old", sent.Get("Authorization"))
	require.Equal(t, "u", sent.Get("X-User-Id"))

	failing := &fakeDoer{responses: map[string]string{"https://": `{"code":40001,"msg":"invalid refresh token"}`}}
	_, err = Refresh(context.Background(), failing, Credentials{Profile: ProfileCNCLI, RefreshToken: "rt"})
	require.ErrorContains(t, err, "invalid refresh token")
}

func TestRefreshEchoesAccountProfile(t *testing.T) {
	t.Parallel()
	base := Credentials{Profile: ProfileCNCLI, AccessToken: "old", RefreshToken: "rt", UID: "u", Domain: "www.codebuddy.cn"}

	// Flat echo of the subscription tier in the refresh payload.
	flat := &fakeDoer{responses: map[string]string{
		"https://copilot.tencent.com/v2/plugin/auth/token/refresh": `{"code":0,"data":{"accessToken":"new","type":"ultimate","enterpriseId":"ent"}}`,
	}}
	res, err := Refresh(context.Background(), flat, base)
	require.NoError(t, err)
	require.Equal(t, "ultimate", res.AccountType)
	require.Equal(t, "ent", res.EnterpriseID)

	// Nested account object.
	nested := &fakeDoer{responses: map[string]string{
		"https://copilot.tencent.com/v2/plugin/auth/token/refresh": `{"code":0,"data":{"accessToken":"new","account":{"type":"exclusive","nickname":"n"}}}`,
	}}
	res, err = Refresh(context.Background(), nested, base)
	require.NoError(t, err)
	require.Equal(t, "exclusive", res.AccountType)
	require.Equal(t, "n", res.Nickname)

	// No echo → empty, caller keeps whatever it already stored.
	bare := &fakeDoer{responses: map[string]string{
		"https://copilot.tencent.com/v2/plugin/auth/token/refresh": `{"code":0,"data":{"accessToken":"new"}}`,
	}}
	res, err = Refresh(context.Background(), bare, base)
	require.NoError(t, err)
	require.Empty(t, res.AccountType)
}

func TestFetchAccountInfo(t *testing.T) {
	t.Parallel()
	doer := &fakeDoer{responses: map[string]string{
		"https://copilot.tencent.com/v2/plugin/login/account": `{"code":0,"data":{"uid":"u","type":"exclusive","nickname":"nick","enterpriseId":"ent","enterpriseName":"Acme"}}`,
	}}
	info, err := FetchAccountInfo(context.Background(), doer, Identity{Profile: ProfileCNCLI, AccessToken: "at", Domain: "www.codebuddy.cn", UID: "u"})
	require.NoError(t, err)
	require.Equal(t, "u", info.UID)
	require.Equal(t, "exclusive", info.AccountType)
	require.Equal(t, "nick", info.Nickname)
	require.Equal(t, "ent", info.EnterpriseID)
	require.Equal(t, "Acme", info.EnterpriseName)

	failing := &fakeDoer{responses: map[string]string{"https://": `{"code":40001,"msg":"nope"}`}}
	_, err = FetchAccountInfo(context.Background(), failing, Identity{Profile: ProfileCNCLI, AccessToken: "at", UID: "u"})
	require.ErrorContains(t, err, "nope")
}
