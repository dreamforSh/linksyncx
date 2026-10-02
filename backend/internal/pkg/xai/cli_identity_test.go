package xai

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveCLIVersionDefaultsToPinnedClientVersion(t *testing.T) {
	t.Setenv(CLIVersionEnv, "")
	// Default advertise pin is CLIClientVersion; CLIStableVersion is only the floor.
	require.Equal(t, CLIClientVersion, ResolveCLIVersion())
	require.True(t, IsSupportedCLIVersion(CLIClientVersion))
	require.True(t, IsSupportedCLIVersion(CLIStableVersion))
}

func TestResolveCLIVersionAcceptsValidOverride(t *testing.T) {
	for _, version := range []string{
		"1.0.47-alpha.1",
		"1.1.0",
		// Overrides may sit below the pin as long as the proxy still accepts them.
		"1.0.13",
		"1.0.20",
	} {
		t.Run(version, func(t *testing.T) {
			t.Setenv(CLIVersionEnv, version)
			require.Equal(t, version, ResolveCLIVersion())
		})
	}
}

func TestResolveCLIVersionRejectsUnsafeOrTooOld(t *testing.T) {
	for _, version := range []string{
		"0.2.120",
		"1.0.12",
		"1.0.13-beta.1",
		"1.0.46\r\nX-Injected: true",
		"1.0.046",
		"1.1",
		"1",
	} {
		t.Run(version, func(t *testing.T) {
			t.Setenv(CLIVersionEnv, version)
			require.Equal(t, CLIClientVersion, ResolveCLIVersion())
		})
	}
}

func TestCLIUserAgentMatchesGrokBuildTUI(t *testing.T) {
	// Grok Build 1.0.46 TUI sends "grok-pager/1.0.46 grok-shell/1.0.46 (windows; x86_64)"
	// on Windows; the gateway advertises the macOS build.
	require.Equal(t, "grok-pager/1.0.46 grok-shell/1.0.46 (macos; aarch64)", CLIUserAgent("1.0.46"))
	require.Equal(t, CLIUserAgent(CLIClientVersion), CLIUserAgent(" "))
}

func TestCLIRequestKindFor(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   CLIRequestKind
	}{
		{http.MethodPost, "/v1/responses", CLIRequestSampler},
		{http.MethodPost, "/v1/chat/completions", CLIRequestSampler},
		{http.MethodPost, "/v1/messages/", CLIRequestSampler},
		{http.MethodGet, "/v1/models", CLIRequestAccount},
		{http.MethodGet, "/v1/billing", CLIRequestAccount},
		{"", "/v1/models", CLIRequestAccount},
		{http.MethodPost, "/v1/images/generations", CLIRequestTool},
		{http.MethodGet, "/v1/videos/req-1", CLIRequestTool},
		{http.MethodGet, "/v1/responses", CLIRequestTool},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, CLIRequestKindFor(tc.method, tc.path), "%s %s", tc.method, tc.path)
	}
}

func TestApplyCLIProxyHeadersSampler(t *testing.T) {
	t.Setenv(CLIVersionEnv, "")

	req, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "legacy-client/1.0")
	req.Header.Set("x-grok-client-identifier", "grok-shell")

	ApplyCLIProxyHeaders(req)

	require.Equal(t, CLIClientVersion, req.Header.Get("x-grok-client-version"))
	require.Equal(t, CLISamplerClientIdentifier, req.Header.Get("x-grok-client-identifier"))
	require.Equal(t, CLIClientMode, req.Header.Get("x-grok-client-mode"))
	require.Equal(t, CLITokenAuth, req.Header.Get("X-XAI-Token-Auth"))
	require.Equal(t, CLIAuthenticateResponseValue, req.Header.Get(CLIAuthenticateResponseHeader))
	require.Equal(t, CLIUserAgent(CLIClientVersion), req.Header.Get("User-Agent"))
}

func TestApplyCLIProxyHeadersAccountAndTool(t *testing.T) {
	t.Setenv(CLIVersionEnv, "1.0.50")

	models, err := http.NewRequest(http.MethodGet, "https://cli-chat-proxy.grok.com/v1/models", nil)
	require.NoError(t, err)
	models.Header.Set("x-grok-client-identifier", "grok-shell")
	models.Header.Set(CLIAuthenticateResponseHeader, CLIAuthenticateResponseValue)
	ApplyCLIProxyHeaders(models)
	require.Equal(t, "1.0.50", models.Header.Get("x-grok-client-version"))
	require.Equal(t, CLIUserAgent("1.0.50"), models.Header.Get("User-Agent"))
	require.Empty(t, models.Header.Get("x-grok-client-identifier"))
	require.Empty(t, models.Header.Get(CLIAuthenticateResponseHeader))
	require.Equal(t, "*/*", models.Header.Get("Accept"))

	images, err := http.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/images/generations", nil)
	require.NoError(t, err)
	ApplyCLIProxyHeaders(images)
	require.Equal(t, CLIClientIdentifier, images.Header.Get("x-grok-client-identifier"))
	require.Equal(t, CLIAuthenticateResponseValue, images.Header.Get(CLIAuthenticateResponseHeader))
}

func TestApplyCLIIdentityHeadersOmitsProxyAuthMarkers(t *testing.T) {
	t.Setenv(CLIVersionEnv, "")

	// Custom upstreams and api.x.ai must not see the OAuth auth-middleware
	// markers; only the transport adds them for the CLI proxy host.
	for _, kind := range []CLIRequestKind{CLIRequestSampler, CLIRequestTool, CLIRequestAccount} {
		headers := http.Header{}
		ApplyCLIIdentityHeaders(headers, kind)
		require.Equal(t, CLIClientVersion, headers.Get("x-grok-client-version"))
		require.Equal(t, CLIClientMode, headers.Get("x-grok-client-mode"))
		require.Equal(t, CLIUserAgent(CLIClientVersion), headers.Get("User-Agent"))
		require.Empty(t, headers.Get("X-XAI-Token-Auth"))
		require.Empty(t, headers.Get(CLIAuthenticateResponseHeader))
	}
}

func TestApplyCLIProxyHeadersLeavesAPIHostUnchanged(t *testing.T) {
	t.Setenv(CLIVersionEnv, "1.0.50")

	req, err := http.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "direct-api-client/1.0")

	ApplyCLIProxyHeaders(req)

	require.Empty(t, req.Header.Get("x-grok-client-version"))
	require.Empty(t, req.Header.Get("x-grok-client-identifier"))
	require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
	require.Empty(t, req.Header.Get(CLIAuthenticateResponseHeader))
	require.Equal(t, "direct-api-client/1.0", req.Header.Get("User-Agent"))
}

func TestParseCLIVersionRejection(t *testing.T) {
	// Body captured from cli-chat-proxy on 2026-09-30.
	body := []byte("{\"error\":\"Your Grok CLI version (0.2.120) is outdated. Please update to version 1.0.13 or later via `grok update` or the installation documentation.\"}")
	rejection, ok := ParseCLIVersionRejection(http.StatusUpgradeRequired, body)
	require.True(t, ok)
	require.Equal(t, CLIVersionRejection{Advertised: "0.2.120", Required: "1.0.13"}, rejection)

	rejection, ok = ParseCLIVersionRejection(http.StatusUpgradeRequired, []byte(`{"error":"Please update to version 1.2.0."}`))
	require.True(t, ok)
	require.Equal(t, CLIVersionRejection{Required: "1.2.0"}, rejection)

	rejection, ok = ParseCLIVersionRejection(http.StatusUpgradeRequired, []byte("upgrade required"))
	require.True(t, ok)
	require.Equal(t, CLIVersionRejection{}, rejection)

	_, ok = ParseCLIVersionRejection(http.StatusForbidden, body)
	require.False(t, ok)
}
