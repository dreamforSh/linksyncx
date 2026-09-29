// Package httpwire 在 HTTP/1.1 线级复刻真实 Claude Code（Bun fetch）的请求头形态。
//
// net/http 写请求时把 Host、User-Agent、Content-Length 提到最前，其余头按字典序
// 写出，且从不写 Connection。真实 Claude Code 2.1.283（claude.exe 内嵌 Bun 1.4.3）
// 的 fetch 按 Bun HTTP 客户端 buildRequest 的规则写头：
//
//  1. 请求行；
//  2. 调用方设置的头：按头名字节序升序（大写开头一组在前、小写一组在后），头名
//     大小写原样保留（网关层 resolveWireCasing 负责还原），同名头合并为 "a, b"；
//  3. Bun 补全的默认头，固定顺序，仅在调用方未设置时出现：Connection、
//     User-Agent、Accept、Host、Accept-Encoding，最后是 Content-Length（流式
//     请求体为 Transfer-Encoding: chunked）。
//
// /v1/messages 的线级形态（本机抓包实证）：
//
//	POST /v1/messages?beta=true HTTP/1.1
//	Accept: application/json
//	Authorization: Bearer …
//	Content-Type: application/json
//	User-Agent: claude-cli/2.1.283 (external, cli)
//	X-Stainless-Arch: x64
//	…
//	anthropic-beta: …
//	x-app: cli
//	Connection: keep-alive
//	Host: api.anthropic.com
//	Accept-Encoding: gzip, deflate, br, zstd
//	Content-Length: 112862
//
// 本包不自建连接池：Conn 包装 net/http Transport 拨出的连接，只在写方向把每个
// 请求头块按上述规则重排，请求体与读方向原样透传。连接池、keep-alive、空闲回收、
// 超时、1xx 与重试仍由 net/http（及其同构分支 imroc/req）负责。
package httpwire

import (
	"bytes"
	"errors"
	"slices"
	"strconv"
	"sync"
)

// Bun fetch 在调用方未设置时补全的默认头值（Bun 1.4.3 buildRequest；
// 均已在本机 claude.exe 2.1.283 二进制中核实）。
const (
	DefaultConnection     = "keep-alive"
	DefaultUserAgent      = "Bun/1.4.3"
	DefaultAccept         = "*/*"
	DefaultAcceptEncoding = "gzip, deflate, br, zstd"
)

// libraryUserAgents 是 Go HTTP 栈在调用方未设置 User-Agent 时自动写出的值，
// 按"未设置"处理：库指纹不能出现在线上，由 Bun 默认值替代。
var libraryUserAgents = []string{
	"Go-http-client/1.1",
	"req/v3 (https://github.com/imroc/req)",
}

var (
	errMalformedHead = errors.New("httpwire: malformed request head")
	errUnsupportedTE = errors.New("httpwire: unsupported request Transfer-Encoding")
)

var crlf = []byte("\r\n")

type field struct {
	name  []byte
	value []byte
}

// headInfo 是请求体的分帧方式，供 Conn 跟踪请求边界。
type headInfo struct {
	contentLength int64 // 无 Content-Length 时为 -1
	chunked       bool
	upgrade       bool // CONNECT / Upgrade：头块之后的字节全部原样透传
}

type headScratch struct {
	fields []field
	merged []bool
}

var scratchPool = sync.Pool{New: func() any {
	return &headScratch{fields: make([]field, 0, 32), merged: make([]bool, 0, 32)}
}}

// appendBunHead 把 net/http 写出的完整请求头块（含结尾空行）按 Bun fetch 的线级
// 规则重排后追加到 dst。
//
// 调用方显式设置、且值恰为 Bun 默认值的 Connection / User-Agent / Accept /
// Accept-Encoding 按"未设置"处理，写在默认头块的位置：真实客户端从不显式设置
// 这些值（它们由 Bun 补全），网关出于非指纹链路一致性会显式写入（如
// Accept-Encoding），线上应呈现为 Bun 补全的形态。Host 与分帧头只信 net/http
// 自己写出的第一行，调用方以原始大小写塞进 Header 的重复项一律丢弃。
func appendBunHead(dst, head []byte) ([]byte, headInfo, error) {
	info := headInfo{contentLength: -1}
	lineEnd := bytes.Index(head, crlf)
	if lineEnd <= 0 {
		return dst, info, errMalformedHead
	}
	requestLine := head[:lineEnd]
	rest := head[lineEnd+2:]

	sc, ok := scratchPool.Get().(*headScratch)
	if !ok {
		sc = &headScratch{}
	}
	defer func() {
		clear(sc.fields) // 不让池里的切片继续引用请求头内存
		sc.fields = sc.fields[:0]
		sc.merged = sc.merged[:0]
		scratchPool.Put(sc)
	}()

	var (
		host                            []byte
		hasHost, hasContentLength       bool
		hasConnection, hasUA, hasAccept bool
		hasAcceptEncoding, sawBlankLine bool
	)
	for len(rest) > 0 {
		i := bytes.Index(rest, crlf)
		if i < 0 {
			return dst, info, errMalformedHead
		}
		line := rest[:i]
		rest = rest[i+2:]
		if len(line) == 0 {
			sawBlankLine = true
			break
		}
		colon := bytes.IndexByte(line, ':')
		if colon <= 0 {
			return dst, info, errMalformedHead
		}
		name, value := line[:colon], trimLeadingSpace(line[colon+1:])
		switch {
		case equalFold(name, "Host"):
			if !hasHost {
				host, hasHost = value, true
			}
			continue
		case equalFold(name, "Content-Length"):
			if !hasContentLength {
				n, ok := parseContentLength(value)
				if !ok {
					return dst, info, errMalformedHead
				}
				info.contentLength, hasContentLength = n, true
			}
			continue
		case equalFold(name, "Transfer-Encoding"):
			if !equalFold(value, "chunked") {
				return dst, info, errUnsupportedTE
			}
			info.chunked = true
			continue
		case equalFold(name, "Connection"):
			if equalFold(value, DefaultConnection) {
				continue
			}
			hasConnection = true
		case equalFold(name, "User-Agent"):
			if isDefaultUserAgent(value) {
				continue
			}
			hasUA = true
		case equalFold(name, "Accept"):
			if string(value) == DefaultAccept {
				continue
			}
			hasAccept = true
		case equalFold(name, "Accept-Encoding"):
			if string(value) == DefaultAcceptEncoding {
				continue
			}
			hasAcceptEncoding = true
		case equalFold(name, "Upgrade"):
			info.upgrade = true
		}
		sc.fields = append(sc.fields, field{name: name, value: value})
	}
	if !sawBlankLine || len(rest) != 0 {
		return dst, info, errMalformedHead
	}
	if bytes.HasPrefix(requestLine, []byte("CONNECT ")) {
		info.upgrade = true
	}
	if info.chunked {
		info.contentLength = -1 // RFC 9112 6.3：两者并存时以 chunked 为准
	}

	// 调用方头：头名字节序升序；稳定排序保证同名多值按写出顺序合并。
	slices.SortStableFunc(sc.fields, func(a, b field) int { return bytes.Compare(a.name, b.name) })
	for range sc.fields {
		sc.merged = append(sc.merged, false)
	}

	dst = append(dst, requestLine...)
	dst = append(dst, crlf...)
	for i, f := range sc.fields {
		if sc.merged[i] {
			continue
		}
		dst = append(dst, f.name...)
		dst = append(dst, ": "...)
		dst = append(dst, f.value...)
		// Bun 的 Headers 大小写不敏感：同名（含仅大小写不同）的值合并为一行。
		for j := i + 1; j < len(sc.fields); j++ {
			if !sc.merged[j] && len(sc.fields[j].name) == len(f.name) && bytes.EqualFold(sc.fields[j].name, f.name) {
				dst = append(dst, ", "...)
				dst = append(dst, sc.fields[j].value...)
				sc.merged[j] = true
			}
		}
		dst = append(dst, crlf...)
	}

	// Bun 补全的默认头块，顺序固定。
	if !hasConnection {
		dst = appendField(dst, "Connection", DefaultConnection)
	}
	if !hasUA {
		dst = appendField(dst, "User-Agent", DefaultUserAgent)
	}
	if !hasAccept {
		dst = appendField(dst, "Accept", DefaultAccept)
	}
	if hasHost {
		dst = append(dst, "Host: "...)
		dst = append(dst, host...)
		dst = append(dst, crlf...)
	}
	if !hasAcceptEncoding {
		dst = appendField(dst, "Accept-Encoding", DefaultAcceptEncoding)
	}
	switch {
	case info.chunked:
		dst = appendField(dst, "Transfer-Encoding", "chunked")
	case info.contentLength >= 0:
		dst = append(dst, "Content-Length: "...)
		dst = strconv.AppendInt(dst, info.contentLength, 10)
		dst = append(dst, crlf...)
	}
	dst = append(dst, crlf...)
	return dst, info, nil
}

func appendField(dst []byte, name, value string) []byte {
	dst = append(dst, name...)
	dst = append(dst, ": "...)
	dst = append(dst, value...)
	return append(dst, crlf...)
}

func isDefaultUserAgent(value []byte) bool {
	if string(value) == DefaultUserAgent {
		return true
	}
	for _, ua := range libraryUserAgents {
		if string(value) == ua {
			return true
		}
	}
	return false
}

// equalFold 按 ASCII 大小写不敏感比较头名/头值与常量（不分配内存）。
func equalFold(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(b); i++ {
		if lowerASCII(b[i]) != lowerASCII(s[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if 'A' <= c && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

func trimLeadingSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}

// parseContentLength 只接受纯十进制数字（net/http 写出的形式），拒绝符号与溢出。
func parseContentLength(b []byte) (int64, bool) {
	if len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	var n int64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}
