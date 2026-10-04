package service

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// jsonBodyIndex 记录一份合法 JSON 文档全部顶层成员的键名与值的位置。大请求体里 messages
// 通常排在前面，对它之后（或不存在）的顶层字段每做一次 gjson.Get / sjson 改写都要把它整段
// 跳过一遍；有了索引，顶层查找只需比较十几个键名，改写也能按位置直接拼接。
//
// 不变式：只为通过 gjson.ValidBytes 的文档建立；splice 只把一个完整的值换成另一个完整的
// 合法值，结果仍然合法。索引只描述 body 这一份字节，取用前须确认待查切片就是 body。
type jsonBodyIndex struct {
	body    []byte
	members []jsonBodyMember
}

type jsonBodyMember struct {
	key        string // 按 gjson 的方式解码的键名，与 gjson.Get 匹配键名的方式一致
	start, end int    // 值在 body 中的字节范围
}

// newJSONBodyIndex 为已通过 gjson.ValidBytes 的 body 建立索引；顶层不是对象时返回 nil
// （这时按键名的 gjson 查找都查不到，调用方退回 gjson 即可）。
func newJSONBodyIndex(body []byte) *jsonBodyIndex {
	root, ok := jsonRootObject(bytesView(body))
	if !ok {
		return nil
	}
	idx := &jsonBodyIndex{body: body, members: make([]jsonBodyMember, 0, 16)}
	root.ForEach(func(key, value gjson.Result) bool {
		idx.members = append(idx.members, jsonBodyMember{key: key.Str, start: value.Index, end: value.Index + len(value.Raw)})
		return true
	})
	return idx
}

// value 还原成员的值：对值的精确字节调用 gjson.Parse，结果与 gjson.Get 命中该成员时一致。
func (idx *jsonBodyIndex) value(m jsonBodyMember) gjson.Result {
	r := gjson.Parse(bytesView(idx.body[m.start:m.end]))
	r.Index = m.start
	return r
}

// get 返回与 gjson.Get(body, key) 一致的结果（同名键取第一次出现）；key 为单个键名。
func (idx *jsonBodyIndex) get(key string) gjson.Result {
	for _, m := range idx.members {
		if m.key == key {
			return idx.value(m)
		}
	}
	return gjson.Result{}
}

// getPath 返回与 gjson.Get(body, key+"."+rest) 一致的结果：依次尝试每个名为 key 的成员，
// 取第一个在其中查得到 rest 的（gjson 只会深入对象和数组）。
func (idx *jsonBodyIndex) getPath(key, rest string) gjson.Result {
	for _, m := range idx.members {
		if m.key != key {
			continue
		}
		v := idx.value(m)
		if !v.IsObject() && !v.IsArray() {
			continue
		}
		if r := v.Get(rest); r.Exists() {
			return r
		}
	}
	return gjson.Result{}
}

// existingValueRange 按 sjson 解析路径的方式（第一级取第一个同名成员，其后逐级 gjson.Get）
// 定位 path 指向的现有值，返回它在 body 中的范围。任何一级不存在、或要深入的不是对象/数组
// 时 ok=false——这些情况下 sjson 会补建结构，不是单纯的替换。
func (idx *jsonBodyIndex) existingValueRange(path string) (start, end int, ok bool) {
	key, rest, plain := splitPlainJSONPath(path)
	if !plain {
		return 0, 0, false
	}
	r := idx.get(key)
	if !r.Exists() {
		return 0, 0, false
	}
	abs := r.Index
	for rest != "" {
		if !r.IsObject() && !r.IsArray() {
			return 0, 0, false
		}
		var part string
		part, rest, _ = strings.Cut(rest, ".")
		sub := gjson.Get(r.Raw, part)
		if sub.Index <= 0 {
			return 0, 0, false
		}
		abs += sub.Index
		r = sub
	}
	return abs, abs + len(r.Raw), true
}

// splice 把 body[start:end)（必须恰是一个完整的值）换成 raw（必须是完整的合法 JSON 值），
// 返回新文档的索引。顶层结构不变，只需平移受影响成员的位置。
func (idx *jsonBodyIndex) splice(start, end int, raw []byte) *jsonBodyIndex {
	delta := len(raw) - (end - start)
	body := make([]byte, 0, len(idx.body)+delta)
	body = append(body, idx.body[:start]...)
	body = append(body, raw...)
	body = append(body, idx.body[end:]...)
	// 键名可能引用旧请求体：一次性复制到同一个字符串里，旧请求体才能及时回收。
	total := 0
	for _, m := range idx.members {
		total += len(m.key)
	}
	var keys strings.Builder
	keys.Grow(total)
	for _, m := range idx.members {
		_, _ = keys.WriteString(m.key)
	}
	allKeys := keys.String()
	members := make([]jsonBodyMember, len(idx.members))
	for i, m := range idx.members {
		m.key, allKeys = allKeys[:len(m.key)], allKeys[len(m.key):]
		switch {
		case m.start >= end:
			m.start += delta
			m.end += delta
		case m.end >= end: // 被替换的值就是该成员的值，或位于其内部
			m.end += delta
		}
		members[i] = m
	}
	return &jsonBodyIndex{body: body, members: members}
}

// jsonBodyView 包装待查找与改写的请求体。持有索引（且索引描述的就是 data）时，顶层查找与
// "路径每级都已存在"的字符串改写直接走索引；否则退回 gjson / sjson。两种方式结果完全一致。
type jsonBodyView struct {
	data []byte
	idx  *jsonBodyIndex
}

func newJSONBodyView(data []byte, idx *jsonBodyIndex) *jsonBodyView {
	v := &jsonBodyView{data: data}
	if idx != nil && sameByteSlice(idx.body, data) {
		v.idx = idx
	}
	return v
}

// get 与 gjson.GetBytes(v.data, path) 取值相同；结果直接引用 data，不复制。
func (v *jsonBodyView) get(path string) gjson.Result {
	if v.idx != nil {
		if key, rest, ok := splitPlainJSONPath(path); ok {
			if rest == "" {
				return v.idx.get(key)
			}
			return v.idx.getPath(key, rest)
		}
	}
	return gjson.Get(bytesView(v.data), path)
}

// setString 等价于 sjson.SetBytes(v.data, path, value)：成功时更新 data，失败时 data 不变。
// 路径每级都已存在时，sjson 的结果就是把最终值的字节换成 value 的 JSON 字符串，这里直接
// 按索引拼接并平移索引。
func (v *jsonBodyView) setString(path, value string) error {
	if v.idx != nil {
		if start, end, ok := v.idx.existingValueRange(path); ok {
			v.idx = v.idx.splice(start, end, sjsonStringify(value))
			v.data = v.idx.body
			return nil
		}
	}
	next, err := sjson.SetBytes(v.data, path, value)
	if err != nil {
		return err
	}
	v.replace(next)
	return nil
}

// deletePath 等价于 sjson.DeleteBytes(v.data, path)。删除字段很少见，直接交给 sjson。
func (v *jsonBodyView) deletePath(path string) error {
	next, err := sjson.DeleteBytes(v.data, path)
	if err != nil {
		return err
	}
	v.replace(next)
	return nil
}

// replace 换成另一份请求体；不是同一份字节时不再持有索引。
func (v *jsonBodyView) replace(data []byte) {
	if v.idx != nil && !sameByteSlice(v.idx.body, data) {
		v.idx = nil
	}
	v.data = data
}

// splitPlainJSONPath 拆出首个键名与其余路径。只接受各级都由字母、数字、"_"、"-" 组成的路径
// （没有通配符、转义、修饰符等 gjson / sjson 特殊语法），否则 ok=false。
func splitPlainJSONPath(path string) (key, rest string, ok bool) {
	componentStart := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			if i == componentStart {
				return "", "", false
			}
			componentStart = i + 1
			continue
		}
		switch c := path[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return "", "", false
		}
	}
	key, rest, _ = strings.Cut(path, ".")
	return key, rest, true
}

// sjsonStringify 与 sjson 写入字符串值时的编码逐字节相同：含控制字符、非 ASCII、引号或
// 反斜杠时用 encoding/json 编码，否则直接加引号。
func sjsonStringify(s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > 0x7f || s[i] == '"' || s[i] == '\\' {
			b, _ := json.Marshal(s)
			return b
		}
	}
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}

// bytesView 返回 b 的只读字符串视图（不复制）；b 之后不得被原地修改。
func bytesView(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
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
