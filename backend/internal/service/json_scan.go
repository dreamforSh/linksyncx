package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// jsonTopLevelPath 是 jsonTopLevelPaths 支持的路径："key" 或 "key.sub"。
type jsonTopLevelPath struct {
	key    string
	sub    string
	nested bool
}

// newJSONTopLevelPaths 预先拆分路径。只接受由字母、数字、"_"、"-" 组成且不以数字开头
// 的一到两段路径，保证与 gjson 路径语法没有歧义（无通配符、转义、数组下标、修饰符）。
func newJSONTopLevelPaths(paths ...string) []jsonTopLevelPath {
	out := make([]jsonTopLevelPath, len(paths))
	for i, path := range paths {
		key, sub, nested := strings.Cut(path, ".")
		if !isPlainJSONPathComponent(key) || (nested && !isPlainJSONPathComponent(sub)) {
			panic(fmt.Sprintf("newJSONTopLevelPaths: unsupported path %q", path))
		}
		out[i] = jsonTopLevelPath{key: key, sub: sub, nested: nested}
	}
	return out
}

func isPlainJSONPathComponent(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// jsonTopLevelPaths 只遍历一次顶层对象，为每个路径填入与 gjson.Get(data, path) 完全一致的
// 结果（含 Index）；dst[i] 对应 paths[i]。大请求体里 messages 通常排在前面，逐个 gjson.Get
// 每查一个排在它后面（或不存在）的字段都要把它整段跳过一遍。
//
// 语义与 gjson 对齐：单段路径取第一次出现的键；两段路径依次尝试每个同名顶层键，取第一个
// 是对象且含有子键的那个。全部找到后提前结束。结果引用 data 本身（不复制）。data 须是合法
// JSON：非法输入上 gjson 的容错方式不同（例如根对象闭合后的多余内容），结果可能不一致。
func jsonTopLevelPaths(data string, paths []jsonTopLevelPath, dst []gjson.Result) {
	dst = dst[:len(paths)]
	for i := range dst {
		dst[i] = gjson.Result{}
	}
	root, ok := jsonRootObject(data)
	if !ok {
		// 顶层不是对象时，这类路径 gjson.Get 都查不到。
		return
	}
	pending := len(paths)
	root.ForEach(func(key, value gjson.Result) bool {
		for i := range paths {
			if dst[i].Exists() || key.Str != paths[i].key {
				continue
			}
			if !paths[i].nested {
				dst[i] = value
			} else if value.IsObject() {
				// 查不到时 Result.Get 仍会叠加父值的 Index，保持零值才与 gjson.Get 一致。
				if r := value.Get(paths[i].sub); r.Exists() {
					dst[i] = r
				}
			}
			if dst[i].Exists() {
				pending--
			}
		}
		return pending > 0
	})
}

// forEachJSONMemberRaw 依次给出 JSON 对象各成员的键名（按 encoding/json 解码）与值的原始
// 文本。与 gjson 的 ForEach 不同，它不解码字符串值：ForEach 会对每个带转义的字符串值做一次
// unescape 拷贝，长文本内容块上开销可观。obj 须是合法 JSON 对象；对非法输入只保证不越界、
// 不死循环，结果无意义。
func forEachJSONMemberRaw(obj string, fn func(key, raw string) bool) {
	i := strings.IndexByte(obj, '{') + 1
	if i == 0 {
		return
	}
	for i < len(obj) {
		switch obj[i] {
		case '}':
			return
		case '"':
		default:
			i++ // 空白与逗号
			continue
		}
		keyEnd := jsonStringLiteralEnd(obj, i)
		if keyEnd < 0 {
			return
		}
		key := obj[i+1 : keyEnd-1]
		if strings.IndexByte(key, '\\') >= 0 || !utf8.ValidString(key) {
			key, _ = decodeJSONStringLiteral(obj[i:keyEnd])
		}
		i = keyEnd
		for i < len(obj) && (obj[i] == ':' || obj[i] == ' ' || obj[i] == '\t' || obj[i] == '\n' || obj[i] == '\r') {
			i++
		}
		valueEnd := jsonValueEnd(obj, i)
		if valueEnd <= i {
			return
		}
		if !fn(key, obj[i:valueEnd]) {
			return
		}
		i = valueEnd
	}
}

// jsonStringLiteralEnd 返回从 s[i]（必须是 '"'）开始的字符串字面量结束后的位置；未闭合返回 -1。
func jsonStringLiteralEnd(s string, i int) int {
	for j := i + 1; ; {
		k := strings.IndexByte(s[j:], '"')
		if k < 0 {
			return -1
		}
		quote := j + k
		// 紧邻的反斜杠为奇数个时这个引号是转义的。
		backslashes := 0
		for p := quote - 1; p > i && s[p] == '\\'; p-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return quote + 1
		}
		j = quote + 1
	}
}

// jsonValueEnd 返回从 s[i] 开始的 JSON 值结束后的位置；无法识别时返回值不大于 i。
func jsonValueEnd(s string, i int) int {
	if i >= len(s) {
		return -1
	}
	switch s[i] {
	case '"':
		return jsonStringLiteralEnd(s, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				end := jsonStringLiteralEnd(s, j)
				if end < 0 {
					return -1
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return -1
	default:
		j := i
		for j < len(s) && s[j] != ',' && s[j] != '}' && s[j] != ']' && s[j] != ' ' && s[j] != '\t' && s[j] != '\n' && s[j] != '\r' {
			j++
		}
		return j
	}
}

// decodeJSONStringLiteral 按 encoding/json 的语义解码字符串字面量（含引号）；不是字符串
// 字面量时返回 ("", false)。
func decodeJSONStringLiteral(raw string) (string, bool) {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	if strings.IndexByte(raw, '\\') < 0 && utf8.ValidString(raw) {
		return raw[1 : len(raw)-1], true
	}
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return "", false
	}
	return s, true
}

// jsonRootObject 返回顶层对象；顶层不是对象时 ok=false。gjson.Parse 不设置 Index，这里带上
// 对象起点，ForEach 给出的 Index 才是 data 内的绝对偏移。
func jsonRootObject(data string) (gjson.Result, bool) {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\t' || data[start] == '\n' || data[start] == '\r') {
		start++
	}
	if start == len(data) || data[start] != '{' {
		return gjson.Result{}, false
	}
	return gjson.Result{Type: gjson.JSON, Raw: data[start:], Index: start}, true
}
