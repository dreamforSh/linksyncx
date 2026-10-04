//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var jsonTopLevelPathsTestNames = []string{
	"model", "stream", "metadata.user_id", "thinking.type", "output_config.effort", "speed", "max_tokens",
	"system", "messages", "input", "systemInstruction.parts", "contents",
}

// requireJSONTopLevelPathsMatchGet 断言：合法 JSON 上 jsonTopLevelPaths 与逐个 gjson.Get 完全一致（含 Index）。
func requireJSONTopLevelPathsMatchGet(t *testing.T, data string) {
	t.Helper()
	if !gjson.Valid(data) {
		return
	}
	paths := newJSONTopLevelPaths(jsonTopLevelPathsTestNames...)
	got := make([]gjson.Result, len(paths))
	jsonTopLevelPaths(data, paths, got)
	for i, name := range jsonTopLevelPathsTestNames {
		want := gjson.Get(data, name)
		require.Equal(t, want.Type, got[i].Type, "%s in %.200q", name, data)
		require.Equal(t, want.Raw, got[i].Raw, "%s in %.200q", name, data)
		require.Equal(t, want.Str, got[i].Str, "%s in %.200q", name, data)
		require.Equal(t, want.Index, got[i].Index, "%s in %.200q", name, data)
		if want.Type == gjson.Number {
			require.Equal(t, want.Num, got[i].Num, "%s in %.200q", name, data)
		}
	}
}

func jsonTopLevelPathsCases() []string {
	esc := jsonUnicodeEscape
	return []string{
		`{"model":"a","model":"b","messages":[1,{"model":"nested"}],"stream":true}`,
		`{"messages":[{"role":"user","content":"x"}],"system":"s","metadata":{"user_id":"u"},"max_tokens":12}`,
		`{"metadata":"x","metadata":{"other":1},"metadata":{"user_id":"u"}}`,
		`{"metadata":[{"user_id":"in-array"}],"metadata":{"user_id":"obj"}}`,
		`{"metadata":{"user_id":null},"metadata":{"user_id":"later"}}`,
		`{"thinking":{"type":"a","type":"b"},"thinking":{"type":"c"}}`,
		`{"mod` + esc("0065") + `l":"esc","metad` + esc("0061") + `ta":{"user_` + esc("0069") + `d":"u2"}}`,
		` ` + "\n\t" + `{"messages":[1,2],"system":[{"type":"text","text":"x"}]}` + "\n",
		`{"systemInstruction":{"parts":"x"},"systemInstruction":{"parts":[1]},"contents":[]}`,
		`{"max_tokens":1e400,"speed":null,"input":{"a":[1,2,{"b":"c"}]}}`,
		`{"model.user_id":"literal dot","model":{"user_id":"x"}}`,
		`{"metadata":{}}`, `{"metadata":{},"metadata":{"user_id":"late"}}`,
		`[{"model":"x"}]`, `"{\"model\":\"x\"}"`, `1`, `null`, `{}`, `[]`,
	}
}

func TestJSONTopLevelPaths_MatchesGJSONGet(t *testing.T) {
	for _, data := range jsonTopLevelPathsCases() {
		requireJSONTopLevelPathsMatchGet(t, data)
	}
}

func FuzzJSONTopLevelPaths(f *testing.F) {
	for _, data := range jsonTopLevelPathsCases() {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data string) {
		requireJSONTopLevelPathsMatchGet(t, data)
	})
}

func TestNewJSONTopLevelPaths_RejectsAmbiguousPaths(t *testing.T) {
	for _, path := range []string{"", "a.b.c", "a.0", "0", "a*", "a|b", "a\\.b", "#", "a.", ".a"} {
		require.Panics(t, func() { newJSONTopLevelPaths(path) }, path)
	}
	require.NotPanics(t, func() { newJSONTopLevelPaths("model", "metadata.user_id", "output_config.effort", "a-b") })
}

func TestScanGatewayRequestFields_PathOrder(t *testing.T) {
	data := `{"model":"m","stream":true,"metadata":{"user_id":"u"},"thinking":{"type":"t"},"output_config":{"effort":"e"},` +
		`"speed":"s","max_tokens":1,"system":"sys","messages":[],"input":"in","systemInstruction":{"parts":[]},"contents":[0]}`
	fields := scanGatewayRequestFields(data)
	got := []gjson.Result{fields.model, fields.stream, fields.metadataUserID, fields.thinkingType, fields.outputEffort, fields.speed,
		fields.maxTokens, fields.system, fields.messages, fields.input, fields.geminiSystemParts, fields.geminiContents}
	require.Len(t, gatewayRequestFieldPaths, len(got))
	for i, name := range jsonTopLevelPathsTestNames {
		require.Equal(t, gjson.Get(data, name).Raw, got[i].Raw, name)
	}
}

type jsonMemberRaw struct{ key, raw string }

// requireForEachJSONMemberRawMatchesGJSON 断言：合法 JSON 对象上 forEachJSONMemberRaw 给出的值
// 范围与 gjson ForEach 一致、键名与 encoding/json 解码一致（逐层递归检查）；非法输入不 panic。
func requireForEachJSONMemberRawMatchesGJSON(t *testing.T, data string) {
	t.Helper()
	forEachJSONMemberRaw(data, func(string, string) bool { return true })
	if !gjson.Valid(data) {
		return
	}
	root, ok := jsonRootObject(data)
	if !ok {
		return
	}
	var check func(obj gjson.Result)
	check = func(obj gjson.Result) {
		var want, got []jsonMemberRaw
		obj.ForEach(func(key, value gjson.Result) bool {
			var decoded string
			require.NoError(t, json.Unmarshal([]byte(key.Raw), &decoded))
			want = append(want, jsonMemberRaw{decoded, value.Raw})
			return true
		})
		forEachJSONMemberRaw(obj.Raw, func(key, raw string) bool {
			got = append(got, jsonMemberRaw{key, raw})
			return true
		})
		require.Equal(t, want, got, "%.200q", obj.Raw)
		obj.ForEach(func(_, value gjson.Result) bool {
			if value.IsObject() {
				check(value)
			}
			return true
		})
	}
	check(root)
}

func forEachJSONMemberRawCases() []string {
	esc := jsonUnicodeEscape
	return append(jsonTopLevelPathsCases(),
		`{"a":"x\\\"y\\\\","b":{"c":[1,"]}",{"d":"}"}]},"e":-1.5e3,"f":true,"g":null}`,
		`{"k`+esc("d800", "0041")+`":1,"t`+esc("0079")+`pe":"v","`+"\xff"+`":2}`,
		`{ "spaced" : [ 1 , 2 ] , "x" : { } }`,
		`{"a":1`, `{"a":"unterminated}`, `{"a":[1,2}`, `{"a"`, `{`, `}`, ``,
	)
}

func TestForEachJSONMemberRaw_MatchesGJSON(t *testing.T) {
	for _, data := range forEachJSONMemberRawCases() {
		requireForEachJSONMemberRawMatchesGJSON(t, data)
	}
}

func FuzzForEachJSONMemberRaw(f *testing.F) {
	for _, data := range forEachJSONMemberRawCases() {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data string) {
		requireForEachJSONMemberRawMatchesGJSON(t, data)
	})
}

// thinkingBlocksNeedFiltering 必须与完整解码的结论一致：完整解码会改动请求体时必须返回
// true；能成功解码且返回 true 时，完整解码也必须真的改动。
func requireThinkingFilterDecisionExact(t *testing.T, body []byte, alwaysThinking bool) {
	t.Helper()
	need := thinkingBlocksNeedFiltering(body, alwaysThinking)
	out := filterThinkingBlocksDecoded(body, alwaysThinking)
	changed := len(out) != len(body) || (len(body) > 0 && !sameByteSlice(out, body))
	if changed {
		require.True(t, need, "decision missed a filtered block: %.300q", body)
	}
	var decoded map[string]any
	if need && json.Unmarshal(body, &decoded) == nil {
		require.True(t, changed, "decision reported a block the decoder keeps: %.300q", body)
	}
}

func thinkingFilterDecisionCases() []string {
	esc := jsonUnicodeEscape
	return []string{
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"text","text":"x"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":""}]}]}`,
		`{"thinking":{"type":"enabled"},"thinking":{"budget_tokens":1},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"}]}]}`,
		`{"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":[{"type":"thinking","thinking":"t","signature":"sig"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","role":"user","content":[{"type":"thinking","signature":"sig"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"thinking":"typeless"},{"type":1,"thinking":"x"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"` + esc("d800") + `"}]}]}`,
		`{"thinking":{"type":"en` + esc("0061") + `bled"},"messages":[{"role":"assist` + esc("0061") + `nt","content":[{"typ` + esc("0065") + `":"thinking","signature":"s"}]}]}`,
		"{\"thinking\":{\"type\":\"enabled\"},\"messages\":[{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\xff\",\"signature\":\"\"}]}]}",
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"s"}]}],"v":1e400}`,
		`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"}]}],"messages":"x"}`,
		`[{"type":"thinking"}]`, `null`, ``, `{"messages":[1,"x",null,{"content":"str"}]}`,
		`{"thinking":{"type":"adaptive"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"sig"}]}]}`,
		`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"text","text":"x","thinking":"extra"}]}]}`,
	}
}

func TestThinkingBlocksNeedFiltering_MatchesDecodedFilter(t *testing.T) {
	for _, data := range thinkingFilterDecisionCases() {
		for _, alwaysThinking := range []bool{false, true} {
			requireThinkingFilterDecisionExact(t, []byte(data), alwaysThinking)
		}
	}
}

func FuzzThinkingBlocksNeedFiltering(f *testing.F) {
	for _, data := range thinkingFilterDecisionCases() {
		f.Add([]byte(data), false)
	}
	f.Fuzz(func(t *testing.T, body []byte, alwaysThinking bool) {
		requireThinkingFilterDecisionExact(t, body, alwaysThinking)
	})
}
