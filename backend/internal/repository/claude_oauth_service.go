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
		baseURL:       "https://claude.ai",
		tokenURL:      oauth.TokenURL,
		clientFactory: createReqClient,
	}
}

type claudeOAuthService struct {
	baseURL       string
	tokenURL      string
	clientFactory func(proxyURL string) (*req.Client, error)
}

// claudeOAuthExchangeBody / claudeOAuthRefreshBody 固定 JSON 字段序，与真实 CLI 的
// 请求体逐字段对齐（map 序列化会按字母序，与实测抓包不符）。字段序来自 2.1.283
// 二进制：登录 sNr 的 k={grant_type,code,redirect_uri,client_id,code_verifier,state}；
// 刷新 helper 的 d={grant_type,refresh_token,client_id}。
type claudeOAuthExchangeBody struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state,omitempty"`
}

type claudeOAuthRefreshBody struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
}

func (s *claudeOAuthService) GetOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error) {
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return "", fmt.Errorf("create HTTP client: %w", err)
	}

	var orgs []struct {
		UUID      string  `json:"uuid"`
		Name      string  `json:"name"`
		RavenType *string `json:"raven_type"` // nil for personal, "team" for team organization
	}

	targetURL := s.baseURL + "/api/organizations"
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1: Getting organization UUID from %s", targetURL)

	resp, err := client.R().
		SetContext(ctx).
		SetCookies(&http.Cookie{
			Name:  "sessionKey",
			Value: sessionKey,
		}).
		SetSuccessResult(&orgs).
		Get(targetURL)

	if err != nil {
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 FAILED - Request error: %v", err)
		return "", fmt.Errorf("request failed: %w", err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 Response - Status: %d", resp.StatusCode)

	if !resp.IsSuccessState() {
		return "", fmt.Errorf("failed to get organizations: status %d, body: %s", resp.StatusCode, resp.String())
	}

	if len(orgs) == 0 {
		return "", fmt.Errorf("no organizations found")
	}

	// 如果只有一个组织，直接使用
	if len(orgs) == 1 {
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 SUCCESS - Single org found, UUID: %s, Name: %s", orgs[0].UUID, orgs[0].Name)
		return orgs[0].UUID, nil
	}

	// 如果有多个组织，优先选择 raven_type 为 "team" 的组织
	for _, org := range orgs {
		if org.RavenType != nil && *org.RavenType == "team" {
			logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 SUCCESS - Selected team org, UUID: %s, Name: %s, RavenType: %s",
				org.UUID, org.Name, *org.RavenType)
			return org.UUID, nil
		}
	}

	// 如果没有 team 类型的组织，使用第一个
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 1 SUCCESS - No team org found, using first org, UUID: %s, Name: %s", orgs[0].UUID, orgs[0].Name)
	return orgs[0].UUID, nil
}

func (s *claudeOAuthService) GetAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error) {
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return "", fmt.Errorf("create HTTP client: %w", err)
	}

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

	resp, err := client.R().
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

	if err != nil {
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 FAILED - Request error: %v", err)
		return "", fmt.Errorf("request failed: %w", err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 Response - Status: %d, Body: %s", resp.StatusCode, logredact.RedactJSON(resp.Bytes()))

	if !resp.IsSuccessState() {
		return "", fmt.Errorf("failed to get authorization code: status %d, body: %s", resp.StatusCode, resp.String())
	}

	if result.RedirectURI == "" {
		return "", fmt.Errorf("no redirect_uri in response")
	}

	parsedURL, err := url.Parse(result.RedirectURI)
	if err != nil {
		return "", fmt.Errorf("failed to parse redirect_uri: %w", err)
	}

	queryParams := parsedURL.Query()
	authCode := queryParams.Get("code")
	responseState := queryParams.Get("state")

	if authCode == "" {
		return "", fmt.Errorf("no authorization code in redirect_uri")
	}

	fullCode := authCode
	if responseState != "" {
		fullCode = authCode + "#" + responseState
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 2 SUCCESS - Got authorization code")
	return fullCode, nil
}

func (s *claudeOAuthService) ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error) {
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// Parse code which may contain state in format "authCode#state"
	authCode := code
	codeState := ""
	if idx := strings.Index(code, "#"); idx != -1 {
		authCode = code[:idx]
		codeState = code[idx+1:]
	}

	// 字段序对齐真实 CLI 登录（sNr）：grant_type, code, redirect_uri, client_id,
	// code_verifier, state。用结构体而非 map，保证 JSON 序列化顺序稳定。
	reqBody := claudeOAuthExchangeBody{
		GrantType:    "authorization_code",
		Code:         authCode,
		RedirectURI:  oauth.RedirectURI,
		ClientID:     oauth.ClientID,
		CodeVerifier: codeVerifier,
		State:        codeState,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode request failed: %w", err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3: Exchanging code for token at %s", s.tokenURL)
	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 Request Body: %s", logredact.RedactJSON(bodyBytes))

	var tokenResp oauth.TokenResponse

	resp, err := client.R().
		SetContext(ctx).
		// 头部形状对齐真实 CLI 2.1.283 的登录授权码交换（sNr → axios 1.9.0）：
		// axios 只显式设 Content-Type，其余由 Node http 适配器补 Accept / User-Agent /
		// Accept-Encoding；**不发 anthropic-beta**（那是刷新 helper 的头，混用会露馅）。
		SetHeader("Content-Type", "application/json").
		SetHeaderNonCanonical("Accept", claude.OAuthLoginAccept).
		SetHeaderNonCanonical("Accept-Encoding", claude.OAuthLoginAcceptEncoding).
		SetHeader("User-Agent", claude.OAuthLoginUserAgent).
		SetBody(bodyBytes).
		SetSuccessResult(&tokenResp).
		Post(s.tokenURL)

	if err != nil {
		logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 FAILED - Request error: %v", err)
		return nil, fmt.Errorf("request failed: %w", err)
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 Response - Status: %d, Body: %s", resp.StatusCode, logredact.RedactJSON(resp.Bytes()))

	if !resp.IsSuccessState() {
		return nil, fmt.Errorf("token exchange failed: status %d, body: %s", resp.StatusCode, resp.String())
	}

	logger.LegacyPrintf("repository.claude_oauth", "[OAuth] Step 3 SUCCESS - Got access token")
	return &tokenResp, nil
}

func (s *claudeOAuthService) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*oauth.TokenResponse, error) {
	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create HTTP client: %w", err)
	}

	// 字段序对齐真实 CLI 刷新 helper（userOAuthProvider）：grant_type, refresh_token,
	// client_id。用结构体而非 map，保证 JSON 序列化顺序稳定（map 会按字母序）。
	reqBody := claudeOAuthRefreshBody{
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		ClientID:     oauth.ClientID,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encode request failed: %w", err)
	}

	var tokenResp oauth.TokenResponse

	resp, err := client.R().
		SetContext(ctx).
		// 头部形状对齐真实 CLI 2.1.283 的 sdk-ts OAuth helper（二进制实证）：
		// fetch headers {"Content-Type", "anthropic-beta", "User-Agent"}，无显式 Accept。
		// Accept: */* 由 httpwire 写在 Bun 默认头块；anthropic-beta 是小写头名，
		// 不能让 req 规范化成 Anthropic-Beta。
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "*/*").
		SetHeaderNonCanonical("anthropic-beta", "oauth-2025-04-20").
		SetHeader("User-Agent", claude.OAuthHelperUserAgent).
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
