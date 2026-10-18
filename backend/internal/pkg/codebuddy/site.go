// Package codebuddy implements the upstream wire protocol of Tencent CodeBuddy /
// WorkBuddy subscriptions: per-site product profiles, the plugin OAuth login and
// token refresh, the stream-only /v2/chat/completions inference endpoint, the
// /v3/config model catalog and the billing (credits) endpoints.
//
// Domestic (国内, *.cn / copilot.tencent.com) and international (*.ai) estates
// are fully isolated: a token from one is rejected by the other, so every
// request is routed by the credential's profile, never by caller input.
package codebuddy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Regions.
const (
	SiteCN   = "cn"
	SiteIntl = "intl"
)

// Login sites accepted by the OAuth start call. SiteIntl logs in through the
// international WorkBuddy site; SiteIntlCodeBuddy through the international
// CodeBuddy (CLI) site. Domestic CLI and domestic WorkBuddy share one login
// host and are told apart by the domain the token comes back with.
const (
	LoginSiteCN            = SiteCN
	LoginSiteIntl          = SiteIntl
	LoginSiteIntlCodeBuddy = "intl-codebuddy"
)

// Products.
const (
	ProductCLI       = "cli"
	ProductWorkBuddy = "workbuddy"
)

// Profiles: region × product. Each one has its own chat origin and identity.
const (
	ProfileCNCLI    = "cn-cli"
	ProfileCNWork   = "cn-work"
	ProfileIntlCLI  = "intl-cli"
	ProfileIntlWork = "intl-work"
)

const (
	DomesticEndpoint      = "https://copilot.tencent.com"
	InternationalEndpoint = "https://www.codebuddy.ai"
	WorkBuddyCNEndpoint   = "https://www.workbuddy.cn"
	WorkBuddyIntlEndpoint = "https://www.workbuddy.ai"

	CodeBuddyCNHost = "https://www.codebuddy.cn"

	// copilotIssuerHost is the domestic Keycloak issuer shared by domestic CLI
	// and WorkBuddy tokens, so it never decides the product on its own.
	copilotIssuerHost = "copilot.tencent.com"
	// DefaultDomain is sent as X-Domain when neither the credential nor the
	// token names one.
	DefaultDomain = "www.codebuddy.cn"
)

const (
	pluginPrefix     = "/v2/plugin"
	RefreshPath      = pluginPrefix + "/auth/token/refresh"
	AuthStatePath    = pluginPrefix + "/auth/state"
	AuthTokenPath    = pluginPrefix + "/auth/token"
	LoginAccountPath = pluginPrefix + "/login/account"
	ChatPath         = "/v2/chat/completions"
	ConfigPath       = "/v3/config"
)

// ProfileEndpoints is the chat / refresh / catalog origin for each profile.
var ProfileEndpoints = map[string]string{
	ProfileCNCLI:    DomesticEndpoint,
	ProfileCNWork:   WorkBuddyCNEndpoint,
	ProfileIntlCLI:  InternationalEndpoint,
	ProfileIntlWork: WorkBuddyIntlEndpoint,
}

// ProfileDomains is the credential domain each product stores; refresh echoes
// the X-Domain header back as auth.domain.
var ProfileDomains = map[string]string{
	ProfileCNCLI:    "www.codebuddy.cn",
	ProfileCNWork:   "www.workbuddy.cn",
	ProfileIntlCLI:  "www.codebuddy.ai",
	ProfileIntlWork: "www.workbuddy.ai",
}

// DomainProfiles maps every allowed credential host to its profile.
var DomainProfiles = map[string]string{
	"www.codebuddy.cn": ProfileCNCLI,
	"www.workbuddy.cn": ProfileCNWork,
	copilotIssuerHost:  ProfileCNCLI,
	"www.codebuddy.ai": ProfileIntlCLI,
	"www.workbuddy.ai": ProfileIntlWork,
}

// LoginHosts are the OAuth hosts per login site.
var LoginHosts = map[string]string{
	LoginSiteCN:            CodeBuddyCNHost,
	LoginSiteIntl:          WorkBuddyIntlEndpoint,
	LoginSiteIntlCodeBuddy: InternationalEndpoint,
}

// BillingHosts are the product websites serving billing endpoints. Domestic
// CLI bills on www.codebuddy.cn, not on its chat origin.
var BillingHosts = map[string]string{
	ProfileCNCLI:    CodeBuddyCNHost,
	ProfileCNWork:   WorkBuddyCNEndpoint,
	ProfileIntlCLI:  InternationalEndpoint,
	ProfileIntlWork: WorkBuddyIntlEndpoint,
}

// ValidProfile reports whether profile is one of the four known profiles.
func ValidProfile(profile string) bool {
	_, ok := ProfileEndpoints[profile]
	return ok
}

// ProfileRegion returns SiteCN or SiteIntl, or "" for an unknown profile.
func ProfileRegion(profile string) string {
	if !ValidProfile(profile) {
		return ""
	}
	if strings.HasPrefix(profile, "intl") {
		return SiteIntl
	}
	return SiteCN
}

// ProfileProduct returns ProductWorkBuddy or ProductCLI.
func ProfileProduct(profile string) string {
	if strings.HasSuffix(profile, "-work") {
		return ProductWorkBuddy
	}
	return ProductCLI
}

// IsInternational reports whether the profile belongs to the .ai estate.
func IsInternational(profile string) bool {
	return ProfileRegion(profile) == SiteIntl
}

// NormalizeHost reduces a domain, origin or issuer URL to a known lowercase
// host. Only https hosts in DomainProfiles are accepted; an issuer may carry a
// path (Keycloak realms), but never a port or userinfo.
func NormalizeHost(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return "", fmt.Errorf("empty host")
	}
	if strings.ContainsAny(raw, "\\?#") {
		return "", fmt.Errorf("invalid host")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid host")
	}
	host := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" || host == "" || parsed.User != nil || parsed.Port() != "" {
		return "", fmt.Errorf("unsupported host")
	}
	if _, ok := DomainProfiles[host]; !ok {
		return "", fmt.Errorf("unsupported host %s", host)
	}
	return host, nil
}

// DecodeJWTClaims decodes a JWT payload without verifying its signature. The
// claims only route requests between fixed endpoints; they never grant trust.
func DecodeJWTClaims(token string) map[string]any {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(decoded, &claims) != nil {
		return nil
	}
	return claims
}

// TokenIssuerHost returns the known host of the token's iss claim, or "".
func TokenIssuerHost(accessToken string) string {
	iss, _ := DecodeJWTClaims(accessToken)["iss"].(string)
	if iss == "" {
		return ""
	}
	host, err := NormalizeHost(iss)
	if err != nil {
		return ""
	}
	return host
}

// TokenSubject returns the sub claim of a JWT (the CodeBuddy uid on refresh
// tokens), or "".
func TokenSubject(token string) string {
	sub, _ := DecodeJWTClaims(token)["sub"].(string)
	return strings.TrimSpace(sub)
}

// ProfileForAuth resolves the profile from the credential domain and the
// access-token issuer. Both hints must agree on the region; the shared
// domestic issuer never picks the product, the branded hosts do.
func ProfileForAuth(domain, accessToken string) (string, error) {
	var hosts []string
	if strings.TrimSpace(domain) != "" {
		host, err := NormalizeHost(domain)
		if err != nil {
			return "", err
		}
		hosts = append(hosts, host)
	}
	if issuer := TokenIssuerHost(accessToken); issuer != "" {
		hosts = append(hosts, issuer)
	}
	regions := map[string]struct{}{}
	profiles := map[string]struct{}{}
	for _, host := range hosts {
		profile := DomainProfiles[host]
		regions[ProfileRegion(profile)] = struct{}{}
		if host != copilotIssuerHost {
			profiles[profile] = struct{}{}
		}
	}
	if len(regions) > 1 {
		return "", fmt.Errorf("conflicting credential sites")
	}
	if len(profiles) > 1 {
		return "", fmt.Errorf("conflicting credential products")
	}
	for profile := range profiles {
		return profile, nil
	}
	return ProfileCNCLI, nil
}

// DomainForAuth is the X-Domain value: the stored domain, else the issuer
// host, else the domestic CodeBuddy domain.
func DomainForAuth(domain, accessToken string) string {
	if host, err := NormalizeHost(domain); err == nil {
		return host
	}
	if host := TokenIssuerHost(accessToken); host != "" {
		return host
	}
	return DefaultDomain
}

// EndpointForProfile returns the chat origin, defaulting to domestic CLI.
func EndpointForProfile(profile string) string {
	if endpoint, ok := ProfileEndpoints[profile]; ok {
		return endpoint
	}
	return DomesticEndpoint
}

func ChatCompletionsURL(profile string) string { return EndpointForProfile(profile) + ChatPath }
func RefreshURL(profile string) string         { return EndpointForProfile(profile) + RefreshPath }
func ConfigURL(profile string) string          { return EndpointForProfile(profile) + ConfigPath }

// BillingHost returns the product website serving billing for the profile.
func BillingHost(profile string) string {
	if host, ok := BillingHosts[profile]; ok {
		return host
	}
	return CodeBuddyCNHost
}

// LoginHostForSite resolves an OAuth login site to its host.
func LoginHostForSite(site string) (string, error) {
	host, ok := LoginHosts[strings.ToLower(strings.TrimSpace(site))]
	if !ok {
		return "", fmt.Errorf("unknown site %q (supported: cn, intl, intl-codebuddy)", site)
	}
	return host, nil
}

// OriginAllowed reports whether the credential domain or token issuer is one
// of the official origins.
func OriginAllowed(domain, accessToken string) bool {
	if _, err := NormalizeHost(domain); err == nil {
		return true
	}
	return TokenIssuerHost(accessToken) != ""
}
