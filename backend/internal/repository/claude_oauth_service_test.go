package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/imroc/req/v3"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ClaudeOAuthServiceSuite struct {
	suite.Suite
	client *claudeOAuthService
}

// requestCapture holds captured request data for assertions in the main goroutine.
type requestCapture struct {
	path        string
	method      string
	cookies     []*http.Cookie
	body        []byte
	bodyJSON    map[string]any
	contentType string
}

func newTestReqClient(rt http.RoundTripper) *req.Client {
	c := req.C()
	c.GetClient().Transport = rt
	return c
}

// failingCLIClientFactory fails the test if a claude.ai cookie step asks for
// the CLI-persona client, which claude.ai's Cloudflare challenges.
func failingCLIClientFactory(t *testing.T) func(string) (*req.Client, error) {
	return func(string) (*req.Client, error) {
		t.Errorf("claude.ai cookie steps must use the browser client, not the CLI client")
		return nil, errors.New("unexpected CLI client")
	}
}

// claude.ai's cookie steps must keep the Chrome persona and the account proxy.
func TestCreateBrowserReqClientUsesChromePersonaThroughProxy(t *testing.T) {
	type seen struct{ requestURI, userAgent, secCHUA string }
	got := make(chan seen, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.RequestURI, r.Header.Get("User-Agent"), r.Header.Get("Sec-Ch-Ua")}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxy.Close()

	client, err := createBrowserReqClient(proxy.URL)
	require.NoError(t, err)
	resp, err := client.R().Get("http://claude.invalid/api/organizations")
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	r := <-got
	// An absolute request URI means the request went through the proxy.
	require.Equal(t, "http://claude.invalid/api/organizations", r.requestURI)
	require.Contains(t, r.userAgent, "Chrome/")
	require.NotEmpty(t, r.secCHUA)

	_, err = createBrowserReqClient("ftp://proxy.invalid:21")
	require.Error(t, err, "an unusable proxy must fail instead of connecting directly")
}

// cloudflareChallengeThen answers the first n requests with Cloudflare's bot
// challenge, then delegates to next.
func cloudflareChallengeThen(n int, calls *int, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if *calls <= n {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<!DOCTYPE html><html><head><title>Just a moment...</title></head></html>"))
			return
		}
		next(w, r)
	}
}

func newChallengeTestClient(t *testing.T, handler http.HandlerFunc, clients *int) *claudeOAuthService {
	t.Helper()
	prevDelay := claudeAIChallengeRetryDelay
	claudeAIChallengeRetryDelay = 0
	t.Cleanup(func() { claudeAIChallengeRetryDelay = prevDelay })
	rt := newInProcessTransport(handler, nil)
	client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
	require.True(t, ok)
	client.baseURL = "http://in-process"
	client.browserClientFactory = func(string) (*req.Client, error) {
		*clients++
		return newTestReqClient(rt), nil
	}
	client.clientFactory = failingCLIClientFactory(t)
	return client
}

func TestClaudeOAuthCookieStepsRetryCloudflareChallenge(t *testing.T) {
	var calls, clients int
	client := newChallengeTestClient(t, cloudflareChallengeThen(2, &calls, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/organizations" {
			_, _ = w.Write([]byte(`[{"uuid":"org-1","name":"Personal"}]`))
			return
		}
		_, _ = w.Write([]byte(`{"redirect_uri":"https://platform.claude.com/oauth/code/callback?code=AUTH&state=ST"}`))
	}), &clients)

	org, err := client.GetOrganizationUUID(context.Background(), "sess", "")
	require.NoError(t, err)
	require.Equal(t, "org-1", org)
	require.Equal(t, 3, calls)
	require.Equal(t, 3, clients, "each resend uses a fresh browser client")

	calls, clients = 0, 0
	code, err := client.GetAuthorizationCode(context.Background(), "sess", "org-1", oauth.ScopeInference, "cc", "ST", "")
	require.NoError(t, err)
	require.Equal(t, "AUTH#ST", code)
	require.Equal(t, 3, calls)
}

func TestClaudeOAuthCookieStepsGiveUpOnPersistentChallenge(t *testing.T) {
	var calls, clients int
	client := newChallengeTestClient(t, cloudflareChallengeThen(100, &calls, nil), &clients)

	_, err := client.GetOrganizationUUID(context.Background(), "sess", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Cloudflare challenged")
	require.NotContains(t, err.Error(), "<!DOCTYPE", "the challenge page must not be surfaced")
	require.Equal(t, claudeAIChallengeAttempts, calls)

	calls = 0
	_, err = client.GetAuthorizationCode(context.Background(), "sess", "org-1", oauth.ScopeInference, "cc", "ST", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "Cloudflare challenged")
	require.Equal(t, claudeAIChallengeAttempts, calls)
}

func TestClaudeOAuthCookieStepsDoNotRetryOriginErrors(t *testing.T) {
	var calls, clients int
	client := newChallengeTestClient(t, cloudflareChallengeThen(0, &calls, func(w http.ResponseWriter, r *http.Request) {
		// claude.ai's own answer to an invalid sessionKey: a JSON 403, not a challenge.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"Invalid authorization"}}`))
	}), &clients)

	_, err := client.GetOrganizationUUID(context.Background(), "sess", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "403")
	require.Equal(t, 1, calls)
}

func TestNewClaudeOAuthClientWiresBrowserClientForClaudeAI(t *testing.T) {
	client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
	require.True(t, ok)
	require.NotNil(t, client.browserClientFactory)
	require.NotNil(t, client.clientFactory)
}

func (s *ClaudeOAuthServiceSuite) TestGetOrganizationUUID() {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantErr    bool
		errContain string
		wantUUID   string
		validate   func(captured requestCapture)
	}{
		{
			name: "success",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"uuid":"org-1"}]`))
			},
			wantUUID: "org-1",
			validate: func(captured requestCapture) {
				require.Equal(s.T(), "/api/organizations", captured.path, "unexpected path")
				require.Len(s.T(), captured.cookies, 1, "expected 1 cookie")
				require.Equal(s.T(), "sessionKey", captured.cookies[0].Name)
				require.Equal(s.T(), "sess", captured.cookies[0].Value)
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("unauthorized"))
			},
			wantErr:    true,
			errContain: "401",
		},
		{
			name: "invalid_json_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("not-json"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.path = r.URL.Path
				captured.cookies = r.Cookies()
				tt.handler(w, r)
			}), nil)

			client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
			require.True(s.T(), ok, "type assertion failed")
			s.client = client
			s.client.baseURL = "http://in-process"
			s.client.browserClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }
			s.client.clientFactory = failingCLIClientFactory(s.T())

			got, err := s.client.GetOrganizationUUID(context.Background(), "sess", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				if tt.errContain != "" {
					require.ErrorContains(s.T(), err, tt.errContain)
				}
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantUUID, got)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

func (s *ClaudeOAuthServiceSuite) TestGetAuthorizationCode() {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantErr  bool
		wantCode string
		validate func(captured requestCapture)
	}{
		{
			name: "parses_redirect_uri",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"redirect_uri": oauth.RedirectURI + "?code=AUTH&state=STATE",
				})
			},
			wantCode: "AUTH#STATE",
			validate: func(captured requestCapture) {
				require.True(s.T(), strings.HasPrefix(captured.path, "/v1/oauth/") && strings.HasSuffix(captured.path, "/authorize"), "unexpected path: %s", captured.path)
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				require.Len(s.T(), captured.cookies, 1, "expected 1 cookie")
				require.Equal(s.T(), "sess", captured.cookies[0].Value)
				require.Equal(s.T(), "org-1", captured.bodyJSON["organization_uuid"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
				require.Equal(s.T(), oauth.RedirectURI, captured.bodyJSON["redirect_uri"])
				require.Equal(s.T(), "st", captured.bodyJSON["state"])
			},
		},
		{
			name: "missing_code_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"redirect_uri": oauth.RedirectURI + "?state=STATE", // no code
				})
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.path = r.URL.Path
				captured.method = r.Method
				captured.cookies = r.Cookies()
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
			require.True(s.T(), ok, "type assertion failed")
			s.client = client
			s.client.baseURL = "http://in-process"
			s.client.browserClientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }
			s.client.clientFactory = failingCLIClientFactory(s.T())

			code, err := s.client.GetAuthorizationCode(context.Background(), "sess", "org-1", oauth.ScopeInference, "cc", "st", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantCode, code)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

func (s *ClaudeOAuthServiceSuite) TestExchangeCodeForToken() {
	tests := []struct {
		name         string
		handler      http.HandlerFunc
		code         string
		isSetupToken bool
		wantErr      bool
		wantResp     *oauth.TokenResponse
		validate     func(captured requestCapture)
	}{
		{
			name: "sends_state_when_embedded",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "at",
					TokenType:    "bearer",
					ExpiresIn:    3600,
					RefreshToken: "rt",
					Scope:        "s",
				})
			},
			code:         "AUTH#STATE2",
			isSetupToken: false,
			wantResp: &oauth.TokenResponse{
				AccessToken:  "at",
				RefreshToken: "rt",
			},
			validate: func(captured requestCapture) {
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				require.True(s.T(), strings.HasPrefix(captured.contentType, "application/json"), "unexpected content-type")
				require.Equal(s.T(), "AUTH", captured.bodyJSON["code"])
				require.Equal(s.T(), "STATE2", captured.bodyJSON["state"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
				require.Equal(s.T(), oauth.RedirectURI, captured.bodyJSON["redirect_uri"])
				require.Equal(s.T(), "ver", captured.bodyJSON["code_verifier"])
				// Regular OAuth should not include expires_in
				require.Nil(s.T(), captured.bodyJSON["expires_in"], "regular OAuth should not include expires_in")
			},
		},
		{
			name: "setup_token_omits_expires_in",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken: "at",
					TokenType:   "bearer",
					ExpiresIn:   31536000,
				})
			},
			code:         "AUTH",
			isSetupToken: true,
			wantResp: &oauth.TokenResponse{
				AccessToken: "at",
			},
			validate: func(captured requestCapture) {
				require.Nil(s.T(), captured.bodyJSON["expires_in"], "setup token should not include expires_in")
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("bad request"))
			},
			code:         "AUTH",
			isSetupToken: false,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.method = r.Method
				captured.contentType = r.Header.Get("Content-Type")
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
			require.True(s.T(), ok, "type assertion failed")
			s.client = client
			s.client.tokenURL = "http://in-process/token"
			s.client.clientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			resp, err := s.client.ExchangeCodeForToken(context.Background(), tt.code, "ver", "", "", tt.isSetupToken)

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantResp.AccessToken, resp.AccessToken)
			require.Equal(s.T(), tt.wantResp.RefreshToken, resp.RefreshToken)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

func (s *ClaudeOAuthServiceSuite) TestRefreshToken() {
	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantErr  bool
		wantResp *oauth.TokenResponse
		validate func(captured requestCapture)
	}{
		{
			name: "sends_json_format",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "new_access_token",
					TokenType:    "bearer",
					ExpiresIn:    28800,
					RefreshToken: "new_refresh_token",
					Scope:        "user:profile user:inference",
				})
			},
			wantResp: &oauth.TokenResponse{
				AccessToken:  "new_access_token",
				RefreshToken: "new_refresh_token",
			},
			validate: func(captured requestCapture) {
				require.Equal(s.T(), http.MethodPost, captured.method, "expected POST")
				// 验证使用 JSON 格式（不是 form 格式）
				require.True(s.T(), strings.HasPrefix(captured.contentType, "application/json"),
					"expected JSON content-type, got: %s", captured.contentType)
				// 验证 JSON body 内容
				require.Equal(s.T(), "refresh_token", captured.bodyJSON["grant_type"])
				require.Equal(s.T(), "rt", captured.bodyJSON["refresh_token"])
				require.Equal(s.T(), oauth.ClientID, captured.bodyJSON["client_id"])
			},
		},
		{
			name: "returns_new_refresh_token",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
					AccessToken:  "at",
					TokenType:    "bearer",
					ExpiresIn:    28800,
					RefreshToken: "rotated_rt", // Anthropic rotates refresh tokens
				})
			},
			wantResp: &oauth.TokenResponse{
				AccessToken:  "at",
				RefreshToken: "rotated_rt",
			},
		},
		{
			name: "non_200_returns_error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		s.Run(tt.name, func() {
			var captured requestCapture

			rt := newInProcessTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.method = r.Method
				captured.contentType = r.Header.Get("Content-Type")
				captured.body, _ = io.ReadAll(r.Body)
				_ = json.Unmarshal(captured.body, &captured.bodyJSON)
				tt.handler(w, r)
			}), nil)

			client, ok := NewClaudeOAuthClient().(*claudeOAuthService)
			require.True(s.T(), ok, "type assertion failed")
			s.client = client
			s.client.tokenURL = "http://in-process/token"
			s.client.clientFactory = func(string) (*req.Client, error) { return newTestReqClient(rt), nil }

			resp, err := s.client.RefreshToken(context.Background(), "rt", "")

			if tt.wantErr {
				require.Error(s.T(), err)
				return
			}

			require.NoError(s.T(), err)
			require.Equal(s.T(), tt.wantResp.AccessToken, resp.AccessToken)
			require.Equal(s.T(), tt.wantResp.RefreshToken, resp.RefreshToken)
			if tt.validate != nil {
				tt.validate(captured)
			}
		})
	}
}

func TestClaudeOAuthServiceSuite(t *testing.T) {
	suite.Run(t, new(ClaudeOAuthServiceSuite))
}
