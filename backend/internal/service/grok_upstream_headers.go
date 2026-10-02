package service

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

const (
	// grokConversationGroupNamespace prefixes the root session id when Grok
	// Build derives x-grok-conv-group-id (UUIDv5 in the OID namespace).
	grokConversationGroupNamespace = "xai:grok-build:conversation-group:"
	// grokAgentIDNamespace seeds the per-account stand-in for Grok Build's
	// machine-derived x-grok-agent-id, so accounts never share one install id.
	grokAgentIDNamespace = "sub2api:grok-build:agent:"
)

// defaultGrokUpstreamUserAgent is the pinned Grok Build TUI User-Agent.
// Grok upstream must not forward Claude Code / Codex / browser client UAs.
func defaultGrokUpstreamUserAgent() string {
	return xai.CLIUserAgent(xai.ResolveCLIVersion())
}

func applyDefaultGrokUpstreamHeaders(req *http.Request) {
	if req == nil {
		return
	}
	// Always stamp CLI identity. Do not preserve inbound client UA (Claude Code,
	// Codex, curl, etc.) — xAI chat/CLI surfaces fingerprint the client string.
	req.Header.Set("User-Agent", defaultGrokUpstreamUserAgent())
	req.Header.Set("x-grok-client-version", xai.ResolveCLIVersion())
	req.Header.Set("x-grok-client-identifier", xai.CLIClientIdentifier)
}

func applyGrokTLSProfileHeaders(req *http.Request, profile *tlsfingerprint.Profile) {
	// HEAD Profile is TLS-only (no HTTP UserAgent/Originator fields). Always stamp CLI identity.
	applyDefaultGrokUpstreamHeaders(req)
	_ = profile
}

// openAITLSFingerprintRuntime is the resolved TLS fingerprint routing result
// used by OpenAI/Grok outbound header application. Defined here so Grok header
// helpers compile even when the full OpenAI TLS router is not present on HEAD.
type openAITLSFingerprintRuntime struct {
	Profile            *tlsfingerprint.Profile
	UpstreamUserAgent  string
	UpstreamOriginator string
	Matched            bool
}

func applyGrokRuntimeHeaders(req *http.Request, runtime openAITLSFingerprintRuntime) {
	applyDefaultGrokUpstreamHeaders(req)
	if req == nil {
		return
	}
	// Apply Originator only; force CLI UA after so router overrides cannot
	// leak Codex/Claude Code identity onto Grok upstream.
	if originator := strings.TrimSpace(runtime.UpstreamOriginator); originator != "" {
		req.Header.Set("Originator", originator)
	}
	req.Header.Set("User-Agent", defaultGrokUpstreamUserAgent())
}

// resolveGrokUpstreamUserAgent always returns the pinned Grok CLI User-Agent.
// Inbound client UAs (Claude Code, Codex, browsers, libraries) are never forwarded.
func resolveGrokUpstreamUserAgent(_ *gin.Context) string {
	return defaultGrokUpstreamUserAgent()
}

// applyGrokCLIHeaders stamps the Grok Build client identity of tool, media and
// voice requests. The CLI gateway rejects otherwise valid OAuth requests
// without it. Identity pins come from package xai so service-layer headers
// match the final transport rewrite on cli-chat-proxy.grok.com.
func applyGrokCLIHeaders(headers http.Header) {
	xai.ApplyCLIIdentityHeaders(headers, xai.CLIRequestTool)
}

// applyGrokCLIAccountHeaders stamps the identity Grok Build uses on account
// reads such as the model list.
func applyGrokCLIAccountHeaders(headers http.Header) {
	xai.ApplyCLIIdentityHeaders(headers, xai.CLIRequestAccount)
}

// applyGrokCLISamplerHeaders stamps the Grok Build sampler identity of an
// inference turn: the client identity plus the per-turn x-grok-* ids captured
// from Grok Build 1.0.46. convID is the request's x-grok-conv-id; Grok Build
// reuses its session id for both values and derives the group id from it.
//
// Left out on purpose: x-grok-turn-idx (unknown for relayed history), the
// doom-loop and exact-repetition checks (they inject check events into the
// stream), x-compaction-* (per-model remote config) and traceparent.
func applyGrokCLISamplerHeaders(headers http.Header, account *Account, model, convID string) {
	if headers == nil {
		return
	}
	xai.ApplyCLIIdentityHeaders(headers, xai.CLIRequestSampler)
	headers.Set("x-grok-req-id", uuid.NewString())
	if model = strings.TrimSpace(model); model != "" {
		headers.Set("x-grok-model-override", model)
	}
	if account != nil && account.ID > 0 {
		headers.Set("x-grok-agent-id", grokCLIAgentID(account.ID))
	}
	if convID = strings.TrimSpace(convID); convID != "" {
		headers.Set("x-grok-session-id", convID)
		headers.Set("x-grok-conv-group-id", grokConversationGroupID(convID))
	} else {
		headers.Del("x-grok-session-id")
		headers.Del("x-grok-conv-group-id")
	}
}

// grokConversationGroupID mirrors Grok Build's derive_conversation_group_id.
func grokConversationGroupID(rootSessionID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(grokConversationGroupNamespace+rootSessionID)).String()
}

func grokCLIAgentID(accountID int64) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(grokAgentIDNamespace+strconv.FormatInt(accountID, 10))).String()
}
