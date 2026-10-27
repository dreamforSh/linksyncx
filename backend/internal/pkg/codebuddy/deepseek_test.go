package codebuddy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestDeepSeekReasoningEffort(t *testing.T) {
	cases := map[string]struct {
		effort string
		send   bool
	}{
		"":        {"max", true},
		"none":    {"", false},
		"minimal": {"high", true},
		"low":     {"high", true},
		"medium":  {"high", true},
		"High":    {"high", true},
		"xhigh":   {"max", true},
		"max":     {"max", true},
		"turbo":   {"max", true},
	}
	for requested, want := range cases {
		effort, send := DeepSeekReasoningEffort(requested)
		require.Equal(t, want.effort, effort, requested)
		require.Equal(t, want.send, send, requested)
	}
	require.True(t, IsDeepSeekModel("DeepSeek-V4-Pro"))
	require.False(t, IsDeepSeekModel("glm-5.2"))
}

func TestPrepareChatBodyDeepSeekEffort(t *testing.T) {
	for requested, want := range map[string]string{
		``:                            "max",
		`"reasoning_effort":null,`:    "max",
		`"reasoning_effort":"low",`:   "high",
		`"reasoning_effort":"xhigh",`: "max",
	} {
		out, err := PrepareChatBody([]byte(`{`+requested+`"model":"deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
		require.NoError(t, err)
		require.Equal(t, want, gjson.GetBytes(out, "reasoning_effort").String(), requested)
	}

	out, err := PrepareChatBody([]byte(`{"reasoning_effort":"none","model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "reasoning_effort").Exists(), "none omits the field like the official client")

	out, err = PrepareChatBody([]byte(`{"reasoning_effort":"medium","model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.Equal(t, "medium", gjson.GetBytes(out, "reasoning_effort").String(), "other models keep their effort")
}

func TestPrepareChatBodyDeepSeekResponseFormat(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-pro","response_format":{"type":"json_schema","json_schema":{"name":"answer","strict":true,"schema":{"type":"object","properties":{"html":{"type":"string","description":"<b>&</b>"}},"required":["html"]}}},` +
		`"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`)
	out, err := PrepareChatBody(body, ProfileCNCLI)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "response_format").Exists())
	system := gjson.GetBytes(out, "messages.0.content").String()
	require.True(t, strings.HasPrefix(system, "sys\n\nYour final answer must be a single valid JSON value"), system)
	require.True(t, strings.HasSuffix(system, `JSON schema:
{"type":"object","properties":{"html":{"type":"string","description":"<b>&</b>"}},"required":["html"]}`), system)
	require.NotContains(t, string(out), `\`+`u003c`, "schema text must not be HTML-escaped")
	require.Equal(t, "user", gjson.GetBytes(out, "messages.1.role").String())

	// json_object onto content parts, and onto the inserted default system message.
	out, err = PrepareChatBody([]byte(`{"model":"deepseek-v4-flash","response_format":{"type":"json_object"},"messages":[{"role":"system","content":[{"type":"text","text":"sys"}]},{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.Equal(t, "sys", gjson.GetBytes(out, "messages.0.content.0.text").String())
	require.Contains(t, gjson.GetBytes(out, "messages.0.content.1.text").String(), "single valid JSON object")
	out, err = PrepareChatBody([]byte(`{"model":"deepseek-v4-flash","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(gjson.GetBytes(out, "messages.0.content").String(), DefaultSystemMessage+"\n\nYour final answer"))

	// A Responses-shaped format sent straight to Chat keeps its schema.
	out, err = PrepareChatBody([]byte(`{"model":"deepseek-v4-pro","response_format":{"type":"json_schema","name":"answer","schema":{"type":"array"}},"messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "response_format").Exists())
	require.True(t, strings.HasSuffix(gjson.GetBytes(out, "messages.0.content").String(), "JSON schema:\n"+`{"type":"array"}`))

	// Plain text needs no instruction.
	out, err = PrepareChatBody([]byte(`{"model":"deepseek-v4-pro","response_format":{"type":"text"},"messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(out, "response_format").Exists())
	require.Equal(t, "sys", gjson.GetBytes(out, "messages.0.content").String())

	// Other models keep response_format as sent.
	out, err = PrepareChatBody([]byte(`{"model":"glm-5.2","response_format":{"type":"json_object"},"messages":[{"role":"user","content":"hi"}]}`), ProfileCNCLI)
	require.NoError(t, err)
	require.Equal(t, "json_object", gjson.GetBytes(out, "response_format.type").String())
}
