package codebuddy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
)

// Pinned official client identities (CodeBuddy CLI and WorkBuddy desktop).
const (
	CLIVersion          = "2.149.0"
	WorkBuddyVersion    = "5.5.2"
	WorkBuddyCLIVersion = "2.137.1"
	CLIUserAgent        = "CLI/" + CLIVersion + " CodeBuddy/" + CLIVersion

	// BrowserUserAgent is used for billing calls, which the official clients
	// make from the product website.
	BrowserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
)

// Identity is the per-account header input shared by every upstream call.
type Identity struct {
	Profile      string
	AccessToken  string
	Domain       string
	UID          string
	EnterpriseID string
}

// IdentityOf extracts the header identity from stored credentials.
func IdentityOf(c Credentials) Identity {
	return Identity{
		Profile:      c.Profile,
		AccessToken:  c.AccessToken,
		Domain:       c.Domain,
		UID:          c.UID,
		EnterpriseID: c.EnterpriseID,
	}
}

func (id Identity) profile() string {
	if ValidProfile(id.Profile) {
		return id.Profile
	}
	if profile, err := ProfileForAuth(id.Domain, id.AccessToken); err == nil {
		return profile
	}
	return ProfileCNCLI
}

// IdentityHeaders returns the product UA and X-IDE-* headers.
func IdentityHeaders(profile string) map[string]string {
	if ProfileProduct(profile) == ProductCLI {
		return map[string]string{
			"User-Agent":    CLIUserAgent,
			"X-IDE-Type":    "CLI",
			"X-IDE-Name":    "CLI",
			"X-IDE-Version": CLIVersion,
		}
	}
	name := "WorkBuddy"
	if ProfileRegion(profile) == SiteIntl {
		name = "WorkBuddy AI"
	}
	return map[string]string{
		"User-Agent":    "WorkBuddy/" + WorkBuddyVersion + " " + name + "/" + WorkBuddyVersion + " CLI/" + WorkBuddyCLIVersion,
		"X-IDE-Type":    "WorkBuddy",
		"X-IDE-Name":    "WorkBuddy",
		"X-IDE-Version": WorkBuddyVersion,
	}
}

// SDKHeaders mimics the Node OpenAI SDK the official clients are built on,
// plus the agent markers sent with every conversation turn.
func SDKHeaders() map[string]string {
	return map[string]string{
		"x-stainless-arch":            "x64",
		"x-stainless-lang":            "js",
		"x-stainless-os":              "Linux",
		"x-stainless-package-version": "6.25.0",
		"x-stainless-retry-count":     "0",
		"x-stainless-runtime":         "node",
		"x-stainless-runtime-version": "v24.21.0",
		"X-Agent-Intent":              "craft",
		"X-Agent-Purpose":             "conversation",
		"X-Agent-Type":                "main",
		"X-Private-Data":              "false",
		"X-CodeBuddy-Request":         "1",
	}
}

func authHeaders(id Identity) map[string]string {
	profile := id.profile()
	headers := map[string]string{
		"Content-Type":     "application/json",
		"Accept":           "application/json",
		"Authorization":    "Bearer " + id.AccessToken,
		"X-User-Id":        id.UID,
		"X-Enterprise-Id":  id.EnterpriseID,
		"X-Tenant-Id":      id.EnterpriseID,
		"X-Domain":         DomainForAuth(id.Domain, id.AccessToken),
		"X-Product":        "SaaS",
		"X-Requested-With": "XMLHttpRequest",
	}
	for key, value := range IdentityHeaders(profile) {
		headers[key] = value
	}
	return headers
}

// CredentialHeaders is the full header set for chat and token refresh.
func CredentialHeaders(id Identity) map[string]string {
	headers := SDKHeaders()
	for key, value := range authHeaders(id) {
		headers[key] = value
	}
	return headers
}

// RefreshHeaders is CredentialHeaders plus the refresh-token exchange fields.
// The current (possibly expired) access token stays in Authorization, as the
// official clients send it.
func RefreshHeaders(id Identity, refreshToken string) map[string]string {
	headers := CredentialHeaders(id)
	headers["X-Refresh-Token"] = refreshToken
	headers["X-Auth-Refresh-Source"] = "plugin"
	if id.AccessToken == "" {
		delete(headers, "Authorization")
	}
	return headers
}

// CatalogHeaders is used for /v3/config. It deliberately omits the SDK and
// agent headers. x-client-platform=cli is CLI-only: on WorkBuddy it would
// select the wrong product catalog.
func CatalogHeaders(id Identity) map[string]string {
	headers := authHeaders(id)
	headers["Connection"] = "close"
	if ProfileProduct(id.profile()) == ProductCLI {
		headers["x-client-platform"] = "cli"
	}
	return headers
}

// OAuthHeaders is used for the unauthenticated login state/token calls.
func OAuthHeaders() map[string]string {
	return map[string]string{
		"User-Agent":   CLIUserAgent,
		"Accept":       "application/json",
		"Content-Type": "application/json",
	}
}

// WebHeaders is used for billing calls on the product website.
func WebHeaders(host string, id Identity) map[string]string {
	return map[string]string{
		"accept":            "application/json, text/plain, */*",
		"content-type":      "application/json",
		"x-client-platform": "web",
		"origin":            host,
		"referer":           host + "/profile/plans-usage",
		"authorization":     "Bearer " + id.AccessToken,
		"x-user-id":         id.UID,
		"x-domain":          DomainForAuth(id.Domain, id.AccessToken),
		"x-enterprise-id":   id.EnterpriseID,
		"x-tenant-id":       id.EnterpriseID,
		"user-agent":        BrowserUserAgent,
	}
}

// ConversationID derives the stable X-Conversation-ID for a session key: the
// first 128 bits of sha256(profile:key) formatted as a UUID. An empty key
// yields a random UUID.
func ConversationID(profile, sessionKey string) string {
	if sessionKey == "" {
		return formatUUID(randomBytes(16))
	}
	sum := sha256.Sum256([]byte(profile + ":" + sessionKey))
	return formatUUID(sum[:16])
}

// RequestIDHeaders returns the per-request conversation and trace headers the
// official clients send. Only X-Conversation-ID is stable across turns.
func RequestIDHeaders(conversationID string) map[string]string {
	requestID := hex.EncodeToString(randomBytes(16))
	rootID := hex.EncodeToString(randomBytes(16))
	span := hex.EncodeToString(randomBytes(8))
	parent := hex.EncodeToString(randomBytes(8))
	return map[string]string{
		"X-Conversation-ID":         conversationID,
		"X-Request-ID":              requestID,
		"X-Conversation-Message-ID": requestID,
		"X-Conversation-Request-ID": rootID,
		"X-Root-Request-ID":         rootID,
		"X-Trace-ID":                rootID,
		"traceparent":               "00-" + rootID + "-" + span + "-01",
		"b3":                        rootID + "-" + span + "-1-" + parent,
		"X-B3-TraceId":              rootID,
		"X-B3-SpanId":               span,
		"X-B3-ParentSpanId":         parent,
		"X-B3-Sampled":              "1",
	}
}

// ApplyHeaders sets every header on h, replacing existing values.
func ApplyHeaders(h http.Header, headers map[string]string) {
	for key, value := range headers {
		h.Set(key, value)
	}
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomBytes(n int) []byte {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return buf
}
