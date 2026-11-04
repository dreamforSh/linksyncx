//go:build unit

package service

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Verbatim copy of normalizeOpenAIRequestLocale before the single-pass
// rewrite; the differential tests require identical output for all inputs.
// legacyNormalizeOpenAIRequestLocale 遍历请求体中的文本字段，改写 environment_context 时区。
func legacyNormalizeOpenAIRequestLocale(account *Account, body []byte) []byte {
	if account == nil || account.Platform != PlatformOpenAI || len(body) == 0 {
		return body
	}
	timezone := account.OpenAIRequestTimezone()
	if !isValidOpenAIRequestTimezone(timezone) {
		return body
	}
	// 快速路径：绝大多数请求不含 environment_context。
	if !strings.Contains(string(body), openAIRequestEnvironmentContextTagName) {
		return body
	}

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
