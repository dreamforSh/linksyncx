//go:build unit

package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The legacy* functions used here are verbatim HEAD copies kept in the
// openai_ttft_legacy_*_test.go files; every fast path must match them for all
// inputs, including invalid JSON.

func ttftDiffSeeds() []string {
	seeds := []string{
		`{"model":"gpt-5.4","input":"hi"}`,
		`{"model":"gpt-5.4","tools":[{"type":"function","name":"a","parameters":{"type":"object","properties":{"x":{"type":"string","pattern":"^(?=a)b$"}}}}],"input":[]}`,
		`{"tools":[{"type":"function","name":"a","parameters":{"type":null,"properties":{}}}],"input":[{"type":"message","role":"user","content":"(x)"}]}`,
		`{"tools":[{"type":"function","name":"a","parameters":{"type":"object","required":null,"properties":{"y":{"pattern":"(?!z)"}}}}]}`,
		`{"tools":[{"type":"function","name":"a","parameters":{"oneOf":[{"type":"object"},{"type":"object"}]}}],"x":"("}`,
		`{"input":[{"type":"additional_tools","tools":[{"type":"function","name":"b","parameters":{"type":"object","properties":{"p":{"type":"string","pattern":"(?<=q)"}}}}]},{"type":"function_call_output","call_id":"c","output":"(?=not a schema)"}]}`,
		`{"input":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"c","parameters":{"type":"object","properties":{"p":{"pattern":"(?<!r)"}}}}]}],"tools":[]}`,
		`{"tools":[{"type":"function","function":{"name":"d","parameters":{"type":"object","properties":{"p":{"pattern":"(?=e)"}}}}}]}`,
		`{"tools":[{"type":"image_generation","format":"png"}],"model":"gpt-5.4"}`,
		`{"tools":[{"type":"image_generation","model":"gpt-image-2","input_fidelity":"high"}]}`,
		`{"tool_choice":{"type":"image_generation"},"input":[]}`,
		`{"input":[{"type":"namespace","name":"image_gen"}],"model":"gpt-image-1"}`,
		`{"model":"gpt-image-2","tools":[]}`,
		`{"client_metadata":{"thread_id":"t1","x-codex-turn-metadata":"{\"thread_id\":\"t2\",\"request_kind\":\"memory\"}","x-openai-subagent":"Guardian"},"prompt_cache_key":"pk"}`,
		`{"client_metadata":{"x-codex-turn-metadata":"{\"request_kind\":\"Prewarm\"}"},"client_metadata":{"thread_id":"dup"},"prompt_cache_key":"pk2"}`,
		`{"client_metadata":{"thread_id":"esc"},"input":[]}`,
		`{"input":[],"prompt_cache_key":"only"}`,
		`{"tools":[{"type":"function","parameters":{"type":"object"}}]} trailing`,
		`{"tools":[{"type":"function","parameters":{"type":"object"}}]`,
		`[{"tools":[]}]`,
		`{"a":"\xff(","tools":[{"type":"function","parameters":{"pattern":"(?=x)"}}]}`,
		`{"tools":[{"type":"function","parameters":{"type":"object","properties":{"a":{"pattern":"(?=x)"},"b":{"pattern":"(?!y)"}}}}],"input":[{"type":"custom","name":"t","parameters":{"pattern":"(?=w)"}}]}`,
		`{"model":"gpt-5.4","tools":[{"type":"image_generation"}],"input":[1,2,{"type":"reasoning","encrypted_content":"AAAA"}]}`,
	}
	// Repair and lookaround deletions as adjacent siblings: one combined delete
	// run must not stand in for the two sequential passes.
	seeds = append(seeds,
		`{"tools":[{"type":"function","parameters":{"type":"object","properties":{"a":{"required":null,"pattern":"(?=x)"}}}}]}`,
		`{"tools":[{"type":"function","parameters":{"type":"object","properties":{"a":{"pattern":"(?=x)", "required" : null ,"type":"string"}}}}]}`,
		`{"tools":[{"type":"function","parameters":{"type":"object","properties":{"a":{"type":"string","required":null,
		"pattern":"(?!y)"}}}}]}`,
		`{"tools":[{"type":"function","parameters":{"required":null,"pattern":"(?=root)","type":null}}]}`,
	)
	deep := strings.Repeat(`{"a":`, 140) + `"(?=x)"` + strings.Repeat(`}`, 140)
	seeds = append(seeds, `{"tools":[{"type":"function","parameters":`+deep+`}]}`)
	seeds = append(seeds, string(ttftCodexShapedBody(24<<10)))
	return seeds
}

// ttftCodexShapedBody builds a Codex-CLI-shaped history (reasoning, tool
// calls with big outputs, escapes) of roughly target bytes.
func ttftCodexShapedBody(target int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"model":"gpt-5.4","instructions":"You are Codex (follow rules)","input":[`)
	b.WriteString(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>"}]}`)
	for i := 0; b.Len() < target; i++ {
		b.WriteString(fmt.Sprintf(`,{"type":"message","role":"user","content":[{"type":"input_text","text":"fix bug %d <tag> & (?=x)"}]}`, i))
		b.WriteString(fmt.Sprintf(`,{"type":"reasoning","summary":[{"type":"summary_text","text":"step %d"}],"encrypted_content":"gAAA%s"}`, i, strings.Repeat("Zm9v", 50)))
		b.WriteString(fmt.Sprintf(`,{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"rg %d\"}","call_id":"call_%d"}`, i, i))
		b.WriteString(fmt.Sprintf(`,{"type":"function_call_output","call_id":"call_%d","output":"src/x.go:1: \u001b[0m <a&b> %d"}`, i, i))
	}
	b.WriteString(`],"tools":[{"type":"function","name":"shell","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string","pattern":"^(?!rm)"}},"required":["cmd"]}}],"tool_choice":"auto","store":false,"stream":true,"include":["reasoning.encrypted_content"],"prompt_cache_key":"0199a0b1"}`)
	return b.Bytes()
}

const ttftMutationAlphabet = "{}[]\",:\\ (?=!<u0123456789abcdefnulltrue-.eE\t\n"

func mutateJSONish(rng *rand.Rand, src []byte) []byte {
	out := append([]byte(nil), src...)
	for n := rng.Intn(4) + 1; n > 0 && len(out) > 0; n-- {
		pos := rng.Intn(len(out))
		switch rng.Intn(4) {
		case 0:
			out = append(out[:pos], out[pos+1:]...)
		case 1:
			out = append(out[:pos], append([]byte{ttftMutationAlphabet[rng.Intn(len(ttftMutationAlphabet))]}, out[pos:]...)...)
		case 2:
			out[pos] = ttftMutationAlphabet[rng.Intn(len(ttftMutationAlphabet))]
		default:
			// Duplicate a slice to create repeated keys and members.
			end := pos + rng.Intn(24)
			if end > len(out) {
				end = len(out)
			}
			chunk := append([]byte(nil), out[pos:end]...)
			out = append(out[:end], append(chunk, out[end:]...)...)
		}
	}
	return out
}

func ttftDiffCorpus(t *testing.T, mutations int) [][]byte {
	t.Helper()
	rng := rand.New(rand.NewSource(20261103))
	var corpus [][]byte
	for _, seed := range ttftDiffSeeds() {
		corpus = append(corpus, []byte(seed))
		for i := 0; i < mutations; i++ {
			corpus = append(corpus, mutateJSONish(rng, []byte(seed)))
		}
	}
	return corpus
}

func TestToolSchemaSinglePassMatchesLegacy(t *testing.T) {
	platforms := []string{PlatformOpenAI, PlatformAnthropic, PlatformGrok, PlatformDeepseek, PlatformGemini}
	singlePassHits := 0
	for i, body := range ttftDiffCorpus(t, 400) {
		for _, platform := range platforms {
			wantOut, wantChanged, wantErr := legacyTS_sanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
			gotOut, gotChanged, gotErr := sanitizeOpenAIResponsesToolSchemasForPlatform(body, platform)
			require.Equal(t, errString(wantErr), errString(gotErr), "case %d platform %s body %q", i, platform, body)
			require.Equal(t, wantChanged, gotChanged, "case %d platform %s body %q", i, platform, body)
			require.True(t, bytes.Equal(wantOut, gotOut), "case %d platform %s body %q\nwant %q\ngot  %q", i, platform, body, wantOut, gotOut)
			if _, _, ok := sanitizeOpenAIResponsesToolSchemasSinglePass(body, platform); ok {
				singlePassHits++
			}
		}
		for _, fn := range []struct {
			name   string
			legacy func([]byte) ([]byte, bool, error)
			fast   func([]byte) ([]byte, bool, error)
		}{
			{"params", legacyTS_sanitizeOpenAIResponsesToolParameterTypes, sanitizeOpenAIResponsesToolParameterTypes},
			{"patterns", legacyTS_sanitizeOpenAIResponsesToolSchemaPatterns, sanitizeOpenAIResponsesToolSchemaPatterns},
		} {
			wantOut, wantChanged, wantErr := fn.legacy(body)
			gotOut, gotChanged, gotErr := fn.fast(body)
			require.Equal(t, errString(wantErr), errString(gotErr), "%s case %d body %q", fn.name, i, body)
			require.Equal(t, wantChanged, gotChanged, "%s case %d", fn.name, i)
			require.True(t, bytes.Equal(wantOut, gotOut), "%s case %d body %q", fn.name, i, body)
		}
	}
	require.Greater(t, singlePassHits, 100, "the single-pass path must actually be exercised")
}

func TestImageIntentDeferredValidityMatchesLegacy(t *testing.T) {
	for i, body := range ttftDiffCorpus(t, 400) {
		for _, model := range []string{"gpt-5.4", "gpt-image-2", ""} {
			require.Equal(t, legacyII_IsImageGenerationIntent("/v1/responses", model, body), IsImageGenerationIntent("/v1/responses", model, body), "case %d %q", i, body)
			require.Equal(t, legacyII_IsExplicitImageGenerationIntent("/v1/responses", model, body), IsExplicitImageGenerationIntent("/v1/responses", model, body), "case %d %q", i, body)
			require.Equal(t, legacyII_isExplicitGrokImageGenerationIntent("/v1/responses", model, body), isExplicitGrokImageGenerationIntent("/v1/responses", model, body), "case %d %q", i, body)
		}
		require.Equal(t, legacyII_openAIRequestBodyImageGenerationToolNeedsNormalization(body), openAIRequestBodyImageGenerationToolNeedsNormalization(body), "case %d %q", i, body)
	}
}

func TestExecutionScopeMetadataMissMatchesLegacy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	headerSets := []map[string]string{
		nil,
		{"thread-id": "hdr-thread"},
		{"x-codex-turn-metadata": `{"thread_id":"tm","request_kind":"memory"}`},
		{"x-codex-window-id": "win:1"},
		{"x-openai-subagent": "guardian"},
		{"session_id": "sess-1"},
	}
	for i, body := range ttftDiffCorpus(t, 400) {
		for _, headers := range headerSets {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			for k, v := range headers {
				c.Request.Header.Set(k, v)
			}
			wantScope, wantThread := legacyES_resolveOpenAIWSExecutionScope(c, body, 7)
			gotScope, gotThread := resolveOpenAIWSExecutionScope(c, body, 7)
			require.Equal(t, wantScope, gotScope, "case %d headers %v body %q", i, headers, body)
			require.Equal(t, wantThread, gotThread, "case %d headers %v body %q", i, headers, body)
		}
	}
}

type ttftNamedString string

type ttftStruct struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func legacyInputContainsText(input []any, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, item := range input {
		b, err := json.Marshal(item)
		if err == nil && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

func TestInputContainsTextSkipsOnlyProvablyEscapedItems(t *testing.T) {
	// The fast path relies on json.Marshal escaping HTML characters.
	raw, err := json.Marshal(map[string]any{"<k&>": "<v>"})
	require.NoError(t, err)
	require.False(t, bytes.ContainsAny(raw, "<>&"))

	marker := openAICompatClaudeCodeTodoGuardMarker
	inputs := [][]any{
		{map[string]any{"type": "message", "content": marker}},
		{map[string]any{"content": []any{map[string]any{"text": "x " + marker}}}, json.Number("12")},
		{json.RawMessage(`{"text":"` + marker + `"}`)},
		{map[string]any{"content": json.RawMessage(`"` + marker + `"`)}},
		{map[string]any{"content": ttftNamedString(marker)}},
		{ttftStruct{Type: "message", Text: marker}},
		{map[string]string{"text": marker}},
		{[]string{marker}},
		{nil, true, 1.5, "plain", []any{"x"}},
	}
	for i, input := range inputs {
		for _, needle := range []string{marker, "plain", "<", "message", " "} {
			require.Equal(t, legacyInputContainsText(input, needle), inputContainsText(input, needle), "input %d needle %q", i, needle)
		}
	}
	for i, body := range ttftDiffCorpus(t, 200) {
		var payload map[string]any
		if decodeOpenAIJSONUseNumber(body, &payload) != nil {
			continue
		}
		input, _ := payload["input"].([]any)
		require.Equal(t, legacyInputContainsText(input, marker), inputContainsText(input, marker), "case %d", i)
	}
}

func TestCompactionTriggerMapDecisionImpliesUnchangedBytes(t *testing.T) {
	trigger := map[string]any{"type": "compaction_trigger"}
	maps := []map[string]any{
		{"input": []any{map[string]any{"type": "message"}, trigger}},
		{"input": []any{trigger, map[string]any{"type": "message"}}},
		{"input": []any{trigger, trigger}},
		{"input": []any{map[string]any{"type": ttftNamedString("compaction_trigger")}, map[string]any{"type": "message"}}},
		{"input": []any{ttftStruct{Type: "compaction_trigger"}, "x"}},
		{"input": []any{map[string]string{"type": "compaction_trigger"}, "x"}},
		{"input": []map[string]any{trigger, {"type": "message"}}},
		{"input": json.RawMessage(`[{"type":"compaction_trigger"},1]`)},
		{"input": "text"},
		{"input": []any{json.Number("1"), nil, true, 2.5, []any{trigger}}},
		{"other": 1},
	}
	for _, body := range ttftDiffCorpus(t, 200) {
		var payload map[string]any
		if decodeOpenAIJSONUseNumber(body, &payload) == nil && payload != nil {
			maps = append(maps, payload)
		}
	}
	for i, m := range maps {
		encoded, err := marshalOpenAIUpstreamJSON(m)
		if err != nil {
			continue
		}
		out, changed, err := NormalizeCompactionTriggerInputOrder(encoded)
		if !compactionTriggerOrderMayChange(m) {
			require.NoError(t, err, "map %d", i)
			require.False(t, changed, "map %d", i)
			require.True(t, bytes.Equal(encoded, out), "map %d", i)
		}
		if changed {
			require.True(t, compactionTriggerOrderMayChange(m), "map %d %s", i, fmt.Sprint(m))
		}
	}
}
