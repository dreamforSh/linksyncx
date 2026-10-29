package codebuddy

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// legacyPrepareChatBody is PrepareChatBody as it was before the scan fast
// path and the desensitization prefilter/memo (git 78ed9aa40), kept verbatim
// as the reference the new implementation must match byte for byte.
func legacyPrepareChatBody(body []byte, profile string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, requestErrorf("invalid chat completions body: %v", err)
	}
	for key := range root {
		if _, ok := allowedBodyKeys[key]; !ok {
			delete(root, key)
		}
	}
	var model string
	_ = json.Unmarshal(root["model"], &model)
	if model = strings.TrimSpace(model); model == "" {
		model = "auto"
	}

	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(root["messages"], &messages); err != nil || len(messages) == 0 {
		return nil, requestErrorf("messages must be a non-empty array of objects")
	}
	for _, message := range messages {
		if message == nil {
			return nil, requestErrorf("messages must be a non-empty array of objects")
		}
		if rawRole(message) == "developer" {
			message["role"] = mustRaw("system")
		}
	}
	messages = leadWithSystem(messages)
	if IsInternational(profile) {
		merged, err := mergeIntlUserImages(messages)
		if err != nil {
			return nil, err
		}
		messages = merged
	}
	legacyDesensitizeSystemMessages(messages)
	if IsDeepSeekModel(model) {
		if err := applyDeepSeekCompat(root, messages); err != nil {
			return nil, err
		}
	}
	encodedMessages, err := marshalNoEscape(messages)
	if err != nil {
		return nil, err
	}
	root["messages"] = encodedMessages

	if err := normalizeToolChoice(root); err != nil {
		return nil, err
	}
	if raw, ok := root["n"]; ok {
		var n float64
		if json.Unmarshal(raw, &n) == nil && n != 1 {
			return nil, requestErrorf("n must be 1")
		}
	}
	root["model"] = mustRaw(UpstreamModel(model, profile))
	root["stream"] = json.RawMessage("true")
	root["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	return marshalNoEscape(root)
}

func legacyDesensitizeSystemMessages(messages []map[string]json.RawMessage) {
	for _, message := range messages {
		if rawRole(message) != "system" {
			continue
		}
		content, ok := message["content"]
		if !ok {
			continue
		}
		if updated, changed := legacyDesensitizeContent(content); changed {
			message["content"] = updated
		}
	}
}

func legacyDesensitizeContent(raw json.RawMessage) (json.RawMessage, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		desensitized := legacyDesensitizeText(text)
		if desensitized == text {
			return raw, false
		}
		encoded, err := marshalNoEscape(desensitized)
		if err != nil {
			return raw, false
		}
		return encoded, true
	}

	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return raw, false
	}
	changed := false
	for _, part := range parts {
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		if partType != "text" {
			continue
		}
		var partText string
		if json.Unmarshal(part["text"], &partText) != nil {
			continue
		}
		desensitized := legacyDesensitizeText(partText)
		if desensitized == partText {
			continue
		}
		encoded, err := marshalNoEscape(desensitized)
		if err != nil {
			continue
		}
		part["text"] = encoded
		changed = true
	}
	if !changed {
		return raw, false
	}
	encoded, err := marshalNoEscape(parts)
	if err != nil {
		return raw, false
	}
	return encoded, true
}

func legacyDesensitizeText(text string) string {
	if text == "" {
		return text
	}
	if loc := gitStatusContextRE.FindStringIndex(text); loc != nil {
		return legacyDesensitizeText(text[:loc[0]]) + replaceGitStatusText(text[loc[0]:], false)
	}
	for _, tr := range templateReplacements {
		text = tr.pattern.ReplaceAllLiteralString(text, tr.target)
	}
	return sensitiveTermRE.ReplaceAllStringFunc(text, zeroWidthSplit)
}

var allProfiles = []string{ProfileCNCLI, ProfileCNWork, ProfileIntlCLI, ProfileIntlWork}

// Test bodies spell JSON escapes as ~uXXXX (see esc) and build special runes
// at run time, so no tool or editor that decodes backslash-u sequences can
// silently turn an escape into the character it names.
var (
	lineSep = string(rune(0x2028))
	longS   = string(rune(0x17f))
	kelvin  = string(rune(0x212a))
)

// esc turns each ~uXXXX into the JSON escape backslash-u-XXXX.
func esc(s string) string { return strings.ReplaceAll(s, "~u", `\`+"u") }

// requirePrepareMatchesLegacy runs body through both implementations, twice
// for the new one so the second run goes through the desensitization memo.
func requirePrepareMatchesLegacy(t testing.TB, body []byte, profile string) {
	t.Helper()
	original := bytes.Clone(body)
	want, wantErr := legacyPrepareChatBody(bytes.Clone(body), profile)
	for run := 0; run < 2; run++ {
		got, gotErr := PrepareChat(body, profile)
		require.Equal(t, original, body, "PrepareChat must not modify its input")
		if wantErr != nil {
			require.Error(t, gotErr, "legacy rejected %q (%v)", body, wantErr)
			require.Equal(t, wantErr.Error(), gotErr.Error(), "body %q", body)
			_, wantRequest := wantErr.(*RequestError)
			_, gotRequest := gotErr.(*RequestError)
			require.Equal(t, wantRequest, gotRequest)
			continue
		}
		require.NoError(t, gotErr, "body %q", body)
		require.Equal(t, string(want), string(got.Body), "profile %s body %q", profile, body)
		require.Equal(t, gjson.GetBytes(got.Body, "model").String(), got.Model)
	}
}

func TestPrepareChatMatchesLegacyCases(t *testing.T) {
	claudeSystem := `You are Claude Code, Anthropic's official CLI for Claude.\n\ngitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time.\nCurrent branch: claude/fix\n\nMain branch (you will usually use this for PRs): main\n\nStatus:\nM Claude.md\n\nRecent commits:\nabc Co-Authored-By: Claude <noreply@anthropic.com>`
	cases := []string{
		`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"glm-5.2","stream":false,"store":true,"metadata":{"a":1},"messages":[{"role":"user","content":"hi"}]}`,
		`{"messages":[{"content":"hi","role":"user"}],"model":"auto"}`,
		`{ "model" : "glm-5.2" , "messages" : [ { "role" : "system" , "content" : "s" } , { "role" : "user" , "content" : [ { "type" : "text" , "text" : "x" } ] } ] , "temperature" : 0.20 , "max_tokens" : 1E+3 }`,
		`{"model":"glm-5.2","messages":[{"role":"developer","content":"d"},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"u"},{"role":"system","content":"late"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"u"},{"role":"developer","content":"late dev"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":"` + claudeSystem + `"},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":[{"type":"text","text":"` + claudeSystem + `","cache_control":{"type":"ephemeral"}},{"type":"text","text":"plain"}]},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":"C` + zwsp + `laude Code is here"},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":"C~u200blaude Code is here"},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":"line~u2028sep"},{"role":"user","content":"u"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"raw ` + lineSep + ` sep"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"system","content":"Claude Code ` + lineSep + `"},{"role":"user","content":"u"}]}`,
		"{\"model\":\"glm-5.2\",\"messages\":[{\"role\":\"user\",\"content\":\"bad \xff utf8\"}]}",
		`{"model":"glm-5.2","messages":[{"role":"user","content":"esc ~u00e9 é ~ud83d~ude00 😀 \/ <>& ~u003c"}]}`,
		`{"model":"glm-5.2","model":"kimi","messages":[{"role":"user","content":"a"}],"messages":[{"role":"user","content":"b"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a","role":"assistant"}]}`,
		`{"mod~u0065l":"glm-5.2","messages":[{"role":"user","content":"a"}]}`,
		`{"model":"glm-5.2","messages":[{"r~u006fle":"user","content":"a"}]}`,
		"{\"model\":\"glm-5.2\",\"messages\":[{\"role\":\"user\",\"content\":\"a\",\"x\xff\":1}]}",
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a","<&>":1,"":2}]}`,
		`{"model":"glm-5.2","messages":[]}`,
		`{"model":"glm-5.2","messages":null}`,
		`{"model":"glm-5.2","messages":{"role":"user"}}`,
		`{"model":"glm-5.2","messages":[null]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"},null]}`,
		`{"model":"glm-5.2","messages":["user"]}`,
		`{"model":"glm-5.2"}`,
		`null`,
		`[]`,
		`"x"`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}]`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a~u12"}]}`,
		"{\"model\":\"glm-5.2\",\"messages\":[{\"role\":\"user\",\"content\":\"ctl\x01\"}]}",
		` {"model":"glm-5.2","messages":[{"role":"user","content":"a"}]} `,
		`{"model":42,"messages":[{"role":"user","content":"a"}]}`,
		`{"model":" auto ","messages":[{"role":"user","content":"a"}]}`,
		`{"model":"deepseek-v4-pro","messages":[{"role":"user","content":"a"}],"reasoning_effort":"low","response_format":{"type":"json_object"}}`,
		`{"model":"deepseek-v4-pro","messages":[{"role":"system","content":[{"type":"text","text":"s"}]},{"role":"user","content":"a"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object" , "required":["a"]}}}}`,
		`{"model":"deepseek-v4-pro","messages":[{"role":"system","content":{"bad":1}},{"role":"user","content":"a"}],"response_format":{"type":"json_object"}}`,
		`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"a"}],"reasoning_effort":"none"}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"tools":[{"type":"function","function":{"name":"f"}},{"type":"function","function":{"name":"g"}}],"tool_choice":{"type":"function","function":{"name":"g"}}}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"function","function":{"name":"missing"}}}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"tool_choice":"auto"}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"n":1}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"n":2}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"n":"2"}`,
		`{"model":"auto","messages":[{"role":"user","content":[{"type":"text","text":"a"}]},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		`{"model":"auto","messages":[{"role":"user","content":"a"},{"role":"user","name":"n","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		`{"model":"auto","messages":[{"role":"user","content":"a"},{"role":"user","content":[{"type":"im~u0061ge_url","image_url":{"url":"data:x"}}]}]}`,
		`{"model":"auto","messages":[{"role":"user","content":"a"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}, "str"]}]}`,
		`{"model":"auto","messages":[{"role":"user","content":"a","name":"x"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}],"name": "x"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c","content":"ok"}]}`,
		`{"model":"glm-5.2","messages":[{"role":7,"content":"a"},{"role":{"x":1},"content":"b"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":[[[[[[[[[[{"deep":[1,2,{"x":null}]}]]]]]]]]]]}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}],"stream_options":[1],"stream":"yes"}`,
	}
	for _, profile := range allProfiles {
		for _, body := range cases {
			requirePrepareMatchesLegacy(t, []byte(esc(body)), profile)
		}
	}
}

// TestPrepareChatScanEngagesForTypicalBodies pins the fast path itself:
// equal output alone would not show that the scan and writer ran.
func TestPrepareChatScanEngagesForTypicalBodies(t *testing.T) {
	for _, body := range []string{
		`{"model":"glm-5.2","messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"x"}]}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"stream":true}`,
		`{ "model": "glm-5.2", "messages": [ {"role": "user", "content": "python style"} ], "temperature": 0.5 }`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"esc ~u00e9 ~ud83d~ude00 \/ <>& and \"quotes\""}]}`,
	} {
		body = esc(body)
		root, messages, ok := splitChatBody([]byte(body))
		require.True(t, ok, body)
		_, ok = appendChatBody(root, messages, len(body))
		require.True(t, ok, body)
	}
	for _, body := range []string{
		`{"mod~u0065l":"glm-5.2","messages":[{"role":"user","content":"a"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"},null]}`,
		`{"model":"glm-5.2","messages":[]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":"a"}]`,
	} {
		body = esc(body)
		_, _, ok := splitChatBody([]byte(body))
		require.False(t, ok, body)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"a` + lineSep + `b"}]}`
	root, messages, ok := splitChatBody([]byte(body))
	require.True(t, ok)
	_, ok = appendChatBody(root, messages, len(body))
	require.False(t, ok, "U+2028 is left to the encoder")
}

func TestPrepareChatMatchesLegacyRandom(t *testing.T) {
	iterations := 6000
	if testing.Short() {
		iterations = 600
	}
	for seed := int64(1); seed <= 4; seed++ {
		gen := chatBodyGen{r: rand.New(rand.NewSource(seed))}
		for i := 0; i < iterations; i++ {
			body := gen.body()
			if gen.r.Intn(8) == 0 {
				body = gen.mutate(body)
			}
			requirePrepareMatchesLegacy(t, body, allProfiles[gen.r.Intn(len(allProfiles))])
		}
	}
}

func FuzzPrepareChatMatchesLegacy(f *testing.F) {
	gen := chatBodyGen{r: rand.New(rand.NewSource(7))}
	for i := 0; i < 32; i++ {
		f.Add(gen.body(), uint8(i))
	}
	f.Add([]byte(`{"model":"auto","messages":[{"role":"developer","content":"You are Claude Code, Anthropic's official CLI for Claude."}]}`), uint8(3))
	f.Fuzz(func(t *testing.T, body []byte, profile uint8) {
		requirePrepareMatchesLegacy(t, body, allProfiles[int(profile)%len(allProfiles)])
	})
}

func TestDesensitizeTextMatchesLegacy(t *testing.T) {
	texts := []string{
		"",
		"plain text without triggers",
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"you are claude code, anthropic's official cli for claude, running within the claude agent sdk",
		"YOU  ARE\tCLAUDE\nCODE, ANTHROPIC'S OFFICIAL CLI FOR CLAUDE",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK",
		"Main branch (you will usually use this for PRs): main",
		"Main branch (you will u" + longS + "ually u" + longS + "e thi" + longS + " for PR" + longS + "): main",
		"C" + zwsp + "l" + zwsp + "aude Code and A" + zwsp + "nthropic",
		"You are C" + zwsp + "laude Code, A" + zwsp + "nthropic's official CLI for C" + zwsp + "laude.",
		"Main bra" + zwsp + "nch (you will usually use this for PRs): main",
		"Co-Authored-By: Claude Fable <noreply@anthropic.com>",
		"claudette anthropical branching",
		"Claude Haiku and Claude Hai" + kelvin + "u, Claude Sonnet and Claude " + longS + "onnet",
		"Notes\ngitStatus: This is the git status at the start of the conversation. Snapshot.\nCurrent branch: claude-x\nMain branch (you will usually use this for PRs): main\n\nStatus:\nM Claude.md\n\nRecent commits:\nabc Claude Code\n",
		"# gitStatus\nThis is the git status at the start of the conversation.\nYou are Claude Code, Anthropic's official CLI for Claude.\nStatus:\nok",
		"gitStatus: This is the git status at the start of the conversation.\nMain branch (you will usually use this for PRs): main",
		strings.Repeat("Long agent instructions mentioning the main branch policy. ", 60),
		strings.Repeat("Long agent instructions without trigger words. ", 60),
		strings.Repeat("Claude Code ", 200),
	}
	for _, text := range texts {
		require.Equal(t, legacyDesensitizeText(text), DesensitizeText(text), "%q", text)
		require.Equal(t, legacyDesensitizeText(text), DesensitizeText(text), "memo hit %q", text)
	}
	gen := chatBodyGen{r: rand.New(rand.NewSource(11))}
	for i := 0; i < 4000; i++ {
		text := gen.text(1 + gen.r.Intn(12))
		if gen.r.Intn(4) == 0 {
			text = strings.Repeat(text, 1+gen.r.Intn(80))
		}
		require.Equal(t, legacyDesensitizeText(text), DesensitizeText(text), "%q", text)
	}
}

// TestDesensitizeLiteralFoldOrbits backs the prefilter: under (?i) each letter
// of a literal must match only its two ASCII cases.
func TestDesensitizeLiteralFoldOrbits(t *testing.T) {
	literals := append(append([]string{gitStatusLiteral}, desensitizeLiterals...), sensitiveTermLiterals...)
	for _, tr := range templateReplacements {
		literals = append(literals, tr.literal)
	}
	for _, literal := range literals {
		require.Equal(t, strings.ToLower(literal), literal)
		for _, r := range literal {
			for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
				require.Less(t, f, rune(0x80), "%q folds to %q", r, f)
			}
		}
	}
}

func TestDesensitizeMemoIsBounded(t *testing.T) {
	var memo desensitizeMemo
	for i := 0; i < desensitizeMemoMaxEntries+40; i++ {
		text := strconv.Itoa(i) + strings.Repeat("x", 2000)
		memo.put(text, text)
	}
	require.Equal(t, desensitizeMemoMaxEntries, memo.lru.Len())
	require.Len(t, memo.entries, desensitizeMemoMaxEntries)
	_, ok := memo.get("0" + strings.Repeat("x", 2000))
	require.False(t, ok, "oldest entries are evicted")
	last := strconv.Itoa(desensitizeMemoMaxEntries+39) + strings.Repeat("x", 2000)
	out, ok := memo.get(last)
	require.True(t, ok)
	require.Equal(t, last, out)

	huge := strings.Repeat("y", desensitizeMemoMaxBytes/8+1)
	memo.put(huge, huge)
	_, ok = memo.get(huge)
	require.False(t, ok, "one huge text must not flush the memo")

	var total int
	for element := memo.lru.Front(); element != nil; element = element.Next() {
		entry, ok := element.Value.(*desensitizeMemoEntry)
		require.True(t, ok)
		total += entry.cost
	}
	require.Equal(t, total, memo.bytes)
}

func TestDesensitizeTextConcurrent(t *testing.T) {
	text := "You are Claude Code, Anthropic's official CLI for Claude.\n" + strings.Repeat("Use the main branch. ", 200)
	want := legacyDesensitizeText(text)
	require.Equal(t, want, DesensitizeText(text))
	remembered, ok := sharedDesensitizeMemo.get(text)
	require.True(t, ok, "a long text that needed the regex passes is remembered")
	require.Equal(t, want, remembered)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if got := DesensitizeText(text); got != want {
					t.Errorf("concurrent DesensitizeText differs")
					return
				}
			}
		}()
	}
	wg.Wait()
}

// chatBodyGen builds random Chat Completions bodies that exercise every
// branch of PrepareChat: key order, whitespace, duplicates, escapes, roles,
// content shapes, DeepSeek and tool_choice rewrites, desensitization triggers.
type chatBodyGen struct{ r *rand.Rand }

var genTextPieces = func() []string {
	pieces := []string{
		"hello", " ", "world", `\n`, `\"`, `\\`, `\/`, `\t`, `~u00e9`, "é", `~ud83d~ude00`, "😀", "<>&", `~u003c`,
		`~u001b[0m`, "Claude Code", "claude", "Anthropic's", "ANTHROPIC", "Co-Authored-By", "noreply@anthropic.com",
		"You are Claude Code, Anthropic's official CLI for Claude", ", running within the Claude Agent SDK",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK", "Main branch (you will usually use this for PRs)",
		`\ngitStatus: This is the git status at the start of the conversation.\n`, `\nStatus:\n`, `\nRecent commits:\n`,
		"Claude Haiku", "Hai" + kelvin + "u", longS + "onnet", zwsp, `~u200b`, "branch", " of the conver", `~u0043laude`,
		"You are C" + zwsp + "laude Code, A" + zwsp + "nthropic's official CLI for C" + zwsp + "laude",
		"Main bra" + zwsp + "nch (you will usually use this for PRs)",
	}
	for i, piece := range pieces {
		pieces[i] = esc(piece)
	}
	return pieces
}()

func (g *chatBodyGen) ws() string {
	switch g.r.Intn(10) {
	case 0:
		return " "
	case 1:
		return "\n  "
	case 2:
		return "\t"
	}
	return ""
}

func (g *chatBodyGen) text(pieces int) string {
	var b strings.Builder
	for i := 0; i < pieces; i++ {
		_, _ = b.WriteString(genTextPieces[g.r.Intn(len(genTextPieces))])
	}
	return b.String()
}

func (g *chatBodyGen) str() string {
	s := `"` + g.text(g.r.Intn(6)) + `"`
	switch g.r.Intn(60) {
	case 0:
		s = `"raw` + lineSep + `sep"`
	case 1:
		s = "\"bad\xffbyte\""
	}
	return s
}

func (g *chatBodyGen) key(name string) string {
	switch g.r.Intn(40) {
	case 0:
		return `"` + strings.Replace(name, "e", esc("~u0065"), 1) + `"`
	case 1:
		return "\"" + name + "\xc3\xa9\""
	}
	return `"` + name + `"`
}

func (g *chatBodyGen) object(members []string) string {
	g.r.Shuffle(len(members), func(i, j int) { members[i], members[j] = members[j], members[i] })
	if len(members) > 0 && g.r.Intn(12) == 0 {
		members = append(members, members[g.r.Intn(len(members))])
	}
	var b strings.Builder
	_, _ = b.WriteString("{" + g.ws())
	for i, member := range members {
		if i > 0 {
			_, _ = b.WriteString(g.ws() + "," + g.ws())
		}
		_, _ = b.WriteString(member)
	}
	_, _ = b.WriteString(g.ws() + "}")
	return b.String()
}

func (g *chatBodyGen) member(name, value string) string {
	return g.key(name) + g.ws() + ":" + g.ws() + value
}

func (g *chatBodyGen) array(items []string) string {
	return "[" + g.ws() + strings.Join(items, g.ws()+","+g.ws()) + g.ws() + "]"
}

func (g *chatBodyGen) scalar() string {
	return []string{"0", "-0", "1.50", "1E+05", "123456789012345678901234567890", "true", "false", "null", g.str()}[g.r.Intn(9)]
}

func (g *chatBodyGen) value(depth int) string {
	if depth > 3 || g.r.Intn(3) > 0 {
		return g.scalar()
	}
	n := g.r.Intn(4)
	if g.r.Intn(2) == 0 {
		items := make([]string, n)
		for i := range items {
			items[i] = g.value(depth + 1)
		}
		return g.array(items)
	}
	members := make([]string, n)
	for i := range members {
		members[i] = g.member("k"+strconv.Itoa(g.r.Intn(4)), g.value(depth+1))
	}
	return g.object(members)
}

func (g *chatBodyGen) content() string {
	switch g.r.Intn(8) {
	case 0, 1, 2:
		return g.str()
	case 3:
		return "null"
	case 4:
		return g.value(1)
	}
	parts := make([]string, 1+g.r.Intn(3))
	for i := range parts {
		if g.r.Intn(4) == 0 {
			parts[i] = g.object([]string{g.member("type", `"image_url"`), g.member("image_url", g.object([]string{g.member("url", `"data:x"`)}))})
			continue
		}
		members := []string{g.member("type", `"text"`), g.member("text", g.str())}
		if g.r.Intn(4) == 0 {
			members = append(members, g.member("cache_control", `{"type":"ephemeral"}`))
		}
		parts[i] = g.object(members)
	}
	return g.array(parts)
}

func (g *chatBodyGen) message() string {
	roles := []string{`"system"`, `"developer"`, `"user"`, `"user"`, `"assistant"`, `"tool"`, `"other"`, `7`, esc(`"~u0073ystem"`)}
	members := []string{g.member("role", roles[g.r.Intn(len(roles))])}
	if g.r.Intn(10) > 0 {
		members = append(members, g.member("content", g.content()))
	}
	switch g.r.Intn(6) {
	case 0:
		members = append(members, g.member("tool_calls", `[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]`))
	case 1:
		members = append(members, g.member("tool_call_id", `"c1"`))
	case 2:
		members = append(members, g.member("name", g.str()))
	case 3:
		members = append(members, g.member("extra", g.value(0)))
	}
	return g.object(members)
}

func (g *chatBodyGen) body() []byte {
	models := []string{`"glm-5.2"`, `"auto"`, `"deepseek-v4-pro"`, `"  deepseek-v4-flash "`, `""`, `7`, `null`, `"kimi"`}
	var members []string
	if g.r.Intn(10) > 0 {
		members = append(members, g.member("model", models[g.r.Intn(len(models))]))
	}
	switch g.r.Intn(20) {
	case 0:
		members = append(members, g.member("messages", "[]"))
	case 1:
		members = append(members, g.member("messages", "null"))
	case 2:
		members = append(members, g.member("messages", `[{"role":"user","content":"a"},null]`))
	case 3:
	default:
		messages := make([]string, 1+g.r.Intn(5))
		for i := range messages {
			messages[i] = g.message()
		}
		members = append(members, g.member("messages", g.array(messages)))
	}
	optional := []func() string{
		func() string { return g.member("stream", []string{"true", "false", `"x"`}[g.r.Intn(3)]) },
		func() string { return g.member("stream_options", g.value(0)) },
		func() string { return g.member("temperature", g.scalar()) },
		func() string { return g.member("n", []string{"1", "1", "1.0", "2", `"2"`, "null"}[g.r.Intn(6)]) },
		func() string { return g.member("store", "true") },
		func() string { return g.member("metadata", g.value(0)) },
		func() string {
			return g.member("reasoning_effort", []string{`"low"`, `"max"`, `"none"`, `""`, "3"}[g.r.Intn(5)])
		},
		func() string {
			return g.member("response_format", []string{`{"type":"json_object"}`, `{"type":"text"}`, `{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}`, `{"type":"json_schema","schema":null}`, `"bad"`}[g.r.Intn(5)])
		},
		func() string {
			return g.member("tools", `[{"type":"function","function":{"name":"f"}},{"type":"function","function":{"name":"g","parameters":{"type":"object"}}}]`)
		},
		func() string {
			return g.member("tool_choice", []string{`"auto"`, `"auto"`, `"required"`, `{"type":"function","function":{"name":"g"}}`, `{"type":"function","name":"f"}`, `{"type":"function","function":{"name":"zz"}}`, `{"bad"`, `{"type":7}`}[g.r.Intn(8)])
		},
	}
	for _, add := range optional {
		if g.r.Intn(3) == 0 {
			members = append(members, add())
		}
	}
	return []byte(g.ws() + g.object(members) + g.ws())
}

// mutate corrupts body the way truncated or hand-edited payloads do.
func (g *chatBodyGen) mutate(body []byte) []byte {
	out := bytes.Clone(body)
	if len(out) == 0 {
		return out
	}
	i := g.r.Intn(len(out))
	switch g.r.Intn(4) {
	case 0:
		return out[:i]
	case 1:
		return append(out[:i], out[i+1:]...)
	case 2:
		const noise = "{}[],:\"\\ a0\x01\xff"
		out[i] = noise[g.r.Intn(len(noise))]
		return out
	}
	return append(out[:i], append([]byte{'"'}, out[i:]...)...)
}
