package repository

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpwire"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"

	"github.com/imroc/req/v3"
)

func NewClaudeOAuthClient() service.ClaudeOAuthClient {
	return &claudeOAuthService{
		baseURL:              "https://claude.ai",
		tokenURL:             oauth.TokenURL,
		clientFactory:        createReqClient,
		browserClientFactory: createBrowserReqClient,
	}
}

type claudeOAuthService struct {
	baseURL  string
	tokenURL string
	// clientFactory serves the token endpoint on platform.claude.com, which the
	// real CLI calls itself (CLI persona).
	clientFactory func(proxyURL string) (*req.Client, error)
	// browserClientFactory serves the sessionKey-cookie steps on claude.ai. They
	// stand in for the user's browser, and claude.ai's Cloudflare answers
	// non-browser TLS with a "Just a moment..." challenge (HTTP 403).
	browserClientFactory func(proxyURL string) (*req.Client, error)
}

// claudeOAuthExchangeBody / claudeOAuthRefreshBody 固定 JSON 字段序，与真实 CLI 的
// 请求体逐字段对齐（map 序列化会按字母序，与实测抓包不符）。字段序来自 2.1.287
// 二进制：交换 zqr 的 E={grant_type,code,redirect_uri,client_id,code_verifier,state}
// （setup-token 再追加 expires_in）；刷新 npe 的 T={grant_type,refresh_token,client_id,scope}。
// state 必填：token 端点对缺失或空 state 一律回 400 "Invalid request format"。
type claudeOAuthExchangeBody struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
}

type claudeOAuthRefreshBody struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope,omitempty"`
}

// claudeAIOrganization 是 claude.ai GET /api/organizations 的列表项。
type claudeAIOrganization struct {
	UUID         string   `json:"uuid"`
	Name         string   `json:"name"`
	RavenType    *string  `json:"raven_type"` // nil for personal, "team" for team organization
	Capabilities []string `json:"capabilities"`
}

func (o claudeAIOrganization) hasCapability(c string) bool {
	for _, v := range o.Capabilities {
		if v == c {
			return true
		}
	}
	return false
}

// selectClaudeAIOrganization 选出要授权的组织：只有带 chat 能力的组织才有 Claude
// 订阅（纯 API/Console 组织换到的 token 用不了 Claude Code），存在 chat 组织时只在
// 其中选；再优先 team 组织，否则取第一个。
func selectClaudeAIOrganization(orgs []claudeAIOrganization) claudeAIOrganization {
	candidates := make([]claudeAIOrganization, 0, len(orgs))
	for _, org := range orgs {
		if org.hasCapability("chat") {
			candidates = append(candidates, org)
		}
	}
	if len(candidates) == 0 {
		candidates = orgs
	}
	for _, org := range candidates {
		if org.RavenType != nil && *org.RavenType == "team" {
			return org
		}
	}
	return candidates[0]
}

func (s *claudeOAuthService) GetOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error) {
	var orgs []claudeAIOrganization

	targetURL := s.baseURL + "/api/organizations"
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1: Getting organization UUID from %s", targetURL)

	resp, err := s.doClaudeAIBrowserRequest(ctx, proxyURL, "Step 1", func(client *req.Client) (*req.Response, error) {
		orgs = nil
		return client.R().
			SetContext(ctx).
			SetCookies(&http.Cookie{
				Name:  "sessionKey",
				Value: sessionKey,
			}).
			SetSuccessResult(&orgs).
			Get(targetURL)
	})
	if err != nil {
		return "", err
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 Response - Status: %d", resp.StatusCode)

	if !resp.IsSuccessState() {
		return "", claudeAIStepError("CLAUDE_OAUTH_ORGANIZATIONS_FAILED", "failed to get organizations", resp)
	}

	if len(orgs) == 0 {
		return "", infraerrors.New(http.StatusBadGateway, "CLAUDE_OAUTH_NO_ORGANIZATION", "no organizations found for this sessionKey")
	}

	org := selectClaudeAIOrganization(orgs)
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 SUCCESS - Selected org (%d found), UUID: %s, Name: %s, Capabilities: %v",
		len(orgs), org.UUID, org.Name, org.Capabilities)
	return org.UUID, nil
}

// claudeAIStepError 把 claude.ai cookie 步骤的上游失败转成管理端可读的错误。
// sessionKey 无效/过期时 claude.ai 回 JSON 401/403（不是 Cloudflare 质询），归为 400。
func claudeAIStepError(reason, what string, resp *req.Response) error {
	status := http.StatusBadGateway
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		status = http.StatusBadRequest
		what += " (sessionKey invalid or expired)"
	}
	return infraerrors.Newf(status, reason, "%s: status %d, body: %s", what, resp.StatusCode, truncateClaudeOAuthBody(resp.String()))
}

// truncateClaudeOAuthBody 限制回显给管理端的上游响应体长度。
func truncateClaudeOAuthBody(body string) string {
	const limit = 512
	body = strings.TrimSpace(body)
	if len(body) <= limit {
		return body
	}
	return body[:limit] + "..."
}

func (s *claudeOAuthService) GetAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error) {
	authURL := fmt.Sprintf("%s/v1/oauth/%s/authorize", s.baseURL, orgUUID)

	reqBody := map[string]any{
		"response_type":         "code",
		"client_id":             oauth.ClientID,
		"organization_uuid":     orgUUID,
		"redirect_uri":          oauth.RedirectURI,
		"scope":                 scope,
		"state":                 state,
		"code_challenge":        codeChallenge,
		"code_challenge_method": "S256",
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2: Getting authorization code from %s", authURL)
	reqBodyJSON, _ := json.Marshal(logredact.RedactMap(reqBody))
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 Request Body: %s", string(reqBodyJSON))

	var result struct {
		RedirectURI string `json:"redirect_uri"`
	}

	resp, err := s.doClaudeAIBrowserRequest(ctx, proxyURL, "Step 2", func(client *req.Client) (*req.Response, error) {
		result.RedirectURI = ""
		return client.R().
			SetContext(ctx).
			SetCookies(&http.Cookie{
				Name:  "sessionKey",
				Value: sessionKey,
			}).
			SetHeader("Accept", "application/json").
			SetHeader("Accept-Language", "en-US,en;q=0.9").
			SetHeader("Cache-Control", "no-cache").
			SetHeader("Origin", "https://claude.ai").
			SetHeader("Referer", "https://claude.ai/new").
			SetHeader("Content-Type", "application/json").
			SetBody(reqBody).
			SetSuccessResult(&result).
			Post(authURL)
	})
	if err != nil {
		return "", err
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 Response - Status: %d, Body: %s", resp.StatusCode, logredact.RedactJSON(resp.Bytes()))

	if !resp.IsSuccessState() {
		return "", claudeAIStepError("CLAUDE_OAUTH_AUTHORIZE_FAILED", "failed to get authorization code", resp)
	}

	if result.RedirectURI == "" {
		return "", infraerrors.New(http.StatusBadGateway, "CLAUDE_OAUTH_AUTHORIZE_FAILED", "no redirect_uri in authorize response")
	}

	parsedURL, err := url.Parse(result.RedirectURI)
	if err != nil {
		return "", infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_AUTHORIZE_FAILED", "failed to parse redirect_uri: %v", err)
	}

	queryParams := parsedURL.Query()
	authCode := queryParams.Get("code")
	responseState := queryParams.Get("state")

	if authCode == "" {
		return "", infraerrors.New(http.StatusBadGateway, "CLAUDE_OAUTH_AUTHORIZE_FAILED", "no authorization code in redirect_uri")
	}

	fullCode := authCode
	if responseState != "" {
		fullCode = authCode + "#" + responseState
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 SUCCESS - Got authorization code")
	return fullCode, nil
}

// claudeAIChallengeAttempts bounds how often a claude.ai cookie step is sent
// when Cloudflare answers with a challenge. Even with the Chrome persona about
// one request in eight is challenged (live check 2026-10-02, mostly the first
// one); a challenged request never reaches the origin, so resending is safe.
const claudeAIChallengeAttempts = 3

var claudeAIChallengeRetryDelay = 600 * time.Millisecond

// doClaudeAIBrowserRequest sends a claude.ai cookie step with the browser
// client, resending on a fresh connection while Cloudflare challenges it.
func (s *claudeOAuthService) doClaudeAIBrowserRequest(ctx context.Context, proxyURL, step string, send func(*req.Client) (*req.Response, error)) (*req.Response, error) {
	for attempt := 1; ; attempt++ {
		client, err := s.browserClientFactory(proxyURL)
		if err != nil {
			return nil, infraerrors.Newf(http.StatusBadRequest, "CLAUDE_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err).WithCause(err)
		}
		resp, err := send(client)
		if err != nil {
			logger.LegacyPrintf("repository.claude_oauth", "[OAuth] %s FAILED - Request error: %v", step, err)
			return nil, infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_REQUEST_FAILED", "claude.ai request failed: %v", err).WithCause(err)
		}
		if !isCloudflareChallenge(resp) {
			return resp, nil
		}
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] %s challenged by Cloudflare (attempt %d/%d)", step, attempt, claudeAIChallengeAttempts)
		if attempt >= claudeAIChallengeAttempts {
			return nil, infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_CLOUDFLARE_CHALLENGE",
				"claude.ai Cloudflare challenged the request %d times (HTTP %d); retry later, use another proxy exit, or authorize manually with the authorization link", attempt, resp.StatusCode)
		}
		select {
		case <-ctx.Done():
			return nil, infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_REQUEST_FAILED", "claude.ai request failed: %v", ctx.Err()).WithCause(ctx.Err())
		case <-time.After(claudeAIChallengeRetryDelay):
		}
	}
}

// isCloudflareChallenge reports a Cloudflare bot challenge, which is answered
// in front of the origin.
func isCloudflareChallenge(resp *req.Response) bool {
	if resp == nil || resp.Response == nil {
		return false
	}
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusServiceUnavailable {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(resp.Header.Get("cf-mitigated")), "challenge") {
		return true
	}
	return strings.Contains(resp.String(), "<title>Just a moment...</title>")
}

// ExchangeCodeForToken 用授权码换 token。code 可带 "#state" 后缀（回调页格式）；
// state 为本次授权生成的 state——真实 CLI 交换时总是发自己生成的 state，空时才退回
// code 里携带的 state。setup-token 与 `claude setup-token` 一样请求一年有效期。
func (s *claudeOAuthService) ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
	authCode, codeState := oauth.ParseAuthorizationCode(code)
	if authCode == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_CODE_REQUIRED", "authorization code is required")
	}
	if state == "" {
		state = codeState
	}
	if state == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_STATE_REQUIRED",
			"oauth state is required; paste the full code shown on the callback page (code#state)")
	}

	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, "CLAUDE_OAUTH_CLIENT_INIT_FAILED", "create HTTP client: %v", err).WithCause(err)
	}

	// 字段序对齐真实 CLI 交换（zqr）：grant_type, code, redirect_uri, client_id,
	// code_verifier, state[, expires_in]。用结构体而非 map，保证 JSON 序列化顺序稳定。
	reqBody := claudeOAuthExchangeBody{
		GrantType:    "authorization_code",
		Code:         authCode,
		RedirectURI:  oauth.RedirectURI,
		ClientID:     oauth.ClientID,
		CodeVerifier: codeVerifier,
		State:        state,
	}
	if isSetupToken {
		reqBody.ExpiresIn = oauth.SetupTokenExpiresIn
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CLAUDE_OAUTH_ENCODE_FAILED", "encode request failed: %v", err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3: Exchanging code for token at %s", s.tokenURL)
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 Request Body: %s", logredact.RedactJSON(bodyBytes))

	var tokenResp oauth.TokenResponse

	resp, err := withClaudeTokenEndpointHeaders(client.R().SetContext(ctx)).
		SetBody(bodyBytes).
		SetSuccessResult(&tokenResp).
		Post(s.tokenURL)

	if err != nil {
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 FAILED - Request error: %v", err)
		return nil, infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_REQUEST_FAILED", "token exchange request failed: %v", err).WithCause(err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 Response - Status: %d, Body: %s", resp.StatusCode, logredact.RedactJSON(resp.Bytes()))

	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CLAUDE_OAUTH_TOKEN_EXCHANGE_FAILED",
			"token exchange failed: status %d, body: %s", resp.StatusCode, truncateClaudeOAuthBody(resp.String()))
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 SUCCESS - Got access token")
	return &tokenResp, nil
}

// withClaudeTokenEndpointHeaders 设置 token 端点（交换与刷新共用）的头部形状，对齐
// 真实 CLI 2.1.287 的 axios 1.9.0 调用：axios 只显式设 Content-Type，其余由 Node http
// 适配器补 Accept / User-Agent / Accept-Encoding；不发 anthropic-beta。
func withClaudeTokenEndpointHeaders(r *req.Request) *req.Request {
	return r.
		SetHeader("Content-Type", "application/json").
		SetHeaderNonCanonical("Accept", claude.OAuthLoginAccept).
		SetHeaderNonCanonical("Accept-Encoding", claude.OAuthLoginAcceptEncoding).
		SetHeader("User-Agent", claude.OAuthLoginUserAgent)
}

// RefreshToken 刷新 token。scope 为空时不发 scope 字段（沿用已授予的 scope）。
// 错误保持普通 error：后台刷新会把 err.Error() 写进账号错误信息并按
// "token refresh failed" / "invalid_grant" 等子串分类。
func (s *claudeOAuthService) RefreshToken(ctx context.Context, refreshToken, scope, proxyURL string) (*oauth.TokenResponse, error) {
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// 字段序对齐真实 CLI 刷新（npe）：grant_type, refresh_token, client_id, scope。
	// 用结构体而非 map，保证 JSON 序列化顺序稳定（map 会按字母序）。
	reqBody := claudeOAuthRefreshBody{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		ClientID:     oauth.ClientID,
		Scope:        scope,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode request failed: %w", err)
	}

	var tokenResp oauth.TokenResponse

	resp, err := withClaudeTokenEndpointHeaders(client.R().SetContext(ctx)).
		SetBody(bodyBytes).
		SetSuccessResult(&tokenResp).
		Post(s.tokenURL)

	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token refresh failed: status %d, body: %s", resp.StatusCode, resp.String())
	}

	return &tokenResp, nil
}

func createReqClient(proxyURL string) (*req.Client, error) {
	return newControlPlaneReqClient(proxyURL, nil)
}

// createBrowserReqClient builds the client for the sessionKey-cookie steps on
// claude.ai with req's Chrome persona (TLS, HTTP/2 and headers). The CLI
// persona is blocked there by Cloudflare, and Firefox is still challenged on
// GET /api/organizations (live check 2026-10-02). An invalid proxy is an
// error; only an empty proxy URL connects directly.
func createBrowserReqClient(proxyURL string) (*req.Client, error) {
	trimmed, _, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	// 禁用 CookieJar，确保每次授权都是干净的会话
	client := req.C().
		SetTimeout(60 * time.Second).
		ImpersonateChrome().
		SetCookieJar(nil)
	if trimmed != "" {
		client.SetProxyURL(trimmed)
	}
	return instrumentReqClient(client), nil
}

// newControlPlaneReqClient 构建控制面（platform.claude.com 等）的 req 客户端。
//
// 传输层与数据面同一 persona（2.1.280 实证控制面同样走 Bun fetch）：uTLS 指纹 +
// ALPN http/1.1，httpwire 重排为 Bun 线级头序；代理由指纹 dialer 自建
// http / https / socks5 隧道，明文请求同样走隧道不直连。Accept-Encoding 不让 req
// 自动追加 "gzip"，由 httpwire 补 Bun 默认值，响应在 decompressResponseBody 解压。
// rootCAs 仅供测试注入自签根证书。
func newControlPlaneReqClient(proxyURL string, rootCAs *x509.CertPool) (*req.Client, error) {
	_, parsedProxy, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	dialer, err := tlsfingerprint.NewProxyDialer(nil, parsedProxy, tlsfingerprint.DialOptions{RootCAs: rootCAs})
	if err != nil {
		return nil, err
	}

	// 禁用 CookieJar，确保每次授权都是干净的会话
	client := req.C().
		SetTimeout(60 * time.Second).
		SetCookieJar(nil).
		DisableCompression().
		SetDial(httpwire.WrapDialer(dialer.DialContext)).
		SetDialTLS(httpwire.WrapDialer(dialer.DialTLSContext))
	client.GetTransport().WrapRoundTripFunc(func(rt http.RoundTripper) req.HttpRoundTripFunc {
		return func(r *http.Request) (*http.Response, error) {
			resp, err := rt.RoundTrip(r)
			if err == nil {
				decompressResponseBody(resp)
			}
			return resp, err
		}
	})
	return instrumentReqClient(client), nil
}
