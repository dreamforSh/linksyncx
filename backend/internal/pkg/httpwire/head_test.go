package httpwire

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// goRequestBytes 用 net/http 自己的写出逻辑（与 Transport 同一实现）序列化请求。
func goRequestBytes(t testing.TB, req *http.Request) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := req.Write(&buf); err != nil {
		t.Fatalf("write request: %v", err)
	}
	return buf.Bytes()
}

// splitHead 把 net/http 写出的请求拆成头块（含结尾空行）与请求体字节。
func splitHead(t testing.TB, raw []byte) (head, body []byte) {
	t.Helper()
	i := bytes.Index(raw, headTerminator)
	if i < 0 {
		t.Fatalf("no head terminator in %q", raw)
	}
	return raw[:i+4], raw[i+4:]
}

func rewriteGoHead(t testing.TB, req *http.Request) (string, headInfo) {
	t.Helper()
	head, _ := splitHead(t, goRequestBytes(t, req))
	out, info, err := appendBunHead(nil, head)
	if err != nil {
		t.Fatalf("appendBunHead: %v", err)
	}
	return string(out), info
}

func wireHead(lines ...string) string {
	return strings.Join(lines, "\r\n") + "\r\n\r\n"
}

func newRequest(t testing.TB, method, target string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// setRaw 模拟网关层 setHeaderRaw：绕过规范化，按线级大小写直写 map。
func setRaw(h http.Header, key, value string) {
	h[key] = []string{value}
}

const testBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14"

func newMessagesRequest(t testing.TB, body []byte) *http.Request {
	t.Helper()
	req := newRequest(t, http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", bytes.NewReader(body))
	h := req.Header
	setRaw(h, "Accept", "application/json")
	setRaw(h, "Authorization", "Bearer sk-ant-oat01-test")
	setRaw(h, "Content-Type", "application/json")
	setRaw(h, "User-Agent", "claude-cli/2.1.283 (external, cli)")
	setRaw(h, "X-Claude-Code-Session-Id", "6f1c1f7e-3f4c-4c55-9f55-0a4b2d0c9a11")
	setRaw(h, "X-Stainless-Arch", "x64")
	setRaw(h, "X-Stainless-Lang", "js")
	setRaw(h, "X-Stainless-OS", "Windows")
	setRaw(h, "X-Stainless-Package-Version", "0.112.1")
	setRaw(h, "X-Stainless-Retry-Count", "0")
	setRaw(h, "X-Stainless-Runtime", "node")
	setRaw(h, "X-Stainless-Runtime-Version", "v24.3.0")
	setRaw(h, "X-Stainless-Timeout", "600")
	setRaw(h, "anthropic-beta", testBeta)
	setRaw(h, "anthropic-dangerous-direct-browser-access", "true")
	setRaw(h, "anthropic-version", "2023-06-01")
	setRaw(h, "x-app", "cli")
	setRaw(h, "x-client-request-id", "0d4c2a7e-9a8b-4c1d-8e7f-112233445566")
	// 网关为非指纹链路一致性显式写入 Bun 默认值：线上应落在默认头块位置。
	setRaw(h, "Accept-Encoding", DefaultAcceptEncoding)
	return req
}

func messagesWireHead(contentLength string) string {
	return wireHead(
		"POST /v1/messages?beta=true HTTP/1.1",
		"Accept: application/json",
		"Authorization: Bearer sk-ant-oat01-test",
		"Content-Type: application/json",
		"User-Agent: claude-cli/2.1.283 (external, cli)",
		"X-Claude-Code-Session-Id: 6f1c1f7e-3f4c-4c55-9f55-0a4b2d0c9a11",
		"X-Stainless-Arch: x64",
		"X-Stainless-Lang: js",
		"X-Stainless-OS: Windows",
		"X-Stainless-Package-Version: 0.112.1",
		"X-Stainless-Retry-Count: 0",
		"X-Stainless-Runtime: node",
		"X-Stainless-Runtime-Version: v24.3.0",
		"X-Stainless-Timeout: 600",
		"anthropic-beta: "+testBeta,
		"anthropic-dangerous-direct-browser-access: true",
		"anthropic-version: 2023-06-01",
		"x-app: cli",
		"x-client-request-id: 0d4c2a7e-9a8b-4c1d-8e7f-112233445566",
		"Connection: keep-alive",
		"Host: api.anthropic.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: "+contentLength,
	)
}

// 真实 Claude Code 2.1.283（Bun fetch）/v1/messages 的线级头序，逐字节对齐。
func TestAppendBunHeadMatchesClaudeCodeMessagesCapture(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 112862)
	got, info := rewriteGoHead(t, newMessagesRequest(t, body))

	if want := messagesWireHead("112862"); got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if info.contentLength != 112862 || info.chunked || info.upgrade {
		t.Fatalf("unexpected framing %+v", info)
	}
}

// 零调用方头时 Bun 默认头块的完整顺序：Connection, User-Agent, Accept, Host,
// Accept-Encoding；GET 无请求体不写 Content-Length。net/http 的默认 UA 不得上线。
func TestAppendBunHeadFillsBunDefaultsInOrder(t *testing.T) {
	got, info := rewriteGoHead(t, newRequest(t, http.MethodGet, "https://api.anthropic.com/api/hello", nil))

	want := wireHead(
		"GET /api/hello HTTP/1.1",
		"Connection: keep-alive",
		"User-Agent: Bun/1.4.3",
		"Accept: */*",
		"Host: api.anthropic.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if info.contentLength != -1 || info.chunked {
		t.Fatalf("GET without body must carry no framing, got %+v", info)
	}
	if strings.Contains(got, "Go-http-client") {
		t.Fatal("net/http default User-Agent leaked onto the wire")
	}
}

// 控制面 token 刷新：真实 helper 只设 Content-Type / UA / anthropic-beta（无显式
// Accept），Accept: */* 与 keep-alive 属于 Bun 默认块；网关显式写入同值时位置不变。
func TestAppendBunHeadPlacesExplicitBunDefaultsInDefaultBlock(t *testing.T) {
	req := newRequest(t, http.MethodPost, "https://platform.claude.com/v1/oauth/token", strings.NewReader(`{"grant_type":"refresh_token"}`))
	setRaw(req.Header, "Content-Type", "application/json")
	setRaw(req.Header, "Accept", DefaultAccept)
	setRaw(req.Header, "Connection", "keep-alive")
	setRaw(req.Header, "anthropic-beta", "oauth-2025-04-20")
	setRaw(req.Header, "User-Agent", "anthropic-sdk-typescript/0.112.1 userOAuthProvider")

	got, _ := rewriteGoHead(t, req)
	want := wireHead(
		"POST /v1/oauth/token HTTP/1.1",
		"Content-Type: application/json",
		"User-Agent: anthropic-sdk-typescript/0.112.1 userOAuthProvider",
		"anthropic-beta: oauth-2025-04-20",
		"Connection: keep-alive",
		"Accept: */*",
		"Host: platform.claude.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: 30",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// 调用方给出非默认值时按 Bun 规则留在调用方头块（字节序位置），且不再补默认值。
func TestAppendBunHeadKeepsCallerOverridesSorted(t *testing.T) {
	req := newRequest(t, http.MethodGet, "https://example.com/x", nil)
	setRaw(req.Header, "Accept", "application/json, text/plain, */*")
	setRaw(req.Header, "Accept-Encoding", "identity")
	setRaw(req.Header, "User-Agent", "claude-code/2.1.283")
	req.Close = true // net/http 写出 Connection: close

	got, _ := rewriteGoHead(t, req)
	want := wireHead(
		"GET /x HTTP/1.1",
		"Accept: application/json, text/plain, */*",
		"Accept-Encoding: identity",
		"Connection: close",
		"User-Agent: claude-code/2.1.283",
		"Host: example.com",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// Bun 的 Headers 大小写不敏感：同名多值与仅大小写不同的重复键合并为一行。
func TestAppendBunHeadMergesDuplicateFieldNames(t *testing.T) {
	req := newRequest(t, http.MethodGet, "https://example.com/", nil)
	req.Header["X-Multi"] = []string{"a", "b"}
	req.Header["X-Case"] = []string{"upper"}
	req.Header["x-case"] = []string{"lower"}
	setRaw(req.Header, "User-Agent", "ua")
	setRaw(req.Header, "Accept", "application/json")

	got, _ := rewriteGoHead(t, req)
	want := wireHead(
		"GET / HTTP/1.1",
		"Accept: application/json",
		"User-Agent: ua",
		"X-Case: upper, lower",
		"X-Multi: a, b",
		"Connection: keep-alive",
		"Host: example.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// 调用方以原始大小写塞进 Header 的 host / content-length 会被 net/http 当普通头
// 再写一遍；只信 net/http 自己写出的那一行，重复项丢弃（否则上游 400）。
func TestAppendBunHeadDropsRawDuplicatesOfTransportHeaders(t *testing.T) {
	req := newRequest(t, http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader("{}"))
	setRaw(req.Header, "host", "evil.example")
	setRaw(req.Header, "content-length", "999")
	setRaw(req.Header, "User-Agent", "ua")
	setRaw(req.Header, "Accept", "application/json")

	got, info := rewriteGoHead(t, req)
	want := wireHead(
		"POST /v1/messages HTTP/1.1",
		"Accept: application/json",
		"User-Agent: ua",
		"Connection: keep-alive",
		"Host: api.anthropic.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Content-Length: 2",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if info.contentLength != 2 {
		t.Fatalf("framing must follow net/http's own Content-Length, got %d", info.contentLength)
	}
}

// 长度未知的请求体：net/http 用 chunked，Bun 同样把 Transfer-Encoding 写在末尾。
func TestAppendBunHeadChunkedBodyGoesToTail(t *testing.T) {
	req := newRequest(t, http.MethodPost, "https://api.anthropic.com/v1/messages", io.NopCloser(strings.NewReader("stream")))
	setRaw(req.Header, "User-Agent", "ua")
	setRaw(req.Header, "Accept", "application/json")

	got, info := rewriteGoHead(t, req)
	want := wireHead(
		"POST /v1/messages HTTP/1.1",
		"Accept: application/json",
		"User-Agent: ua",
		"Connection: keep-alive",
		"Host: api.anthropic.com",
		"Accept-Encoding: gzip, deflate, br, zstd",
		"Transfer-Encoding: chunked",
	)
	if got != want {
		t.Fatalf("wire head mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if !info.chunked || info.contentLength != -1 {
		t.Fatalf("expected chunked framing, got %+v", info)
	}
}

func TestAppendBunHeadMarksUpgradeAndConnect(t *testing.T) {
	for _, head := range []string{
		wireHead("GET /ws HTTP/1.1", "Host: example.com", "Connection: Upgrade", "Upgrade: websocket"),
		wireHead("CONNECT example.com:443 HTTP/1.1", "Host: example.com:443"),
	} {
		_, info, err := appendBunHead(nil, []byte(head))
		if err != nil {
			t.Fatalf("appendBunHead(%q): %v", head, err)
		}
		if !info.upgrade {
			t.Fatalf("expected raw passthrough after %q", head)
		}
	}
}

func TestAppendBunHeadRejectsMalformedHeads(t *testing.T) {
	for name, head := range map[string]string{
		"no request line":      "\r\n\r\n",
		"field without colon":  wireHead("GET / HTTP/1.1", "Host example.com"),
		"empty field name":     wireHead("GET / HTTP/1.1", ": value"),
		"signed length":        wireHead("POST / HTTP/1.1", "Content-Length: +5"),
		"overflowing length":   wireHead("POST / HTTP/1.1", "Content-Length: 99999999999999999999"),
		"non-chunked encoding": wireHead("POST / HTTP/1.1", "Transfer-Encoding: gzip"),
		"bytes after head":     wireHead("GET / HTTP/1.1", "Host: a") + "junk",
		"unterminated":         "GET / HTTP/1.1\r\nHost: a\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := appendBunHead(nil, []byte(head)); err == nil {
				t.Fatalf("expected error for %q", head)
			}
		})
	}
}
