// Package proxyurl 提供代理 URL 的统一验证（fail-fast，无效代理不回退直连）
//
// 所有需要解析代理 URL 的地方必须通过此包的 Parse 函数。
// 直接使用 url.Parse 处理代理 URL 是被禁止的。
// 这确保了 fail-fast 行为：无效代理配置在创建时立即失败，
// 而不是在运行时静默回退到直连（产生 IP 关联风险）。
package proxyurl

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// allowedSchemes 代理协议白名单
var allowedSchemes = map[string]bool{
	"http":    true,
	"https":   true,
	"socks5":  true,
	"socks5h": true,
}

// Parse 解析并验证代理 URL。
//
// 语义:
//   - 空字符串 → ("", nil, nil)，表示直连
//   - 非空且有效 → (trimmed, *url.URL, nil)
//   - 非空但无效 → ("", nil, error)，fail-fast 不回退
//
// 验证规则:
//   - TrimSpace 后为空视为直连
//   - url.Parse 失败返回 error（不含原始 URL 及其片段，防凭据泄露）
//   - Host 为空返回 error（不回显 URL，防凭据泄露）
//   - Scheme 必须为 http/https/socks5/socks5h
//   - socks5:// 自动升级为 socks5h://（确保 DNS 由代理端解析，防止 DNS 泄漏）
func Parse(raw string) (trimmed string, parsed *url.URL, err error) {
	trimmed = strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil, nil
	}

	parsed, err = url.Parse(trimmed)
	if err != nil {
		// 不使用 %w 包装，也不直接格式化 err：*url.Error 的 Error() 会带上完整原始 URL
		// （含账号密码），这类错误会一路进入传输层错误、ops 日志与管理端提示。
		return "", nil, fmt.Errorf("invalid proxy URL: %s", parseFailureReason(err))
	}

	if parsed.Host == "" || parsed.Hostname() == "" {
		// 不回显 URL：Redacted() 只遮住 userinfo 里的密码，而漏写 "//" 的
		// "user:pass@host:port" 会被解析成 scheme=user、opaque=pass@host:port，
		// "http:///user:pass@host" 会把凭据解析进 path，Redacted() 都会原样输出。
		return "", nil, errors.New("proxy URL missing host (expected scheme://host:port)")
	}

	scheme := strings.ToLower(parsed.Scheme)
	if !allowedSchemes[scheme] {
		return "", nil, fmt.Errorf("unsupported proxy scheme %q (allowed: http, https, socks5, socks5h)", scheme)
	}

	// 自动升级 socks5 → socks5h，确保 DNS 由代理端解析，防止 DNS 泄漏。
	// Go 的 golang.org/x/net/proxy 对 socks5:// 默认在客户端本地解析 DNS，
	// 仅 socks5h:// 才将域名发送给代理端做远程 DNS 解析。
	if scheme == "socks5" {
		parsed.Scheme = "socks5h"
		trimmed = parsed.String()
	}

	return trimmed, parsed, nil
}

// quotedInputFragment 匹配 net/url 错误里经 strconv.Quote 回显的输入片段
// （非法转义、非法 host 字符、端口等）。
var quotedInputFragment = regexp.MustCompile(` ?"(?:[^"\\]|\\.)*"`)

// parseFailureReason 返回 url.Parse 失败的原因，不含原始 URL 及其任何片段：
// 只取 *url.Error 的内层错误，并去掉其中回显的输入片段——畸形 URL 里这些片段
// 可能截自密码（如密码含未转义的 "/" 时，"/" 之前的密码段会被当作端口报错）。
func parseFailureReason(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return quotedInputFragment.ReplaceAllString(err.Error(), "")
}
