package oauth

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthorizeURLMatchesClaudeCodeCLI(t *testing.T) {
	const want = "https://claude.com/cai/oauth/authorize"
	if AuthorizeURL != want {
		t.Fatalf("AuthorizeURL = %q, want %q", AuthorizeURL, want)
	}

	authURL := BuildAuthorizationURL("state-value", "challenge-value", ScopeOAuth)
	if !strings.HasPrefix(authURL, want+"?") {
		t.Fatalf("BuildAuthorizationURL() = %q, want prefix %q", authURL, want+"?")
	}
	for _, part := range []string{
		"code=true",
		"client_id=" + ClientID,
		"response_type=code",
		"code_challenge=challenge-value",
		"code_challenge_method=S256",
		"state=state-value",
	} {
		if !strings.Contains(authURL, part) {
			t.Fatalf("BuildAuthorizationURL() missing %q\nURL: %s", part, authURL)
		}
	}
}

// Claude Code 2.1.287：登录 URL 的 scope 为 k7r()=[org:create_api_key, user:profile]
// + XZe()（默认集 + user:plugins），刷新默认请求 XZe()。
func TestScopesMatchClaudeCodeCLI(t *testing.T) {
	const wantAPI = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload user:plugins"
	if ScopeAPI != wantAPI {
		t.Fatalf("ScopeAPI = %q, want %q", ScopeAPI, wantAPI)
	}
	if ScopeOAuth != "org:create_api_key "+wantAPI {
		t.Fatalf("ScopeOAuth = %q", ScopeOAuth)
	}
	authURL := BuildAuthorizationURL("s", "c", ScopeOAuth)
	if !strings.Contains(authURL, "scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference+user%3Asessions%3Aclaude_code+user%3Amcp_servers+user%3Afile_upload+user%3Aplugins&") {
		t.Fatalf("authorize URL scope not encoded like URLSearchParams: %s", authURL)
	}
	if got := RefreshScope("user:inference user:projects:write user:profile user:projects:read"); got != wantAPI+" user:projects:write user:projects:read" {
		t.Fatalf("RefreshScope kept wrong scopes: %q", got)
	}
	if got := RefreshScope(""); got != wantAPI {
		t.Fatalf("RefreshScope(\"\") = %q", got)
	}
}

func TestParseAuthorizationCode(t *testing.T) {
	tests := []struct {
		in, code, state string
	}{
		{"AUTH#STATE", "AUTH", "STATE"},
		{"  AUTH#STATE \n", "AUTH", "STATE"},
		{"AUTH", "AUTH", ""},
		{"", "", ""},
		{RedirectURI + "?code=AUTH&state=STATE", "AUTH", "STATE"},
		{RedirectURI + "?code=AUTH%23STATE", "AUTH", "STATE"},
		{"code=AUTH&state=STATE", "AUTH", "STATE"},
		{"?code=AUTH&state=STATE", "AUTH", "STATE"},
		{"https://example.com/cb#code=AUTH&state=STATE", "AUTH", "STATE"},
	}
	for _, tt := range tests {
		code, state := ParseAuthorizationCode(tt.in)
		if code != tt.code || state != tt.state {
			t.Errorf("ParseAuthorizationCode(%q) = (%q, %q), want (%q, %q)", tt.in, code, state, tt.code, tt.state)
		}
	}
}

func TestSessionStore_Stop_Idempotent(t *testing.T) {
	store := NewSessionStore()

	store.Stop()
	store.Stop()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}

func TestSessionStore_Stop_Concurrent(t *testing.T) {
	store := NewSessionStore()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Stop()
		}()
	}

	wg.Wait()

	select {
	case <-store.stopCh:
		// ok
	case <-time.After(time.Second):
		t.Fatal("stopCh 未关闭")
	}
}
