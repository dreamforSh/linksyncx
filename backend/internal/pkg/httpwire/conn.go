package httpwire

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
)

// maxHeadBytes 是单个请求头块的累积上限。net/http 写出的请求头通常只有 1–2 KiB，
// 超限说明写入的不是 HTTP/1.1 请求流，立即作废连接而不是无界缓存。
const maxHeadBytes = 1 << 20

var errHeadTooLarge = errors.New("httpwire: request head exceeds 1 MiB")

var headTerminator = []byte("\r\n\r\n")

// DialFunc 与 http.Transport.DialContext / DialTLSContext 的签名一致。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// WrapDialer 返回一个拨号函数：连接由 dial 建立，写出的请求头块按 Bun fetch
// 线级形态重排。直接用作 http.Transport 的 DialContext / DialTLSContext。
func WrapDialer(dial DialFunc) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return NewConn(conn), nil
	}
}

type writeState uint8

const (
	stateHead    writeState = iota // 等待（或正在累积）请求头块
	stateBody                      // 透传 Content-Length 定长请求体
	stateChunked                   // 透传 chunked 请求体，同时跟踪分块边界
	stateRaw                       // CONNECT / Upgrade 之后：余下字节全部透传
)

// Conn 在写方向把 net/http 写出的每个 HTTP/1.1 请求头块重排为 Bun fetch 的
// 线级形态，请求体与读方向原样透传。
//
// 请求边界由头块中的 Content-Length / Transfer-Encoding: chunked 跟踪：net/http
// 与 imroc/req 的 HTTP/1.1 客户端在同一连接上严格串行（不做 pipelining；
// Go 1.27 的 Expect: 100-continue 只要连接继续复用就一定发出请求体），字节流
// 必然是"头块 + 完整请求体"的顺序拼接。
//
// 同一次 Write 内产生的重写头块与随后的请求体合并为一次底层写（net/http 的
// bufio 首次 flush 通常就是"头 + 请求体开头"），纯请求体的 Write 零拷贝直写。
type Conn struct {
	net.Conn

	mu      sync.Mutex
	state   writeState
	pending []byte       // 跨多次 Write 尚未收齐的头块
	remain  int64        // stateBody：剩余请求体字节
	chunk   chunkScanner // stateChunked：分块边界
	err     error        // 粘滞写错误：出错即作废连接
}

// NewConn 包装 conn；读方向与其余方法直接委托给 conn。
func NewConn(conn net.Conn) *Conn {
	return &Conn{Conn: conn}
}

// Write 实现 io.Writer。成功时返回 len(p)（包括暂存在 pending 的头块字节）。
// 失败时连接即作废；返回值只区分"本次一个字节都没写到底层"（0）与"已写出部分"，
// net/http 据此判断复用连接上的请求能否安全重试（nothingWrittenError）。
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0, c.err
	}

	var (
		out    *[]byte // 本次重写过头块时启用：之后的字节都追加进来，合并为一次写
		direct int     // out 未启用时，p[:direct] 是原样透传的前缀
	)
	for i := 0; i < len(p); {
		var seg []byte
		switch c.state {
		case stateHead:
			n, head, complete, err := c.takeHead(p[i:])
			if err != nil {
				putBuffer(out)
				return c.fail(0, err)
			}
			i += n
			if !complete {
				continue // 头块未收齐，已全部暂存；此时 i == len(p)
			}
			if out == nil {
				out = getBuffer()
				*out = append(*out, p[:direct]...)
			}
			var info headInfo
			*out, info, err = appendBunHead(*out, head)
			c.releasePending()
			if err != nil {
				putBuffer(out)
				return c.fail(0, err)
			}
			c.enterBody(info)
			continue
		case stateBody:
			n := len(p) - i
			if int64(n) > c.remain {
				n = int(c.remain)
			}
			seg = p[i : i+n]
			c.remain -= int64(n)
			if c.remain == 0 {
				c.state = stateHead
			}
		case stateChunked:
			n, done, err := c.chunk.scan(p[i:])
			if err != nil {
				putBuffer(out)
				return c.fail(0, err)
			}
			seg = p[i : i+n]
			if done {
				c.state = stateHead
			}
		default: // stateRaw
			seg = p[i:]
		}
		i += len(seg)
		if out != nil {
			*out = append(*out, seg...)
		} else {
			direct = i
		}
	}

	if out != nil {
		wn, err := c.Conn.Write(*out)
		putBuffer(out)
		if err != nil {
			return c.fail(min(wn, len(p)), err)
		}
		return len(p), nil
	}
	if direct > 0 {
		if wn, err := c.Conn.Write(p[:direct]); err != nil {
			return c.fail(wn, err)
		}
	}
	return len(p), nil
}

func (c *Conn) fail(n int, err error) (int, error) {
	c.err = err
	return n, err
}

// takeHead 从 p 中收取请求头块。头块完整时返回其字节（可能指向 p 或 pending）
// 与从 p 消费的字节数；未收齐时把 p 全部暂存进 pending。
func (c *Conn) takeHead(p []byte) (consumed int, head []byte, complete bool, err error) {
	if len(c.pending) == 0 {
		if i := bytes.Index(p, headTerminator); i >= 0 {
			return i + len(headTerminator), p[:i+len(headTerminator)], true, nil
		}
		if len(p) > maxHeadBytes {
			return 0, nil, false, errHeadTooLarge
		}
		c.pending = append(c.pending, p...)
		return len(p), nil, false, nil
	}

	end := straddleEnd(c.pending, p)
	if end == 0 {
		if i := bytes.Index(p, headTerminator); i >= 0 {
			end = i + len(headTerminator)
		}
	}
	if end == 0 {
		if len(c.pending)+len(p) > maxHeadBytes {
			return 0, nil, false, errHeadTooLarge
		}
		c.pending = append(c.pending, p...)
		return len(p), nil, false, nil
	}
	if len(c.pending)+end > maxHeadBytes {
		return 0, nil, false, errHeadTooLarge
	}
	c.pending = append(c.pending, p[:end]...)
	return end, c.pending, true, nil
}

// straddleEnd 返回跨越 pending|p 边界的头块结束符在 p 中的结束位置（1..3），
// 没有则返回 0。pending 自身不含完整结束符（否则早已收齐）。
func straddleEnd(pending, p []byte) int {
	for k := 1; k <= 3 && k <= len(p); k++ {
		if bytes.HasSuffix(pending, headTerminator[:4-k]) && bytes.Equal(p[:k], headTerminator[4-k:]) {
			return k
		}
	}
	return 0
}

// releasePending 清空头块暂存；罕见的大头块不在空闲连接上长期占用内存。
func (c *Conn) releasePending() {
	if cap(c.pending) > 16<<10 {
		c.pending = nil
		return
	}
	c.pending = c.pending[:0]
}

func (c *Conn) enterBody(info headInfo) {
	switch {
	case info.upgrade:
		c.state = stateRaw
	case info.chunked:
		c.state = stateChunked
		c.chunk = chunkScanner{}
	case info.contentLength > 0:
		c.state = stateBody
		c.remain = info.contentLength
	default:
		c.state = stateHead
	}
}

var bufferPool = sync.Pool{New: func() any {
	b := make([]byte, 0, 4<<10)
	return &b
}}

func getBuffer() *[]byte {
	if b, ok := bufferPool.Get().(*[]byte); ok {
		*b = (*b)[:0]
		return b
	}
	b := make([]byte, 0, 4<<10)
	return &b
}

func putBuffer(b *[]byte) {
	if b == nil || cap(*b) > 64<<10 {
		return
	}
	bufferPool.Put(b)
}
