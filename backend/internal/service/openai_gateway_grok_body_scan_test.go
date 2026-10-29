//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// grokBodyScanCorpus covers the shapes the fast paths must not misjudge:
// client-only tools, history that is always lowered, escaped and padded type
// names, duplicate keys, and words that only appear inside text.
func grokBodyScanCorpus() map[string]string {
	corpus := map[string]string{
		"function tools with lookalike text": `{"model":"grok-4.6","tools":[{"type":"function","name":"run","parameters":{"type":"object"}},{"type":"web_search"}],"input":[{"type":"message","role":"user","content":"customize the custom tool call parser; null is fine; compact the logs"}]}`,
		"output type named in text":          `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"message","role":"user","content":"rename custom_tool_call_output and compaction"}]}`,
		"no tools":                           `{"model":"grok-4.6","input":[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`,
		"null tools":                         `{"model":"grok-4.6","tools":null,"input":[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`,
		"empty tools":                        `{"model":"grok-4.6","tools":[],"input":[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`,
		"custom tool":                        `{"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar"}}],"input":[{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch"}]}`,
		"padded custom type":                 `{"tools":[{"type":" custom ","name":"apply_patch"}],"input":[]}`,
		"escaped custom type":                `{"tools":[{"type":"~u0063ustom","name":"apply_patch"}],"input":[]}`,
		"duplicate type keeps last":          `{"tools":[{"type":"function","type":"custom","name":"apply_patch"}],"input":[]}`,
		"duplicate tools keeps last":         `{"tools":[{"type":"function","name":"a"}],"tools":[{"type":"custom","name":"b"}],"input":[]}`,
		"tool search":                        `{"tools":[{"type":"tool_search"},{"type":"function","name":"a"}],"input":[]}`,
		"namespace tool":                     `{"tools":[{"type":"namespace","name":"github","tools":[{"type":"function","name":"search"}]}],"input":[{"type":"function_call","call_id":"c1","name":"github__search","arguments":"{}"}]}`,
		"deferred function":                  `{"tools":[{"type":"function","name":"a","defer_loading":true}],"input":[]}`,
		"lowered output history":             `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"custom_tool_call_output","call_id":"c1","output":{"ok":true}}]}`,
		"nested lowered output history":      `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"message","role":"user","content":[{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}]}`,
		"escaped lowered output history":     `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"~u0063ustom_tool_call_output","call_id":"c1","output":"ok"}]}`,
		"compaction item":                    `{"input":[{"type":"compaction","encrypted_content":"blob"},{"type":"message","role":"user","content":"hi"}]}`,
		"padded compaction summary":          `{"input":[{"type":"compaction_summary ","summary":[{"type":"summary_text","text":"earlier"}]}]}`,
		"escaped compaction":                 `{"input":[{"type":"~u0063ompaction","encrypted_content":"blob"}]}`,
		"duplicate input keeps last":         `{"input":[{"type":"message","role":"user","content":"hi"}],"input":[{"type":"compaction","encrypted_content":"blob"},{"type":"reasoning","summary":[],"content":null}]}`,
		"null field in input":                `{"input":[{"type":"reasoning","summary":[],"content":null,"encrypted_content":"x"}]}`,
		"null only inside strings":           `{"input":[{"type":"message","role":"user","content":"null \"null\" \\null"}],"tools":[{"type":"function","name":"a","parameters":{"default":null}}]}`,
		"compaction keeps its nulls":         `{"input":[{"type":"compaction","encrypted_content":"blob","summary":null}]}`,
		"html escaped text":                  `{"tools":[{"type":"function","name":"a"}],"input":[{"type":"message","role":"user","content":"~u003cdiv~u003e ~u00e9 ~u2028"}]}`,
		"string input":                       `{"input":"hello","tools":[{"type":"function","name":"a"}]}`,
		"object input":                       `{"input":{"type":"compaction","encrypted_content":"blob"}}`,
	}
	for name, raw := range corpus {
		corpus[name] = grokEscapedJSON(raw)
	}
	return corpus
}

// grokEscapedJSON turns the ~u placeholder into a JSON \u escape, keeping the
// escapes visible in the corpus source.
func grokEscapedJSON(raw string) string {
	return strings.ReplaceAll(raw, "~u", `\`+"u")
}

func TestAdaptGrokResponsesClientToolsFastPathMatchesFullAdapter(t *testing.T) {
	for name, raw := range grokBodyScanCorpus() {
		t.Run(name, func(t *testing.T) {
			body := []byte(raw)
			wantBody, wantMapping, wantErr := adaptResponsesClientToolsForFunctionUpstream(append([]byte(nil), body...), "Grok")
			gotBody, gotMapping, gotErr := adaptGrokResponsesClientTools(append([]byte(nil), body...))

			require.Equal(t, wantErr, gotErr)
			require.Equal(t, string(wantBody), string(gotBody))
			require.Equal(t, wantMapping, gotMapping)
			if !grokResponsesClientToolsNeedAdaptation(body) {
				require.Equal(t, raw, string(wantBody), "a skipped body must be one the adapter leaves unchanged")
			}
		})
	}
}

func TestGrokResponsesClientToolsNeedAdaptation(t *testing.T) {
	corpus := grokBodyScanCorpus()
	for _, name := range []string{"function tools with lookalike text", "no tools", "null tools", "empty tools", "html escaped text", "string input"} {
		require.False(t, grokResponsesClientToolsNeedAdaptation([]byte(corpus[name])), name)
	}
	for _, name := range []string{
		"custom tool", "padded custom type", "escaped custom type", "duplicate type keeps last",
		"duplicate tools keeps last", "tool search", "namespace tool", "deferred function",
		"lowered output history", "nested lowered output history", "escaped lowered output history",
		// A type name quoted in text only costs the full adapter, never a wrong skip.
		"output type named in text",
	} {
		require.True(t, grokResponsesClientToolsNeedAdaptation([]byte(corpus[name])), name)
	}
	require.True(t, grokResponsesClientToolsNeedAdaptation([]byte(`[{"tools":[]}]`)), "non-object bodies take the full adapter")
}

func TestGrokResponsesCompactionFastPathMatchesConversion(t *testing.T) {
	for name, raw := range grokBodyScanCorpus() {
		t.Run(name, func(t *testing.T) {
			body := []byte(raw)
			converted, err := convertOpenAICompactInputsForGrok(append([]byte(nil), body...))
			require.NoError(t, err)
			if !grokResponsesInputMayContainCompaction(body) {
				require.Equal(t, raw, string(converted), "a skipped body must be one the conversion leaves unchanged")
			}
		})
	}
	corpus := grokBodyScanCorpus()
	require.False(t, grokResponsesInputMayContainCompaction([]byte(corpus["null field in input"])))
	for _, name := range []string{"compaction item", "padded compaction summary", "escaped compaction", "duplicate input keeps last", "object input"} {
		require.True(t, grokResponsesInputMayContainCompaction([]byte(corpus[name])), name)
	}
}

func TestSanitizeGrokReasoningNullContentFastPathMatchesFullDecode(t *testing.T) {
	for name, raw := range grokBodyScanCorpus() {
		t.Run(name, func(t *testing.T) {
			body := []byte(raw)
			if grokInputNullsWouldBeStripped(t, body) {
				require.True(t, grokResponsesInputHasNullLiteral(body))
			}
			out, err := sanitizeGrokReasoningNullContent(append([]byte(nil), body...))
			require.NoError(t, err)
			if !grokResponsesInputHasNullLiteral(body) {
				require.Equal(t, raw, string(out))
			}
		})
	}
	corpus := grokBodyScanCorpus()
	require.False(t, grokResponsesInputHasNullLiteral([]byte(corpus["null only inside strings"])))
	require.True(t, grokResponsesInputHasNullLiteral([]byte(corpus["null field in input"])))

	out, err := sanitizeGrokReasoningNullContent([]byte(corpus["null field in input"]))
	require.NoError(t, err)
	require.JSONEq(t, `{"input":[{"type":"reasoning","summary":[],"encrypted_content":"x"}]}`, string(out))
}

// grokInputNullsWouldBeStripped runs the full decode the null fast path
// replaces and reports whether it would change input.
func grokInputNullsWouldBeStripped(t *testing.T, body []byte) bool {
	t.Helper()
	var decoded map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&decoded))
	input, ok := decoded["input"]
	if !ok {
		return false
	}
	_, changed := stripExplicitNullsFromGrokInput(input)
	return changed
}

func TestGrokJSONHasNullLiteral(t *testing.T) {
	for raw, want := range map[string]bool{
		`[]`:                          false,
		`[{"a":"null"}]`:              false,
		`[{"a":"x\"null"}]`:           false,
		`[{"a":"x\\"},null]`:          true,
		`[{"a":null}]`:                true,
		`[{"a":[1,true,false,null]}]`: true,
		`[{"a":"unterminated`:         true,
	} {
		require.Equal(t, want, grokJSONHasNullLiteral(raw), raw)
	}
}

func TestGrokJSONMayEscapeIdentifier(t *testing.T) {
	for raw, want := range map[string]bool{
		`"plain"`:                    false,
		`"~u003c~u00e9~u2028"`:       false,
		`"~u0063ustom"`:              true,
		`"~u005F"`:                   true,
		`"~u00"`:                     false,
		`"~u00zz ~u006F"`:            true,
		`"\\u0063 is an escaped bs"`: true,
	} {
		require.Equal(t, want, grokJSONMayEscapeIdentifier(grokEscapedJSON(raw)), raw)
	}
}

func TestForEachJSONArrayElementRaw(t *testing.T) {
	var got []string
	forEachJSONArrayElementRaw(` [ {"a":"]"} , "x,y" ,1, [2,[3]] ,null ] `, func(raw string) bool {
		got = append(got, raw)
		return true
	})
	require.Equal(t, []string{`{"a":"]"}`, `"x,y"`, `1`, `[2,[3]]`, `null`}, got)

	got = got[:0]
	forEachJSONArrayElementRaw(`[1,2,3]`, func(raw string) bool {
		got = append(got, raw)
		return len(got) < 2
	})
	require.Equal(t, []string{`1`, `2`}, got)
}
