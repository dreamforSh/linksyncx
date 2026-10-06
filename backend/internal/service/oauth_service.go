package service

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// OpenAIOAuthClient interface for OpenAI OAuth operations
type OpenAIOAuthClient interface {
	ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error)
	RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error)
	RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error)
}

// GrokOAuthClient interface for xAI/Grok OAuth operations.
type GrokOAuthClient interface {
	ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*xai.TokenResponse, error)
	// RefreshToken sends the consent-time principal back like Grok Build does,
	// so a team-scoped grant keeps refreshing as the same principal.
	RefreshToken(ctx context.Context, refreshToken, proxyURL, clientID string, principal xai.TokenPrincipal) (*xai.TokenResponse, error)
	// LoginWithPassword exchanges email/password for a short-lived Web SSO cookie.
	// Callers must convert via ConvertSSOToBuild and must not persist password or raw SSO.
	LoginWithPassword(ctx context.Context, email, password, proxyURL string) (*GrokPasswordLoginResult, error)
	ConvertSSOToBuild(ctx context.Context, ssoToken, proxyURL string) (*xai.TokenResponse, error)
}

// GrokOAuthTokenService is the narrow refresh port used by Grok token providers.
type GrokOAuthTokenService interface {
	RefreshAccountToken(ctx context.Context, account *Account) (*GrokTokenInfo, error)
	BuildAccountCredentials(tokenInfo *GrokTokenInfo) map[string]any
}

// ClaudeOAuthClient handles HTTP requests for Claude OAuth flows
type ClaudeOAuthClient interface {
	GetOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error)
	GetAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error)
	ExchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*oauth.TokenResponse, error)
	// RefreshToken refreshes with the given space-separated scope; an empty
	// scope omits the field so the grant keeps its current scopes.
	RefreshToken(ctx context.Context, refreshToken, scope, proxyURL string) (*oauth.TokenResponse, error)
}

// OAuthService handles OAuth authentication flows
type OAuthService struct {
	sessionStore *oauth.SessionStore
	proxyRepo    ProxyRepository
	oauthClient  ClaudeOAuthClient
}

// NewOAuthService creates a new OAuth service
func NewOAuthService(proxyRepo ProxyRepository, oauthClient ClaudeOAuthClient) *OAuthService {
	return &OAuthService{
		sessionStore: oauth.NewSessionStore(),
		proxyRepo:    proxyRepo,
		oauthClient:  oauthClient,
	}
}

// GenerateAuthURLResult contains the authorization URL and session info
type GenerateAuthURLResult struct {
	AuthURL   string `json:"auth_url"`
	SessionID string `json:"session_id"`
}

// GenerateAuthURL generates an OAuth authorization URL with full scope
func (s *OAuthService) GenerateAuthURL(ctx context.Context, proxyID *int64) (*GenerateAuthURLResult, error) {
	return s.generateAuthURLWithScope(ctx, oauth.ScopeOAuth, proxyID)
}

// GenerateSetupTokenURL generates an OAuth authorization URL for setup token (inference only)
func (s *OAuthService) GenerateSetupTokenURL(ctx context.Context, proxyID *int64) (*GenerateAuthURLResult, error) {
	scope := oauth.ScopeInference
	return s.generateAuthURLWithScope(ctx, scope, proxyID)
}

func (s *OAuthService) generateAuthURLWithScope(ctx context.Context, scope string, proxyID *int64) (*GenerateAuthURLResult, error) {
	// Generate PKCE values
	state, err := oauth.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	codeVerifier, err := oauth.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}

	codeChallenge := oauth.GenerateCodeChallenge(codeVerifier)

	// Generate session ID
	sessionID, err := oauth.GenerateSessionID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate session ID: %w", err)
	}

	// Get proxy URL if specified（fail-closed：选了代理但解析失败时报错，绝不直连）
	proxyURL, err := resolveProxyURLByID(ctx, s.proxyRepo, proxyID)
	if err != nil {
		return nil, claudeOAuthProxyError(err)
	}

	// Store session
	session := &oauth.OAuthSession{
		State:        state,
		CodeVerifier: codeVerifier,
		Scope:        scope,
		ProxyURL:     proxyURL,
		CreatedAt:    time.Now(),
	}
	s.sessionStore.Set(sessionID, session)

	// Build authorization URL
	authURL := oauth.BuildAuthorizationURL(state, codeChallenge, scope)

	return &GenerateAuthURLResult{
		AuthURL:   authURL,
		SessionID: sessionID,
	}, nil
}

// ExchangeCodeInput represents the input for code exchange
type ExchangeCodeInput struct {
	SessionID string
	Code      string
	ProxyID   *int64
}

// TokenInfo represents the token information stored in credentials
type TokenInfo struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	RefreshToken string `json:"refresh_token,omitempty"`
	// RefreshTokenExpiresAt 是 refresh token 的到期时间（Unix 秒）。授权码交换时总会给出
	// （上游未返回则按 30 天估算）；刷新时只有上游返回才非零，零值表示沿用账号已存的值。
	RefreshTokenExpiresAt int64  `json:"refresh_token_expires_at,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	OrgUUID               string `json:"org_uuid,omitempty"`
	AccountUUID           string `json:"account_uuid,omitempty"`
	EmailAddress          string `json:"email_address,omitempty"`
}

// ExchangeCode exchanges authorization code for tokens
func (s *OAuthService) ExchangeCode(ctx context.Context, input *ExchangeCodeInput) (*TokenInfo, error) {
	// Get session
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok {
		return nil, infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_SESSION_NOT_FOUND",
			"OAuth session not found or expired; generate a new authorization link")
	}

	// 回调页给的是 "code#state"，也兼容整条回调 URL。state 只用于校验粘贴的授权码
	// 属于本次授权；交换时与真实 CLI 一样发送本次会话生成的 state。
	code, pastedState := oauth.ParseAuthorizationCode(input.Code)
	if code == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_CODE_REQUIRED", "authorization code is required")
	}
	if pastedState != "" && pastedState != session.State {
		return nil, infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_STATE_MISMATCH",
			"the authorization code belongs to a different authorization link; open the current link and paste the code it shows")
	}

	// Get proxy URL（fail-closed：选了代理但解析失败时报错，绝不直连）
	proxyURL := session.ProxyURL
	if input.ProxyID != nil {
		resolved, err := resolveProxyURLByID(ctx, s.proxyRepo, input.ProxyID)
		if err != nil {
			return nil, claudeOAuthProxyError(err)
		}
		proxyURL = resolved
	}

	// Determine if this is a setup token (scope is inference only)
	isSetupToken := session.Scope == oauth.ScopeInference

	// Exchange code for token
	tokenInfo, err := s.exchangeCodeForToken(ctx, code, session.CodeVerifier, session.State, proxyURL, isSetupToken)
	if err != nil {
		return nil, err
	}

	// Delete session after successful exchange
	s.sessionStore.Delete(input.SessionID)

	return tokenInfo, nil
}

// CookieAuthInput represents the input for cookie-based authentication
type CookieAuthInput struct {
	SessionKey string
	ProxyID    *int64
	Scope      string // "full" or "inference"
}

// CookieAuth performs OAuth using sessionKey (cookie-based auto-auth)
func (s *OAuthService) CookieAuth(ctx context.Context, input *CookieAuthInput) (*TokenInfo, error) {
	// Get proxy URL if specified（fail-closed：选了代理但解析失败时报错，绝不直连）
	proxyURL, err := resolveProxyURLByID(ctx, s.proxyRepo, input.ProxyID)
	if err != nil {
		return nil, claudeOAuthProxyError(err)
	}

	// Determine scope and if this is a setup token
	// Internal API call uses ScopeAPI (org:create_api_key not supported)
	scope := oauth.ScopeAPI
	isSetupToken := false
	if input.Scope == "inference" {
		scope = oauth.ScopeInference
		isSetupToken = true
	}

	// Step 1: Get organization info using sessionKey
	orgUUID, err := s.getOrganizationUUID(ctx, input.SessionKey, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get organization info: %w", err)
	}

	// Step 2: Generate PKCE values
	codeVerifier, err := oauth.GenerateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}
	codeChallenge := oauth.GenerateCodeChallenge(codeVerifier)

	state, err := oauth.GenerateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	// Step 3: Get authorization code using cookie
	authCode, err := s.getAuthorizationCode(ctx, input.SessionKey, orgUUID, scope, codeChallenge, state, proxyURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get authorization code: %w", err)
	}

	// Step 4: Exchange code for token
	tokenInfo, err := s.exchangeCodeForToken(ctx, authCode, codeVerifier, state, proxyURL, isSetupToken)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}

	// Ensure org_uuid is set (from step 1 if not from token response)
	if tokenInfo.OrgUUID == "" && orgUUID != "" {
		tokenInfo.OrgUUID = orgUUID
		log.Printf("[OAuth] Set org_uuid from cookie auth")
	}

	return tokenInfo, nil
}

// getOrganizationUUID gets the organization UUID from claude.ai using sessionKey
func (s *OAuthService) getOrganizationUUID(ctx context.Context, sessionKey, proxyURL string) (string, error) {
	return s.oauthClient.GetOrganizationUUID(ctx, sessionKey, proxyURL)
}

// getAuthorizationCode gets the authorization code using sessionKey
func (s *OAuthService) getAuthorizationCode(ctx context.Context, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL string) (string, error) {
	return s.oauthClient.GetAuthorizationCode(ctx, sessionKey, orgUUID, scope, codeChallenge, state, proxyURL)
}

// exchangeCodeForToken exchanges authorization code for tokens
func (s *OAuthService) exchangeCodeForToken(ctx context.Context, code, codeVerifier, state, proxyURL string, isSetupToken bool) (*TokenInfo, error) {
	tokenResp, err := s.oauthClient.ExchangeCodeForToken(ctx, code, codeVerifier, state, proxyURL, isSetupToken)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tokenInfo := &TokenInfo{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    now.Unix() + tokenResp.ExpiresIn,
		RefreshToken: tokenResp.RefreshToken,
		Scope:        tokenResp.Scope,
	}
	// 与真实 CLI 登录一致：上游给了 refresh_token_expires_in 就用它，否则按 30 天估算。
	if tokenResp.RefreshToken != "" {
		if tokenResp.RefreshTokenExpiresIn > 0 {
			tokenInfo.RefreshTokenExpiresAt = now.Unix() + tokenResp.RefreshTokenExpiresIn
		} else {
			tokenInfo.RefreshTokenExpiresAt = now.Add(oauth.DefaultRefreshTokenLifetime).Unix()
		}
	}

	if tokenResp.Organization != nil && tokenResp.Organization.UUID != "" {
		tokenInfo.OrgUUID = tokenResp.Organization.UUID
		log.Printf("[OAuth] Got org_uuid")
	}
	if tokenResp.Account != nil {
		if tokenResp.Account.UUID != "" {
			tokenInfo.AccountUUID = tokenResp.Account.UUID
			log.Printf("[OAuth] Got account_uuid")
		}
		if tokenResp.Account.EmailAddress != "" {
			tokenInfo.EmailAddress = tokenResp.Account.EmailAddress
			log.Printf("[OAuth] Got email_address")
		}
	}

	return tokenInfo, nil
}

// RefreshToken refreshes a Claude Code OAuth token whose granted scopes are unknown.
func (s *OAuthService) RefreshToken(ctx context.Context, refreshToken string, proxyURL string) (*TokenInfo, error) {
	return s.refreshToken(ctx, refreshToken, claudeRefreshScopeAttempts(AccountTypeOAuth, ""), proxyURL)
}

// claudeRefreshScopeAttempts 返回刷新时依次请求的 scope（空串表示不发 scope 字段），
// 只有上游回 invalid_scope 时才换下一个。订阅登录 token 与真实 CLI 2.1.287 一致：
// 先请求默认 scope 集（含 user:plugins，保留已授予的 user:projects:*），被拒再退回
// 已授予的 scope。setup-token 只有 user:inference，原样请求，避免越权扩 scope。
func claudeRefreshScopeAttempts(accountType, granted string) []string {
	granted = strings.Join(strings.Fields(granted), " ")
	if accountType == AccountTypeSetupToken {
		if granted == "" {
			granted = oauth.ScopeInference
		}
		return []string{granted}
	}
	if granted == "" {
		// 未记录 scope 的旧账号：按 CLI 请求默认集；被拒时退回旧行为（不发 scope）。
		return []string{oauth.RefreshScope(""), ""}
	}
	if !oauth.HasScope(granted, oauth.ScopeInference) {
		return []string{granted}
	}
	requested := oauth.RefreshScope(granted)
	if requested == granted {
		return []string{requested}
	}
	return []string{requested, granted}
}

func isInvalidScopeError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "invalid_scope")
}

func (s *OAuthService) refreshToken(ctx context.Context, refreshToken string, scopes []string, proxyURL string) (*TokenInfo, error) {
	var (
		tokenResp *oauth.TokenResponse
		err       error
	)
	for i, scope := range scopes {
		tokenResp, err = s.oauthClient.RefreshToken(ctx, refreshToken, scope, proxyURL)
		if err == nil || i == len(scopes)-1 || !isInvalidScopeError(err) {
			break
		}
		log.Printf("[OAuth] Token refresh rejected requested scope with invalid_scope; retrying with the granted scope")
	}
	if err != nil {
		return nil, err
	}

	now := time.Now()
	info := &TokenInfo{
		AccessToken:  tokenResp.AccessToken,
		TokenType:    tokenResp.TokenType,
		ExpiresIn:    tokenResp.ExpiresIn,
		ExpiresAt:    now.Unix() + tokenResp.ExpiresIn,
		RefreshToken: tokenResp.RefreshToken,
		Scope:        tokenResp.Scope,
	}
	// 与真实 CLI 刷新一致：只有上游返回 refresh_token_expires_in 才更新期限，
	// 否则保持零值，由凭据合并沿用账号已存的期限。
	if tokenResp.RefreshTokenExpiresIn > 0 {
		info.RefreshTokenExpiresAt = now.Unix() + tokenResp.RefreshTokenExpiresIn
	}
	return info, nil
}

// RefreshAccountToken refreshes token for an account
func (s *OAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*TokenInfo, error) {
	refreshToken := account.GetCredential("refresh_token")
	if refreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	// fail-closed：账号分配了代理但解析失败时报错，绝不直连刷新 token。
	proxyURL, err := accountProxyURLWithRepo(ctx, s.proxyRepo, account)
	if err != nil {
		return nil, err
	}

	return s.refreshToken(ctx, refreshToken, claudeRefreshScopeAttempts(account.Type, account.GetCredential("scope")), proxyURL)
}

// claudeOAuthProxyError 让管理端看到代理不可用的原因（否则只显示 internal error），
// 同时保留 errors.Is(err, ErrAccountProxyUnavailable)。
func claudeOAuthProxyError(err error) error {
	return infraerrors.Newf(http.StatusBadRequest, "CLAUDE_OAUTH_PROXY_UNAVAILABLE", "%v", err).WithCause(err)
}

// Stop stops the session store cleanup goroutine
func (s *OAuthService) Stop() {
	s.sessionStore.Stop()
}
