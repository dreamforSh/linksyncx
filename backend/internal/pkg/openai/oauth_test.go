package openai

import (
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// 授权链接对齐 Codex CLI 0.159.2：参数顺序、scope（含 api.connectors.*）、originator。
func TestBuildAuthorizationURLMatchesCodexCLI(t *testing.T) {
	authURL := BuildAuthorizationURLForPlatform("state-1", "challenge-1", DefaultRedirectURI, OAuthPlatformOpenAI, "codex-tui")
	want := AuthorizeURL + "?response_type=code&client_id=" + ClientID +
		"&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback&code_challenge=challenge-1&code_challenge_method=S256" +
		"&state=state-1&scope=openid+profile+email+offline_access+api.connectors.read+api.connectors.invoke" +
		"&id_token_add_organizations=true&codex_cli_simplified_flow=true&originator=codex-tui"
	if authURL != want {
		t.Fatalf("authorize URL mismatch: got=%s want=%s", authURL, want)
	}
	if strings.Contains(BuildAuthorizationURL("s", "c", ""), "originator=") {
		t.Fatal("empty originator must be omitted")
	}

	verifier, err := GenerateCodeVerifier()
	if err != nil || len(verifier) != 86 || strings.ContainsAny(verifier, "+/=") {
		t.Fatalf("code verifier must be base64url of 64 bytes, got %q (%v)", verifier, err)
	}
	state, err := GenerateState()
	if err != nil || len(state) != 43 || strings.ContainsAny(state, "+/=") {
		t.Fatalf("state must be base64url of 32 bytes, got %q (%v)", state, err)
	}
}

func TestBuildAuthorizationURLForPlatform_OpenAI(t *testing.T) {
	authURL := BuildAuthorizationURLForPlatform("state-1", "challenge-1", DefaultRedirectURI, OAuthPlatformOpenAI, "")
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("Parse URL failed: %v", err)
	}
	q := parsed.Query()
	if got := q.Get("client_id"); got != ClientID {
		t.Fatalf("client_id mismatch: got=%q want=%q", got, ClientID)
	}
	if got := q.Get("codex_cli_simplified_flow"); got != "true" {
		t.Fatalf("codex flow mismatch: got=%q want=true", got)
	}
	if got := q.Get("id_token_add_organizations"); got != "true" {
		t.Fatalf("id_token_add_organizations mismatch: got=%q want=true", got)
	}
}
