//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type unavailableIdentityCache struct {
	IdentityCache
	readErr, writeErr error
	fingerprint       *Fingerprint
}

func (c *unavailableIdentityCache) GetFingerprint(context.Context, int64) (*Fingerprint, error) {
	return c.fingerprint, c.readErr
}
func (c *unavailableIdentityCache) CreateFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	return nil, c.writeErr
}
func (c *unavailableIdentityCache) SetFingerprint(context.Context, int64, *Fingerprint) (*Fingerprint, error) {
	return nil, c.writeErr
}

func TestCRSIdentityStoreFailuresDoNotProduceIdentity(t *testing.T) {
	down := errors.New("identity storage unavailable")
	for name, cache := range map[string]*unavailableIdentityCache{
		"read":   {readErr: down},
		"create": {writeErr: down},
		"renew":  {writeErr: down, fingerprint: &Fingerprint{ClientID: "existing", UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Add(-48 * time.Hour).Unix()}},
	} {
		t.Run(name, func(t *testing.T) {
			fp, err := NewIdentityService(cache).GetOrCreateFingerprint(t.Context(), 1, http.Header{})
			require.ErrorIs(t, err, down)
			require.Nil(t, fp)
		})
	}
}

func TestCRSIdentityFailureStopsRequestBuilders(t *testing.T) {
	svc := &GatewayService{identityService: NewIdentityService(&unavailableIdentityCache{readErr: errors.New("offline")})}
	account := newAnthropicOAuthAccountForPartialUsageTest()
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	req, _, err := svc.buildUpstreamRequest(t.Context(), nil, account, body, "dummy", "oauth", "claude-sonnet-4-5", false, false)
	require.Error(t, err)
	require.Nil(t, req)
	req, _, err = svc.buildCountTokensRequest(t.Context(), nil, account, body, "dummy", "oauth", "claude-sonnet-4-5", false)
	require.Error(t, err)
	require.Nil(t, req)
}

func TestCRSNativePassThroughDoesNotRequireCachedIdentity(t *testing.T) {
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{
		fingerprintUnification: false,
		metadataPassthrough:    true,
		expiresAt:              time.Now().Add(time.Minute).UnixNano(),
	})
	t.Cleanup(func() { gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{}) })
	svc := &GatewayService{
		identityService: NewIdentityService(&unavailableIdentityCache{readErr: errors.New("offline")}),
		settingService:  NewSettingService(newMockSettingRepo(), &config.Config{}),
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.283 (external, cli)")
	account := newAnthropicOAuthAccountForPartialUsageTest()
	account.Extra = map[string]any{"account_uuid": "account", "session_id_masking_enabled": true}
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.283.49b; cch=4abcd;"}],"metadata":{"user_id":"user_native_account_account_session_11111111-2222-4333-8444-555555555555"}}`)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		t.Run(endpoint, func(t *testing.T) {
			build := func(mimic bool) (*http.Request, []byte, error) {
				if endpoint == "count_tokens" {
					return svc.buildCountTokensRequest(t.Context(), c, account, body, "dummy", "oauth", "claude-sonnet-4-5", mimic)
				}
				return svc.buildUpstreamRequest(t.Context(), c, account, body, "dummy", "oauth", "claude-sonnet-4-5", false, mimic)
			}
			req, out, err := build(false)
			require.NoError(t, err)
			require.Equal(t, c.Request.UserAgent(), getHeaderRaw(req.Header, "User-Agent"))
			if endpoint == "messages" { // count_tokens intentionally omits system and metadata.
				require.Equal(t, gjson.GetBytes(body, "system.0.text").String(), gjson.GetBytes(out, "system.0.text").String())
				require.Equal(t, gjson.GetBytes(body, "metadata.user_id").String(), gjson.GetBytes(out, "metadata.user_id").String())
			}
			req, _, err = build(true)
			require.ErrorContains(t, err, "account identity unavailable")
			require.Nil(t, req)
		})
	}
}

func TestCRSForwardingSettingsReadFailureKeepsNativePolicyConservative(t *testing.T) {
	resetGatewayForwardingSettingsCacheForTest(t)
	repo := &forwardedIPMigrationRepoStub{getMultipleErr: errors.New("database unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	before := time.Now()
	policy := svc.getGatewayForwardingSettingsCached(t.Context())
	require.Equal(t, gatewayForwardingSettingsResult{openAITTFTMode: OpenAITTFTModeSemantic, mp: true}, policy)
	cached := gatewayForwardingCache.Load().(*cachedGatewayForwardingSettings)
	require.WithinDuration(t, before.Add(gatewayForwardingErrorTTL), time.Unix(0, cached.expiresAt), time.Second)
	require.Less(t, gatewayForwardingErrorTTL, gatewayForwardingCacheTTL)

	// Recovery is retried after the error TTL; it is not stored as a successful
	// load for the longer normal TTL. Explicit settings can then take effect.
	repo.getMultipleErr = nil
	repo.values = map[string]string{SettingKeyEnableFingerprintUnification: "true", SettingKeyEnableMetadataPassthrough: "false"}
	expired := *cached
	expired.expiresAt = time.Now().Add(-time.Second).UnixNano()
	gatewayForwardingCache.Store(&expired)
	policy = svc.getGatewayForwardingSettingsCached(t.Context())
	require.True(t, policy.fp)
	require.False(t, policy.mp)
}

func TestCRSProxyBindingFailureStopsCredentials(t *testing.T) {
	id := int64(9)
	account := newAnthropicOAuthAccountForPartialUsageTest()
	account.ProxyID = &id
	svc := &GatewayService{}
	token, _, err := svc.GetAccessToken(t.Context(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Empty(t, token)
	account.Proxy = &Proxy{ID: id, Protocol: "unsupported", Host: "example.invalid", Port: 1080}
	_, _, err = svc.GetAccessToken(t.Context(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	account.Proxy = &Proxy{ID: id, Protocol: "socks5", Host: "127.0.0.1", Port: 1080}
	token, _, err = svc.GetAccessToken(t.Context(), account)
	require.NoError(t, err)
	require.Equal(t, "oauth-token", token)
}

func TestCRSOAuthProxyRepositoryFailureStopsRefresh(t *testing.T) {
	id := int64(10)
	account := &Account{ProxyID: &id, Credentials: map[string]any{"refresh_token": "dummy"}}
	svc := &OAuthService{proxyRepo: &mockProxyRepoForOAuth{getByIDFunc: func(context.Context, int64) (*Proxy, error) { return nil, errors.New("proxy database unavailable") }}}
	// No OAuth client is installed: any attempt to fall back to direct would panic.
	result, err := svc.RefreshAccountToken(t.Context(), account)
	require.ErrorIs(t, err, ErrAccountProxyUnavailable)
	require.Nil(t, result)
}

type rejectionUpstream struct {
	HTTPUpstream
	calls  int
	status int
}

func (u *rejectionUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.calls++
	status := u.status
	if status == 0 {
		status = 403
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"permission_error","message":"Access forbidden"}}`))}, nil
}
func TestCRSForbiddenRequestIsSentOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", claude.DefaultUserAgent())
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}],"metadata":{"user_id":"user_device_account__session_11111111-2222-4333-8444-555555555555"}}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	upstream := &rejectionUpstream{}
	cfg := &config.Config{}
	repo := &rateLimitAccountRepoStub{}
	svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil)}
	_, err = svc.Forward(t.Context(), c, newAnthropicOAuthAccountForPartialUsageTest(), parsed)
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.False(t, failure.RetryableOnSameAccount)
	require.Equal(t, 1, upstream.calls)
}

func (c *unavailableIdentityCache) GetOrCreateMaskedSessionID(context.Context, int64, string) (string, error) {
	return "", errors.New("session storage unavailable")
}

func TestCRSMaskingFailureStopsRequestBuilders(t *testing.T) {
	cache := &unavailableIdentityCache{fingerprint: &Fingerprint{ClientID: "stable", UserAgent: claude.DefaultUserAgent(), UpdatedAt: time.Now().Unix()}}
	svc := &GatewayService{identityService: NewIdentityService(cache)}
	account := newAnthropicOAuthAccountForPartialUsageTest()
	account.Extra = map[string]any{"account_uuid": "account", "session_id_masking_enabled": true}
	uid := FormatMetadataUserID("device", "account", "11111111-2222-4333-8444-555555555555", "2.1.280")
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[],"metadata":{"user_id":` + strconvQuote(uid) + `}}`)
	req, _, err := svc.buildUpstreamRequest(t.Context(), nil, account, body, "dummy", "oauth", "claude-sonnet-4-5", false, false)
	require.ErrorContains(t, err, "session storage unavailable")
	require.Nil(t, req)
	req, _, err = svc.buildCountTokensRequest(t.Context(), nil, account, body, "dummy", "oauth", "claude-sonnet-4-5", false)
	require.ErrorContains(t, err, "session storage unavailable")
	require.Nil(t, req)
}

func TestCRSClaudePoolRejectsAuthorizationRetryAcrossEntryPoints(t *testing.T) {
	for _, endpoint := range []string{"messages", "responses", "chat"} {
		for _, status := range []int{401, 403} {
			t.Run(fmt.Sprintf("%s-%d", endpoint, status), func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+endpoint, nil)
				account := newAnthropicAPIKeyAccountForTest()
				account.Credentials["pool_mode"] = true
				account.Credentials["pool_mode_retry_status_codes"] = []any{float64(401), float64(403), float64(429)}
				upstream := &rejectionUpstream{status: status}
				cfg := &config.Config{}
				repo := &rateLimitAccountRepoStub{}
				svc := &GatewayService{cfg: cfg, httpUpstream: upstream, rateLimitService: NewRateLimitService(repo, nil, cfg, nil, nil)}
				body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
				var err error
				switch endpoint {
				case "responses":
					_, err = svc.ForwardAsResponses(t.Context(), c, account, []byte(`{"model":"claude-sonnet-4-5","input":"hello"}`), nil)
				case "chat":
					_, err = svc.ForwardAsChatCompletions(t.Context(), c, account, body, nil)
				default:
					parsed, parseErr := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
					require.NoError(t, parseErr)
					_, err = svc.Forward(t.Context(), c, account, parsed)
				}
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.False(t, failure.RetryableOnSameAccount)
				require.Equal(t, 1, upstream.calls)
				require.Greater(t, repo.setErrorCalls+repo.tempCalls, 0, "permission rejection must not be ignored by pool mode")
			})
		}
	}
}

func TestCRSBillingSyncPreservesOpaqueCCH(t *testing.T) {
	for _, suffix := range []string{"", " cch=4abcd;"} {
		body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280; cc_entrypoint=cli;` + suffix + `"}],"messages":[]}`)
		updated := syncBillingHeaderVersion(body, "claude-cli/2.1.281")
		require.Contains(t, string(updated), "cc_version=2.1.281")
		if suffix == "" {
			require.NotContains(t, string(updated), "cch=")
		} else {
			require.Contains(t, string(updated), suffix)
		}
	}
}

func TestCRSRealClientTracingHeadersArePreserved(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("X-Claude-Code-Prompt-Id", "11111111-2222-4333-8444-555555555555")
	c.Request.Header.Set("X-Claude-Code-Request-Class", "main")
	svc := &GatewayService{}
	account := newAnthropicOAuthAccountForPartialUsageTest()
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	req, _, err := svc.buildUpstreamRequest(t.Context(), c, account, body, "dummy", "oauth", "claude-sonnet-4-5", true, false)
	require.NoError(t, err)
	require.Equal(t, "11111111-2222-4333-8444-555555555555", getHeaderRaw(req.Header, "x-claude-code-prompt-id"))
	require.Equal(t, "main", getHeaderRaw(req.Header, "x-claude-code-request-class"))
}

func TestCRSNativeBillingSyntheticSamplesAreOpaque(t *testing.T) {
	// All values are synthetic. The arbitrary suffix and cch are opaque inputs,
	// not a claim that the service can generate or validate client signatures.
	for _, entrypoint := range []string{"cli", "sdk-cli"} {
		for _, cch := range []string{"", " cch=abcde;"} {
			for name, content := range map[string]any{
				"string": "synthetic user message",
				"blocks": []map[string]string{{"type": "text", "text": "synthetic context"}, {"type": "text", "text": "synthetic request"}},
			} {
				t.Run(fmt.Sprintf("%s/cch=%t/%s", entrypoint, cch != "", name), func(t *testing.T) {
					before := "x-anthropic-billing-header: cc_version=2.1.283.abc; cc_entrypoint=" + entrypoint + ";" + cch
					body, err := json.Marshal(map[string]any{
						"model":    "claude-sonnet-4-5",
						"system":   []map[string]string{{"type": "text", "text": before}},
						"messages": []map[string]any{{"role": "user", "content": content}},
					})
					require.NoError(t, err)
					require.NotEqual(t, "abc", computeClaudeCodeFingerprint(body, "2.1.283"), "synthetic suffix must detect an unintended rewrite")
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
					c.Request.Header.Set("User-Agent", "claude-cli/2.1.283 (external, cli)")
					svc := &GatewayService{identityService: NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{ClientID: "stable", UserAgent: "claude-cli/2.1.283 (external, cli)", UpdatedAt: time.Now().Unix()}})}
					_, out, err := svc.buildUpstreamRequest(t.Context(), c, newAnthropicOAuthAccountForPartialUsageTest(), body, "dummy", "oauth", "claude-sonnet-4-5", true, false)
					require.NoError(t, err)
					require.Equal(t, before, gjson.GetBytes(out, "system.0.text").String())
				})
			}
		}
	}
}
