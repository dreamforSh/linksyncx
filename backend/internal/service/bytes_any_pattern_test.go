//go:build unit

package service

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

var anchoredPatternSetsUnderTest = []anchoredPatterns{emptyTextBlockPatterns, thinkingContentPatterns, webSearchHistoryBlockPatterns,
	outputConfigPattern, cacheControlKeyPattern, lowercaseLetterEscapePatterns}

func requireAnchoredPatternsMatchContains(t *testing.T, data []byte) {
	t.Helper()
	for _, ap := range anchoredPatternSetsUnderTest {
		want := false
		for _, p := range ap.patterns {
			want = want || bytes.Contains(data, p)
		}
		require.Equal(t, want, ap.containedIn(data), "anchor %q data %.200q", ap.anchor, data)
	}
}

// anchoredPatternFragments 由各模式的前缀、后缀与锚点拼出大量"差一点命中"的输入。
func anchoredPatternFragments() [][]byte {
	var out [][]byte
	for _, ap := range anchoredPatternSetsUnderTest {
		for _, p := range ap.patterns {
			out = append(out, p, p[:len(p)-1], p[1:], p[:len(p)/2], p[len(p)/2:])
		}
		out = append(out, []byte{ap.anchor})
	}
	return append(out, []byte(`"`), []byte(` `), []byte(`:`), []byte(`\"`), []byte("\xff"), []byte(`{"type":"text","text":"x"}`))
}

func TestAnchoredPatterns_MatchesContains(t *testing.T) {
	frags := anchoredPatternFragments()
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 50000; i++ {
		var data []byte
		for j, n := 0, r.Intn(8); j < n; j++ {
			data = append(data, frags[r.Intn(len(frags))]...)
		}
		requireAnchoredPatternsMatchContains(t, data)
	}
	requireAnchoredPatternsMatchContains(t, nil)
	requireAnchoredPatternsMatchContains(t, []byte(`"text"x:""`))
}

func FuzzAnchoredPatterns(f *testing.F) {
	for _, frag := range anchoredPatternFragments() {
		f.Add(frag)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		requireAnchoredPatternsMatchContains(t, data)
	})
}

func TestNewAnchoredPatterns_RequiresAnchorInEveryPattern(t *testing.T) {
	require.Panics(t, func() { newAnchoredPatterns('x', []byte("text"), []byte("thinking")) })
}
