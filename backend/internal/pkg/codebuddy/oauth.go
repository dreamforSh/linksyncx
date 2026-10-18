package codebuddy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	OAuthTimeout    = 10 * time.Minute
	ResultRetention = 5 * time.Minute
	RequestTimeout  = 15 * time.Second
	RefreshTimeout  = 15 * time.Second
)

// ErrAuthExpired marks a 401 from CodeBuddy: the access token is dead and the
// caller should refresh instead of retrying.
var ErrAuthExpired = errors.New("codebuddy credentials expired")

// HTTPDoer is the transport the package sends through. Callers build it from
// the account's proxy so no request ever leaves directly.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// APIResponse is CodeBuddy's {code, msg, data} envelope.
type APIResponse struct {
	Code       any             `json:"code"`
	Msg        string          `json:"msg"`
	Message    string          `json:"message"`
	Data       json.RawMessage `json:"data"`
	HTTPStatus int             `json:"-"`
}

// Success reports code 0 or 200.
func (r APIResponse) Success() bool {
	switch v := r.Code.(type) {
	case float64:
		return v == 0 || v == 200
	case json.Number:
		n, _ := v.Int64()
		return n == 0 || n == 200
	case string:
		return v == "0" || v == "200"
	default:
		return false
	}
}

func (r APIResponse) ErrorMessage() string {
	if msg := firstNonEmpty(r.Msg, r.Message); msg != "" {
		return msg
	}
	if r.HTTPStatus >= 400 {
		return fmt.Sprintf("upstream HTTP %d", r.HTTPStatus)
	}
	return "upstream request failed"
}

// DoJSON sends a JSON request and decodes the envelope. Header values are
// applied exactly as given; callers always pass a full identity.
func DoJSON(ctx context.Context, client HTTPDoer, method, rawURL string, headers map[string]string, body any) (*APIResponse, error) {
	if client == nil {
		return nil, errors.New("codebuddy: http client is required")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrAuthExpired
	}
	var parsed APIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode upstream json (http %d): %w", resp.StatusCode, err)
	}
	parsed.HTTPStatus = resp.StatusCode
	return &parsed, nil
}

// StartResult is returned to the admin UI to open the login page.
type StartResult struct {
	LoginID         string `json:"login_id"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int64  `json:"expires_in"`
}

// PollResult reports login progress. Creds is kept server-side.
type PollResult struct {
	Done     bool         `json:"done"`
	Error    string       `json:"error,omitempty"`
	UID      string       `json:"uid,omitempty"`
	Nickname string       `json:"nickname,omitempty"`
	Profile  string       `json:"profile,omitempty"`
	Creds    *Credentials `json:"-"`
}

type oauthSession struct {
	State     string
	Host      string
	ExpiresAt time.Time
	Done      bool
	Result    *Credentials
	Error     string
	Client    HTTPDoer
}

// OAuthManager tracks in-flight scan logins. Sessions live in memory only:
// a login must be completed on the instance that started it.
type OAuthManager struct {
	mu       sync.Mutex
	sessions map[string]*oauthSession
	now      func() time.Time
}

func NewOAuthManager() *OAuthManager {
	return &OAuthManager{sessions: make(map[string]*oauthSession), now: time.Now}
}

// Start requests a login state on the selected site and returns the page the
// user scans or opens.
func (m *OAuthManager) Start(ctx context.Context, site string, client HTTPDoer) (*StartResult, error) {
	site = strings.ToLower(strings.TrimSpace(site))
	host, err := LoginHostForSite(site)
	if err != nil {
		return nil, err
	}
	// International CodeBuddy requires the uppercase CLI platform; the other
	// sites accept the WorkBuddy parameter.
	platform := "workbuddy"
	if site == LoginSiteIntlCodeBuddy {
		platform = "CLI"
	}
	resp, err := DoJSON(ctx, client, http.MethodPost, host+AuthStatePath+"?platform="+platform, OAuthHeaders(), map[string]any{})
	if err != nil {
		return nil, err
	}
	var data struct {
		State    string `json:"state"`
		AuthURL  string `json:"authUrl"`
		AuthURL2 string `json:"auth_url"`
		URL      string `json:"url"`
	}
	if len(resp.Data) > 0 {
		_ = json.Unmarshal(resp.Data, &data)
	}
	if data.State == "" {
		return nil, fmt.Errorf("auth/state missing state: %s", resp.ErrorMessage())
	}
	authURL := firstNonEmpty(data.AuthURL, data.AuthURL2, data.URL)
	if authURL == "" {
		authURL = host + "/login?state=" + url.QueryEscape(data.State)
	}
	loginID := randomLoginID()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeLocked()
	m.sessions[loginID] = &oauthSession{
		State:     data.State,
		Host:      host,
		ExpiresAt: m.now().Add(OAuthTimeout),
		Client:    client,
	}
	return &StartResult{
		LoginID:         loginID,
		VerificationURI: authURL,
		ExpiresIn:       int64(OAuthTimeout / time.Second),
	}, nil
}

// Poll checks whether the user finished the login. Transport errors and
// pending codes keep the session open.
func (m *OAuthManager) Poll(ctx context.Context, loginID string) *PollResult {
	m.mu.Lock()
	m.purgeLocked()
	session := m.sessions[loginID]
	m.mu.Unlock()
	if session == nil {
		return &PollResult{Done: true, Error: "login request not found or expired"}
	}
	m.mu.Lock()
	if !session.Done && m.now().After(session.ExpiresAt) {
		session.Done = true
		session.Error = "login timed out"
	}
	done := session.Done
	m.mu.Unlock()
	if done {
		return pollFromSession(session)
	}

	state := url.QueryEscape(session.State)
	resp, err := DoJSON(ctx, session.Client, http.MethodGet, session.Host+AuthTokenPath+"?state="+state, OAuthHeaders(), nil)
	if err != nil || !resp.Success() {
		return &PollResult{Done: false}
	}
	var tokenData map[string]any
	if json.Unmarshal(resp.Data, &tokenData) != nil {
		return &PollResult{Done: false}
	}
	accessToken := stringField(tokenData, "accessToken", KeyAccessToken)
	if accessToken == "" {
		return &PollResult{Done: false}
	}

	accountHeaders := OAuthHeaders()
	accountHeaders["Authorization"] = "Bearer " + accessToken
	if domain := stringField(tokenData, "domain"); domain != "" {
		accountHeaders["X-Domain"] = domain
	}
	accResp, err := DoJSON(ctx, session.Client, http.MethodGet, session.Host+LoginAccountPath+"?state="+state, accountHeaders, nil)
	if err != nil {
		return &PollResult{Done: false}
	}
	var accountData map[string]any
	if json.Unmarshal(accResp.Data, &accountData) != nil || uidField(accountData) == "" {
		m.finish(session, nil, "official API did not return uid")
		return pollFromSession(session)
	}
	creds, err := parseOfficialInfo(map[string]any{"account": accountData}, withExpiry(tokenData, m.now()))
	if err != nil {
		m.finish(session, nil, err.Error())
		return pollFromSession(session)
	}
	m.finish(session, &creds, "")
	return pollFromSession(session)
}

// Take consumes a completed login.
func (m *OAuthManager) Take(loginID string) (*Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[loginID]
	switch {
	case session == nil:
		return nil, fmt.Errorf("login request not found or expired")
	case !session.Done:
		return nil, fmt.Errorf("login is not complete")
	case session.Error != "":
		return nil, errors.New(session.Error)
	case session.Result == nil:
		return nil, fmt.Errorf("login result is empty")
	}
	creds := *session.Result
	delete(m.sessions, loginID)
	return &creds, nil
}

func (m *OAuthManager) finish(session *oauthSession, creds *Credentials, errMsg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session.Done = true
	session.Result = creds
	session.Error = errMsg
}

func (m *OAuthManager) purgeLocked() {
	now := m.now()
	for id, session := range m.sessions {
		if now.After(session.ExpiresAt.Add(ResultRetention)) {
			delete(m.sessions, id)
		}
	}
}

func pollFromSession(session *oauthSession) *PollResult {
	result := &PollResult{Done: session.Done, Error: session.Error}
	if session.Result != nil {
		result.UID = session.Result.UID
		result.Nickname = session.Result.Nickname
		result.Profile = session.Result.Profile
		result.Creds = session.Result
	}
	return result
}

// withExpiry fills expiresAt from expiresIn when the token payload only
// carries the relative value.
func withExpiry(tokenData map[string]any, now time.Time) map[string]any {
	out := make(map[string]any, len(tokenData)+2)
	for k, v := range tokenData {
		out[k] = v
	}
	if unixSeconds(out["expiresAt"], out[KeyExpiresAt]) == 0 {
		if in := unixSeconds(out["expiresIn"], out["expires_in"]); in > 0 && in < 1e10 {
			out["expiresAt"] = now.Unix() + in
		}
	}
	if unixSeconds(out["refreshExpiresAt"], out[KeyRefreshExpiresAt]) == 0 {
		if in := unixSeconds(out["refreshExpiresIn"], out["refresh_expires_in"]); in > 0 && in < 1e10 {
			out["refreshExpiresAt"] = now.Unix() + in
		}
	}
	return out
}

func randomLoginID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("oa_%d", time.Now().UnixNano())
	}
	return "oa_" + hex.EncodeToString(buf[:])
}

// RefreshResult carries the rotated tokens. CodeBuddy rotates the refresh
// token, so the caller must persist it before the next refresh.
//
// Account-profile fields (subscription tier, enterprise, nickname) are filled
// only when the refresh response echoes them; they are empty otherwise and the
// caller keeps whatever it already stored.
type RefreshResult struct {
	AccessToken      string
	RefreshToken     string
	IDToken          string
	TokenType        string
	ExpiresAt        int64
	RefreshExpiresAt int64
	Domain           string
	AccountType      string
	EnterpriseID     string
	EnterpriseName   string
	Nickname         string
}

// AccountInfo is the account profile returned by /v2/plugin/login/account:
// identity plus the subscription tier (AccountType: personal / ultimate /
// exclusive / ...).
type AccountInfo struct {
	UID            string
	Nickname       string
	EnterpriseID   string
	EnterpriseName string
	AccountType    string
}

// FetchAccountInfo reads the account profile for an authenticated identity. It
// is the shared primitive behind refresh-token import and the refresh-path
// subscription-tier backfill.
func FetchAccountInfo(ctx context.Context, client HTTPDoer, id Identity) (*AccountInfo, error) {
	resp, err := DoJSON(ctx, client, http.MethodGet, EndpointForProfile(id.profile())+LoginAccountPath, CredentialHeaders(id), nil)
	if err != nil {
		return nil, err
	}
	if !resp.Success() {
		return nil, fmt.Errorf("fetch account failed: %s", resp.ErrorMessage())
	}
	var account map[string]any
	if json.Unmarshal(resp.Data, &account) != nil {
		return nil, fmt.Errorf("decode account payload")
	}
	return &AccountInfo{
		UID:            uidField(account),
		Nickname:       stringField(account, "nickname"),
		EnterpriseID:   stringField(account, "enterpriseId", KeyEnterpriseID),
		EnterpriseName: stringField(account, "enterpriseName"),
		AccountType:    stringField(account, "type", "accountType", KeyAccountType),
	}, nil
}

// Refresh exchanges the refresh token at the profile's chat origin.
func Refresh(ctx context.Context, client HTTPDoer, creds Credentials) (*RefreshResult, error) {
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, fmt.Errorf("refresh token is missing")
	}
	profile := creds.Profile
	if !ValidProfile(profile) {
		parsed, err := ProfileForAuth(creds.Domain, creds.AccessToken)
		if err != nil {
			return nil, err
		}
		profile = parsed
		creds.Profile = parsed
	}
	resp, err := DoJSON(ctx, client, http.MethodPost, RefreshURL(profile), RefreshHeaders(IdentityOf(creds), creds.RefreshToken), map[string]any{})
	if err != nil {
		return nil, err
	}
	if !resp.Success() || len(resp.Data) == 0 || string(resp.Data) == "null" {
		return nil, fmt.Errorf("refresh token failed: %s", resp.ErrorMessage())
	}
	var data map[string]any
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return nil, fmt.Errorf("decode refresh payload: %w", err)
	}
	data = withExpiry(data, time.Now())
	accessToken := stringField(data, "accessToken", KeyAccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("refresh response missing accessToken")
	}
	expiresAt := unixSeconds(data["expiresAt"], data[KeyExpiresAt])
	refreshExpiresAt := unixSeconds(data["refreshExpiresAt"], data[KeyRefreshExpiresAt])
	if refreshExpiresAt == 0 {
		refreshExpiresAt = expiresAt
	}
	// Some refresh responses echo the account profile (flat, or nested under an
	// "account" object). Capture the subscription tier / enterprise fields when
	// present so the caller can backfill them without an extra round trip.
	account := mapField(data, "account")
	echoField := func(keys ...string) string {
		if v := stringField(account, keys...); v != "" {
			return v
		}
		return stringField(data, keys...)
	}
	return &RefreshResult{
		AccessToken:      accessToken,
		RefreshToken:     firstNonEmpty(stringField(data, "refreshToken", KeyRefreshToken), creds.RefreshToken),
		IDToken:          firstNonEmpty(stringField(data, "idToken", KeyIDToken), creds.IDToken),
		TokenType:        firstNonEmpty(stringField(data, "tokenType", KeyTokenType), "Bearer"),
		ExpiresAt:        expiresAt,
		RefreshExpiresAt: refreshExpiresAt,
		Domain:           firstNonEmpty(stringField(data, "domain"), creds.Domain),
		AccountType:      echoField("type", "accountType", KeyAccountType),
		EnterpriseID:     echoField("enterpriseId", KeyEnterpriseID),
		EnterpriseName:   echoField("enterpriseName", KeyEnterpriseName),
		Nickname:         echoField("nickname"),
	}, nil
}

// ImportRefreshToken turns a pasted refresh token into a full credential for
// an explicitly chosen profile. A domestic issuer cannot tell CLI from
// WorkBuddy, and a cross-region endpoint rejects the token, so the caller must
// pick the profile.
func ImportRefreshToken(ctx context.Context, client HTTPDoer, profile, refreshToken string) (*Credentials, error) {
	if !ValidProfile(profile) {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	refreshToken = strings.TrimSpace(refreshToken)
	uid := TokenSubject(refreshToken)
	if uid == "" {
		return nil, fmt.Errorf("refresh token carries no sub claim")
	}
	base := Credentials{Profile: profile, Domain: ProfileDomains[profile], UID: uid, RefreshToken: refreshToken}
	refreshed, err := Refresh(ctx, client, base)
	if err != nil {
		return nil, err
	}
	info, err := FetchAccountInfo(ctx, client, Identity{Profile: profile, AccessToken: refreshed.AccessToken, Domain: refreshed.Domain, UID: uid})
	if err != nil {
		return nil, err
	}
	if info.UID == "" {
		return nil, fmt.Errorf("official API did not return uid")
	}
	if info.UID != uid {
		return nil, fmt.Errorf("account uid %s does not match the refresh token subject", info.UID)
	}
	creds := Credentials{
		AccessToken:      refreshed.AccessToken,
		RefreshToken:     refreshed.RefreshToken,
		IDToken:          refreshed.IDToken,
		TokenType:        refreshed.TokenType,
		ExpiresAt:        refreshed.ExpiresAt,
		RefreshExpiresAt: refreshed.RefreshExpiresAt,
		Domain:           refreshed.Domain,
		UID:              uid,
		Nickname:         info.Nickname,
		EnterpriseID:     info.EnterpriseID,
		EnterpriseName:   info.EnterpriseName,
		AccountType:      info.AccountType,
		Profile:          profile,
	}
	out, err := finalizeCredentials(creds)
	if err != nil {
		return nil, err
	}
	return &out, nil
}
