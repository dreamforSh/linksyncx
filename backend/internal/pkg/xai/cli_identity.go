package xai

import (
	"net/http"
	"os"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// Fixed Grok Build / CLI-chat-proxy client identity.
// These values are intentionally pinned in-binary (not scraped from live CLI).
// Operators may bump the version via XAI_GROK_CLI_VERSION without a release.
//
// The header set mirrors captured Grok Build 1.0.46 traffic from the
// interactive TUI (grok-pager), which is what most real subscribers run.
const (
	// CLIProxyHost is the hostname that requires the official CLI identity headers.
	CLIProxyHost = "cli-chat-proxy.grok.com"

	// CLIStableVersion is the lowest client version cli-chat-proxy still
	// accepts. It answers older versions with HTTP 426 and names this floor in
	// the body. Overrides below it are ignored.
	CLIStableVersion = "1.0.13"

	// CLIVersionEnv is the optional operator override for CLIClientVersion.
	CLIVersionEnv = "XAI_GROK_CLI_VERSION"

	// CLITokenAuth is required by cli-chat-proxy for Grok Build OAuth tokens.
	CLITokenAuth = "xai-grok-cli"

	// CLIClientIdentifier is the process-level x-grok-client-identifier Grok
	// Build sends on tool, media and account requests.
	CLIClientIdentifier = "grok-shell"

	// CLISamplerClientIdentifier is the x-grok-client-identifier the
	// interactive TUI's sampler sends on inference turns.
	CLISamplerClientIdentifier = "grok-pager"

	// CLIClientMode is the x-grok-client-mode of the interactive TUI. Headless
	// runs (`grok -p`) send "headless" with the single-product shell UA instead.
	CLIClientMode = "interactive"

	// CLIAuthenticateResponseHeader is required by the cli-chat-proxy auth
	// middleware on inference and tool requests. Grok Build omits it on
	// account endpoints such as /models, /settings and /billing.
	CLIAuthenticateResponseHeader = "x-authenticateresponse"
	CLIAuthenticateResponseValue  = "authenticate-response"

	// cliUserAgentPlatform is the "(os; arch)" suffix of the advertised client.
	cliUserAgentPlatform = "(macos; aarch64)"
)

// CLIRequestKind selects which Grok Build client surface a request imitates.
type CLIRequestKind int

const (
	// CLIRequestTool covers tool, media and voice traffic (image/video
	// generation, TTS, STT). It is the default for unclassified requests.
	CLIRequestTool CLIRequestKind = iota
	// CLIRequestSampler covers model inference turns (Responses, Chat
	// Completions, Messages).
	CLIRequestSampler
	// CLIRequestAccount covers account reads such as model lists and billing.
	CLIRequestAccount
)

// cliSamplerPathSuffixes are the inference endpoints of the Grok Build sampler.
var cliSamplerPathSuffixes = []string{"/responses", "/chat/completions", "/messages"}

// cliAccountPathSuffixes are account endpoints Grok Build calls without the
// x-authenticateresponse header.
var cliAccountPathSuffixes = []string{"/models", "/models-v2", "/settings", "/user", "/billing", "/login-config", "/bundle/archive"}

// CLIRequestKindFor classifies a request by method and URL path.
func CLIRequestKindFor(method, path string) CLIRequestKind {
	path = strings.TrimRight(strings.ToLower(strings.TrimSpace(path)), "/")
	if strings.EqualFold(method, http.MethodPost) {
		for _, suffix := range cliSamplerPathSuffixes {
			if strings.HasSuffix(path, suffix) {
				return CLIRequestSampler
			}
		}
	}
	if method == "" || strings.EqualFold(method, http.MethodGet) {
		for _, suffix := range cliAccountPathSuffixes {
			if strings.HasSuffix(path, suffix) {
				return CLIRequestAccount
			}
		}
	}
	return CLIRequestTool
}

// ResolveCLIVersion returns a supported CLI client version.
// Empty or invalid overrides fall back to CLIClientVersion (the pinned
// preferred client pin in billing.go). CLIStableVersion is only the minimum
// accepted by IsSupportedCLIVersion, not the default identity we advertise, so
// an override may sit below the pin (rollback) or above it (a newer release).
func ResolveCLIVersion() string {
	version := strings.TrimSpace(os.Getenv(CLIVersionEnv))
	if !IsSupportedCLIVersion(version) {
		return CLIClientVersion
	}
	return version
}

// IsSupportedCLIVersion reports whether version is a valid semver string at or
// above CLIStableVersion (prereleases below a higher release are rejected when
// they compare less than the stable pin).
func IsSupportedCLIVersion(version string) bool {
	canonical := "v" + version
	minimum := "v" + CLIStableVersion
	return semver.IsValid(canonical) &&
		semver.Canonical(canonical) == canonical &&
		semver.Compare(canonical, minimum) >= 0
}

// CLIUserAgent builds the interactive Grok Build User-Agent for a CLI client
// version. Grok Build renders "{origin}/{v} grok-shell/{v} ({os}; {arch})";
// the TUI's origin product is grok-pager.
func CLIUserAgent(version string) string {
	if strings.TrimSpace(version) == "" {
		version = CLIClientVersion
	}
	return "grok-pager/" + version + " grok-shell/" + version + " " + cliUserAgentPlatform
}

// ApplyCLIIdentityHeaders stamps the host-independent Grok Build client
// identity of the given request kind onto headers, using the resolved CLI
// version. It deliberately leaves out X-XAI-Token-Auth and
// x-authenticateresponse: they switch the upstream auth middleware to OAuth
// handling, so Grok Build only sends them to the CLI proxy, and
// ApplyCLIProxyHeaders adds them for that host alone.
func ApplyCLIIdentityHeaders(headers http.Header, kind CLIRequestKind) {
	if headers == nil {
		return
	}
	version := ResolveCLIVersion()
	headers.Set("x-grok-client-version", version)
	headers.Set("x-grok-client-mode", CLIClientMode)
	headers.Set("User-Agent", CLIUserAgent(version))
	switch kind {
	case CLIRequestSampler:
		headers.Set("x-grok-client-identifier", CLISamplerClientIdentifier)
	case CLIRequestAccount:
		// Grok Build's model list and billing reads carry no identifier and
		// keep reqwest's default Accept.
		headers.Del("x-grok-client-identifier")
		headers.Set("Accept", "*/*")
	default:
		headers.Set("x-grok-client-identifier", CLIClientIdentifier)
	}
}

// IsCLIProxyHost reports whether host is the Grok CLI chat proxy.
func IsCLIProxyHost(host string) bool {
	return strings.EqualFold(strings.TrimSpace(host), CLIProxyHost)
}

// ApplyCLIProxyHeaders stamps the fixed Grok CLI identity when the request
// targets cli-chat-proxy, choosing the header set by method and path. Direct
// api.x.ai and custom upstream traffic is left unchanged.
func ApplyCLIProxyHeaders(req *http.Request) {
	if req == nil || req.URL == nil || !IsCLIProxyHost(req.URL.Hostname()) {
		return
	}
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	kind := CLIRequestKindFor(req.Method, req.URL.Path)
	ApplyCLIIdentityHeaders(req.Header, kind)
	req.Header.Set("X-XAI-Token-Auth", CLITokenAuth)
	if kind == CLIRequestAccount {
		// Grok Build omits the marker on account reads.
		req.Header.Del(CLIAuthenticateResponseHeader)
	} else {
		req.Header.Set(CLIAuthenticateResponseHeader, CLIAuthenticateResponseValue)
	}
}

// CLIVersionRejection describes cli-chat-proxy's HTTP 426 answer to a client
// version below its floor. Either field may be empty when the body does not
// name it.
type CLIVersionRejection struct {
	// Advertised is the version the proxy saw in x-grok-client-version.
	Advertised string
	// Required is the minimum version the proxy asked for.
	Required string
}

var (
	cliRejectedVersionPattern = regexp.MustCompile(`(?i)cli version \(([^)\s]{1,40})\)`)
	cliRequiredVersionPattern = regexp.MustCompile(`(?i)update to version v?([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z][0-9A-Za-z.-]{0,30})?)`)
)

// ParseCLIVersionRejection reports whether an upstream response is the CLI
// proxy rejecting the advertised client version. It is an operator signal (the
// gateway's pin is too old), not an account failure.
func ParseCLIVersionRejection(statusCode int, body []byte) (CLIVersionRejection, bool) {
	if statusCode != http.StatusUpgradeRequired {
		return CLIVersionRejection{}, false
	}
	var rejection CLIVersionRejection
	text := string(body)
	if match := cliRejectedVersionPattern.FindStringSubmatch(text); len(match) == 2 {
		rejection.Advertised = match[1]
	}
	if match := cliRequiredVersionPattern.FindStringSubmatch(text); len(match) == 2 {
		rejection.Required = strings.TrimRight(match[1], ".")
	}
	return rejection, true
}
