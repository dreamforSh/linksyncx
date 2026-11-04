package service

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Codex 请求体里的 environment_context 会带客户端所在时区。上游会把这个时区
// 当作账号环境的一部分，中国时区是降智判定里最明显的信号之一，所以转发前
// 统一改写为账号出口所在地的时区（默认跟随出口 IP 检测结果）。
const (
	openAIRequestTimezoneExtraKey          = "openai_request_timezone"
	openAIRequestTimezoneModeExtraKey      = "openai_request_timezone_mode"
	openAIRequestTimezoneDetectedExtraKey  = "openai_request_timezone_detected"
	DefaultOpenAIRequestTimezone           = "Asia/Singapore"
	OpenAIRequestTimezoneModeFixed         = "fixed"
	OpenAIRequestTimezoneModeFollowProxy   = "follow_proxy"
	openAIRequestPrefixToTimezoneTag       = "<timezone>"
	openAIRequestSuffixToTimezoneTag       = "</timezone>"
	openAIRequestEnvironmentContextTagName = "environment_context"
)

// OpenAIDetectedTimezone 是出口时区检测的落库结构（extra.openai_request_timezone_detected）。
type OpenAIDetectedTimezone struct {
	IP         string `json:"ip,omitempty"`
	Country    string `json:"country,omitempty"`
	Region     string `json:"region,omitempty"`
	City       string `json:"city,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	DetectedAt string `json:"detected_at"`
	Error      string `json:"error,omitempty"`
}

// OpenAIRequestTimezoneMode 返回时区策略，存量账号没有该字段时按跟随出口处理。
func (a *Account) OpenAIRequestTimezoneMode() string {
	if a == nil || a.Extra == nil {
		return OpenAIRequestTimezoneModeFollowProxy
	}
	raw, _ := a.Extra[openAIRequestTimezoneModeExtraKey].(string)
	if raw == OpenAIRequestTimezoneModeFixed {
		return OpenAIRequestTimezoneModeFixed
	}
	return OpenAIRequestTimezoneModeFollowProxy
}

// OpenAIRequestTimezoneFollowsProxy 报告该账号是否使用「跟随出口 IP」的时区策略。
func (a *Account) OpenAIRequestTimezoneFollowsProxy() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.OpenAIRequestTimezoneMode() == OpenAIRequestTimezoneModeFollowProxy
}

// OpenAIRequestTimezone 返回改写使用的 IANA 时区：优先账号上显式/检测写入的值。
func (a *Account) OpenAIRequestTimezone() string {
	if a != nil && a.Extra != nil {
		if name, ok := a.Extra[openAIRequestTimezoneExtraKey].(string); ok && isValidOpenAIRequestTimezone(name) {
			return name
		}
	}
	return DefaultOpenAIRequestTimezone
}

// openAIRequestValidTimezones remembers names time.LoadLocation accepted. Every
// Codex request checks its account timezone twice, and each LoadLocation call
// reads and parses zone data again.
var openAIRequestValidTimezones sync.Map

func isValidOpenAIRequestTimezone(name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	if _, ok := openAIRequestValidTimezones.Load(name); ok {
		return true
	}
	if _, err := time.LoadLocation(name); err != nil {
		return false
	}
	openAIRequestValidTimezones.Store(name, struct{}{})
	return true
}

// rewriteOpenAIRequestEnvironment 只改写 <environment_context> 里的 <timezone> 值，
// 其余标签与空白原样保留。返回改写后的文本、原时区与原因。
func rewriteOpenAIRequestEnvironment(text, timezone string) (string, string, string) {
	start := strings.Index(text, "<"+openAIRequestEnvironmentContextTagName)
	if start < 0 {
		return text, "", "no_environment_context"
	}
	endTag := "</" + openAIRequestEnvironmentContextTagName + ">"
	end := strings.LastIndex(text, endTag)
	if end < start {
		return text, "", "invalid_environment_context"
	}
	segment := text[start : end+len(endTag)]
	tzStart := strings.Index(segment, openAIRequestPrefixToTimezoneTag)
	if tzStart < 0 {
		return text, "", "no_timezone"
	}
	tzValueStart := tzStart + len(openAIRequestPrefixToTimezoneTag)
	tzValueEnd := strings.Index(segment[tzValueStart:], openAIRequestSuffixToTimezoneTag)
	if tzValueEnd < 0 {
		return text, "", "invalid_environment_context"
	}
	tzValueEnd += tzValueStart
	previous := strings.TrimSpace(segment[tzValueStart:tzValueEnd])
	if previous == "" || strings.Contains(previous, "<") {
		return text, "", "invalid_environment_context"
	}
	if previous == timezone {
		return text, previous, "already_target"
	}
	absStart := start + tzValueStart
	absEnd := start + tzValueEnd
	return text[:absStart] + timezone + text[absEnd:], previous, "replaced"
}

// normalizeOpenAIRequestLocale 遍历请求体中的文本字段，改写 environment_context 时区。
func normalizeOpenAIRequestLocale(account *Account, body []byte) []byte {
	if account == nil || account.Platform != PlatformOpenAI || len(body) == 0 {
		return body
	}
	timezone := account.OpenAIRequestTimezone()
	if !isValidOpenAIRequestTimezone(timezone) {
		return body
	}
	// 快速路径：绝大多数请求不含 environment_context。
	if !bytes.Contains(body, []byte(openAIRequestEnvironmentContextTagName)) {
		return body
	}
	if result, ok := normalizeOpenAIRequestLocaleSinglePass(body, timezone); ok {
		return result
	}

	// 回退路径：非法 JSON 或相关键重复时，gjson 读取与 sjson 写入可能落在不同成员上，
	// 保留逐路径改写以维持原有结果。
	rewrite := func(path string, current []byte) ([]byte, bool) {
		text := gjson.GetBytes(current, path)
		if !text.Exists() || text.Type != gjson.String {
			return current, false
		}
		next, _, reason := rewriteOpenAIRequestEnvironment(text.String(), timezone)
		if reason != "replaced" {
			return current, false
		}
		updated, err := sjson.SetBytes(current, path, next)
		if err != nil {
			return current, false
		}
		return updated, true
	}

	result := body
	changed := false
	// input.#.content.#.text
	inputs := gjson.GetBytes(result, "input")
	if inputs.IsArray() {
		for i := range inputs.Array() {
			contents := gjson.GetBytes(result, "input."+strconv.Itoa(i)+".content")
			if !contents.IsArray() {
				continue
			}
			for j := range contents.Array() {
				path := "input." + strconv.Itoa(i) + ".content." + strconv.Itoa(j) + ".text"
				if updated, ok := rewrite(path, result); ok {
					result = updated
					changed = true
				}
			}
		}
	}
	// instructions 里也可能带 environment_context（部分客户端把它塞进去）。
	if updated, ok := rewrite("instructions", result); ok {
		result = updated
		changed = true
	}
	if !changed {
		return body
	}
	return result
}

// normalizeOpenAIRequestLocaleSinglePass 与上面的逐路径改写结果逐字节相同，但只扫描一遍。
// 逐路径版本对每个 input 项、每个 content 块都从头定位一次路径，每次改写还整体复制一遍
// body，长会话下耗时随条目数平方增长。
//
// 等价前提：body 是合法 JSON 对象，且 input / instructions（顶层）、content（input 项内）、
// text（content 块内）在各自层级都唯一。此时 gjson 读取与 sjson 逐级写入定位到同一个值，
// 各处改写互不重叠，可以一次拼接；sjsonStringify 与 sjson 的字符串编码相同。
// 不满足前提时 ok=false，由调用方走逐路径版本。
func normalizeOpenAIRequestLocaleSinglePass(body []byte, timezone string) ([]byte, bool) {
	if !gjson.ValidBytes(body) {
		return nil, false
	}
	view := bytesView(body)
	if _, isObject := jsonRootObject(view); !isObject {
		return nil, false
	}
	type edit struct {
		start, end  int
		replacement []byte
	}
	var edits []edit
	offsetOf := func(raw string) int {
		return int(uintptr(unsafe.Pointer(unsafe.StringData(raw))) - uintptr(unsafe.Pointer(unsafe.StringData(view))))
	}
	// rewriteText 处理一个字符串值的原始字面量。不含 environment_context 字样、也没有
	// \u 转义的字面量解码后不可能出现该标签，免去解码。
	rewriteText := func(raw string) {
		if len(raw) == 0 || raw[0] != '"' ||
			(!strings.Contains(raw, openAIRequestEnvironmentContextTagName) && !strings.Contains(raw, `\u`)) {
			return
		}
		next, _, reason := rewriteOpenAIRequestEnvironment(gjson.Parse(raw).String(), timezone)
		if reason != "replaced" {
			return
		}
		start := offsetOf(raw)
		edits = append(edits, edit{start: start, end: start + len(raw), replacement: sjsonStringify(next)})
	}

	unique := true
	var inputRaw, instructionsRaw string
	inputCount, instructionsCount := 0, 0
	forEachJSONMemberRaw(view, func(key, raw string) bool {
		switch key {
		case "input":
			inputCount++
			inputRaw = raw
		case "instructions":
			instructionsCount++
			instructionsRaw = raw
		}
		return true
	})
	if inputCount > 1 || instructionsCount > 1 {
		return nil, false
	}
	if inputCount == 1 && inputRaw[0] == '[' {
		forEachJSONArrayElementRaw(inputRaw, func(item string) bool {
			if item[0] != '{' {
				return true
			}
			var contentRaw string
			contentCount := 0
			forEachJSONMemberRaw(item, func(key, raw string) bool {
				if key == "content" {
					contentCount++
					contentRaw = raw
				}
				return true
			})
			if contentCount > 1 {
				unique = false
				return false
			}
			if contentCount == 0 || contentRaw[0] != '[' {
				return true
			}
			forEachJSONArrayElementRaw(contentRaw, func(part string) bool {
				if part[0] != '{' {
					return true
				}
				var textRaw string
				textCount := 0
				forEachJSONMemberRaw(part, func(key, raw string) bool {
					if key == "text" {
						textCount++
						textRaw = raw
					}
					return true
				})
				if textCount > 1 {
					unique = false
					return false
				}
				if textCount == 1 {
					rewriteText(textRaw)
				}
				return true
			})
			return unique
		})
		if !unique {
			return nil, false
		}
	}
	if instructionsCount == 1 {
		rewriteText(instructionsRaw)
	}
	if len(edits) == 0 {
		return body, true
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(body)
	for _, e := range edits {
		size += len(e.replacement) - (e.end - e.start)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, e := range edits {
		out = append(out, body[last:e.start]...)
		out = append(out, e.replacement...)
		last = e.end
	}
	return append(out, body[last:]...), true
}
