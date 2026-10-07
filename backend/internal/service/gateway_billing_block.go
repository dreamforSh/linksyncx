package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// fingerprintSalt 是计算 cc_version 后缀指纹的盐值。
//
// 来源：与 Parrot src/transform/cc_mimicry.py 的 FINGERPRINT_SALT 完全一致；
// 这是真实 Claude Code CLI 抓包推导出的常量，改动会导致 fp 与 CLI 不一致，
// 进一步触发 Anthropic 的第三方检测。
const fingerprintSalt = "59cf53e54c78"

// computeClaudeCodeFingerprint 复刻真实 Claude Code CLI 的 cc_version 指纹算法：
//
//  1. 取 messages 中第一条 role=user 的纯文本（首块 text）
//  2. 取该文本的第 4、7、20 字符（不足以 '0' 补齐）
//  3. SHA256(SALT + chars + cc_version) 取 hex 前 3 字符
//
// 算法来自 Parrot src/transform/cc_mimicry.py:compute_fingerprint，与官方 CLI 字节对齐。
// 任何偏差都会导致 cc_version=X.Y.Z.{fp} 在上游侧与真实 CLI 不一致。
func computeClaudeCodeFingerprint(body []byte, version string) string {
	return computeClaudeCodeFingerprintView(newJSONBodyView(body, nil), version)
}

// computeClaudeCodeFingerprintView 是 computeClaudeCodeFingerprint 作用于 jsonBodyView 的版本。
func computeClaudeCodeFingerprintView(view *jsonBodyView, version string) string {
	firstText := extractFirstUserTextView(view)
	indices := []int{4, 7, 20}
	chars := make([]byte, 0, 3)
	for _, i := range indices {
		if i < len(firstText) {
			chars = append(chars, firstText[i])
		} else {
			chars = append(chars, '0')
		}
	}
	sum := sha256.Sum256([]byte(fingerprintSalt + string(chars) + version))
	return hex.EncodeToString(sum[:])[:3]
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
// cch 字段：2.1.290 官方构建上为逐请求内容哈希（5 位 hex）。2026-10-06 实证要点：
//   - 对最终请求体（含占位符）的确定性哈希：同一 body 三次重试 cch 完全一致；
//     重试不重复签名；请求头（含 x-client-request-id）不参与哈希。
//   - 已否证的形态：xxh64/xxh3/xxh32/rapidhash/fnv/murmur64a/md5/sha/blake 的
//     零种子/默认密钥/二进制全段 8 字节常量种子/字符串派生种子/字段派生种子，
//     在 6 组输入跨度上全部不命中——密钥被混淆/加密保护（原 2.1.37 时代为
//     xxh64+内嵌种子，当前版本已加固）。
//   - JS 模板里的字面量 `cch=00000` 是签名不可用/vertex 的回退形态
//     （`m==="firstParty"&&ni()||m==="vertex"` 分支），属客户端合法形态。
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
