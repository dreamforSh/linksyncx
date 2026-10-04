package service

import (
	"bytes"
	"fmt"
)

// anchoredPatterns 一次扫描判断是否包含任意一个模式，结果与逐个 bytes.Contains 完全一致。
//
// 这些预过滤模式大多以 `"` 开头，而 JSON 里引号极多，bytes.Contains 会退化成逐字节比较
// （2MB 请求体约 0.5ms/次）。改为用 IndexByte 跳到模式中较少见的锚点字节，再核对整个模式：
// 每个模式都含锚点字节，模式的每次出现都必然对应请求体中某个锚点位置，因此不会漏判。
type anchoredPatterns struct {
	anchor   byte
	patterns [][]byte
	offsets  []int // 锚点字节在各模式中第一次出现的位置
}

func newAnchoredPatterns(anchor byte, patterns ...[]byte) anchoredPatterns {
	ap := anchoredPatterns{anchor: anchor, patterns: patterns, offsets: make([]int, len(patterns))}
	for i, p := range patterns {
		ap.offsets[i] = bytes.IndexByte(p, anchor)
		if ap.offsets[i] < 0 {
			panic(fmt.Sprintf("newAnchoredPatterns: pattern %q lacks anchor %q", p, anchor))
		}
	}
	return ap
}

// containedIn 等价于 bytes.Contains(data, p0) || bytes.Contains(data, p1) || ...
func (ap anchoredPatterns) containedIn(data []byte) bool {
	for i := 0; i < len(data); {
		j := bytes.IndexByte(data[i:], ap.anchor)
		if j < 0 {
			return false
		}
		j += i
		for k, p := range ap.patterns {
			if start := j - ap.offsets[k]; start >= 0 && bytes.HasPrefix(data[start:], p) {
				return true
			}
		}
		i = j + 1
	}
	return false
}
