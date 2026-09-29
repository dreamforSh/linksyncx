package httpwire

import (
	"bytes"
	"testing"
)

type discardConn struct{ recordConn }

func (*discardConn) Write(p []byte) (int, error) { return len(p), nil }

// 典型 /v1/messages 请求头块的重排开销。
func BenchmarkAppendBunHead(b *testing.B) {
	head, _ := splitHead(b, goRequestBytes(b, newMessagesRequest(b, []byte("{}"))))
	dst := make([]byte, 0, 4096)
	b.SetBytes(int64(len(head)))
	b.ReportAllocs()
	for b.Loop() {
		var err error
		if dst, _, err = appendBunHead(dst[:0], head); err != nil {
			b.Fatal(err)
		}
	}
}

// 按 net/http 的写出节奏（bufio 首次 4 KiB flush，之后 32 KiB 一块）写一个
// 112 KiB 请求，对比直写与经 Conn 重排的开销。
func BenchmarkConnWriteMessagesRequest(b *testing.B) {
	raw := goRequestBytes(b, newMessagesRequest(b, bytes.Repeat([]byte("x"), 112862)))
	write := func(w interface{ Write([]byte) (int, error) }) {
		first := raw[:4096]
		if _, err := w.Write(first); err != nil {
			b.Fatal(err)
		}
		for rest := raw[4096:]; len(rest) > 0; {
			n := min(len(rest), 32<<10)
			if _, err := w.Write(rest[:n]); err != nil {
				b.Fatal(err)
			}
			rest = rest[n:]
		}
	}

	b.Run("direct", func(b *testing.B) {
		b.SetBytes(int64(len(raw)))
		b.ReportAllocs()
		for b.Loop() {
			write(&discardConn{})
		}
	})
	b.Run("httpwire", func(b *testing.B) {
		c := NewConn(&discardConn{})
		b.SetBytes(int64(len(raw)))
		b.ReportAllocs()
		for b.Loop() {
			write(c)
		}
	})
}
