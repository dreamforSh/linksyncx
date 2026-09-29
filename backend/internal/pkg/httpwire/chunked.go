package httpwire

import "errors"

var errBadChunk = errors.New("httpwire: malformed chunked request body")

type chunkState uint8

const (
	chunkSize         chunkState = iota // chunk-size 十六进制数字
	chunkExt                            // chunk-ext，跳到 CR
	chunkSizeLF                         // size 行结尾的 LF
	chunkData                           // chunk-data
	chunkDataCR                         // 数据后的 CR
	chunkDataLF                         // 数据后的 LF
	chunkTrailerStart                   // trailer 行首：CR 表示结束空行
	chunkTrailerLine                    // trailer 行内，跳到 LF
	chunkEndLF                          // 结束空行的 LF
)

// chunkScanner 跟踪 chunked 请求体（RFC 9112 7.1）的边界。字节本身由 Conn 原样
// 透传，这里只判断请求体在哪里结束，以便从下一个字节起识别下一个请求头块。
type chunkScanner struct {
	state  chunkState
	size   int64 // 解析中的 chunk-size，进入 chunkData 后为剩余数据字节
	digits int
}

// scan 消费 p 中属于当前请求体的字节，返回消费数，以及请求体是否已在其中结束。
func (s *chunkScanner) scan(p []byte) (n int, done bool, err error) {
	for n < len(p) {
		if s.state == chunkData {
			k := int64(len(p) - n)
			if k > s.size {
				k = s.size
			}
			n += int(k)
			s.size -= k
			if s.size == 0 {
				s.state = chunkDataCR
			}
			continue
		}

		b := p[n]
		n++
		switch s.state {
		case chunkSize:
			switch v := hexValue(b); {
			case v >= 0:
				if s.digits == 15 { // 超过 15 位十六进制可能溢出 int64
					return n, false, errBadChunk
				}
				s.size = s.size<<4 | int64(v)
				s.digits++
			case b == ';' && s.digits > 0:
				s.state = chunkExt
			case b == '\r' && s.digits > 0:
				s.state = chunkSizeLF
			default:
				return n, false, errBadChunk
			}
		case chunkExt:
			if b == '\r' {
				s.state = chunkSizeLF
			}
		case chunkSizeLF:
			if b != '\n' {
				return n, false, errBadChunk
			}
			s.digits = 0
			if s.size == 0 {
				s.state = chunkTrailerStart
			} else {
				s.state = chunkData
			}
		case chunkDataCR:
			if b != '\r' {
				return n, false, errBadChunk
			}
			s.state = chunkDataLF
		case chunkDataLF:
			if b != '\n' {
				return n, false, errBadChunk
			}
			s.state = chunkSize
		case chunkTrailerStart:
			if b == '\r' {
				s.state = chunkEndLF
			} else {
				s.state = chunkTrailerLine
			}
		case chunkTrailerLine:
			if b == '\n' {
				s.state = chunkTrailerStart
			}
		case chunkEndLF:
			if b != '\n' {
				return n, false, errBadChunk
			}
			*s = chunkScanner{}
			return n, true, nil
		}
	}
	return n, false, nil
}

func hexValue(b byte) int {
	switch {
	case '0' <= b && b <= '9':
		return int(b - '0')
	case 'a' <= b && b <= 'f':
		return int(b-'a') + 10
	case 'A' <= b && b <= 'F':
		return int(b-'A') + 10
	}
	return -1
}
