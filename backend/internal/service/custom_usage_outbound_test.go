package service

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type customUsageRepoStub struct {
	mu      sync.Mutex
	account *Account
}

func (r *customUsageRepoStub) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	copied := *r.account
	return &copied, nil
}

func (r *customUsageRepoStub) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if a, err := r.GetByID(ctx, id); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *customUsageRepoStub) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	return 0, errors.New("not used")
}

type customUsageRoundTrip func(*http.Request) (*http.Response, error)

func (f customUsageRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func customUsageTestAccount() *Account {
	return &Account{
		ID:   7,
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example.com/v1",
			"api_key":  "sk-account-secret-key",
		},
		Extra: map[string]any{
			CustomUsageExtraKey: map[string]any{
				"enabled":          true,
				"template":         "general",
				"timeout_seconds":  5,
				"interval_minutes": 10,
			},
		},
	}
}

func customUsageJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestCustomUsageQueryFailsClosedWhenAssignedProxyIsUnresolvable(t *testing.T) {
	account := customUsageTestAccount()
	proxyID := int64(3)
	account.ProxyID = &proxyID // relation not loaded: must never fall back to a direct dial
	svc := NewCustomUsageService(&customUsageRepoStub{account: account})
	clients := 0
	svc.newClient = func(string) (*http.Client, error) {
		clients++
		return nil, errors.New("must not build a client")
	}

	result, err := svc.Query(context.Background(), account.ID, true, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Error != "proxy_unavailable" {
		t.Fatalf("expected proxy_unavailable, got %+v", result)
	}
	if clients != 0 {
		t.Fatalf("no client may be built for an unresolvable proxy, built %d", clients)
	}
}

func TestCustomUsageQueryUsesAccountProxyAndSurvivesCallerCancel(t *testing.T) {
	account := customUsageTestAccount()
	proxyID := int64(3)
	account.ProxyID = &proxyID
	account.Proxy = &Proxy{ID: proxyID, Protocol: "socks5", Host: "127.0.0.1", Port: 7891}
	svc := NewCustomUsageService(&customUsageRepoStub{account: account})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var gotProxy, gotAuth, gotURL string
	svc.newClient = func(proxyURL string) (*http.Client, error) {
		gotProxy = proxyURL
		return &http.Client{Transport: customUsageRoundTrip(func(req *http.Request) (*http.Response, error) {
			gotAuth = req.Header.Get("Authorization")
			gotURL = req.URL.String()
			// The admin page aborts in-flight queries when a row scrolls away;
			// the upstream answer must still be read and cached.
			cancel()
			if err := req.Context().Err(); err != nil {
				return nil, err
			}
			return customUsageJSONResponse(`{"remaining": 12.5}`), nil
		})}, nil
	}

	result, err := svc.Query(ctx, account.ID, true, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if result.Error != "" || result.Remaining == nil || *result.Remaining != 12.5 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if !strings.HasPrefix(gotProxy, "socks5://127.0.0.1:7891") {
		t.Fatalf("request must use the account proxy, got %q", gotProxy)
	}
	if gotAuth != "Bearer sk-account-secret-key" || gotURL != "https://relay.example.com/v1/usage" {
		t.Fatalf("unexpected request: url=%q auth=%q", gotURL, gotAuth)
	}

	cached, err := svc.Query(context.Background(), account.ID, false, nil)
	if err != nil {
		t.Fatalf("cached Query: %v", err)
	}
	if cached.Error != "" || cached.Remaining == nil || *cached.Remaining != 12.5 {
		t.Fatalf("cancelled caller must not poison the cache: %+v", cached)
	}
}

func TestPrepareCustomUsageKeepsAccountCredentialsOnTheAccountHost(t *testing.T) {
	account := customUsageTestAccount()
	draft := CustomUsageConfig{
		Enabled:        true,
		Template:       "custom",
		BaseURL:        "https://collector.example.net",
		TimeoutSeconds: 5,
		Request: CustomUsageRequest{
			URL:     "{{baseUrl}}/usage",
			Method:  http.MethodGet,
			Headers: map[string]string{"X-Key": "{{apiKey}}"},
		},
		Extractor: CustomUsageExtractor{Remaining: &CustomUsageNumber{Path: "remaining"}},
	}
	if _, err := prepareCustomUsage(account, draft, customUsageSecrets{}); !errors.Is(err, ErrCustomUsageConfig) {
		t.Fatalf("the account key must not be sent to another host, got %v", err)
	}

	// A credential entered for this config may go to the host it was entered for.
	if _, err := prepareCustomUsage(account, draft, customUsageSecrets{APIKey: "dashboard-key-123"}); err != nil {
		t.Fatalf("a dedicated credential may target any public host: %v", err)
	}

	draft.BaseURL = "https://RELAY.example.com."
	if _, err := prepareCustomUsage(account, draft, customUsageSecrets{}); err != nil {
		t.Fatalf("the account's own host must stay allowed: %v", err)
	}
	draft.BaseURL = "https://relay.example.com:443"
	if _, err := prepareCustomUsage(account, draft, customUsageSecrets{}); err != nil {
		t.Fatalf("the explicit default port is the same origin: %v", err)
	}

	// Same host, other scheme or port: another destination for the account key.
	for _, base := range []string{"http://relay.example.com", "https://relay.example.com:8443"} {
		draft.BaseURL = base
		if _, err := prepareCustomUsage(account, draft, customUsageSecrets{}); !errors.Is(err, ErrCustomUsageConfig) {
			t.Fatalf("the account key must not go to %s: %v", base, err)
		}
	}
}

func TestCustomUsageAccountDefaultsSkipOfficialAPIs(t *testing.T) {
	for _, base := range []string{"https://api.anthropic.com", "https://api.openai.com/v1", "https://api.minimaxi.com/v1", "https://api.commandcode.ai/provider/v1"} {
		account := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": base, "api_key": "sk-test"}}
		if _, configured := customUsageAccountDefaults(account); configured {
			t.Fatalf("%s must not be auto-configured", base)
		}
	}
}

func TestParseCustomUsageOnlyRejectsEchoedCredentials(t *testing.T) {
	c := CustomUsageConfig{Extractor: CustomUsageExtractor{
		Remaining: &CustomUsageNumber{Path: "remaining"},
		Unit:      &CustomUsageText{Path: "unit"},
		PlanName:  &CustomUsageText{Value: "Plan 1"},
	}}
	sensitive := []string{"USD", "1", "sk-account-secret-key"}
	result, err := parseCustomUsage([]byte(`{"remaining": 3, "unit": "USD"}`), c, sensitive)
	if err != nil || result.Unit != "USD" || result.PlanName != "Plan 1" {
		t.Fatalf("short header/query values must not reject units or literals: %+v %v", result, err)
	}
	if _, err := parseCustomUsage([]byte(`{"remaining": 3, "unit": "key sk-account-secret-key"}`), c, sensitive); err == nil {
		t.Fatal("a response echoing the credential must be rejected")
	}
}

func TestCustomUsageClientTunnelsPinnedIPThroughHTTPProxy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	connectLine := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		connectLine <- strings.TrimSpace(line)
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
	}()

	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
	}
	client, err := newCustomUsageClientWithLookup("http://"+listener.Addr().String(), lookup)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://relay.example.com/v1/usage", nil)
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("a refused tunnel must fail the request")
	}
	select {
	case line := <-connectLine:
		if line != "CONNECT 93.184.216.34:443 HTTP/1.1" {
			t.Fatalf("the proxy must receive the validated IP literal, got %q", line)
		}
	case <-ctx.Done():
		t.Fatal("request never reached the proxy")
	}

	if _, err := newCustomUsageClient("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("an unsupported proxy scheme must fail instead of dialing directly")
	}
}
