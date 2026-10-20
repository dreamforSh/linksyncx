package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func customUsageMergeSaved() (CustomUsageConfig, customUsageSecrets) {
	old := CustomUsageConfig{
		Enabled:        true,
		Template:       "custom",
		BaseURL:        "https://dash.example.com",
		TimeoutSeconds: 5,
		Request: CustomUsageRequest{
			URL:    "{{baseUrl}}/api/usage?token=lit-query-secret&uid={{userId}}",
			Method: http.MethodGet,
			Headers: map[string]string{
				"Authorization": "Bearer {{apiKey}}",
				"X-Token":       "lit-header-secret",
			},
		},
		Extractor: CustomUsageExtractor{Remaining: &CustomUsageNumber{Path: "remaining"}},
	}
	return old, customUsageSecrets{
		APIKey:      "saved-dashboard-key",
		AccessToken: "saved-dashboard-token",
		RequestURL:  old.Request.URL,
		Headers:     old.Request.Headers,
	}
}

func TestMergeCustomUsagePublicRoundTripIsNoOp(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	old.Request.URL = "https://dash.example.com/api/usage?z=last&token=lit-query-secret&token={{apiKey}}&token=a%2Bb"
	for _, accountBase := range []string{"", "https://relay.example.com/v1"} {
		merged, next := mergeCustomUsage(publicCustomUsage(old), old, secrets, accountBase)
		if !reflect.DeepEqual(merged, old) {
			t.Fatalf("GET -> PUT without edits must restore the saved config\n got %+v\nwant %+v", merged, old)
		}
		if !reflect.DeepEqual(next, secrets) {
			t.Fatalf("GET -> PUT without edits must keep every secret: %+v", next)
		}
	}

	// An empty base_url means the account's; the editor fills that in before saving.
	old.BaseURL = ""
	old.Request.URL = "{{baseUrl}}/api/usage?token=lit-query-secret"
	incoming := publicCustomUsage(old)
	incoming.BaseURL = "https://relay.example.com/v1"
	merged, next := mergeCustomUsage(incoming, old, secrets, "https://relay.example.com/v1/")
	if merged.Request.URL != old.Request.URL || merged.Request.Headers["X-Token"] != "lit-header-secret" || next.APIKey != secrets.APIKey {
		t.Fatalf("an explicit account base_url is the same target as an empty one: %+v %+v", merged.Request, next)
	}
}

func TestMergeCustomUsagePathEditKeepsQuerySecret(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	incoming := publicCustomUsage(old)
	if incoming.Request.URL != "{{baseUrl}}/api/usage?token=&uid={{userId}}" {
		t.Fatalf("unexpected redacted URL %q", incoming.Request.URL)
	}
	incoming.Request.URL = "{{baseUrl}}/api/v2/usage?uid={{userId}}&token=&extra=typed"
	merged, next := mergeCustomUsage(incoming, old, secrets, "")
	if want := "{{baseUrl}}/api/v2/usage?uid={{userId}}&token=lit-query-secret&extra=typed"; merged.Request.URL != want {
		t.Fatalf("a path edit must keep the saved query value in place:\n got %q\nwant %q", merged.Request.URL, want)
	}
	if merged.Request.Headers["X-Token"] != "lit-header-secret" || next.APIKey != "saved-dashboard-key" || next.AccessToken != "saved-dashboard-token" {
		t.Fatalf("same-origin edit must keep saved credentials: %+v %+v", merged.Request.Headers, next)
	}

	// A value typed by the admin replaces the saved one.
	incoming.Request.URL = "{{baseUrl}}/api/v2/usage?token=new-value"
	merged, _ = mergeCustomUsage(incoming, old, secrets, "")
	if merged.Request.URL != "{{baseUrl}}/api/v2/usage?token=new-value" {
		t.Fatalf("typed query values must win: %q", merged.Request.URL)
	}
}

func TestMergeCustomUsageQueryRestoreKeepsPlaceholdersAndRepeatedValues(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	old.BaseURL = ""
	old.Request.URL = "https://dash.example.com/usage?k=one&k={{apiKey}}&k=th%2Bree&uid={{userId}}"
	incoming := publicCustomUsage(old)
	if incoming.Request.URL != "https://dash.example.com/usage?k=&k={{apiKey}}&k=&uid={{userId}}" {
		t.Fatalf("unexpected redacted URL %q", incoming.Request.URL)
	}
	incoming.Request.URL = "https://dash.example.com/v2/usage?uid={{userId}}&k=&k={{apiKey}}&k="
	merged, _ := mergeCustomUsage(incoming, old, secrets, "")
	if want := "https://dash.example.com/v2/usage?uid={{userId}}&k=one&k={{apiKey}}&k=th%2Bree"; merged.Request.URL != want {
		t.Fatalf("repeated values must be restored in order, verbatim:\n got %q\nwant %q", merged.Request.URL, want)
	}

	// The j-th blank or typed value of a name stands for the j-th value GET hid;
	// placeholders, visible in GET, do not count, wherever they were moved.
	for _, tc := range []struct{ url, want string }{
		// A value typed into the first hidden slot replaces only that one.
		{"https://dash.example.com/v2/usage?k=typed&k={{apiKey}}&k=", "https://dash.example.com/v2/usage?k=typed&k={{apiKey}}&k=th%2Bree"},
		// A blanked placeholder reads as one more hidden slot.
		{"https://dash.example.com/v2/usage?k=&k=&k=typed", "https://dash.example.com/v2/usage?k=one&k=th%2Bree&k=typed"},
		// Removing the placeholder pair, or moving it, keeps both hidden values.
		{"https://dash.example.com/v2/usage?k=&k=", "https://dash.example.com/v2/usage?k=one&k=th%2Bree"},
		{"https://dash.example.com/v2/usage?k={{apiKey}}&k=&k=", "https://dash.example.com/v2/usage?k={{apiKey}}&k=one&k=th%2Bree"},
		// Removing a hidden pair drops the last hidden value.
		{"https://dash.example.com/v2/usage?k=", "https://dash.example.com/v2/usage?k=one"},
	} {
		incoming.Request.URL = tc.url
		if merged, _ := mergeCustomUsage(incoming, old, secrets, ""); merged.Request.URL != tc.want {
			t.Fatalf("%q:\n got %q\nwant %q", tc.url, merged.Request.URL, tc.want)
		}
	}

	// A literal saved after a placeholder survives the placeholder pair being
	// deleted or reordered on a same-origin path edit.
	old.Request.URL = "{{baseUrl}}/api/usage?k={{apiKey}}&k=lit-query-secret"
	old.BaseURL = "https://dash.example.com"
	incoming = publicCustomUsage(old)
	if incoming.Request.URL != "{{baseUrl}}/api/usage?k={{apiKey}}&k=" {
		t.Fatalf("unexpected redacted URL %q", incoming.Request.URL)
	}
	for _, tc := range []struct{ url, want string }{
		{"{{baseUrl}}/api/v2/usage?k=", "{{baseUrl}}/api/v2/usage?k=lit-query-secret"},
		{"{{baseUrl}}/api/v2/usage?k=&k={{apiKey}}", "{{baseUrl}}/api/v2/usage?k=lit-query-secret&k={{apiKey}}"},
	} {
		incoming.Request.URL = tc.url
		if merged, _ := mergeCustomUsage(incoming, old, secrets, ""); merged.Request.URL != tc.want {
			t.Fatalf("%q:\n got %q\nwant %q", tc.url, merged.Request.URL, tc.want)
		}
	}

	// A saved empty value is a hidden slot too, so later values keep their place.
	old.Request.URL = "{{baseUrl}}/api/usage?k=&k=lit-query-secret"
	incoming.Request.URL = "{{baseUrl}}/api/v2/usage?k=&k="
	if merged, _ := mergeCustomUsage(incoming, old, secrets, ""); merged.Request.URL != "{{baseUrl}}/api/v2/usage?k=&k=lit-query-secret" {
		t.Fatalf("saved empty values must keep their slot: %q", merged.Request.URL)
	}
}

func TestMergeCustomUsageHostChangeDropsSavedLiterals(t *testing.T) {
	for name, edit := range map[string]func(*CustomUsageConfig){
		"base url host":   func(c *CustomUsageConfig) { c.BaseURL = "https://collector.example.net" },
		"base url port":   func(c *CustomUsageConfig) { c.BaseURL = "https://dash.example.com:8443" },
		"base url scheme": func(c *CustomUsageConfig) { c.BaseURL = "http://dash.example.com" },
		"literal host": func(c *CustomUsageConfig) {
			c.Request.URL = "https://collector.example.net/api/usage?token=&uid={{userId}}"
		},
		"host suffix":    func(c *CustomUsageConfig) { c.Request.URL = "{{baseUrl}}.collector.example.net/api/usage?token=" },
		"userinfo trick": func(c *CustomUsageConfig) { c.Request.URL = "{{baseUrl}}@collector.example.net/api/usage?token=" },
	} {
		t.Run(name, func(t *testing.T) {
			old, secrets := customUsageMergeSaved()
			incoming := publicCustomUsage(old)
			edit(&incoming)
			merged, next := mergeCustomUsage(incoming, old, secrets, "")
			if strings.Contains(merged.Request.URL, "lit-query-secret") {
				t.Fatalf("query secret followed the request to another origin: %q", merged.Request.URL)
			}
			if merged.Request.Headers["X-Token"] != "" {
				t.Fatalf("header secret followed the request to another origin: %+v", merged.Request.Headers)
			}
			if merged.Request.Headers["Authorization"] != "Bearer {{apiKey}}" {
				t.Fatalf("visible template headers must be kept: %+v", merged.Request.Headers)
			}
			if next.APIKey != "" || next.AccessToken != "" {
				t.Fatalf("saved dedicated credentials must not follow the request: %+v", next)
			}
		})
	}

	old, secrets := customUsageMergeSaved()
	incoming := publicCustomUsage(old)
	incoming.BaseURL = "https://collector.example.net"
	incoming.APIKey = "collector-key"
	_, next := mergeCustomUsage(incoming, old, secrets, "")
	if next.APIKey != "collector-key" || next.AccessToken != "" {
		t.Fatalf("a credential entered with the new target must be kept: %+v", next)
	}

	// An omitted URL means "unchanged", but its literals stay with the old origin.
	incoming = CustomUsageConfig{Template: "custom", BaseURL: "https://collector.example.net", Request: CustomUsageRequest{Headers: map[string]string{"X-Token": ""}}}
	merged, _ := mergeCustomUsage(incoming, old, secrets, "")
	if merged.Request.URL != "{{baseUrl}}/api/usage?token=&uid={{userId}}" || merged.Request.Headers["X-Token"] != "" {
		t.Fatalf("an omitted URL must not carry literals to another origin: %+v", merged.Request)
	}
}

func TestMergeCustomUsageSameHostKeepsHeaders(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	incoming := publicCustomUsage(old)
	incoming.BaseURL = "https://DASH.example.com.:443/v2/"
	incoming.Request.URL = "{{baseUrl}}/balance"
	merged, next := mergeCustomUsage(incoming, old, secrets, "")
	if merged.Request.Headers["X-Token"] != "lit-header-secret" {
		t.Fatalf("same origin with a different path must keep header values: %+v", merged.Request.Headers)
	}
	if next.APIKey != "saved-dashboard-key" || next.AccessToken != "saved-dashboard-token" {
		t.Fatalf("same origin must keep saved credentials: %+v", next)
	}

	incoming.ClearAPIKey = true
	_, next = mergeCustomUsage(incoming, old, secrets, "")
	if next.APIKey != "" || next.AccessToken != "saved-dashboard-token" {
		t.Fatalf("explicit clear flags must keep working: %+v", next)
	}
}

func TestMergeCustomUsageTemplateChangeUnchanged(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	old.Template = "general"
	for _, url := range []string{old.Request.URL, customUsagePublicURL(old.Request.URL), ""} {
		incoming := publicCustomUsage(old)
		incoming.Template = "newapi"
		incoming.Request.URL = url
		merged, next := mergeCustomUsage(incoming, old, secrets, "")
		if merged.Request.URL != "" {
			t.Fatalf("a new preset must not reuse the old private request (%q): %q", url, merged.Request.URL)
		}
		// The preset URL still targets the same base_url origin.
		if merged.Request.Headers["X-Token"] != "lit-header-secret" || next.APIKey != "saved-dashboard-key" {
			t.Fatalf("same-origin preset switch keeps saved headers/credentials: %+v %+v", merged.Request.Headers, next)
		}
	}

	incoming := publicCustomUsage(old)
	incoming.Template = "custom"
	incoming.Request.URL = "{{baseUrl}}/api/other?token="
	merged, _ := mergeCustomUsage(incoming, old, secrets, "")
	if merged.Request.URL != "{{baseUrl}}/api/other?token=" {
		t.Fatalf("query values are not carried across a template change: %q", merged.Request.URL)
	}
}

func TestCustomUsageTargetOrigin(t *testing.T) {
	for _, tc := range []struct {
		template, base, url, account, want string
	}{
		{"custom", "https://A.Example.com.:443/v1/", "{{baseUrl}}/usage", "", "https://a.example.com"},
		{"custom", "", "{{baseUrl}}/usage", "http://relay.example.com:80/v1", "http://relay.example.com"},
		{"custom", "", "{{baseUrl}}/usage", "", customUsageNoBaseOrigin},
		{"custom", "https://a.example.com", "https://[2001:DB8::1]:8443/x?k={{apiKey}}", "", "https://[2001:db8::1]:8443"},
		{"custom", "https://a.example.com", "{{baseUrl}}:8443/x", "", "https://a.example.com:8443"},
		{"general", "https://a.example.com/v1", "", "", "https://a.example.com"},
		{"newapi", "https://a.example.com/v1", "", "", "https://a.example.com"},
		{"custom", "https://a.example.com", "", "", ""},
		{"custom", "https://a.example.com", "/relative/path", "", ""},
	} {
		c := CustomUsageConfig{Template: tc.template, BaseURL: tc.base, Request: CustomUsageRequest{URL: tc.url}}
		if got := customUsageTargetOrigin(c, tc.account); got != tc.want {
			t.Errorf("customUsageTargetOrigin(%q, %q, %q) = %q, want %q", tc.base, tc.url, tc.account, got, tc.want)
		}
	}
}

type customUsageMergeRepo struct {
	mu      sync.Mutex
	account *Account
	update  AccountBulkUpdate
}

func (r *customUsageMergeRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account == nil || r.account.ID != id {
		return nil, ErrAccountNotFound
	}
	copied := *r.account
	return &copied, nil
}

func (r *customUsageMergeRepo) GetByIDs(ctx context.Context, ids []int64) ([]*Account, error) {
	out := make([]*Account, 0, len(ids))
	for _, id := range ids {
		if a, err := r.GetByID(ctx, id); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func (r *customUsageMergeRepo) BulkUpdate(_ context.Context, _ []int64, update AccountBulkUpdate) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.update = update
	return 1, nil
}

func TestCustomUsagePutConfigBindsLiteralsToTargetOrigin(t *testing.T) {
	saved, _ := customUsageMergeSaved()
	saved.BaseURL = "" // follows the account's base_url
	saved.UserID = "42"
	var public map[string]any
	raw, _ := json.Marshal(publicCustomUsage(saved))
	_ = json.Unmarshal(raw, &public)
	account := &Account{
		ID:   9,
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url": "https://relay.example.com/v1",
			"api_key":  "sk-account-secret-key",
			CustomUsageCredentialsKey: map[string]any{
				"request_url": saved.Request.URL,
				"headers":     map[string]any{"Authorization": "Bearer {{apiKey}}", "X-Token": "lit-header-secret"},
				"api_key":     "saved-dashboard-key",
				"origin":      "https://relay.example.com",
			},
		},
		Extra: map[string]any{CustomUsageExtraKey: public},
	}
	repo := &customUsageMergeRepo{account: account}
	svc := NewCustomUsageService(repo)

	view, err := svc.GetConfig(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if strings.Contains(view.Request.URL, "lit-query-secret") || view.Request.Headers["X-Token"] != "" {
		t.Fatalf("GET must redact literals: %+v", view.Request)
	}
	stored := func() customUsageSecrets {
		var s customUsageSecrets
		raw, _ := json.Marshal(repo.update.Credentials[CustomUsageCredentialsKey])
		_ = json.Unmarshal(raw, &s)
		return s
	}

	// The editor fills an empty base_url with the account's before saving.
	edited := view.CustomUsageConfig
	edited.BaseURL = "https://relay.example.com/v1"
	if _, err := svc.PutConfig(context.Background(), account.ID, edited); err != nil {
		t.Fatalf("PutConfig (same origin): %v", err)
	}
	if s := stored(); !strings.Contains(s.RequestURL, "token=lit-query-secret") || s.Headers["X-Token"] != "lit-header-secret" || s.APIKey != "saved-dashboard-key" || s.Origin != "https://relay.example.com" {
		t.Fatalf("an unedited save must keep the saved literals: %+v", s)
	}

	// The saved dashboard key stays behind and the account key may not leave its
	// host, so the retargeted request is rejected until a key is entered for it.
	edited.BaseURL = "https://collector.example.net"
	if _, err := svc.PutConfig(context.Background(), account.ID, edited); !errors.Is(err, ErrCustomUsageConfig) {
		t.Fatalf("PutConfig (new origin, no credential) must be rejected, got %v", err)
	}
	edited.APIKey = "collector-key"
	if _, err := svc.PutConfig(context.Background(), account.ID, edited); err != nil {
		t.Fatalf("PutConfig (new origin): %v", err)
	}
	if s := stored(); strings.Contains(s.RequestURL, "lit-query-secret") || s.Headers["X-Token"] != "" || s.APIKey != "collector-key" || s.Origin != "https://collector.example.net" {
		t.Fatalf("a new target origin must not inherit saved literals: %+v", s)
	}
}

// apply stores the last write the way the repository's JSONB merge does.
func (r *customUsageMergeRepo) apply() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, v := range r.update.Credentials {
		r.account.Credentials[k] = v
	}
	for k, v := range r.update.Extra {
		r.account.Extra[k] = v
	}
}

// setBaseURL is a generic account edit: the repository keeps custom_usage_secrets.
func (r *customUsageMergeRepo) setBaseURL(base string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.account.Credentials["base_url"] = base
}

// customUsageMergeAccount stores saved the way PutConfig does, with the fixture's
// literals; origin "" stands for secrets saved before origins were recorded.
func customUsageMergeAccount(saved CustomUsageConfig, accountBase, origin string) *Account {
	var public map[string]any
	raw, _ := json.Marshal(publicCustomUsage(saved))
	_ = json.Unmarshal(raw, &public)
	secrets := map[string]any{
		"request_url":  saved.Request.URL,
		"headers":      saved.Request.Headers,
		"api_key":      "saved-dashboard-key",
		"access_token": "saved-dashboard-token",
	}
	if origin != "" {
		secrets["origin"] = origin
	}
	return &Account{
		ID:   9,
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                accountBase,
			"api_key":                 "sk-account-secret-key",
			"user_id":                 "42",
			CustomUsageCredentialsKey: secrets,
		},
		Extra: map[string]any{CustomUsageExtraKey: public},
	}
}

func customUsageHasLiterals(c CustomUsageConfig, s customUsageSecrets) bool {
	return strings.Contains(c.Request.URL, "lit-query-secret") || c.Request.Headers["X-Token"] != "" || s.APIKey != "" || s.AccessToken != ""
}

// Built from code points so the source stays ASCII.
var (
	customUsageDottedI = string(rune(0x130))  // LATIN CAPITAL LETTER I WITH DOT ABOVE
	customUsageKelvin  = string(rune(0x212a)) // KELVIN SIGN
	customUsageAcute   = string(rune(0x301))  // COMBINING ACUTE ACCENT
	// "api.provider.com" with U+0130 for the "i": strings.ToLower folds it back.
	customUsageLookalike = "api.prov" + customUsageDottedI + "der.com"
)

func TestCustomUsageTargetOriginUsesTheDialledIDNAHost(t *testing.T) {
	origin := func(base string) string {
		return customUsageTargetOrigin(CustomUsageConfig{Template: "custom", BaseURL: base, Request: CustomUsageRequest{URL: "{{baseUrl}}/usage"}}, "")
	}
	// U+0130 lower-cases to "i", but net/http dials its IDNA form ("i" + U+0307).
	if got := origin("https://" + customUsageLookalike); got == origin("https://api.provider.com") || got != "https://api.xn--provider-s0e.com" {
		t.Fatalf("a U+0130 lookalike must not share the ASCII origin: %q", got)
	}
	literal := CustomUsageConfig{Template: "custom", Request: CustomUsageRequest{URL: "https://api.prov%C4%B0der.com/usage"}}
	if got := customUsageTargetOrigin(literal, ""); got != "https://api.xn--provider-s0e.com" {
		t.Fatalf("a percent-encoded IDN host must resolve the same way: %q", got)
	}
	// U+212A maps to "k" either way, and the transport dials api.key.com.
	if got := origin("https://api." + customUsageKelvin + "ey.com"); got != "https://api.key.com" {
		t.Fatalf("Kelvin sign: %q", got)
	}
	// A host IDNA rejects is dialled verbatim, so it only ever matches itself.
	invalid := customUsageAcute + "a.example.com"
	if got := origin("https://" + invalid); got != "https://"+invalid {
		t.Fatalf("an IDNA-invalid host must be kept verbatim: %q", got)
	}
}

func TestMergeCustomUsageIDNLookalikeDropsSavedLiterals(t *testing.T) {
	old, secrets := customUsageMergeSaved()
	old.BaseURL = "https://api.provider.com"
	for name, edit := range map[string]func(*CustomUsageConfig){
		"base url": func(c *CustomUsageConfig) { c.BaseURL = "https://" + customUsageLookalike },
		"literal url": func(c *CustomUsageConfig) {
			c.Request.URL = "https://" + customUsageLookalike + "/api/usage?token=&uid={{userId}}"
		},
		"escaped url": func(c *CustomUsageConfig) {
			c.Request.URL = "https://api.prov%C4%B0der.com/api/usage?token=&uid={{userId}}"
		},
	} {
		incoming := publicCustomUsage(old)
		edit(&incoming)
		if merged, next := mergeCustomUsage(incoming, old, secrets, ""); customUsageHasLiterals(merged, next) {
			t.Fatalf("%s: saved literals followed the request to an IDN lookalike: %+v %+v", name, merged.Request, next)
		}
	}
}

func TestCustomUsageIDNLookalikeDraftIsRejected(t *testing.T) {
	saved, _ := customUsageMergeSaved()
	saved.BaseURL = "https://api.provider.com"
	account := customUsageMergeAccount(saved, "https://api.provider.com", "https://api.provider.com")
	svc := NewCustomUsageService(&customUsageMergeRepo{account: account})
	svc.newClient = func(string) (*http.Client, error) {
		t.Error("an IDN lookalike draft must never be sent")
		return nil, errors.New("unexpected request")
	}
	ctx := context.Background()
	view, err := svc.GetConfig(ctx, account.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	draft := view.CustomUsageConfig
	draft.BaseURL = "https://" + customUsageLookalike
	if result, err := svc.Query(ctx, account.ID, true, &draft); err != nil || result.Error != "invalid_config" {
		t.Fatalf("Query with an IDN draft: %+v, %v", result, err)
	}
	if _, err := svc.PutConfig(ctx, account.ID, draft); !errors.Is(err, ErrCustomUsageConfig) {
		t.Fatalf("PutConfig with an IDN base_url must be rejected, got %v", err)
	}
}

func customUsageIDNConfig(base, url string) CustomUsageConfig {
	return CustomUsageConfig{
		Enabled:        true,
		Template:       "custom",
		BaseURL:        base,
		TimeoutSeconds: 5,
		Request:        CustomUsageRequest{URL: url, Method: http.MethodGet, Headers: map[string]string{"Authorization": "Bearer {{apiKey}}"}},
		Extractor:      CustomUsageExtractor{Remaining: &CustomUsageNumber{Path: "remaining"}},
	}
}

func TestPrepareCustomUsageRejectsCaseFoldLookalikeHosts(t *testing.T) {
	account := &Account{Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.provider.com", "api_key": "sk-account-secret-key"}}
	for _, c := range []CustomUsageConfig{
		customUsageIDNConfig("", "https://"+customUsageLookalike+"/v1/usage"),
		customUsageIDNConfig("", "https://api.prov%C4%B0der.com/v1/usage"),
		customUsageIDNConfig("https://"+customUsageLookalike, "{{baseUrl}}/v1/usage"),
		customUsageIDNConfig("https://api.prov", "{{baseUrl}}"+customUsageDottedI+"der.com/v1/usage"),
		// The lookalike label inside an otherwise internationalised host.
		customUsageIDNConfig("", "https://"+customUsageDottedI+string([]rune{0x4f8b, 0x5b50})+".provider.com/v1/usage"),
	} {
		// A plain case fold maps U+0130 to "i", so the account key would pass a fold-only check.
		if _, err := prepareCustomUsage(account, c, customUsageSecrets{}); !errors.Is(err, ErrCustomUsageConfig) {
			t.Fatalf("a case-fold lookalike host must be rejected (%q, %q): %v", c.BaseURL, c.Request.URL, err)
		}
	}
	// Punycode is the unambiguous spelling; it is another host for the account key.
	punycode := customUsageIDNConfig("", "https://api.xn--provider-s0e.com/v1/usage")
	if _, err := prepareCustomUsage(account, punycode, customUsageSecrets{APIKey: "dashboard-key-123"}); err != nil {
		t.Fatalf("a punycode host with its own key must be accepted: %v", err)
	}
	if _, err := prepareCustomUsage(account, punycode, customUsageSecrets{}); !errors.Is(err, ErrCustomUsageConfig) {
		t.Fatalf("the account key must not go to the punycode lookalike: %v", err)
	}
	// U+212A lower-cases and IDNA-maps to "k" alike: the same host either way.
	kelvin := customUsageIDNConfig("https://api."+customUsageKelvin+"ey.com", "{{baseUrl}}/v1/usage")
	if _, err := prepareCustomUsage(account, kelvin, customUsageSecrets{APIKey: "dashboard-key-123"}); err != nil {
		t.Fatalf("a host that lower-cases to what it dials must be accepted: %v", err)
	}
}

func TestCustomUsageAcceptsInternationalisedHosts(t *testing.T) {
	// api.<two CJK characters>.com, built from code points so the source stays ASCII.
	idnBase := "https://API." + string([]rune{0x4f8b, 0x5b50}) + ".com/v1"
	account := &Account{ID: 9, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": idnBase, "api_key": "sk-account-secret-key"}}
	const dialled = "api.xn--fsqu00a.com"

	// The automatic default config sends the account key to the account's own IDN host.
	c, s, configured := readCustomUsage(account)
	if !configured || c.BaseURL != idnBase {
		t.Fatalf("expected the default config for an IDN base_url: %+v", c)
	}
	p, err := prepareCustomUsage(account, c, s)
	if err != nil {
		t.Fatalf("the default config of an IDN account must be usable: %v", err)
	}
	if got := customUsageDialHost(p.request.URL.Hostname()); got != dialled || p.request.Header.Get("Authorization") != "Bearer sk-account-secret-key" {
		t.Fatalf("unexpected default request: host %q, header %q", got, p.request.Header.Get("Authorization"))
	}

	for _, c := range []CustomUsageConfig{
		customUsageIDNConfig(idnBase, "{{baseUrl}}/usage"),                                   // the editor fills in the account's base_url
		customUsageIDNConfig("", "{{baseUrl}}/usage"),                                        // follows the account
		customUsageIDNConfig("", "https://api."+string([]rune{0x4f8b, 0x5b50})+".COM/usage"), // literal, other case
	} {
		if _, err := prepareCustomUsage(account, c, customUsageSecrets{}); err != nil {
			t.Fatalf("an IDN host must be accepted (%q, %q): %v", c.BaseURL, c.Request.URL, err)
		}
	}

	// Saving the editor's shape goes through PutConfig unchanged.
	repo := &customUsageMergeRepo{account: account}
	svc := NewCustomUsageService(repo)
	view, err := svc.GetConfig(context.Background(), account.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if _, err := svc.PutConfig(context.Background(), account.ID, view.CustomUsageConfig); err != nil {
		t.Fatalf("PutConfig with an IDN base_url: %v", err)
	}
	var stored customUsageSecrets
	raw, _ := json.Marshal(repo.update.Credentials[CustomUsageCredentialsKey])
	_ = json.Unmarshal(raw, &stored)
	if stored.Origin != "https://"+dialled {
		t.Fatalf("the saved origin must be the dialled IDNA host: %q", stored.Origin)
	}
}

func TestMergeCustomUsageUnpinningDropsSavedLiterals(t *testing.T) {
	const accountBase = "https://relay.example.com/v1"
	old, secrets := customUsageMergeSaved()
	old.BaseURL = "https://relay.example.com"
	incoming := publicCustomUsage(old)
	incoming.BaseURL = ""
	if merged, next := mergeCustomUsage(incoming, old, secrets, accountBase); customUsageHasLiterals(merged, next) {
		t.Fatalf("a pinned target switched to follow the account must not keep literals: %+v %+v", merged.Request, next)
	}

	// Rewriting a literal URL to follow the account is the same retarget.
	old.BaseURL = ""
	old.Request.URL = "https://relay.example.com/api/usage?token=lit-query-secret"
	incoming = publicCustomUsage(old)
	incoming.Request.URL = "{{baseUrl}}/api/usage?token="
	if merged, next := mergeCustomUsage(incoming, old, secrets, accountBase); customUsageHasLiterals(merged, next) {
		t.Fatalf("a literal URL switched to follow the account must not keep literals: %+v %+v", merged.Request, next)
	}

	// A target that already followed the account still does: GET -> PUT is a no-op.
	old.Request.URL = "{{baseUrl}}/api/usage?token=lit-query-secret"
	merged, next := mergeCustomUsage(publicCustomUsage(old), old, secrets, accountBase)
	if !reflect.DeepEqual(merged, old) || !reflect.DeepEqual(next, secrets) {
		t.Fatalf("an unedited account-following config must keep its literals: %+v %+v", merged.Request, next)
	}

	// End to end: unpinning through PutConfig stores none of the old literals.
	saved, _ := customUsageMergeSaved()
	saved.BaseURL = "https://relay.example.com"
	repo := &customUsageMergeRepo{account: customUsageMergeAccount(saved, accountBase, "https://relay.example.com")}
	svc := NewCustomUsageService(repo)
	view, err := svc.GetConfig(context.Background(), 9)
	if err != nil || !view.HasAPIKey {
		t.Fatalf("GetConfig: %+v, %v", view, err)
	}
	edited := view.CustomUsageConfig
	edited.BaseURL = ""
	if _, err := svc.PutConfig(context.Background(), 9, edited); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	var stored customUsageSecrets
	raw, _ := json.Marshal(repo.update.Credentials[CustomUsageCredentialsKey])
	_ = json.Unmarshal(raw, &stored)
	if strings.Contains(stored.RequestURL, "lit-query-secret") || stored.Headers["X-Token"] != "" || stored.APIKey != "" || stored.AccessToken != "" {
		t.Fatalf("unpinning must not store the old literals: %+v", stored)
	}
}

func TestCustomUsageSavedLiteralsStayWithTheSavedOrigin(t *testing.T) {
	saved, _ := customUsageMergeSaved()
	saved.BaseURL = "" // follows the account's base_url
	repo := &customUsageMergeRepo{account: customUsageMergeAccount(saved, "https://relay.example.com/v1", "")}
	svc := NewCustomUsageService(repo)
	var sent []*http.Request
	svc.newClient = func(string) (*http.Client, error) {
		return &http.Client{Transport: customUsageRoundTrip(func(req *http.Request) (*http.Response, error) {
			sent = append(sent, req)
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"remaining":1}`))}, nil
		})}, nil
	}
	ctx := context.Background()

	// Saved before origins were recorded: trusted for the target the config
	// resolves to now, as before the upgrade, whether pinned or following the account.
	pinned := saved
	pinned.BaseURL = "https://dash.example.com"
	c, s, _ := readCustomUsage(customUsageMergeAccount(pinned, "https://collector.example.net", ""))
	if !strings.Contains(c.Request.URL, "lit-query-secret") || c.Request.Headers["X-Token"] != "lit-header-secret" || s.APIKey != "saved-dashboard-key" {
		t.Fatalf("legacy pinned literals must stay usable: %+v %+v", c.Request, s)
	}
	c, s, _ = readCustomUsage(repo.account)
	if !strings.Contains(c.Request.URL, "lit-query-secret") || c.Request.Headers["X-Token"] != "lit-header-secret" || s.APIKey != "saved-dashboard-key" || s.AccessToken != "saved-dashboard-token" {
		t.Fatalf("legacy account-following literals must stay usable: %+v %+v", c.Request, s)
	}
	view, err := svc.GetConfig(ctx, 9)
	if err != nil || !view.HasAPIKey || !view.HasAccessToken {
		t.Fatalf("legacy secrets must be reported as saved: %+v, %v", view, err)
	}
	// An unedited GET -> PUT keeps them and binds them to the current origin.
	if _, err := svc.PutConfig(ctx, 9, view.CustomUsageConfig); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	var stored customUsageSecrets
	raw, _ := json.Marshal(repo.update.Credentials[CustomUsageCredentialsKey])
	_ = json.Unmarshal(raw, &stored)
	if stored.Origin != "https://relay.example.com" || !strings.Contains(stored.RequestURL, "token=lit-query-secret") || stored.Headers["X-Token"] != "lit-header-secret" || stored.APIKey != "saved-dashboard-key" || stored.AccessToken != "saved-dashboard-token" {
		t.Fatalf("the first save must keep legacy literals and record their origin: %+v", stored)
	}

	// From then on the literals follow the account within that origin only.
	repo.apply()
	repo.setBaseURL("https://relay.example.com/v2")
	if c, s, _ := readCustomUsage(repo.account); !strings.Contains(c.Request.URL, "lit-query-secret") || c.Request.Headers["X-Token"] != "lit-header-secret" || s.APIKey != "saved-dashboard-key" {
		t.Fatalf("a same-origin account edit must keep the literals: %+v %+v", c.Request, s)
	}

	// A generic account edit moves the base_url without a merge step.
	repo.setBaseURL("https://collector.example.net")
	if view, err := svc.GetConfig(ctx, 9); err != nil || view.HasAPIKey || view.HasAccessToken {
		t.Fatalf("moved secrets must not be reported as saved: %+v, %v", view, err)
	}
	if _, err := svc.Query(ctx, 9, true, nil); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected one request, got %d", len(sent))
	}
	req := sent[0]
	if req.URL.Host != "collector.example.net" || strings.Contains(req.URL.String(), "lit-query-secret") || req.Header.Get("X-Token") != "" {
		t.Fatalf("saved literals followed the account to a new origin: %s %v", req.URL, req.Header)
	}
	// Only the account's own key goes to the account's own base_url.
	if got := req.Header.Get("Authorization"); got != "Bearer sk-account-secret-key" {
		t.Fatalf("the saved dashboard key followed the account to a new origin: %q", got)
	}
}
