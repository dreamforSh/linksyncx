package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ccVersionInBillingRe matches the semver part of cc_version (X.Y.Z).
var ccVersionInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+`)

var ccVersionWithFingerprintInBillingRe = regexp.MustCompile(`cc_version=\d+\.\d+\.\d+\.[0-9a-fA-F]{3}\b`)

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

// syncBillingHeaderVersion rewrites cc_version in x-anthropic-billing-header
// system text blocks to match the version extracted from userAgent.
// Recompute any recognized fingerprint suffix because its input includes the version.
// Only touches system array blocks whose text starts with "x-anthropic-billing-header".
func syncBillingHeaderVersion(body []byte, userAgent string) []byte {
	version := ExtractCLIVersion(userAgent)
	if version == "" {
		return body
	}

	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() || !systemResult.IsArray() {
		return body
	}

	replacement := "cc_version=" + version
	idx := 0
	systemResult.ForEach(func(_, item gjson.Result) bool {
		text := item.Get("text")
		if text.Exists() && text.Type == gjson.String &&
			strings.HasPrefix(text.String(), "x-anthropic-billing-header") {
			fingerprintedReplacement := replacement + "." + computeClaudeCodeFingerprint(body, version)
			newText := ccVersionWithFingerprintInBillingRe.ReplaceAllString(text.String(), fingerprintedReplacement)
			newText = ccVersionInBillingRe.ReplaceAllString(newText, replacement)
			// 2.1.280 实证：cch 是第一方请求恒带的字面量占位符（无签名）；
			// 客户端指向 localhost/3P 时会省略，发往第一方前补回；已有值原样保留。
			if !strings.Contains(newText, "cch=") {
				newText = ensureBillingCCHStubText(newText)
			}
			if newText != text.String() {
				if updated, err := sjson.SetBytes(body, fmt.Sprintf("system.%d.text", idx), newText); err == nil {
					body = updated
				}
			}
		}
		idx++
		return true
	})

	return body
}

// ensureBillingCCHStubText 为 billing attribution 文本补齐 " cch=00000;" 占位符，
// 插在 cc_entrypoint 子句之后。
func ensureBillingCCHStubText(text string) string {
	i := strings.Index(text, "cc_entrypoint=")
	if i < 0 {
		return text
	}
	j := strings.IndexByte(text[i:], ';')
	if j < 0 {
		return text
	}
	at := i + j + 1
	return text[:at] + " cch=00000;" + text[at:]
}
