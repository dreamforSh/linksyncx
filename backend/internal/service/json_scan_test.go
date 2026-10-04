//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// jsonBodyIndexTestPaths 覆盖单段、两段、数组下标与多级路径。
var jsonBodyIndexTestPaths = []string{
	"model", "stream", "metadata", "metadata.user_id", "thinking.type", "output_config.effort", "speed", "max_tokens",
	"system", "system.0", "system.0.text", "system.1.text", "messages", "messages.0.content", "input",
	"systemInstruction.parts", "contents", "tools", "tools.0.cache_control", "a.b", "a.b.c", "0", "x-y", "missing", "missing.sub",
}

func requireSameGJSONResult(t *testing.T, want, got gjson.Result, path, data string) {
	t.Helper()
	require.Equal(t, want.Type, got.Type, "%s in %.200q", path, data)
	require.Equal(t, want.Raw, got.Raw, "%s in %.200q", path, data)
	require.Equal(t, want.Str, got.Str, "%s in %.200q", path, data)
	require.Equal(t, want.Index, got.Index, "%s in %.200q", path, data)
	if want.Type == gjson.Number {
		require.Equal(t, want.Num, got.Num, "%s in %.200q", path, data)
	}
}

// requireJSONBodyIndexMatchesGJSON 断言：合法 JSON 上带索引的查找与 gjson.GetBytes 完全一致（含 Index）。
func requireJSONBodyIndexMatchesGJSON(t *testing.T, data string) {
	t.Helper()
	if !gjson.Valid(data) {
		return
	}
	body := []byte(data)
	view := newJSONBodyView(body, newJSONBodyIndex(body))
	for _, path := range jsonBodyIndexTestPaths {
		requireSameGJSONResult(t, gjson.GetBytes(body, path), view.get(path), path, data)
	}
}

// requireJSONBodySetMatchesSJSON 断言：带索引的 setString 与 sjson.SetBytes 逐字节相同（含错误）；
// 仍持有索引时新文档合法，且索引与重新建立的完全一致。
func requireJSONBodySetMatchesSJSON(t *testing.T, data, path, value string) {
	t.Helper()
	if !gjson.Valid(data) {
		return
	}
	want, wantErr := sjson.SetBytes([]byte(data), path, value)
	body := []byte(data)
	view := newJSONBodyView(body, newJSONBodyIndex(body))
	gotErr := view.setString(path, value)
	require.Equal(t, wantErr != nil, gotErr != nil, "%s=%q in %.200q", path, value, data)
	if wantErr != nil {
		require.Equal(t, data, string(view.data))
		return
	}
	require.Equal(t, string(want), string(view.data), "%s=%q in %.200q", path, value, data)
	if view.idx != nil {
		require.True(t, sameByteSlice(view.idx.body, view.data))
		require.True(t, gjson.ValidBytes(view.data), "%.200q", view.data)
		require.Equal(t, newJSONBodyIndex(view.data).members, view.idx.members)
	}
}

func jsonBodyIndexCases() []string {
	esc := jsonUnicodeEscape
	return []string{
		`{"model":"a","model":"b","messages":[1,{"model":"nested"}],"stream":true}`,
		`{"messages":[{"role":"user","content":"x"}],"system":"s","metadata":{"user_id":"u"},"max_tokens":12}`,
		`{"metadata":"x","metadata":{"other":1},"metadata":{"user_id":"u"}}`,
		`{"metadata":[{"user_id":"in-array"}],"metadata":{"user_id":"obj"}}`,
		`{"metadata":{"user_id":null},"metadata":{"user_id":"later"}}`,
		`{"metadata":{"user_id":"u","user_id":"v"},"metadata":{"user_id":"w"}}`,
		`{"thinking":{"type":"a","type":"b"},"thinking":{"type":"c"}}`,
		`{"mod` + esc("0065") + `l":"esc","metad` + esc("0061") + `ta":{"user_` + esc("0069") + `d":"u2"}}`,
		` ` + "\n\t" + `{ "messages" : [1,2] , "system":[{"type":"text","text":"x"}]}` + "\n",
		`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=1.2.3; cc_entrypoint=cli;"},{"text":"b"}],` +
			`"tools":[{"name":"t","cache_control":{"type":"ephemeral"}}],"metadata":{"user_id":"u"}}`,
		`{"system":{"0":{"text":"obj"}},"tools":{"0":1}}`,
		`{"systemInstruction":{"parts":"x"},"systemInstruction":{"parts":[1]},"contents":[]}`,
		`{"max_tokens":1e400,"speed":null,"input":{"a":[1,2,{"b":"c"}]},"x-y":-0.5,"0":true}`,
		`{"a":{"b":{"c":1}},"a":{"b":2}}`, `{"a":[{"b":1}],"a":{"b":{"c":"d"}}}`,
		`{"model.user_id":"literal dot","model":{"user_id":"x"}}`,
		`{"metadata":{}}`, `{"metadata":{},"metadata":{"user_id":"late"}}`,
		`[{"model":"x"}]`, `"{\"model\":\"x\"}"`, `1`, `null`, `{}`, `[]`,
	}
}

// jsonBodySetTestValues 覆盖 sjson 的两种字符串编码：只有控制字符、非 ASCII、引号、反斜杠
// 才走 encoding/json（此时 <>& 也会被转义），否则原样加引号。
var jsonBodySetTestValues = []string{"plain", "with \"quote\"", "中文", "<b>&</b>", "&amp; first", "x>y&\"q\"", "", "\x7f", "a\nb", "\xff", "back\\slash"}

func TestJSONBodyIndex_MatchesGJSONAndSJSON(t *testing.T) {
	for _, data := range jsonBodyIndexCases() {
		requireJSONBodyIndexMatchesGJSON(t, data)
		for _, path := range jsonBodyIndexTestPaths {
			for _, value := range jsonBodySetTestValues {
				requireJSONBodySetMatchesSJSON(t, data, path, value)
			}
		}
	}
}

func FuzzJSONBodyIndex(f *testing.F) {
	for i, data := range jsonBodyIndexCases() {
		f.Add(data, uint8(i), jsonBodySetTestValues[i%len(jsonBodySetTestValues)])
	}
	f.Fuzz(func(t *testing.T, data string, pathIndex uint8, value string) {
		requireJSONBodyIndexMatchesGJSON(t, data)
		requireJSONBodySetMatchesSJSON(t, data, jsonBodyIndexTestPaths[int(pathIndex)%len(jsonBodyIndexTestPaths)], value)
	})
}

func TestSplitPlainJSONPath(t *testing.T) {
	for _, path := range []string{"", "a..b", "a.", ".a", "a*", "a|b", "a\\.b", "#", "@this", "a b", "a:b"} {
		_, _, ok := splitPlainJSONPath(path)
		require.False(t, ok, path)
	}
	key, rest, ok := splitPlainJSONPath("system.0.text")
	require.True(t, ok)
	require.Equal(t, "system", key)
	require.Equal(t, "0.text", rest)
	key, rest, ok = splitPlainJSONPath("x-y_1")
	require.True(t, ok)
	require.Equal(t, "x-y_1", key)
	require.Empty(t, rest)
}

func TestScanGatewayRequestFields_MatchesGJSON(t *testing.T) {
	names := []string{"model", "stream", "metadata.user_id", "thinking.type", "output_config.effort", "speed", "max_tokens",
		"system", "messages", "input", "systemInstruction.parts", "contents"}
	for _, data := range append(jsonBodyIndexCases(),
		`{"model":"m","stream":true,"metadata":{"user_id":"u"},"thinking":{"type":"t"},"output_config":{"effort":"e"},`+
			`"speed":"s","max_tokens":1,"system":"sys","messages":[],"input":"in","systemInstruction":{"parts":[]},"contents":[0]}`) {
		if !gjson.Valid(data) {
			continue
		}
		fields := scanGatewayRequestFields(newJSONBodyIndex([]byte(data)))
		got := []gjson.Result{fields.model, fields.stream, fields.metadataUserID, fields.thinkingType, fields.outputEffort, fields.speed,
			fields.maxTokens, fields.system, fields.messages, fields.input, fields.geminiSystemParts, fields.geminiContents}
		for i, name := range names {
			requireSameGJSONResult(t, gjson.Get(data, name), got[i], name, data)
		}
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
	return append(jsonBodyIndexCases(),
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
	need := thinkingBlocksNeedFiltering(body, alwaysThinking, nil)
	if gjson.ValidBytes(body) {
		require.Equal(t, need, thinkingBlocksNeedFiltering(body, alwaysThinking, newJSONBodyIndex(body)), "indexed decision differs: %.300q", body)
	}
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

// 索引只描述建立它的那份字节：内容相同但底层数组不同、或另一份请求体，都不得使用。
func TestJSONBodyView_IgnoresIndexOfOtherBody(t *testing.T) {
	a := []byte(`{"model":"a","metadata":{"user_id":"u"}}`)
	b := []byte(`{"metadata":{"user_id":"v"},"model":"b"}`)
	view := newJSONBodyView(b, newJSONBodyIndex(a))
	require.Nil(t, view.idx)
	require.Equal(t, "v", view.get("metadata.user_id").String())
	require.Nil(t, newJSONBodyView(append([]byte(nil), a...), newJSONBodyIndex(a)).idx)
}

func TestParseGatewayRequestBody_IgnoresIndexOfOtherBody(t *testing.T) {
	valid := []byte(`{"model":"m"}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef([]byte(`{"model":`))}
	require.Error(t, parseGatewayRequestBody(parsed, PlatformAnthropic, newJSONBodyIndex(valid)))
}
