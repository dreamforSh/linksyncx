package httpwire

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordConn 是记录每次底层写入的内存 net.Conn。
type recordConn struct {
	mu     sync.Mutex
	writes [][]byte // 每次写入的切片本身（用于零拷贝断言，内容可能被池复用）
	copies [][]byte // 每次写入内容的副本
	buf    bytes.Buffer
	err    error // 非 nil 时 Write 返回 (0, err)
}

func (c *recordConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}
	c.writes = append(c.writes, p)
	c.copies = append(c.copies, bytes.Clone(p))
	_, _ = c.buf.Write(p)
	return len(p), nil
}

func (c *recordConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *recordConn) Close() error                     { return nil }
func (c *recordConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *recordConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *recordConn) SetDeadline(time.Time) error      { return nil }
func (c *recordConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordConn) SetWriteDeadline(time.Time) error { return nil }

// keepAliveStream 构造同一连接上串行的四个请求：定长请求体、带 trailer 的
// chunked 请求体、无请求体 GET、超过 bufio 缓冲的大请求体。返回 net/http 写出
// 的原始字节流与期望的线级字节流。
func keepAliveStream(t testing.TB) (input, want []byte) {
	t.Helper()

	fixed := newMessagesRequest(t, []byte(`{"model":"claude-fable-5-1","messages":[]}`))

	chunked := newRequest(t, http.MethodPost, "https://api.anthropic.com/v1/messages/count_tokens?beta=true",
		io.NopCloser(io.MultiReader(strings.NewReader("first-part,"), strings.NewReader("second-part"))))
	setRaw(chunked.Header, "User-Agent", "claude-cli/2.1.283 (external, cli)")
	setRaw(chunked.Header, "Accept", "application/json")
	chunked.Trailer = http.Header{"X-Checksum": {"abc"}}

	get := newRequest(t, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	setRaw(get.Header, "Authorization", "Bearer at")

	large := newMessagesRequest(t, bytes.Repeat([]byte("0123456789abcdef"), 16<<10)) // 256 KiB

	for _, req := range []*http.Request{fixed, chunked, get, large} {
		raw := goRequestBytes(t, req)
		head, body := splitHead(t, raw)
		rewritten, _, err := appendBunHead(nil, head)
		if err != nil {
			t.Fatalf("appendBunHead: %v", err)
		}
		input = append(input, raw...)
		want = append(want, rewritten...)
		want = append(want, body...)
	}
	return input, want
}

func writeInPieces(t testing.TB, c *Conn, stream []byte, piece func(remaining int) int) {
	t.Helper()
	for len(stream) > 0 {
		n := min(piece(len(stream)), len(stream))
		wn, err := c.Write(stream[:n])
		if err != nil || wn != n {
			t.Fatalf("Write(%d bytes) = %d, %v", n, wn, err)
		}
		stream = stream[n:]
	}
}

// 无论 net/http 如何切分写入（逐字节、跨头块边界、头与体混合），线上字节流都
// 必须是"每个请求头块重排 + 请求体逐字节不变"。
func TestConnRewritesEveryRequestOnKeepAliveStream(t *testing.T) {
	input, want := keepAliveStream(t)

	splits := map[string]func(int) int{
		"whole stream": func(n int) int { return n },
		"1 byte":       func(int) int { return 1 },
		"2 bytes":      func(int) int { return 2 },
		"3 bytes":      func(int) int { return 3 },
		"bufio 4KiB":   func(int) int { return 4096 },
		"io.Copy 32KiB": func(int) int {
			return 32 << 10
		},
	}
	for seed := uint64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed))
		splits[fmt.Sprintf("random seed %d", seed)] = func(int) int { return 1 + rng.IntN(700) }
	}

	for name, piece := range splits {
		t.Run(name, func(t *testing.T) {
			rec := &recordConn{}
			writeInPieces(t, NewConn(rec), input, piece)
			if got := rec.buf.Bytes(); !bytes.Equal(got, want) {
				t.Fatalf("wire stream mismatch (%d vs %d bytes)\n got prefix: %q", len(got), len(want), got[:min(len(got), 600)])
			}
		})
	}
}

// 同一次 Write 里的"头块 + 请求体开头"合并为一次底层写；之后纯请求体的 Write
// 零拷贝直接透传调用方的切片。
func TestConnCoalescesHeadWithBodyAndPassesBodyThrough(t *testing.T) {
	raw := goRequestBytes(t, newMessagesRequest(t, bytes.Repeat([]byte("b"), 10000)))
	headEnd := bytes.Index(raw, headTerminator) + 4
	first, rest := raw[:headEnd+100], raw[headEnd+100:]

	rec := &recordConn{}
	c := NewConn(rec)
	if _, err := c.Write(first); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(rest); err != nil {
		t.Fatal(err)
	}

	if len(rec.writes) != 2 {
		t.Fatalf("expected 2 underlying writes, got %d", len(rec.writes))
	}
	if !bytes.HasSuffix(rec.copies[0], raw[headEnd:headEnd+100]) || !bytes.HasPrefix(rec.copies[0], []byte("POST ")) {
		t.Fatal("head and first body bytes were not coalesced into one write")
	}
	if &rec.writes[1][0] != &rest[0] || len(rec.writes[1]) != len(rest) {
		t.Fatal("pure body write must pass the caller's slice through without copying")
	}
}

// 底层写失败且一个字节都没写出时返回 0：net/http 据此把复用连接上的失败识别为
// nothingWrittenError 并换连接重试；错误是粘滞的。
func TestConnWriteFailureReportsNothingWrittenAndSticks(t *testing.T) {
	boom := errors.New("broken pipe")
	rec := &recordConn{err: boom}
	c := NewConn(rec)

	raw := goRequestBytes(t, newMessagesRequest(t, []byte("{}")))
	n, err := c.Write(raw)
	if !errors.Is(err, boom) || n != 0 {
		t.Fatalf("Write = %d, %v; want 0, %v", n, err, boom)
	}
	rec.err = nil
	if _, err := c.Write(raw); !errors.Is(err, boom) {
		t.Fatalf("write error must be sticky, got %v", err)
	}
}

func TestConnRejectsOversizedHead(t *testing.T) {
	c := NewConn(&recordConn{})
	huge := append([]byte("GET / HTTP/1.1\r\nX-Big: "), bytes.Repeat([]byte("a"), maxHeadBytes)...)
	if _, err := c.Write(huge[:len(huge)/2]); err != nil {
		t.Fatalf("first half must be buffered: %v", err)
	}
	if _, err := c.Write(huge[len(huge)/2:]); !errors.Is(err, errHeadTooLarge) {
		t.Fatalf("expected errHeadTooLarge, got %v", err)
	}
}

func TestConnRejectsMalformedChunkedBody(t *testing.T) {
	rec := &recordConn{}
	c := NewConn(rec)
	head := wireHead("POST / HTTP/1.1", "Host: a", "Transfer-Encoding: chunked")
	if _, err := c.Write([]byte(head + "zz\r\n")); !errors.Is(err, errBadChunk) {
		t.Fatalf("expected errBadChunk, got %v", err)
	}
}

// 协议升级之后连接交给上层协议：余下字节不能再按请求头解析。
func TestConnPassesThroughAfterUpgrade(t *testing.T) {
	rec := &recordConn{}
	c := NewConn(rec)
	head := wireHead("GET /ws HTTP/1.1", "Host: example.com", "Connection: Upgrade", "Upgrade: websocket")
	frames := "\x81\x05hello\r\n\r\nnot-a-head"
	if _, err := c.Write([]byte(head)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(frames)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rec.buf.String(), frames) {
		t.Fatalf("frames after upgrade must pass through verbatim, got %q", rec.buf.String())
	}
}
