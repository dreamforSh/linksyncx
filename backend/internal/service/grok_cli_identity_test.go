//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// grokCLI426Body is the body cli-chat-proxy returned to 0.2.120 on 2026-09-30.
const grokCLI426Body = "{\"error\":\"Your Grok CLI version (0.2.120) is outdated. Please update to version 1.0.13 or later via `grok update` or the installation documentation.\"}"

func TestApplyGrokCLISamplerHeadersMatchesGrokBuildCapture(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	// Session and group ids captured from a Grok Build 1.0.46 TUI turn.
	const sessionID = "01a0fadb-822e-7191-9137-38dcfe57ecc3"
	headers := http.Header{}
	applyGrokCLISamplerHeaders(headers, &Account{ID: 42}, "grok-4.6", sessionID)

	require.Equal(t, "grok-pager/"+xai.CLIClientVersion+" grok-shell/"+xai.CLIClientVersion+" (macos; aarch64)", headers.Get("User-Agent"))
	// The OAuth auth-middleware markers are added by the transport for the CLI
	// proxy host only, never by request builders.
	require.Empty(t, headers.Get("X-XAI-Token-Auth"))
	require.Empty(t, headers.Get("x-authenticateresponse"))
	require.Equal(t, "interactive", headers.Get("x-grok-client-mode"))
	require.Equal(t, xai.CLIClientVersion, headers.Get("x-grok-client-version"))
	require.Equal(t, "grok-pager", headers.Get("x-grok-client-identifier"))
	require.Equal(t, "grok-4.6", headers.Get("x-grok-model-override"))
	require.Equal(t, sessionID, headers.Get("x-grok-session-id"))
	require.Equal(t, "65f7d481-70a7-57de-a969-c6e50aa4094d", headers.Get("x-grok-conv-group-id"))

	agentID, err := uuid.Parse(headers.Get("x-grok-agent-id"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(5), agentID.Version())
	reqID, err := uuid.Parse(headers.Get("x-grok-req-id"))
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), reqID.Version())

	// Grok Build sends neither account identity nor stream-altering checks on
	// inference turns.
	for _, name := range []string{"X-UserID", "X-Email", "x-grok-doom-loop-check", "x-grok-exact-repetition-check", "x-grok-turn-idx"} {
		require.Empty(t, headers.Get(name), name)
	}

	again := http.Header{}
	applyGrokCLISamplerHeaders(again, &Account{ID: 42}, "grok-4.6", "")
	require.Equal(t, headers.Get("x-grok-agent-id"), again.Get("x-grok-agent-id"), "agent id is stable per account")
	require.NotEqual(t, headers.Get("x-grok-req-id"), again.Get("x-grok-req-id"), "request id is per request")
	require.Empty(t, again.Get("x-grok-session-id"))
	require.Empty(t, again.Get("x-grok-conv-group-id"))

	other := http.Header{}
	applyGrokCLISamplerHeaders(other, &Account{ID: 43}, "grok-4.6", sessionID)
	require.NotEqual(t, headers.Get("x-grok-agent-id"), other.Get("x-grok-agent-id"), "accounts never share an install id")
}

func TestApplyGrokCLIAccountHeadersOmitsInferenceMarkers(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	headers := http.Header{}
	applyGrokCLIAccountHeaders(headers)

	require.Equal(t, xai.CLIClientVersion, headers.Get("x-grok-client-version"))
	require.Equal(t, "interactive", headers.Get("x-grok-client-mode"))
	require.Empty(t, headers.Get("x-grok-client-identifier"))
	require.Empty(t, headers.Get("x-authenticateresponse"))
}

func TestBuildGrokResponsesRequestStampsSamplerHeadersForOAuth(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	account := &Account{ID: 7, Platform: PlatformGrok, Type: AccountTypeOAuth}
	body := []byte(`{"model":"grok-4.6","stream":true,"input":"hi"}`)
	req, err := buildGrokResponsesRequest(context.Background(), nil, account, body, "access-token", "isolated-cache-id", nil)
	require.NoError(t, err)

	require.Equal(t, "https://cli-chat-proxy.grok.com/v1/responses", req.URL.String())
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	require.Equal(t, "grok-pager", req.Header.Get("x-grok-client-identifier"))
	require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
	require.Equal(t, "grok-4.6", req.Header.Get("x-grok-model-override"))
	require.Equal(t, "isolated-cache-id", req.Header.Get(grokConversationIDHeader))
	require.Equal(t, "isolated-cache-id", req.Header.Get("x-grok-session-id"))
	require.Equal(t, grokConversationGroupID("isolated-cache-id"), req.Header.Get("x-grok-conv-group-id"))
	require.Equal(t, grokCLIAgentID(7), req.Header.Get("x-grok-agent-id"))
	require.NotEmpty(t, req.Header.Get("x-grok-req-id"))

	// The shared transport completes the identity for the CLI proxy host.
	xai.ApplyCLIProxyHeaders(req)
	require.Equal(t, "xai-grok-cli", req.Header.Get("X-XAI-Token-Auth"))
	require.Equal(t, "authenticate-response", req.Header.Get("x-authenticateresponse"))
	require.Equal(t, "grok-pager", req.Header.Get("x-grok-client-identifier"))
}

func TestBuildGrokResponsesRequestKeepsProxyAuthMarkersOffCustomUpstreams(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")

	account := &Account{
		ID:          9,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"base_url": "https://relay.example.test/v1"},
	}
	req, err := buildGrokResponsesRequest(context.Background(), nil, account, []byte(`{"model":"grok-4.6"}`), "access-token", "", nil)
	require.NoError(t, err)
	xai.ApplyCLIProxyHeaders(req)

	require.Equal(t, "https://relay.example.test/v1/responses", req.URL.String())
	require.Equal(t, xai.CLIClientVersion, req.Header.Get("x-grok-client-version"))
	require.Empty(t, req.Header.Get("X-XAI-Token-Auth"))
	require.Empty(t, req.Header.Get("x-authenticateresponse"))
}

func TestBuildGrokResponsesRequestLeavesAPIKeyAccountsWithoutCLIIdentity(t *testing.T) {
	account := &Account{ID: 8, Platform: PlatformGrok, Type: AccountTypeAPIKey}
	body := []byte(`{"model":"grok-4.6","stream":true}`)
	req, err := buildGrokResponsesRequest(context.Background(), nil, account, body, "xai-key", "isolated-cache-id", nil)
	require.NoError(t, err)

	require.Equal(t, "isolated-cache-id", req.Header.Get(grokConversationIDHeader))
	for _, name := range []string{"X-XAI-Token-Auth", "x-grok-client-version", "x-authenticateresponse", "x-grok-model-override", "x-grok-session-id", "x-grok-agent-id", "x-grok-req-id"} {
		require.Empty(t, req.Header.Get(name), name)
	}
}

func TestGrokCLIVersionRejectionIsNotAnAccountFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(grokCLI426Body)
	repo := &grokQuotaAccountRepo{}
	svc := &OpenAIGatewayService{
		accountRepo:      repo,
		rateLimitService: NewRateLimitService(repo, nil, nil, nil, nil),
	}
	// A keyword rule that would match the 426 must still not cool the account.
	account := &Account{
		ID:       5120,
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules": []any{map[string]any{
				"error_code":       float64(http.StatusUpgradeRequired),
				"keywords":         []any{"outdated"},
				"duration_minutes": float64(30),
			}},
		},
	}

	require.True(t, isGrokCLIVersionRejection(http.StatusUpgradeRequired, body))
	require.False(t, isGrokCLIVersionRejection(http.StatusForbidden, body))
	require.False(t, svc.shouldFailoverGrokUpstreamError(http.StatusUpgradeRequired, body))

	svc.handleGrokAccountUpstreamError(context.Background(), account, http.StatusUpgradeRequired, nil, body)
	require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusUpgradeRequired, nil, body, "grok-4.6"))

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp := &http.Response{StatusCode: http.StatusUpgradeRequired, Header: http.Header{}}
	require.Nil(t, svc.failoverOpenAIUpstreamHTTPError(context.Background(), c, account, resp, body, "outdated", "grok-4.6"))

	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	resp = &http.Response{
		StatusCode: http.StatusUpgradeRequired,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(grokCLI426Body)),
	}
	_, err := svc.handleErrorResponse(context.Background(), resp, c, account, nil, "grok-4.6")
	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, recorder.Code)
	require.Contains(t, recorder.Body.String(), "client version")

	require.Zero(t, repo.tempUnschedCalls)
	require.Zero(t, repo.rateLimitedCalls)
	require.Zero(t, repo.updateCalls)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account))
}

func TestGrokCLIVersionRejectionHintNamesRequiredVersion(t *testing.T) {
	t.Setenv(xai.CLIVersionEnv, "")

	hint := grokCLIVersionRejectionHint(http.StatusUpgradeRequired, []byte(grokCLI426Body))
	require.Contains(t, hint, "0.2.120")
	require.Contains(t, hint, "1.0.13 or later")
	require.Contains(t, hint, xai.CLIVersionEnv)
	require.Empty(t, grokCLIVersionRejectionHint(http.StatusForbidden, []byte(grokCLI426Body)))
}
