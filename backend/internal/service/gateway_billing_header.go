package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// ccVersionInBillingRe matches cc_version=X.Y.Z with its optional 3-hex fingerprint suffix.
// Group 1 is the version, group 2 the suffix (empty when absent).
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=(\d+\.\d+\.\d+)(?:\.([0-9a-fA-F]{3})\b)?`)

// billingFingerprintCandidateUserMessages 限定版本变化时反推客户端指纹输入所扫描的 user 消息数。
// CLI 取的是首条非 isMeta 的 user 消息，正常都在最前面，扫前几条足够且开销可控。
const billingFingerprintCandidateUserMessages = 8

// billingFingerprintSource 说明 billing 块 cc_version 指纹后缀由谁算出，决定版本同步时如何处理后缀。
type billingFingerprintSource uint8

const (
	// billingFingerprintFromClient：透传真实客户端的 billing 块。后缀由客户端在其内部消息列表上
	// 算出（跳过 isMeta 等），网关从 API body 未必能复现，因此它是权威值：版本不变时原样保留，
	// 版本变化时先反推客户端用的输入文本，再按新版本重算。
	billingFingerprintFromClient billingFingerprintSource = iota
	// billingFingerprintFromBody：OAuth mimic 路径由网关合成的 billing 块。合成时 body 尚未定型
	// （之后还会注入 [System Instructions] 消息对等改写），按最终 body 重算。
	billingFingerprintFromBody
)

// effectiveBillingUserAgent 选择写进 x-anthropic-billing-header 的 User-Agent。
// OAuth mimicry 强制使用调用方传入的 mimicUserAgent（与出站 User-Agent 头同源、
// 同一次请求内取一次复用，保证 cc_version 与出站头版本严格一致），
// 其余情况使用账号指纹 UA。
func effectiveBillingUserAgent(mimicUserAgent, tokenType string, mimicClaudeCode bool, fingerprint *Fingerprint) string {
	if tokenType == "oauth" && mimicClaudeCode {
		return mimicUserAgent
	}
	if fingerprint == nil {
		return ""
	}
	return fingerprint.UserAgent
}

// billingFingerprintSourceFor 与 effectiveBillingUserAgent 使用同一个 mimic 判定。
func billingFingerprintSourceFor(tokenType string, mimicClaudeCode bool) billingFingerprintSource {
	if tokenType == "oauth" && mimicClaudeCode {
		return billingFingerprintFromBody
	}
	return billingFingerprintFromClient
}

// syncBillingHeaderVersion rewrites cc_version in x-anthropic-billing-header
// system text blocks to match the version extracted from userAgent.
// The fingerprint suffix depends on the version, so it is kept or recomputed
// according to source (see billingFingerprintSource).
// Only touches system array blocks whose text starts with "x-anthropic-billing-header".
func syncBillingHeaderVersion(body []byte, userAgent string, source billingFingerprintSource) []byte {
	view := newJSONBodyView(body, nil)
	syncBillingHeaderVersionView(view, userAgent, source)
	return view.data
}

// syncBillingHeaderVersionView 是 syncBillingHeaderVersion 作用于 jsonBodyView 的版本。
//
// 版本号恒与出站 User-Agent 对齐（一致性铁律，透传路径同样适用：指纹 UA 会被抬到
// EffectiveCLIVersion 下限，客户端自报的旧版本不能留在 body 里）。后缀：
//   - FromBody：按最终 body 重算；
//   - FromClient：版本不变时整段保留；版本变化时反推客户端的指纹输入后按新版本重算；
//   - 原本没有后缀的不补。
//
// cch 不补也不改（见函数内注释）。
func syncBillingHeaderVersionView(view *jsonBodyView, userAgent string, source billingFingerprintSource) {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return
	}

	systemResult := view.get("system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			newText := ccVersionInBillingRe.ReplaceAllStringFunc(text.String(), func(match string) string {
				parts := ccVersionInBillingRe.FindStringSubmatch(match)
				oldVersion, oldSuffix := parts[1], parts[2]
				switch {
				case oldSuffix == "":
					return replacement
				case source == billingFingerprintFromBody:
					return replacement + "." + computeClaudeCodeFingerprintView(view, version)
				case oldVersion == version:
					return match
				default:
					return replacement + "." + rederiveClientBillingFingerprintView(view, oldVersion, oldSuffix, version)
				}
			})
			// cch 不在这里补：客户端指向自定义 base URL 时，CLI 把 cch 与 cc_prompt_id /
			// cc_turn_* 放在同一个第一方门控下一起省略，只补 cch 会拼出真实客户端不会发的
			// 混合形态。已有的 cch（客户端原生层的逐请求签名，或 mimic 合成块里的 00000
			// 占位符）原样保留：网关没有签名实现，无法重算。
			if newText != text.String() {
				_ = view.setString(fmt.Sprintf("system.%d.text", idx), newText)
			}
		}
		idx++
		return true
	})
}

// rederiveClientBillingFingerprintView 把客户端在 oldVersion 下算出的后缀 oldSuffix 换算到
// newVersion。
//
// 后缀只取决于输入文本的 3 个字符和版本号，所以先在 body 里找出能在 oldVersion 下复现
// oldSuffix 的输入（先试 CLI 规则下的首个正文块，再依次扫前几条 user 消息的 string content
// 与各 text 块），再用同一输入按 newVersion 重算。找不到时（非 CLI 客户端、或输入已不在
// body 里）退回 CLI 规则下的首个正文块。
func rederiveClientBillingFingerprintView(view *jsonBodyView, oldVersion, oldSuffix, newVersion string) string {
	want := strings.ToLower(oldSuffix)
	preferred := claudeCodeFingerprintCharsView(view)
	if claudeCodeFingerprintForChars(preferred, oldVersion) == want {
		return claudeCodeFingerprintForChars(preferred, newVersion)
	}

	matched, found := "", false
	tryText := func(text string) bool {
		chars := claudeCodeFingerprintChars(text)
		if claudeCodeFingerprintForChars(chars, oldVersion) != want {
			return true
		}
		matched, found = chars, true
		return false
	}
	userMessages := 0
	view.get("messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("role").String() != "user" {
			return true
		}
		userMessages++
		if userMessages > billingFingerprintCandidateUserMessages {
			return false
		}
		content := msg.Get("content")
		if content.Type == gjson.String {
			return tryText(content.String())
		}
		if content.IsArray() {
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() != "text" {
					return true
				}
				return tryText(block.Get("text").String())
			})
		}
		return !found
	})
	if found {
		return claudeCodeFingerprintForChars(matched, newVersion)
	}
	return claudeCodeFingerprintForChars(preferred, newVersion)
}
