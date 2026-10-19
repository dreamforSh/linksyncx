package service

import (
	"net/http"
	"strings"
	"testing"
)

func TestCustomUsageAccountDefaultsRequiresBaseURL(t *testing.T) {
	account := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://api.example.com/",
			"api_key":  "sk-test",
		},
	}
	c, configured := customUsageAccountDefaults(account)
	if !configured || !c.Enabled {
		t.Fatalf("expected auto-configuration for API-key account with base_url, got configured=%v enabled=%v", configured, c.Enabled)
	}
	if c.Request.URL != "{{baseUrl}}/v1/usage" || c.BaseURL != "https://api.example.com" {
		t.Fatalf("unexpected defaults: url=%q base=%q", c.Request.URL, c.BaseURL)
	}

	official := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}}
	if _, configured := customUsageAccountDefaults(official); configured {
		t.Fatal("account without base_url must not be auto-configured")
	}
}

func TestPublicCustomUsageRedactsLiteralSecrets(t *testing.T) {
	c := CustomUsageConfig{
		Enabled:     true,
		Template:    "custom",
		APIKey:      "sk-secret",
		AccessToken: "access-secret",
		Request: CustomUsageRequest{
			URL:    "{{baseUrl}}/v1/usage?token=literal-secret&key={{apiKey}}",
			Method: http.MethodGet,
			Headers: map[string]string{
				"Authorization": "Bearer {{apiKey}}",
				"X-Literal":     "header-secret",
			},
		},
	}
	public := publicCustomUsage(c)
	if public.APIKey != "" || public.AccessToken != "" {
		t.Fatal("public config must not expose API key or access token")
	}
	if !strings.Contains(public.Request.URL, "{{apiKey}}") {
		t.Fatalf("placeholder must stay editable: %q", public.Request.URL)
	}
	if strings.Contains(public.Request.URL, "literal-secret") {
		t.Fatalf("literal query value leaked: %q", public.Request.URL)
	}
	if public.Request.Headers["Authorization"] != "Bearer {{apiKey}}" {
		t.Fatalf("template header changed: %q", public.Request.Headers["Authorization"])
	}
	if public.Request.Headers["X-Literal"] != "" {
		t.Fatalf("literal header value leaked: %q", public.Request.Headers["X-Literal"])
	}
}

func TestMergeCustomUsagePreservesSavedSecrets(t *testing.T) {
	old := CustomUsageConfig{
		Template: "custom",
		Request: CustomUsageRequest{
			URL:     "https://api.example.com/usage",
			Method:  http.MethodGet,
			Headers: map[string]string{"Authorization": "Bearer {{apiKey}}"},
		},
	}
	secrets := customUsageSecrets{
		APIKey:      "saved-key",
		AccessToken: "saved-token",
		RequestURL:  old.Request.URL,
		Headers:     old.Request.Headers,
	}
	merged, next := mergeCustomUsage(CustomUsageConfig{
		Template: "custom",
		Request: CustomUsageRequest{
			Method:  http.MethodGet,
			Headers: map[string]string{"Authorization": ""},
		},
	}, old, secrets)
	if next.APIKey != "saved-key" || next.AccessToken != "saved-token" {
		t.Fatalf("empty secret fields must preserve saved values: %+v", next)
	}
	if merged.Request.URL != old.Request.URL {
		t.Fatalf("empty URL must keep the saved URL: %q", merged.Request.URL)
	}
	if merged.Request.Headers["Authorization"] != "Bearer {{apiKey}}" {
		t.Fatalf("empty header must keep the saved value: %q", merged.Request.Headers["Authorization"])
	}

	_, cleared := mergeCustomUsage(CustomUsageConfig{ClearAPIKey: true}, old, secrets)
	if cleared.APIKey != "" {
		t.Fatal("clear_api_key must clear the saved key")
	}

	_, replaced := mergeCustomUsage(CustomUsageConfig{APIKey: "new-key"}, old, secrets)
	if replaced.APIKey != "new-key" {
		t.Fatal("non-empty secret must replace the saved key")
	}
}

func TestStripCustomUsageManaged(t *testing.T) {
	if got := StripCustomUsageManaged(nil, CustomUsageExtraKey); got != nil {
		t.Fatalf("nil map must stay nil, got %#v", got)
	}
	values := map[string]any{
		CustomUsageExtraKey: map[string]any{"enabled": true},
		"keep":              "value",
	}
	stripped := StripCustomUsageManaged(values, CustomUsageExtraKey)
	if _, exists := stripped[CustomUsageExtraKey]; exists {
		t.Fatal("managed key must be removed")
	}
	if stripped["keep"] != "value" {
		t.Fatal("unrelated keys must be preserved")
	}
	if _, exists := values[CustomUsageExtraKey]; !exists {
		t.Fatal("input map must not be mutated")
	}
}
