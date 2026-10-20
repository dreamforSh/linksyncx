package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// fingerprintSalt 是计算 cc_version 后缀指纹的盐值。
//
// 来源：与 Parrot src/transform/cc_mimicry.py 的 FINGERPRINT_SALT 完全一致；
// 这是真实 Claude Code CLI 抓包推导出的常量，改动会导致 fp 与 CLI 不一致，
// 进一步触发 Anthropic 的第三方检测。
const fingerprintSalt = "59cf53e54c78"

// computeClaudeCodeFingerprint 复刻真实 Claude Code CLI 的 cc_version 指纹算法
// （2.1.296 二进制 RY + cg：`[4,7,20].map(i=>e[i]||"0")`，sha256 取前 3 位 hex）：
//
//  1. 取第一条非 isMeta 的 user 消息的纯文本（string content 或首个 text 块）
//  2. 取该文本 JS 下标 4、7、20 处的字符（UTF-16 码元，越界以 '0' 补齐）
//  3. SHA256(SALT + chars + cc_version) 取 hex 前 3 字符
//
// 算法最初取自 Parrot src/transform/cc_mimicry.py:compute_fingerprint，但那份实现按字节
// 取 API body 首个 text 块，与 CLI 有两处偏差，已按二进制修正：
//   - CLI 在内部消息列表上计算：会话上下文（"<system-reminder>\nAs you answer ..."）是前置的
//     isMeta 消息，RY 跳过它取用户真正的 prompt；发往 API 时两者合并成首条 user 消息的多个
//     text 块。因此在 API body 上要跳过开头的 <system-reminder> 块，否则所有真实 CLI 请求
//     都会落到同一个按版本恒定的后缀上。
//   - JS 按 UTF-16 码元取下标，中文、emoji 等非 ASCII prompt 按字节取会算出不同的值。
//
// 任何偏差都会导致 cc_version=X.Y.Z.{fp} 在上游侧与真实 CLI 不一致。
func computeClaudeCodeFingerprint(body []byte, version string) string {
	return computeClaudeCodeFingerprintView(newJSONBodyView(body, nil), version)
}

// computeClaudeCodeFingerprintView 是 computeClaudeCodeFingerprint 作用于 jsonBodyView 的版本。
func computeClaudeCodeFingerprintView(view *jsonBodyView, version string) string {
	return claudeCodeFingerprintForChars(claudeCodeFingerprintCharsView(view), version)
}

// claudeCodeFingerprintForChars 对已取出的 3 个指纹字符计算后缀。
func claudeCodeFingerprintForChars(chars, version string) string {
	sum := sha256.Sum256([]byte(fingerprintSalt + chars + version))
	return hex.EncodeToString(sum[:])[:3]
}

// claudeCodeFingerprintChars 按 JS `[4,7,20].map(i=>e[i]||"0").join("")` 语义取指纹字符。
//
// JS 字符串按 UTF-16 码元下标：BMP 外的字符（emoji 等）占两个码元，下标可能落在其中一半，
// 取到代理项。三个码元先 join 成一个字符串再交给 node crypto 的 update(string) 按 UTF-8
// 编码，所以相邻两个取出位恰好是高代理 + 低代理时会重新配成一个完整字符（4 字节 UTF-8），
// 只有落单的代理项才编成 U+FFFD（EF BF BD）。utf16.Decode 的语义与此完全一致。
// 按字节取会让中文、emoji 等 prompt 与 CLI 算出不同的后缀。
func claudeCodeFingerprintChars(text string) string {
	// 只需前 21 个码元；长文本（如整段 CLAUDE.md）不必整体转换。
	var units [21]uint16
	n := 0
	for _, r := range text {
		if r >= 0x10000 {
			hi, lo := utf16.EncodeRune(r)
			units[n] = uint16(hi)
			n++
			if n < len(units) {
				units[n] = uint16(lo)
				n++
			}
		} else {
			// Go 解码 string 时不会产出代理项码点（非法 UTF-8 得到 U+FFFD），这里都是 BMP 字符。
			units[n] = uint16(r)
			n++
		}
		if n == len(units) {
			break
		}
	}
	picked := make([]uint16, 0, 3)
	for _, i := range [...]int{4, 7, 20} {
		if i < n {
			picked = append(picked, units[i])
		} else {
			picked = append(picked, '0')
		}
	}
	return string(utf16.Decode(picked))
}

// claudeCodeFingerprintCharsView 取 CLI 指纹的输入字符。输入文本是首条 user 消息中第一个
// 不是 <system-reminder> meta 块的文本（string content 视为单个文本块）；首条 user 消息只剩
// meta 块（如纯图片 prompt）时按空串计，与 CLI 在无 text 块时取 "" 一致。只返回取出的字符，
// 不持有对请求体的引用。
func claudeCodeFingerprintCharsView(view *jsonBodyView) string {
	messages := view.get("messages")
	source := ""
	if !messages.IsArray() {
		return claudeCodeFingerprintChars(source)
	}
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			if text := content.String(); !isAnthropicSystemReminderText(text) {
				source = text
			}
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() != "text" {
					return true
				}
				text := block.Get("text").String()
				if isAnthropicSystemReminderText(text) {
					return true
				}
				source = text
				return false
			})
		}
		return false
	})
	return claudeCodeFingerprintChars(source)
}

// extractFirstUserText 提取 messages 中第一条 user 消息的首段 text 内容。
// 兼容 string 和 []block 两种 content 格式。
func extractFirstUserText(body []byte) string {
	return extractFirstUserTextView(newJSONBodyView(body, nil))
}

// extractFirstUserTextView 是 extractFirstUserText 作用于 jsonBodyView 的版本。查找结果直接
// 引用请求体（gjson.GetBytes 会把整个 messages 复制一份），命中的文本在返回前复制。
func extractFirstUserTextView(view *jsonBodyView) string {
	messages := view.get("messages")
	if !messages.IsArray() {
		return ""
	}
	first := ""
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			first = content.String()
			return false
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					first = block.Get("text").String()
					return false
				}
				return true
			})
			return false
		}
		return false
	})
	return strings.Clone(first)
}

// buildBillingAttributionText 构造 system 数组的 billing attribution 文本。
//
// 形态对齐真实 Claude Code CLI 2.1.290 第一方直连抓包（2026-10-05 MITM 实证）：
//
//	x-anthropic-billing-header: cc_version=2.1.290.{fp}; cc_entrypoint=cli; cch=00000; cc_prompt_id={uuid}; cc_turn_origin=cli; cc_prompt_index={n}; cc_turn_index=1;
//
// cch 字段：2.1.281 起官方构建为逐请求内容哈希（5 位 hex；2.1.281/283/285/287/289/290
// 逐版本抓包实证均已签名，2.1.280 为最后一批 00000 占位形态）。2026-10-06 实证要点：
//   - 对最终请求体（含占位符）的确定性哈希：同一 body 三次重试 cch 完全一致；
//     重试不重复签名；请求头（含 x-client-request-id）不参与哈希。
//   - 已否证的形态：xxh64/xxh3/xxh32/rapidhash/fnv/murmur64a/md5/sha/blake 的
//     零种子/默认密钥/二进制全段 8 字节常量种子/字符串派生种子/字段派生种子，
//     在 6 组输入跨度上全部不命中——密钥被混淆/加密保护（原 2.1.37 时代为
//     xxh64+内嵌种子，当前版本已加固）。
//   - JS 模板里的字面量 `cch=00000` 是占位符，不是回退形态：JS 在正常的第一方分支
//     （`m==="firstParty"&&wi()||m==="vertex"`，wi() 即 base URL 未设置或为
//     api.anthropic.com）总是写入它，第一方请求再由 JS 之下的原生传输层替换成签名（2.1.296
//     二进制里 `cch=` 只出现在两份模板中，JS 层没有替换代码）。线上出现字面量 00000 即未被
//     替换的占位符；指向自定义 base URL 的客户端则与 cc_prompt_id 等字段一起整段省略 cch。
//
// 网关无签名模块，维持 00000 占位，属已知残留差异。
// cc_prompt_id：2.1.283+ 第一方携带，与 x-claude-code-prompt-id 头同值
// （在 buildUpstreamRequest 中从最终 body 镜像到头，保证两者一致）。
// cc_turn_origin：与 entrypoint 对应（cli/sdk/...），mimic 固定 cli。
// cc_prompt_index：用"非 tool_result 的 user 消息数 - 1"近似（网关无会话状态）；
// cc_turn_index 恒 1（单次出站请求形态）。
//
// 此 block 不带 cache_control（与真实 CLI 一致；cache breakpoint 由后续的
// Claude Code prompt block 承担）。
func buildBillingAttributionText(body []byte, cliVersion string) (string, error) {
	if cliVersion == "" {
		return "", fmt.Errorf("cliVersion required")
	}
	view := newJSONBodyView(body, nil)
	fp := computeClaudeCodeFingerprintView(view, cliVersion)
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000; cc_prompt_id=%s; cc_turn_origin=cli; cc_prompt_index=%d; cc_turn_index=1;",
		cliVersion, fp, uuid.NewString(), countUserPromptIndex(view),
	), nil
}

// countUserPromptIndex 近似 cc_prompt_index：非 tool_result 的 user 消息数 - 1。
func countUserPromptIndex(view *jsonBodyView) int {
	messages := view.get("messages")
	if !messages.IsArray() {
		return 0
	}
	count := 0
	messages.ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		content := msg.Get("content")
		// 纯文本 user 消息计入；块形态时首块为 tool_result 的是工具回传，不计入
		if content.Type == gjson.String {
			count++
			return true
		}
		if content.IsArray() {
			if first := content.Get("0.type").String(); first != "" && first != "tool_result" {
				count++
			}
		}
		return true
	})
	if count == 0 {
		return 0
	}
	return count - 1
}

// extractBillingPromptIDView 从最终请求体的 billing 块中提取 cc_prompt_id 值。
func extractBillingPromptIDView(view *jsonBodyView) string {
	text := view.get("system.0.text").String()
	const marker = "cc_prompt_id="
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := text[i+len(marker):]
	if j := strings.IndexByte(rest, ';'); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}
