//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// resolveProxyURLByID 是只持有代理 ID 的出站路径（OAuth 授权、token 刷新、privacy 设置等）
// 的唯一解析入口：选了代理却解析不出来时必须报错，绝不返回空串让调用方直连。
func TestResolveProxyURLByID(t *testing.T) {
	id := int64(5)
	lookupErr := errors.New("db unavailable")
	repo := &mockProxyRepoForOAuth{getByIDFunc: func(_ context.Context, got int64) (*Proxy, error) {
		switch got {
		case id:
			return &Proxy{ID: id, Protocol: "socks5", Host: "127.0.0.1", Port: 7891}, nil
		case 6:
			return nil, lookupErr
		default:
			return nil, nil
		}
	}}
	ctx := context.Background()

	url, err := resolveProxyURLByID(ctx, repo, nil)
	require.NoError(t, err, "no proxy selected: direct is expected")
	require.Empty(t, url)

	url, err = resolveProxyURLByID(ctx, repo, &id)
	require.NoError(t, err)
	require.Equal(t, "socks5://127.0.0.1:7891", url)

	_, err = resolveProxyURLByID(ctx, nil, &id)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable, "missing repository must not degrade to direct")

	missing := int64(404)
	url, err = resolveProxyURLByID(ctx, repo, &missing)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable, "deleted proxy must not degrade to direct")
	require.Empty(t, url)

	broken := int64(6)
	_, err = resolveProxyURLByID(ctx, repo, &broken)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.ErrorIs(t, err, lookupErr, "the lookup cause stays inspectable")
}

func TestAccountProxyURLWithRepo(t *testing.T) {
	id := int64(9)
	calls := 0
	repo := &mockProxyRepoForOAuth{getByIDFunc: func(_ context.Context, got int64) (*Proxy, error) {
		calls++
		if got == id {
			return &Proxy{ID: id, Protocol: "http", Host: "repo.example.com", Port: 3128}, nil
		}
		return nil, nil
	}}
	ctx := context.Background()

	loaded := &Account{ProxyID: &id, Proxy: &Proxy{ID: id, Protocol: "http", Host: "loaded.example.com", Port: 8080}}
	url, err := accountProxyURLWithRepo(ctx, repo, loaded)
	require.NoError(t, err)
	require.Equal(t, "http://loaded.example.com:8080", url, "a loaded relation is used as-is")
	require.Zero(t, calls, "no repository round-trip when the relation is loaded")

	url, err = accountProxyURLWithRepo(ctx, repo, &Account{ProxyID: &id})
	require.NoError(t, err)
	require.Equal(t, "http://repo.example.com:3128", url, "a missing relation is re-resolved from the repository")

	gone := int64(77)
	_, err = accountProxyURLWithRepo(ctx, repo, &Account{ProxyID: &gone})
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)

	url, err = accountProxyURLWithRepo(ctx, repo, &Account{})
	require.NoError(t, err)
	require.Empty(t, url)
}

// token 刷新：账号分配了代理但代理已被删除时，必须报错而不是直连刷新。
func TestOAuthServiceRefreshAccountTokenRefusesDirectWhenProxyMissing(t *testing.T) {
	client := &mockClaudeOAuthClient{
		refreshTokenFunc: func(context.Context, string, string, string) (*oauth.TokenResponse, error) {
			t.Fatal("token refresh must not be sent when the assigned proxy is unavailable")
			return nil, nil
		},
	}
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, client) // GetByID → "proxy not found"
	defer svc.Stop()

	proxyID := int64(10)
	_, err := svc.RefreshAccountToken(context.Background(), &Account{
		ID:          4,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		ProxyID:     &proxyID,
		Credentials: map[string]any{"refresh_token": "rt"},
	})
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
}

// OAuth 授权：管理员为待建账号选了代理但代理不存在时，不能静默改为直连授权。
func TestOAuthServiceGenerateAuthURLRefusesDirectWhenSelectedProxyMissing(t *testing.T) {
	svc := NewOAuthService(&mockProxyRepoForOAuth{}, &mockClaudeOAuthClient{})
	defer svc.Stop()

	proxyID := int64(11)
	_, err := svc.GenerateAuthURL(context.Background(), &proxyID)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
}

// failOpenProxyResolution 匹配"关系缺失时静默留空串"的 fail-open 写法：
//
//	if account.ProxyID != nil && account.Proxy != nil {
//		proxyURL = account.Proxy.URL()   // 或 return account.Proxy.URL()
//	}
//
// 代理关系缺失时 proxyURL 保持 ""，传输层把空串当直连 → 泄漏出口 IP。
var failOpenProxyResolution = regexp.MustCompile(`if\s+\w+\.ProxyID != nil && \w+\.Proxy != nil \{\s*\n\s*(?:\w+\s*:?=|return)\s*\w+\.Proxy\.URL\(\)`)

// 防回归护栏：service 包内禁止再出现上述 fail-open 写法，新代码请用
// Account.ProxyURLForOutbound / accountProxyURLWithRepo / resolveProxyURLByID。
func TestNoFailOpenAccountProxyResolution(t *testing.T) {
	// 自检：正则必须能识别典型写法，否则护栏形同虚设。
	require.Regexp(t, failOpenProxyResolution, "if account.ProxyID != nil && account.Proxy != nil {\n\t\tproxyURL = account.Proxy.URL()\n\t}")
	require.Regexp(t, failOpenProxyResolution, "if acc.ProxyID != nil && acc.Proxy != nil {\n\t\treturn acc.Proxy.URL()\n\t}")

	allowed := map[string]bool{
		// buildCustomRelayURL 拼 relay 的 &proxy= 参数；所有调用方都已先经 ProxyURLForOutbound
		// fail-closed 校验，走到这里时 Proxy 关系必然存在。
		"gateway_upstream_request.go": true,
	}
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		src, err := os.ReadFile(name)
		require.NoError(t, err)
		if failOpenProxyResolution.Match(src) {
			offenders = append(offenders, name)
		}
	}
	sort.Strings(offenders)
	require.Empty(t, offenders, "fail-open proxy resolution silently connects directly when the relation is missing; use Account.ProxyURLForOutbound instead")
}

// 账号测试总闸：任何平台的账号分配了代理但代理关系缺失时，测试直接失败，上游一次都不能被调用。
func TestAccountTestServiceRefusesDirectWhenAssignedProxyUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	proxyID := int64(21)
	cases := []struct {
		name        string
		platform    string
		accountType string
	}{
		{"anthropic oauth", PlatformAnthropic, AccountTypeOAuth},
		{"openai oauth", PlatformOpenAI, AccountTypeOAuth},
		{"openai apikey", PlatformOpenAI, AccountTypeAPIKey},
		{"gemini apikey", PlatformGemini, AccountTypeAPIKey},
		{"grok oauth", PlatformGrok, AccountTypeOAuth},
		{"antigravity oauth", PlatformAntigravity, AccountTypeOAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{
				ID:          900,
				Name:        "proxied",
				Platform:    tc.platform,
				Type:        tc.accountType,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				ProxyID:     &proxyID, // Proxy relation missing
				Credentials: map[string]any{"access_token": "at", "api_key": "sk-test", "refresh_token": "rt"},
			}
			repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
			upstream := &httpUpstreamRecorder{}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/900/test", nil)

			err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)
			require.Error(t, err)
			require.Contains(t, rec.Body.String(), "Proxy unavailable")
			require.Nil(t, upstream.lastReq, "no upstream request may leave without the assigned proxy")
		})
	}
}
