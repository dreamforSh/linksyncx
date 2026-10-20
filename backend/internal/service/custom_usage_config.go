package service

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"golang.org/x/net/idna"
)

const (
	// CustomUsageExtraKey stores only a redacted, declarative configuration.
	CustomUsageExtraKey = "custom_usage_config"
	// CustomUsageCredentialsKey is never serialized by ordinary account DTOs.
	CustomUsageCredentialsKey      = "custom_usage_secrets"
	CustomUsageMaxBatchSize        = 50
	customUsageMaxURLBytes         = 8192
	customUsageMaxHeaderValueBytes = 8192
	customUsageMaxHeaderBytes      = 32 * 1024
	customUsageMaxHeaderNameBytes  = 128
	customUsageMinSensitiveLen     = 8
)

var ErrCustomUsageConfig = infraerrors.BadRequest("INVALID_CUSTOM_USAGE_CONFIG", "invalid custom usage configuration")
var errCustomUsageResponse = errors.New("invalid_response")

// CustomUsageNumber selects a finite, nonnegative JSON number; divisor defaults to one.
type CustomUsageNumber struct {
	Path    string   `json:"path"`
	Divisor *float64 `json:"divisor,omitempty"`
}

// CustomUsageText selects a short string or a fixed literal.
type CustomUsageText struct {
	Path  string `json:"path,omitempty"`
	Value string `json:"value,omitempty"`
}

// CustomUsageExtractor deliberately has no executable expressions.
type CustomUsageExtractor struct {
	Remaining *CustomUsageNumber `json:"remaining,omitempty"`
	Used      *CustomUsageNumber `json:"used,omitempty"`
	Total     *CustomUsageNumber `json:"total,omitempty"`
	Unit      *CustomUsageText   `json:"unit,omitempty"`
	PlanName  *CustomUsageText   `json:"plan_name,omitempty"`
}

// CustomUsageRequest describes a single credential-bearing GET request.
type CustomUsageRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
}

// CustomUsageConfig is the admin input. Empty secret values preserve previous values.
type CustomUsageConfig struct {
	Enabled          bool                 `json:"enabled"`
	Template         string               `json:"template"`
	BaseURL          string               `json:"base_url"`
	APIKey           string               `json:"api_key,omitempty"`
	AccessToken      string               `json:"access_token,omitempty"`
	UserID           string               `json:"user_id,omitempty"`
	TimeoutSeconds   int                  `json:"timeout_seconds"`
	IntervalMinutes  int                  `json:"interval_minutes"`
	Request          CustomUsageRequest   `json:"request"`
	Extractor        CustomUsageExtractor `json:"extractor"`
	ClearAPIKey      bool                 `json:"clear_api_key,omitempty"`
	ClearAccessToken bool                 `json:"clear_access_token,omitempty"`
}

// CustomUsageConfigView has the same editable shape but never includes stored secrets.
type CustomUsageConfigView struct {
	CustomUsageConfig
	Configured             bool `json:"configured"`
	HasAPIKey              bool `json:"has_api_key"`
	HasAccessToken         bool `json:"has_access_token"`
	UsesAccountAPIKey      bool `json:"uses_account_api_key"`
	UsesAccountAccessToken bool `json:"uses_account_access_token"`
}

type customUsageSecrets struct {
	APIKey      string            `json:"api_key,omitempty"`
	AccessToken string            `json:"access_token,omitempty"`
	RequestURL  string            `json:"request_url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	// Origin is the target the values above were saved for (customUsageTargetOrigin).
	// readCustomUsage drops them once the request would go anywhere else, e.g.
	// after a generic account edit moved the base_url a config follows. Values
	// saved before origins were recorded have none; the next PutConfig adds it.
	Origin string `json:"origin,omitempty"`
}

func customUsageDefaults() CustomUsageConfig {
	return CustomUsageConfig{Template: "custom", TimeoutSeconds: 10, Request: CustomUsageRequest{Method: http.MethodGet, Headers: map[string]string{}}}
}

func customUsageAccountDefaults(a *Account) (CustomUsageConfig, bool) {
	c := customUsageDefaults()
	if a == nil || a.Type != AccountTypeAPIKey || a.IsCredentialShadow() {
		return c, false
	}
	base := strings.TrimRight(strings.TrimSpace(a.GetCredential("base_url")), "/")
	// Official API roots (the create form fills them in for an empty base_url)
	// have no usage endpoint; never poll them with the account key.
	if base == "" || upstreamBillingProbeTargetIsOfficialAPI(base) {
		return c, false
	}
	return CustomUsageConfig{
		Enabled:         true,
		Template:        "general",
		BaseURL:         base,
		TimeoutSeconds:  10,
		IntervalMinutes: 10,
		Request: CustomUsageRequest{
			URL:     "{{baseUrl}}/v1/usage",
			Method:  http.MethodGet,
			Headers: map[string]string{"Authorization": "Bearer {{apiKey}}"},
		},
		Extractor: CustomUsageExtractor{
			Remaining: &CustomUsageNumber{Path: "remaining"},
			Unit:      &CustomUsageText{Value: "USD"},
		},
	}, true
}

func readCustomUsage(a *Account) (CustomUsageConfig, customUsageSecrets, bool) {
	c, configured := customUsageAccountDefaults(a)
	var secrets customUsageSecrets
	var raw any
	explicitlyConfigured := false
	if a != nil {
		raw, explicitlyConfigured = a.Extra[CustomUsageExtraKey]
	}
	if explicitlyConfigured {
		configured = true
		c = customUsageDefaults()
		b, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(b, &c) != nil {
			c = customUsageDefaults()
			c.Enabled = true
			c.TimeoutSeconds = -1
		}
	}
	if a != nil && explicitlyConfigured {
		if b, err := json.Marshal(a.Credentials[CustomUsageCredentialsKey]); err == nil {
			_ = json.Unmarshal(b, &secrets)
		}
	}
	private := c
	if secrets.RequestURL != "" {
		private.Request.URL = secrets.RequestURL
	}
	if secrets.Headers != nil {
		private.Request.Headers = secrets.Headers
	}
	if explicitlyConfigured && !customUsageSecretsBound(private, secrets, a.GetCredential("base_url")) {
		// The stored (redacted) config stays usable; its literals do not travel.
		return c, customUsageSecrets{}, configured
	}
	return private, secrets, configured
}

// customUsageSecretsBound reports whether saved literals may be used for c, the
// config with those literals applied.
func customUsageSecretsBound(c CustomUsageConfig, secrets customUsageSecrets, accountBaseURL string) bool {
	if secrets.Origin == "" {
		// Saved before origins were recorded: trusted for the target c resolves to
		// now, as before, so an unedited GET -> PUT keeps them and records it.
		return true
	}
	return secrets.Origin == customUsageTargetOrigin(c, accountBaseURL)
}

// mergeCustomUsage folds an edited (redacted) config onto the saved one. Saved
// literal credentials — blanked query values, blanked header values and the
// dedicated api_key / access_token — stay bound to the target origin they were
// saved for: they are restored only while the request still reaches that origin.
// accountBaseURL resolves an empty config base_url, as prepareCustomUsage does.
func mergeCustomUsage(c CustomUsageConfig, old CustomUsageConfig, secrets customUsageSecrets, accountBaseURL string) (CustomUsageConfig, customUsageSecrets) {
	publicURL := customUsagePublicURL(old.Request.URL)
	unchangedURL := false
	if c.Template != old.Template {
		// Different presets must not reuse an old private request accidentally.
		if c.Request.URL == old.Request.URL || c.Request.URL == publicURL {
			c.Request.URL = ""
		}
	} else if c.Request.URL == "" || c.Request.URL == publicURL {
		// An omitted URL, or the redacted one read back from GET, means "unchanged".
		c.Request.URL = old.Request.URL
		unchangedURL = true
	}
	// Unpinning a target (empty base_url, so it follows the account's) is a
	// retarget even while the account still points at the same origin: a later
	// account edit could move it without coming through here.
	sameTarget := customUsageTargetOrigin(c, accountBaseURL) == customUsageTargetOrigin(old, accountBaseURL) &&
		(!customUsageFollowsAccount(c) || customUsageFollowsAccount(old))
	switch {
	case unchangedURL && !sameTarget:
		c.Request.URL = publicURL
	case !unchangedURL && sameTarget && c.Template == old.Template:
		c.Request.URL = customUsageRestoreQuery(c.Request.URL, old.Request.URL)
	}
	headers := make(map[string]string, len(c.Request.Headers))
	for k, v := range c.Request.Headers {
		if v == "" && sameTarget {
			v = old.Request.Headers[k]
		}
		headers[k] = v
	}
	c.Request.Headers = headers
	if c.ClearAPIKey || (c.APIKey == "" && !sameTarget) {
		secrets.APIKey = ""
	} else if c.APIKey != "" {
		secrets.APIKey = c.APIKey
	}
	if c.ClearAccessToken || (c.AccessToken == "" && !sameTarget) {
		secrets.AccessToken = ""
	} else if c.AccessToken != "" {
		secrets.AccessToken = c.AccessToken
	}
	c.APIKey = ""
	c.AccessToken = ""
	c.ClearAPIKey = false
	c.ClearAccessToken = false
	return c, secrets
}

// customUsageNoBaseOrigin stands in for {{baseUrl}} when neither the config nor
// the account has a base_url; such a request cannot be prepared, so two of them
// compare equal without an account lookup.
const customUsageNoBaseOrigin = "https://usage.invalid"

func customUsagePresetURL(template string) string {
	switch template {
	case "general", "sub2api":
		return "{{baseUrl}}/v1/usage"
	case "newapi":
		return "{{baseUrl}}/api/user/self"
	}
	return ""
}

// customUsageRequestURL is c's URL template, with the preset URL that
// normalizeCustomUsage fills in for an empty one.
func customUsageRequestURL(c CustomUsageConfig) string {
	if c.Request.URL == "" {
		return customUsagePresetURL(c.Template)
	}
	return c.Request.URL
}

// customUsageFollowsAccount reports whether c's target comes from the account's
// base_url ({{baseUrl}} with an empty config base_url), which generic account
// edits can change without going through mergeCustomUsage.
func customUsageFollowsAccount(c CustomUsageConfig) bool {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") == "" && strings.Contains(customUsageRequestURL(c), "{{baseUrl}}")
}

func customUsageASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// customUsageDialHost returns the name the HTTP transport resolves for host.
// net/http maps non-ASCII hosts with IDNA (UTS #46) Lookup, under which U+0130
// becomes "i" + U+0307 rather than the plain "i" strings.ToLower yields, so a
// case fold would equate a lookalike domain with the real one. A host IDNA
// rejects is dialled verbatim, so it is kept byte-for-byte.
func customUsageDialHost(host string) string {
	if customUsageASCII(host) {
		return strings.TrimSuffix(strings.ToLower(host), ".")
	}
	if ascii, err := idna.Lookup.ToASCII(host); err == nil {
		return strings.TrimSuffix(strings.ToLower(ascii), ".")
	}
	return host
}

// customUsageTargetOrigin returns the lower-cased scheme://host[:port] (default
// port and trailing dot dropped, IDN hosts in their dialled ASCII form) that
// prepareCustomUsage would send c to, or "" when the URL has no parseable host.
// {{baseUrl}} is substituted textually, as customUsageExpand does, so
// "{{baseUrl}}.evil.example" resolves to its real host.
func customUsageTargetOrigin(c CustomUsageConfig, accountBaseURL string) string {
	raw := customUsageRequestURL(c)
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(accountBaseURL), "/")
	}
	if base == "" {
		base = customUsageNoBaseOrigin
	}
	u, err := url.Parse(strings.ReplaceAll(raw, "{{baseUrl}}", base))
	if err != nil {
		return ""
	}
	return customUsageURLOrigin(u)
}

// customUsageURLOrigin returns u's lower-cased scheme://host[:port] with the
// default port and trailing dot dropped and IDN hosts in their dialled ASCII
// form, or "" when u has no host.
func customUsageURLOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := customUsageDialHost(u.Hostname())
	if host == "" {
		return ""
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

// customUsageQueryPairs visits raw "name=value" pairs by url.ParseQuery's rules
// (skipping the pairs it would drop), so callers can rewrite a query in place
// without re-encoding it.
func customUsageQueryPairs(pairs []string, visit func(index int, name, rawValue string)) {
	for i, pair := range pairs {
		if pair == "" || strings.Contains(pair, ";") {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			continue
		}
		if _, err := url.QueryUnescape(value); err != nil {
			continue
		}
		visit(i, name, value)
	}
}

// customUsageRestoreQuery fills the blank query values of next with the values
// GET hid from prev. Placeholders stay visible in GET, so for each name the
// j-th non-placeholder value sent back stands for the j-th hidden value, however
// placeholder pairs were moved or removed; a blank one gets that value back.
// Everything else — order, placeholders, values the admin typed — is kept verbatim.
// Callers must only use it when next and prev reach the same origin.
func customUsageRestoreQuery(next, prev string) string {
	start := strings.IndexByte(next, '?')
	if start < 0 {
		return next
	}
	end := len(next)
	if hash := strings.IndexByte(next[start:], '#'); hash >= 0 {
		end = start + hash
	}
	hidden := map[string][]string{}
	if _, query, ok := strings.Cut(prev, "?"); ok {
		query, _, _ = strings.Cut(query, "#")
		customUsageQueryPairs(strings.Split(query, "&"), func(_ int, name, rawValue string) {
			if decoded, _ := url.QueryUnescape(rawValue); !customUsageQueryPlaceholder(decoded) {
				hidden[name] = append(hidden[name], rawValue)
			}
		})
	}
	pairs := strings.Split(next[start+1:end], "&")
	customUsageQueryPairs(pairs, func(i int, name, rawValue string) {
		if decoded, _ := url.QueryUnescape(rawValue); customUsageQueryPlaceholder(decoded) || len(hidden[name]) == 0 {
			return
		}
		old := hidden[name][0]
		hidden[name] = hidden[name][1:]
		if rawValue != "" || old == "" {
			return
		}
		key, _, _ := strings.Cut(pairs[i], "=")
		pairs[i] = key + "=" + old
	})
	return next[:start+1] + strings.Join(pairs, "&") + next[end:]
}

func customUsageQueryPlaceholder(v string) bool {
	return v == "{{apiKey}}" || v == "{{accessToken}}" || v == "{{userId}}"
}

func normalizeCustomUsage(c *CustomUsageConfig) error {
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 30 || c.IntervalMinutes < 0 || (c.IntervalMinutes > 0 && c.IntervalMinutes < 5) {
		return ErrCustomUsageConfig
	}
	if c.Template == "" {
		c.Template = "custom"
	}
	switch c.Template {
	case "custom", "general", "newapi", "sub2api":
	default:
		return ErrCustomUsageConfig
	}
	if c.Request.Method == "" {
		c.Request.Method = http.MethodGet
	}
	if c.Request.Method != http.MethodGet {
		return ErrCustomUsageConfig
	}
	if len(c.BaseURL) > 2048 || !customUsageSafeText(c.UserID, 128) || len(c.Request.URL) > 4096 || len(c.Request.Headers) > 24 {
		return ErrCustomUsageConfig
	}
	if c.Request.Headers == nil {
		c.Request.Headers = map[string]string{}
	}
	if c.Request.URL == "" {
		c.Request.URL = customUsagePresetURL(c.Template)
	}
	if c.Template != "custom" && len(c.Request.Headers) == 0 {
		c.Request.Headers["Authorization"] = "Bearer {{apiKey}}"
		if c.Template == "newapi" {
			c.Request.Headers["Authorization"] = "Bearer {{accessToken}}"
			c.Request.Headers["New-Api-User"] = "{{userId}}"
		}
	}
	if c.Template != "custom" && c.Extractor.Remaining == nil && c.Extractor.Used == nil && c.Extractor.Total == nil {
		c.Extractor.Remaining = &CustomUsageNumber{Path: "remaining"}
		c.Extractor.Unit = &CustomUsageText{Value: "USD"}
		if c.Template == "sub2api" {
			c.Extractor.Unit = &CustomUsageText{Path: "unit"}
			c.Extractor.PlanName = &CustomUsageText{Path: "planName"}
		}
		if c.Template == "newapi" {
			// QuantumNous/new-api controller/user.go GetSelf: quota + used_quota.
			// common/constants.go defaults QuotaPerUnit to 500000. Custom sites
			// can change that value; the editable divisor is the authority.
			divisor := 500000.0
			c.Extractor.Remaining = &CustomUsageNumber{Path: "data.quota", Divisor: &divisor}
			c.Extractor.Used = &CustomUsageNumber{Path: "data.used_quota", Divisor: &divisor}
			c.Extractor.Unit = &CustomUsageText{Value: "USD"}
		}
	}
	if c.Enabled && (c.Request.URL == "" || (c.Extractor.Remaining == nil && c.Extractor.Used == nil && c.Extractor.Total == nil)) {
		return ErrCustomUsageConfig
	}
	for _, n := range []*CustomUsageNumber{c.Extractor.Remaining, c.Extractor.Used, c.Extractor.Total} {
		if n == nil {
			continue
		}
		if _, err := customUsagePath(n.Path); err != nil {
			return ErrCustomUsageConfig
		}
		if n.Divisor != nil && (*n.Divisor <= 0 || math.IsNaN(*n.Divisor) || math.IsInf(*n.Divisor, 0)) {
			return ErrCustomUsageConfig
		}
	}
	for _, v := range []*CustomUsageText{c.Extractor.Unit, c.Extractor.PlanName} {
		if v == nil {
			continue
		}
		if len(v.Value) > 128 || (v.Path != "" && v.Value != "") {
			return ErrCustomUsageConfig
		}
		if v.Path != "" {
			if _, err := customUsagePath(v.Path); err != nil {
				return ErrCustomUsageConfig
			}
		}
		if !customUsageSafeText(v.Value, 128) {
			return ErrCustomUsageConfig
		}
	}
	seen := map[string]bool{}
	headerBytes := 0
	for k, v := range c.Request.Headers {
		lower := strings.ToLower(k)
		if len(k) > customUsageMaxHeaderNameBytes || k == "__proto__" || k == "constructor" || k == "prototype" || !customUsageHeaderName.MatchString(k) || seen[lower] || len(v) > customUsageMaxHeaderValueBytes || strings.ContainsAny(v, "\r\n\x00") {
			return ErrCustomUsageConfig
		}
		headerBytes += len(k) + len(v) + 4
		if headerBytes > customUsageMaxHeaderBytes {
			return ErrCustomUsageConfig
		}
		seen[lower] = true
		switch lower {
		case "host", "connection", "content-length", "transfer-encoding", "upgrade", "proxy-authorization", "proxy-connection", "te", "trailer", "accept-encoding":
			return ErrCustomUsageConfig
		}
		if !customUsageKnownTemplates(v) {
			return ErrCustomUsageConfig
		}
	}
	if !customUsageKnownTemplates(c.Request.URL) || strings.ContainsAny(c.UserID, "\r\n\x00") {
		return ErrCustomUsageConfig
	}
	if !customUsageUnambiguousHost(c.BaseURL) || !customUsageUnambiguousHost(c.Request.URL) {
		return ErrCustomUsageConfig
	}
	return nil
}

// customUsageUnambiguousHost rejects a host whose lower-cased spelling, which the
// host checks compare (customUsageSameOrigin), dials another host than the host
// itself: U+0130 lower-cases to "i" but IDNA maps it to "i" + U+0307, so such a
// lookalike would pass for the account's own ASCII host. Other internationalised
// hosts are accepted; the transport dials their IDNA form. {{baseUrl}} becomes a
// pathless sentinel, so text appended to it is checked as host text.
func customUsageUnambiguousHost(raw string) bool {
	u, err := url.Parse(strings.ReplaceAll(raw, "{{baseUrl}}", customUsageNoBaseOrigin))
	if err != nil {
		return true
	}
	host := u.Hostname()
	return customUsageDialHost(host) == customUsageDialHost(strings.ToLower(host))
}

var customUsageHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
var customUsagePathSyntax = regexp.MustCompile(`^(\$\.)?[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_0-9][A-Za-z0-9_-]*|\[[0-9]+\])*$`)
var customUsagePathPart = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$|^[0-9]+$`)

func customUsageKnownTemplates(s string) bool {
	for _, key := range []string{"baseUrl", "apiKey", "accessToken", "userId"} {
		s = strings.ReplaceAll(s, "{{"+key+"}}", "")
	}
	return !strings.ContainsAny(s, "{}")
}

func customUsageTemplateParts(raw string, variables map[string]string, visit func(string, bool) error) error {
	for {
		start := strings.Index(raw, "{{")
		if start < 0 {
			return visit(raw, false)
		}
		if err := visit(raw[:start], false); err != nil {
			return err
		}
		raw = raw[start+2:]
		end := strings.Index(raw, "}}")
		if end < 0 {
			return ErrCustomUsageConfig
		}
		key := raw[:end]
		value, exists := variables[key]
		if !exists || value == "" {
			return ErrCustomUsageConfig
		}
		if err := visit(value, key != "baseUrl"); err != nil {
			return err
		}
		raw = raw[end+2:]
	}
}

func customUsageExpandedLength(raw string, variables map[string]string, query bool, limit int) (int, error) {
	length := 0
	err := customUsageTemplateParts(raw, variables, func(value string, variable bool) error {
		size := len(value)
		if size > limit-length {
			return ErrCustomUsageConfig
		}
		if query && variable {
			for index := 0; index < len(value); index++ {
				letter := value[index]
				if (letter >= 'a' && letter <= 'z') || (letter >= 'A' && letter <= 'Z') || (letter >= '0' && letter <= '9') || strings.ContainsRune("-_.~ ", rune(letter)) {
					continue
				}
				if limit-length-size < 2 {
					return ErrCustomUsageConfig
				}
				size += 2
			}
		}
		length += size
		return nil
	})
	return length, err
}

func customUsageExpand(raw string, variables map[string]string, query bool, limit int) (string, error) {
	length, err := customUsageExpandedLength(raw, variables, query, limit)
	if err != nil {
		return "", err
	}
	var expanded strings.Builder
	expanded.Grow(length)
	err = customUsageTemplateParts(raw, variables, func(value string, variable bool) error {
		if query && variable {
			value = url.QueryEscape(value)
		}
		_, _ = expanded.WriteString(value)
		return nil
	})
	if err != nil || expanded.Len() > limit {
		return "", ErrCustomUsageConfig
	}
	return expanded.String(), nil
}

func customUsagePath(path string) ([]string, error) {
	if len(path) == 0 || len(path) > 256 || !customUsagePathSyntax.MatchString(path) {
		return nil, errCustomUsageResponse
	}
	path = strings.TrimPrefix(path, "$.")
	if strings.HasPrefix(path, "$") {
		return nil, errCustomUsageResponse
	}
	path = strings.ReplaceAll(path, "[", ".")
	path = strings.ReplaceAll(path, "]", "")
	parts := strings.Split(path, ".")
	if len(parts) > 24 {
		return nil, errCustomUsageResponse
	}
	for _, p := range parts {
		if !customUsagePathPart.MatchString(p) || p == "__proto__" || p == "constructor" || p == "prototype" {
			return nil, errCustomUsageResponse
		}
	}
	return parts, nil
}
func customUsageLookup(root any, path string) (any, bool) {
	parts, err := customUsagePath(path)
	if err != nil {
		return nil, false
	}
	current := root
	for _, p := range parts {
		switch v := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = v[p]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			current = v[i]
		default:
			return nil, false
		}
	}
	return current, true
}
func customUsageSafeText(s string, max int) bool {
	if len(s) > max || strings.ContainsAny(s, "<>{}\\") {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func customUsagePublicURL(raw string) string {
	// Never expose literal query credentials, even when their names are vendor-specific.
	u, err := url.Parse(strings.ReplaceAll(raw, "{{baseUrl}}", "https://usage.invalid"))
	if err != nil {
		return ""
	}
	q := u.Query()
	for k, vs := range q {
		for i, v := range vs {
			if !customUsageQueryPlaceholder(v) {
				vs[i] = ""
			}
		}
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	u.User = nil
	u.Fragment = ""
	out := u.String()
	if strings.HasPrefix(raw, "{{baseUrl}}") {
		out = strings.Replace(out, "https://usage.invalid", "{{baseUrl}}", 1)
	}
	// Keep placeholders editable rather than percent-encoded.
	for _, key := range []string{"apiKey", "accessToken", "userId"} {
		out = strings.ReplaceAll(out, url.QueryEscape("{{"+key+"}}"), "{{"+key+"}}")
	}
	return out
}
func publicCustomUsage(c CustomUsageConfig) CustomUsageConfig {
	c.APIKey = ""
	c.AccessToken = ""
	c.ClearAPIKey = false
	c.ClearAccessToken = false
	c.Request.URL = customUsagePublicURL(c.Request.URL)
	headers := map[string]string{}
	for k, v := range c.Request.Headers {
		switch v {
		case "{{apiKey}}", "Bearer {{apiKey}}", "{{accessToken}}", "Bearer {{accessToken}}", "{{userId}}", "application/json":
			headers[k] = v
		default:
			headers[k] = ""
		}
	}
	c.Request.Headers = headers
	return c
}
