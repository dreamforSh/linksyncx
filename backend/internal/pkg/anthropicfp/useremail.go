package anthropicfp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Claude Code 2.1.283 二进制实证的 "# userEmail" 上下文段：
//
//	userEmail:`The user's email address is ${email}. Use it only to identify the user, ...`
//
// 渲染为 "# userEmail\n<value>"，与其它段以 "\n" 连接。它出现在两处，都位于
// <system-reminder> 块内：首条消息前的会话上下文块（"As you answer the user's
// questions, you can use the following context:"），以及会话上下文变化时注入的
// session_context 附件（"The session context has changed; these values replace the
// earlier ones:"）。同一请求体可能同时含有两处。
const (
	userEmailSectionMarker = "# userEmail"
	userEmailSectionPrefix = "# userEmail\nThe user's email address is "
	userEmailSectionSuffix = ". Use it only to identify the user, such as for authorship, attribution, or filtering their own work. Never send it to an unrelated service, such as in a request header, URL, or payload, unless the user explicitly asks."

	// maxUserEmailLen 是 email 地址的长度上限（RFC 3696 勘误：64 local + "@" + 255 domain）。
	maxUserEmailLen = 320
)

// userEmailSectionRegex 由上面的前后缀逐字构造，只在两处放宽：
//   - email 必须是不含空白、带 "@" 的单个 token（\S+@\S+）：不能跨越空白，也就不可能
//     跨越段落、消息或 JSON 结构；
//   - 撇号额外容忍 dateline 指纹中见过的三种变体（防御性；2.1.283 模板本身是 ASCII），
//     替换时统一写回 ASCII。
//
// 前后缀任何一处文案变化都不再匹配，原样放行，不做模糊猜测。
var userEmailSectionRegex = regexp.MustCompile(
	strings.Replace(regexp.QuoteMeta(userEmailSectionPrefix), "'", "['’ʼʹ]", 1) +
		`(\S+@\S+)` + regexp.QuoteMeta(userEmailSectionSuffix))

func canonicalUserEmailSection(email string) string {
	return userEmailSectionPrefix + email + userEmailSectionSuffix
}

// usableAccountEmail 校验用于替换的上游账号 email：必须是不含空白/控制字符、带 "@"
// 的单个 token，保证写回的段仍能被 userEmailSectionRegex 识别（幂等）。不合格返回 ""，
// 调用方随之改为整段删除。
func usableAccountEmail(email string) string {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > maxUserEmailLen {
		return ""
	}
	for _, r := range email {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return ""
		}
	}
	if at := strings.LastIndexByte(email, '@'); at <= 0 || at == len(email)-1 {
		return ""
	}
	return email
}

// sanitizeUserEmailInText 改写 text 中所有 "# userEmail" 段：replacement 非空时整段
// 重写为该 email 的规范形态，否则整段删除。删除时只隔一个换行的相邻段合并为一个
// 区间，整体再带走一个段落分隔换行（优先其后，末尾时取其前），结果与这些段从未
// 渲染过的 join("\n") 一致。
func sanitizeUserEmailInText(text, replacement string) (string, bool) {
	locs := userEmailSectionRegex.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text, false
	}
	section := ""
	if replacement != "" {
		section = canonicalUserEmailSection(replacement)
	}
	type span struct{ start, end int }
	var spans []span
	for _, m := range locs {
		start, end := m[0], m[1]
		if m[3]-m[2] > maxUserEmailLen {
			continue
		}
		if section != "" {
			if text[start:end] != section {
				spans = append(spans, span{start, end})
			}
			continue
		}
		if n := len(spans); n > 0 && spans[n-1].end+1 == start && text[start-1] == '\n' {
			spans[n-1].end = end
			continue
		}
		spans = append(spans, span{start, end})
	}
	if len(spans) == 0 {
		return text, false
	}

	var b strings.Builder
	b.Grow(len(text))
	prev := 0
	for _, sp := range spans {
		start, end := sp.start, sp.end
		if section == "" {
			switch {
			case end < len(text) && text[end] == '\n':
				end++
			case start > prev && text[start-1] == '\n':
				start--
			}
		}
		_, _ = b.WriteString(text[prev:start])
		_, _ = b.WriteString(section)
		prev = end
	}
	_, _ = b.WriteString(text[prev:])
	return b.String(), true
}

// sanitizeUserEmailInSystemReminders 只改写 <system-reminder> 块内的 "# userEmail" 段，
// 块外文本逐字节保留。
func sanitizeUserEmailInSystemReminders(text, replacement string) (string, bool) {
	if !strings.Contains(text, userEmailSectionMarker) || !strings.Contains(text, "<system-reminder>") {
		return text, false
	}
	locs := systemReminderRegex.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return text, false
	}
	var b strings.Builder
	b.Grow(len(text))
	prev, changed := 0, false
	for _, loc := range locs {
		block, ok := sanitizeUserEmailInText(text[loc[0]:loc[1]], replacement)
		if !ok {
			continue
		}
		_, _ = b.WriteString(text[prev:loc[0]])
		_, _ = b.WriteString(block)
		prev = loc[1]
		changed = true
	}
	if !changed {
		return text, false
	}
	_, _ = b.WriteString(text[prev:])
	return b.String(), true
}

// marshalJSONString 按 JSON.stringify 的方式编码字符串（不做 HTML 转义）。sjson.SetBytes
// 遇到含换行/非 ASCII 的字符串会走 encoding/json 默认编码，把 < > & 写成 Unicode 转义
// 序列，与真实客户端（从不转义这三个字符）的线级字节不一致。
func marshalJSONString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // 编码 string 不会失败
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// SanitizeUserEmail 脱敏 Anthropic /v1/messages 请求体中 Claude Code 注入的
// "# userEmail" 上下文段。relay 场景下终端用户的 email 与上游 OAuth 账号身份不一致
// （既是可观测差异，也是隐私泄漏）：
//   - accountEmail 合格（见 usableAccountEmail）：整段重写为上游账号 email；
//   - 否则：整段删除（连同一个段落分隔换行）。
//
// 作用范围与 NormalizeDateline 同一原则：只改 <system-reminder> 块内的文本，涉及
// system（字符串或 text 块）、messages[].content 的字符串 / text 块，以及 tool_result
// 的 content（字符串或 text 块；客户端可能把附件提醒并入 tool_result）。块外的用户
// 正文、tool_use.input 等从不改写。段模板必须逐字命中（见 userEmailSectionRegex），
// 不命中即原样放行。
//
// 纯函数：不修改入参；没有改写时返回原切片与 false。
func SanitizeUserEmail(body []byte, accountEmail string) ([]byte, bool) {
	if len(body) == 0 || !bytes.Contains(body, []byte(userEmailSectionMarker)) {
		return body, false
	}
	replacement := usableAccountEmail(accountEmail)

	out := body
	changed := false
	rewrite := func(path, text string) {
		next, ok := sanitizeUserEmailInSystemReminders(text, replacement)
		if !ok {
			return
		}
		if updated, err := sjson.SetRawBytes(out, path, marshalJSONString(next)); err == nil {
			out = updated
			changed = true
		}
	}
	var visit func(path string, content gjson.Result, allowToolResult bool)
	visit = func(path string, content gjson.Result, allowToolResult bool) {
		switch {
		case content.Type == gjson.String:
			rewrite(path, content.String())
		case content.IsArray():
			idx := -1
			content.ForEach(func(_, block gjson.Result) bool {
				idx++
				blockPath := fmt.Sprintf("%s.%d", path, idx)
				switch block.Get("type").String() {
				case "text":
					if t := block.Get("text"); t.Type == gjson.String {
						rewrite(blockPath+".text", t.String())
					}
				case "tool_result":
					if allowToolResult {
						visit(blockPath+".content", block.Get("content"), false)
					}
				}
				return true
			})
		}
	}

	visit("system", gjson.GetBytes(out, "system"), false)
	if messages := gjson.GetBytes(out, "messages"); messages.IsArray() {
		idx := -1
		messages.ForEach(func(_, msg gjson.Result) bool {
			idx++
			visit(fmt.Sprintf("messages.%d.content", idx), msg.Get("content"), true)
			return true
		})
	}

	if !changed {
		return body, false
	}
	return out, true
}
