package anthropicfp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// cc283UserEmailSuffix 是 Claude Code 2.1.283 二进制中 userEmail 模板的固定后缀，
// 独立于实现常量硬编码，模板一旦偏离二进制即由 TestSanitizeUserEmail_TemplateMatchesClaudeCode283 报警。
const cc283UserEmailSuffix = ". Use it only to identify the user, such as for authorship, attribution, or filtering their own work. Never send it to an unrelated service, such as in a request header, URL, or payload, unless the user explicitly asks."

const cc283ImportantTail = "IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task."

var (
	sectionClaudeMd    = [2]string{"claudeMd", "Contents of CLAUDE.md (project instructions)"}
	sectionCurrentDate = [2]string{"currentDate", "Today's date is 2026-09-28."}
	sectionGitStatus   = [2]string{"gitStatus", "Current branch: main"}
)

func userEmailValue(email string) string {
	return "The user's email address is " + email + cc283UserEmailSuffix
}

func joinSections(sections [][2]string) string {
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		parts = append(parts, "# "+s[0]+"\n"+s[1])
	}
	return strings.Join(parts, "\n")
}

// renderUserContext 复刻 2.1.283 首条消息前的会话上下文块（Sjn + 段 + gTo）。
func renderUserContext(sections ...[2]string) string {
	return "<system-reminder>\nAs you answer the user's questions, you can use the following context:\n" +
		joinSections(sections) + "\n\n      " + cc283ImportantTail + "\n</system-reminder>\n"
}

// renderSessionContextChanged 复刻会话上下文变化时的 session_context 附件（RKe，changed=true），
// 附件经 xi()/Qa() 包进 <system-reminder>。
func renderSessionContextChanged(sections ...[2]string) string {
	return "<system-reminder>\nThe session context has changed; these values replace the earlier ones:\n" +
		joinSections(sections) + "\n\n" + cc283ImportantTail + "\n</system-reminder>"
}

// mustStringify 按 JSON.stringify 的方式编码（不做 HTML 转义），模拟真实客户端的线级形态。
func mustStringify(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func requestBody(t *testing.T, messages ...any) []byte {
	t.Helper()
	return mustStringify(t, map[string]any{"model": "claude-sonnet-4-6", "max_tokens": 1024, "messages": messages})
}

func userTextMessage(texts ...string) map[string]any {
	blocks := make([]any, 0, len(texts))
	for _, s := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": s})
	}
	return map[string]any{"role": "user", "content": blocks}
}

func readToolUseMessage() map[string]any {
	return map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Read", "input": map[string]any{"file_path": "useremail.go"}},
	}}
}

func toolResultMessage(content any, trailingTexts ...string) map[string]any {
	blocks := []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": content}}
	for _, s := range trailingTexts {
		blocks = append(blocks, map[string]any{"type": "text", "text": s})
	}
	return map[string]any{"role": "user", "content": blocks}
}

func mustValidJSON(t *testing.T, body []byte) {
	t.Helper()
	if !json.Valid(body) {
		t.Fatalf("result is not valid JSON: %s", body)
	}
}

func TestSanitizeUserEmail_TemplateMatchesClaudeCode283(t *testing.T) {
	want := "# userEmail\nThe user's email address is a@b.c" + cc283UserEmailSuffix
	if got := canonicalUserEmailSection("a@b.c"); got != want {
		t.Fatalf("template drifted from 2.1.283:\nwant %q\ngot  %q", want, got)
	}
}

func TestSanitizeUserEmail_ContextBlockStripMatchesRenderWithoutEmail(t *testing.T) {
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx, "hello"))

	next, changed := SanitizeUserEmail(body, "")
	if !changed {
		t.Fatal("expected changed")
	}
	mustValidJSON(t, next)
	// 删除后应与客户端根本没有 userEmail 时的渲染逐字一致。
	want := renderUserContext(sectionClaudeMd, sectionCurrentDate)
	if got := gjson.GetBytes(next, "messages.0.content.0.text").String(); got != want {
		t.Fatalf("stripped context mismatch:\nwant %q\ngot  %q", want, got)
	}
	if got := gjson.GetBytes(next, "messages.0.content.1.text").String(); got != "hello" {
		t.Fatalf("sibling text block changed: %q", got)
	}
}

func TestSanitizeUserEmail_ContextBlockReplaceWithAccountEmail(t *testing.T) {
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx))

	next, changed := SanitizeUserEmail(body, "upstream-account@example.com")
	if !changed {
		t.Fatal("expected changed")
	}
	mustValidJSON(t, next)
	want := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("upstream-account@example.com")}, sectionCurrentDate)
	if got := gjson.GetBytes(next, "messages.0.content.0.text").String(); got != want {
		t.Fatalf("replaced context mismatch:\nwant %q\ngot  %q", want, got)
	}
}

// 会话中途的 session_context 附件会再带一段 userEmail（2.1.283：TJt 含 userEmail），
// 与首条消息的上下文块同处一个请求体；每一处都必须处理。
func TestSanitizeUserEmail_SessionContextAttachmentSanitizedToo(t *testing.T) {
	initial := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("first@example.com")}, sectionCurrentDate)
	changedCtx := renderSessionContextChanged([2]string{"userEmail", userEmailValue("switched@example.com")}, sectionGitStatus)
	body := requestBody(t,
		userTextMessage(initial, "hello"),
		readToolUseMessage(),
		toolResultMessage("file contents", changedCtx),
	)

	stripped, changed := SanitizeUserEmail(body, "")
	if !changed {
		t.Fatal("expected changed")
	}
	mustValidJSON(t, stripped)
	if bytes.Contains(stripped, []byte("first@example.com")) || bytes.Contains(stripped, []byte("switched@example.com")) {
		t.Fatalf("an email leaked: %s", stripped)
	}
	wantAttachment := renderSessionContextChanged(sectionGitStatus)
	if got := gjson.GetBytes(stripped, "messages.2.content.1.text").String(); got != wantAttachment {
		t.Fatalf("attachment mismatch:\nwant %q\ngot  %q", wantAttachment, got)
	}

	replaced, changed := SanitizeUserEmail(body, "upstream@example.com")
	if !changed {
		t.Fatal("expected changed")
	}
	mustValidJSON(t, replaced)
	if n := bytes.Count(replaced, []byte("The user's email address is upstream@example.com.")); n != 2 {
		t.Fatalf("want both sections rewritten, got %d: %s", n, replaced)
	}
}

// 审计复现的回归：客户端改了后缀文案，对话里另有旧后缀字面量（例如读过本包源码）。
// 旧实现把两者之间的内容整段吞掉（JSON 仍合法、消息被静默删除）；现在必须原样放行。
func TestSanitizeUserEmail_TemplateDriftWithStaleSuffixElsewhereLeavesBodyUntouched(t *testing.T) {
	drifted := "The user's email address is relay-user@example.com. Use it only to identify the user; never share it."
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", drifted}, sectionCurrentDate)
	source := "var userEmailSectionSuffix = `" + cc283UserEmailSuffix + "`"
	body := requestBody(t,
		userTextMessage(ctx, "audit useremail.go"),
		readToolUseMessage(),
		toolResultMessage(source),
		map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "done"}}},
		userTextMessage("continue"),
	)

	for _, accountEmail := range []string{"", "upstream@example.com"} {
		next, changed := SanitizeUserEmail(body, accountEmail)
		if changed || !bytes.Equal(next, body) {
			t.Fatalf("accountEmail=%q: drifted template must be left untouched, got %s", accountEmail, next)
		}
	}
}

func TestSanitizeUserEmail_TextOutsideSystemReminderUntouched(t *testing.T) {
	section := "# userEmail\n" + userEmailValue("someone@example.com")
	body := requestBody(t,
		userTextMessage("I pasted this:\n"+section+"\nwhat is it?"),
		readToolUseMessage(),
		toolResultMessage("log line\n"+section+"\nend of log"),
	)
	for _, accountEmail := range []string{"", "upstream@example.com"} {
		next, changed := SanitizeUserEmail(body, accountEmail)
		if changed || !bytes.Equal(next, body) {
			t.Fatalf("accountEmail=%q: text outside <system-reminder> must not be touched, got %s", accountEmail, next)
		}
	}
}

func TestSanitizeUserEmail_ToolResultSystemReminderSanitized(t *testing.T) {
	reminder := renderSessionContextChanged([2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionGitStatus)
	cases := map[string]any{
		"string content": "file contents\n" + reminder,
		"block content":  []any{map[string]any{"type": "text", "text": "file contents"}, map[string]any{"type": "text", "text": reminder}},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			body := requestBody(t, userTextMessage("hi"), readToolUseMessage(), toolResultMessage(content))
			next, changed := SanitizeUserEmail(body, "")
			if !changed {
				t.Fatal("expected changed")
			}
			mustValidJSON(t, next)
			if bytes.Contains(next, []byte("relay-user@example.com")) {
				t.Fatalf("email inside tool_result reminder leaked: %s", next)
			}
			if !bytes.Contains(next, []byte("file contents")) || !bytes.Contains(next, []byte("# gitStatus")) {
				t.Fatalf("tool_result content damaged: %s", next)
			}
		})
	}
}

func TestSanitizeUserEmail_SystemFieldScopedToSystemReminder(t *testing.T) {
	inside := renderUserContext([2]string{"userEmail", userEmailValue("inside@example.com")}, sectionCurrentDate)
	outside := "# userEmail\n" + userEmailValue("outside@example.com")

	stringSystem := mustStringify(t, map[string]any{"system": outside + "\n" + inside, "messages": []any{}})
	next, changed := SanitizeUserEmail(stringSystem, "")
	if !changed {
		t.Fatal("expected changed for string system")
	}
	mustValidJSON(t, next)
	if bytes.Contains(next, []byte("inside@example.com")) || !bytes.Contains(next, []byte("outside@example.com")) {
		t.Fatalf("string system: only the <system-reminder> section may change: %s", next)
	}

	blockSystem := mustStringify(t, map[string]any{"system": []any{
		map[string]any{"type": "text", "text": outside},
		map[string]any{"type": "text", "text": inside},
	}, "messages": []any{}})
	next, changed = SanitizeUserEmail(blockSystem, "")
	if !changed {
		t.Fatal("expected changed for block system")
	}
	mustValidJSON(t, next)
	if got := gjson.GetBytes(next, "system.0.text").String(); got != outside {
		t.Fatalf("system block outside <system-reminder> changed: %q", got)
	}
	if bytes.Contains(next, []byte("inside@example.com")) {
		t.Fatalf("system block inside <system-reminder> not stripped: %s", next)
	}
}

func TestSanitizeUserEmailInText_StripKeepsSectionStructure(t *testing.T) {
	email := "# userEmail\n" + userEmailValue("u@example.com")
	cases := []struct {
		name, in, want string
	}{
		{"middle section", "ctx:\n# a\n1\n" + email + "\n# b\n2", "ctx:\n# a\n1\n# b\n2"},
		{"last section before IMPORTANT", "ctx:\n# a\n1\n" + email + "\n\n" + cc283ImportantTail, "ctx:\n# a\n1\n\n" + cc283ImportantTail},
		{"end of text", "ctx:\n# a\n1\n" + email, "ctx:\n# a\n1"},
		{"adjacent sections", "ctx:\n" + email + "\n" + email + "\n# b\n2", "ctx:\n# b\n2"},
		{"adjacent sections at end", "ctx:\n# a\n1\n" + email + "\n" + email, "ctx:\n# a\n1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := sanitizeUserEmailInText(tc.in, "")
			if !changed {
				t.Fatal("expected changed")
			}
			if got != tc.want {
				t.Fatalf("want %q\ngot  %q", tc.want, got)
			}
		})
	}
}

func TestSanitizeUserEmail_AccountEmailValidation(t *testing.T) {
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx))

	cases := []struct {
		name, accountEmail, wantEmail string // wantEmail 为空表示回退为整段删除
	}{
		{"plain", "acct@upstream.example", "acct@upstream.example"},
		{"trimmed", "  acct@upstream.example \n", "acct@upstream.example"},
		{"quote is escaped on write-back", `quo"te@upstream.example`, `quo"te@upstream.example`},
		{"inner tab", "acct\t@upstream.example", ""},
		{"inner space", "acct @upstream.example", ""},
		{"no at sign", "not-an-email", ""},
		{"leading at", "@upstream.example", ""},
		{"trailing at", "acct@", ""},
		{"too long", strings.Repeat("a", maxUserEmailLen) + "@x", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next, changed := SanitizeUserEmail(body, tc.accountEmail)
			if !changed {
				t.Fatal("expected changed")
			}
			mustValidJSON(t, next)
			text := gjson.GetBytes(next, "messages.0.content.0.text").String()
			if strings.Contains(text, "relay-user@example.com") {
				t.Fatalf("client email leaked: %q", text)
			}
			if tc.wantEmail == "" {
				if strings.Contains(text, "# userEmail") {
					t.Fatalf("want section stripped, got %q", text)
				}
				return
			}
			if !strings.Contains(text, "The user's email address is "+tc.wantEmail+". Use it only") {
				t.Fatalf("want section rewritten to %q, got %q", tc.wantEmail, text)
			}
		})
	}
}

func TestSanitizeUserEmail_Idempotent(t *testing.T) {
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx))
	for _, accountEmail := range []string{"", "upstream@example.com"} {
		once, changed := SanitizeUserEmail(body, accountEmail)
		if !changed {
			t.Fatalf("accountEmail=%q: expected first pass to change", accountEmail)
		}
		twice, changed := SanitizeUserEmail(once, accountEmail)
		if changed || !bytes.Equal(once, twice) {
			t.Fatalf("accountEmail=%q: second pass must be a no-op", accountEmail)
		}
	}
}

func TestSanitizeUserEmail_EmailTokenMustBeSingleToken(t *testing.T) {
	for name, email := range map[string]string{
		"contains space": "relay user@example.com",
		"no at sign":     "relay-user.example.com",
		"too long":       strings.Repeat("a", maxUserEmailLen) + "@example.com",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue(email)}, sectionCurrentDate)
			body := requestBody(t, userTextMessage(ctx))
			next, changed := SanitizeUserEmail(body, "")
			if changed || !bytes.Equal(next, body) {
				t.Fatalf("non-email token must be left untouched, got %s", next)
			}
		})
	}
}

func TestSanitizeUserEmail_ApostropheVariantRewrittenToASCII(t *testing.T) {
	variant := strings.Replace(userEmailValue("relay-user@example.com"), "user's", "user’s", 1)
	ctx := renderUserContext(sectionClaudeMd, [2]string{"userEmail", variant}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx))

	next, changed := SanitizeUserEmail(body, "upstream@example.com")
	if !changed {
		t.Fatal("expected changed")
	}
	want := renderUserContext(sectionClaudeMd, [2]string{"userEmail", userEmailValue("upstream@example.com")}, sectionCurrentDate)
	if got := gjson.GetBytes(next, "messages.0.content.0.text").String(); got != want {
		t.Fatalf("want canonical ASCII section:\nwant %q\ngot  %q", want, got)
	}
}

// 写回保持真实客户端的线级形态：不做 HTML 转义、不转义非 ASCII，未改写的消息逐字节不变。
func TestSanitizeUserEmail_PreservesWireFormat(t *testing.T) {
	ctx := renderUserContext([2]string{"claudeMd", "中文项目说明 & <notes>"}, [2]string{"userEmail", userEmailValue("relay-user@example.com")}, sectionCurrentDate)
	body := requestBody(t, userTextMessage(ctx), map[string]any{"role": "assistant", "content": "好的 <ok> & done"})

	next, changed := SanitizeUserEmail(body, "upstream@example.com")
	if !changed {
		t.Fatal("expected changed")
	}
	mustValidJSON(t, next)
	for _, want := range []string{"<system-reminder>", "</system-reminder>", "中文项目说明 & <notes>"} {
		if !bytes.Contains(next, []byte(want)) {
			t.Fatalf("wire bytes should keep %q verbatim: %s", want, next)
		}
	}
	// HTML 转义会留下 "003c"/"003e"/"0026" 形式的 Unicode 转义序列。
	for _, escaped := range []string{"003c", "003e", "0026"} {
		if bytes.Contains(next, []byte(escaped)) {
			t.Fatalf("write-back must not HTML-escape (found %q): %s", escaped, next)
		}
	}
	if got, want := gjson.GetBytes(next, "messages.1").Raw, gjson.GetBytes(body, "messages.1").Raw; got != want {
		t.Fatalf("untouched message changed:\nwant %s\ngot  %s", want, got)
	}
}

func TestSanitizeUserEmail_NoMarkerReturnsInputSlice(t *testing.T) {
	body := requestBody(t, userTextMessage(renderUserContext(sectionClaudeMd, sectionCurrentDate)))
	next, changed := SanitizeUserEmail(body, "upstream@example.com")
	if changed {
		t.Fatal("no userEmail section: must not change")
	}
	if len(next) == 0 || &next[0] != &body[0] {
		t.Fatal("no-op must return the input slice itself")
	}
}
