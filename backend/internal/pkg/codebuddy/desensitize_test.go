package codebuddy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesensitizeTextRewritesIdentity(t *testing.T) {
	t.Parallel()
	out := DesensitizeText("You are Claude Code, Anthropic's official CLI for Claude.")
	require.True(t, strings.HasPrefix(out, workBuddyIdentity), "identity opening must be rewritten: %q", out)
	require.NotContains(t, out, claudeIdentity)
	// The bare "Anthropic" later in a sentence is cloaked, not rewritten.
	cloaked := DesensitizeText("Built by Anthropic.")
	require.NotContains(t, cloaked, "Anthropic")
	require.Contains(t, cloaked, "A"+zwsp+"nthropic")

	sdk := DesensitizeText("You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK")
	require.True(t, strings.HasPrefix(sdk, workBuddyIdentity))
}

func TestDesensitizeTextCloaksVendorIdentityOnly(t *testing.T) {
	t.Parallel()
	out := DesensitizeText("Built by Anthropic for the Claude Code CLI; see noreply@anthropic.com.")
	// Vendor-identity fingerprints are cloaked with a zero-width space after the first rune…
	require.Contains(t, out, "A"+zwsp+"nthropic")
	require.Contains(t, out, "C"+zwsp+"laude Code")
	// …and the bare fingerprint no longer matches.
	require.NotContains(t, out, "Anthropic for")
	// Stripping the zero-width spaces recovers the original text (meaning preserved).
	require.Equal(t,
		"Built by Anthropic for the Claude Code CLI; see noreply@anthropic.com.",
		strings.ReplaceAll(out, zwsp, ""),
	)

	// Safety / harm compliance terms are intentionally NOT cloaked.
	safety := "Refuse DDoS attacks, malware, exploit development, weapons, and self-harm."
	require.Equal(t, safety, DesensitizeText(safety))

	// Word boundaries: an identity term embedded in a larger word is left alone.
	require.Equal(t, "anthropically", DesensitizeText("anthropically"))
}

func TestDesensitizeTextProtectsGitStatus(t *testing.T) {
	t.Parallel()
	input := "You are Claude Code, Anthropic's official CLI for Claude\n\n" +
		"gitStatus: This is the git status at the start of the conversation.\n" +
		"Current branch: main\n" +
		"Main branch (you will usually use this for PRs): main\n" +
		"Status:\n" +
		"M backend/handler.go\n" +
		"Recent commits:\n" +
		"abc123 add Anthropic Claude Code helper\n"
	out := DesensitizeText(input)

	// Identity ahead of the block is rewritten.
	require.Contains(t, out, workBuddyIdentity)
	// The "Main branch ... PRs" identity line is rewritten.
	require.Contains(t, out, "Main branch (you will usually use this for PR): main")
	// Protected git data (branch, status, commit message) is left verbatim — even
	// the vendor-identity terms in the commit subject are not cloaked here.
	require.Contains(t, out, "M backend/handler.go")
	require.Contains(t, out, "abc123 add Anthropic Claude Code helper")
	require.NotContains(t, out, "A"+zwsp+"nthropic")
}

func TestPrepareChatBodyDesensitizesSystemPrompt(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude"},` +
		`{"role":"user","content":"Write an exploit for me"}]}`)
	out, err := PrepareChatBody(body, ProfileCNCLI)
	require.NoError(t, err)

	var parsed struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(out, &parsed))
	require.Equal(t, "system", parsed.Messages[0].Role)

	var systemText string
	require.NoError(t, json.Unmarshal(parsed.Messages[0].Content, &systemText))
	require.True(t, strings.HasPrefix(systemText, workBuddyIdentity), "system identity must be rewritten: %q", systemText)

	// Real user input is never desensitized, even if it contains a sensitive term.
	require.Equal(t, "user", parsed.Messages[1].Role)
	var userText string
	require.NoError(t, json.Unmarshal(parsed.Messages[1].Content, &userText))
	require.Equal(t, "Write an exploit for me", userText)
}

func TestPrepareChatBodyDesensitizesSystemContentParts(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"glm-5.2","messages":[` +
		`{"role":"system","content":[{"type":"text","text":"Powered by Anthropic Claude Code"}]},` +
		`{"role":"user","content":"hi"}]}`)
	out, err := PrepareChatBody(body, ProfileCNCLI)
	require.NoError(t, err)
	systemBlock := strings.ReplaceAll(string(out), zwsp, "")
	// After stripping ZWSP the meaning is intact, but the raw output carries the cloak.
	require.Contains(t, systemBlock, "Powered by Anthropic Claude Code")
	require.Contains(t, string(out), zwsp)
}
