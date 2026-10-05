// Package oauth provides helpers for OAuth flows used by this service.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Claude OAuth Constants
const (
	// OAuth Client ID for Claude
	ClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

	// OAuth endpoints
	AuthorizeURL = "https://claude.com/cai/oauth/authorize"
	TokenURL     = "https://platform.claude.com/v1/oauth/token"
	RedirectURI  = "https://platform.claude.com/oauth/code/callback"

	// Scopes 对齐 Claude Code 2.1.287：登录 URL 用 [org:create_api_key, user:profile]
	// + 默认 scope 集（user:profile user:inference user:sessions:claude_code
	// user:mcp_servers user:file_upload，PLUGINS_SCOPE_REGISTERED 时追加 user:plugins）。
	//
	// Scopes - Browser URL (includes org:create_api_key for user authorization)
	ScopeOAuth = "org:create_api_key " + ScopeAPI
	// Scopes - Internal API call (org:create_api_key not supported in API).
	// 也是真实 CLI token 刷新默认请求的 scope 集。
	ScopeAPI = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
	// Scopes - Setup token (inference only)
	ScopeInference = "user:inference"

	// SetupTokenExpiresIn 是 `claude setup-token` 换取 token 时请求的有效期（一年，
	// 真实 CLI 的 expiresIn:c9=31536000）。普通登录不发 expires_in。
	SetupTokenExpiresIn int64 = 31536000

	// Session TTL
	SessionTTL = 30 * time.Minute
)

// projectsScopes 是刷新时从已授予 scope 里保留下来的可选 scope（真实 CLI 的 Cor 过滤）。
var projectsScopes = []string{"user:projects:read", "user:projects:write"}

// RefreshScope 计算真实 CLI 刷新订阅登录 token 时请求的 scope（2.1.287 的 T7r）：
// 默认 scope 集 + 已授予 scope 中的 user:projects:*，去重保序、空格分隔。
func RefreshScope(granted string) string {
	scopes := strings.Fields(ScopeAPI)
	seen := make(map[string]struct{}, len(scopes))
	for _, s := range scopes {
		seen[s] = struct{}{}
	}
	for _, s := range strings.Fields(granted) {
		if _, ok := seen[s]; ok {
			continue
		}
		for _, p := range projectsScopes {
			if s == p {
				scopes = append(scopes, s)
				seen[s] = struct{}{}
				break
			}
		}
	}
	return strings.Join(scopes, " ")
}

// HasScope reports whether the space-separated scope list contains scope.
func HasScope(scopeList, scope string) bool {
	for _, s := range strings.Fields(scopeList) {
		if s == scope {
			return true
		}
	}
	return false
}

// ParseAuthorizationCode 解析管理员粘贴的授权码。回调页展示的是 "code#state"；
// 也接受整条回调 URL（?code=...&state=...）或裸 query 串，以及只有 code 的情况
// （state 返回空串）。
func ParseAuthorizationCode(input string) (code, state string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", ""
	}
	if strings.Contains(trimmed, "code=") {
		query := trimmed
		if idx := strings.Index(query, "?"); idx >= 0 {
			query = query[idx+1:]
		}
		if idx := strings.Index(query, "#"); idx >= 0 && !strings.Contains(query[:idx], "code=") {
			query = query[idx+1:]
		}
		if values, err := url.ParseQuery(query); err == nil {
			if c := strings.TrimSpace(values.Get("code")); c != "" {
				code, state = splitCodeState(c)
				if s := strings.TrimSpace(values.Get("state")); s != "" {
					state = s
				}
				return code, state
			}
		}
	}
	return splitCodeState(trimmed)
}

func splitCodeState(raw string) (code, state string) {
	if idx := strings.Index(raw, "#"); idx >= 0 {
		return strings.TrimSpace(raw[:idx]), strings.TrimSpace(raw[idx+1:])
	}
	return strings.TrimSpace(raw), ""
}

// OAuthSession stores OAuth flow state

type OAuthSession struct {
	State        string    `json:"state"`
	CodeVerifier string    `json:"code_verifier"`
	Scope        string    `json:"scope"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// SessionStore manages OAuth sessions in memory
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*OAuthSession
	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewSessionStore creates a new session store
func NewSessionStore() *SessionStore {
	store := &SessionStore{
		sessions: make(map[string]*OAuthSession),
		stopCh:   make(chan struct{}),
	}
	go store.cleanup()
	return store
}

// Stop stops the cleanup goroutine
func (s *SessionStore) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

// Set stores a session
func (s *SessionStore) Set(sessionID string, session *OAuthSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = session
}

// Get retrieves a session
func (s *SessionStore) Get(sessionID string) (*OAuthSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, false
	}
	if time.Since(session.CreatedAt) > SessionTTL {
		return nil, false
	}
	return session, true
}

// Delete removes a session
func (s *SessionStore) Delete(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessionID)
}

// cleanup removes expired sessions periodically
func (s *SessionStore) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.mu.Lock()
			for id, session := range s.sessions {
				if time.Since(session.CreatedAt) > SessionTTL {
					delete(s.sessions, id)
				}
			}
			s.mu.Unlock()
		}
	}
}

// GenerateRandomBytes generates cryptographically secure random bytes
func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// GenerateState generates a random state string for OAuth (base64url encoded)
func GenerateState() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

// GenerateSessionID generates a unique session ID
func GenerateSessionID() (string, error) {
	bytes, err := GenerateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GenerateCodeVerifier generates a PKCE code verifier (RFC 7636).
// Uses 32 random bytes → base64url-no-pad, producing a 43-char verifier.
func GenerateCodeVerifier() (string, error) {
	bytes, err := GenerateRandomBytes(32)
	if err != nil {
		return "", err
	}
	return base64URLEncode(bytes), nil
}

// GenerateCodeChallenge generates a PKCE code challenge using S256 method
func GenerateCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64URLEncode(hash[:])
}

// base64URLEncode encodes bytes to base64url without padding
func base64URLEncode(data []byte) string {
	encoded := base64.URLEncoding.EncodeToString(data)
	return strings.TrimRight(encoded, "=")
}

// BuildAuthorizationURL builds the OAuth authorization URL with correct parameter order
func BuildAuthorizationURL(state, codeChallenge, scope string) string {
	encodedRedirectURI := url.QueryEscape(RedirectURI)
	encodedScope := strings.ReplaceAll(url.QueryEscape(scope), "%20", "+")

	return fmt.Sprintf("%s?code=true&client_id=%s&response_type=code&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		AuthorizeURL,
		ClientID,
		encodedRedirectURI,
		encodedScope,
		codeChallenge,
		state,
	)
}

// TokenResponse represents the token response from OAuth provider
type TokenResponse struct {
	AccessToken  string       `json:"access_token"`
	TokenType    string       `json:"token_type"`
	ExpiresIn    int64        `json:"expires_in"`
	RefreshToken string       `json:"refresh_token,omitempty"`
	Scope        string       `json:"scope,omitempty"`
	Organization *OrgInfo     `json:"organization,omitempty"`
	Account      *AccountInfo `json:"account,omitempty"`
}

// OrgInfo represents organization info from OAuth response
type OrgInfo struct {
	UUID string `json:"uuid"`
}

// AccountInfo represents account info from OAuth response
type AccountInfo struct {
	UUID         string `json:"uuid"`
	EmailAddress string `json:"email_address"`
}
