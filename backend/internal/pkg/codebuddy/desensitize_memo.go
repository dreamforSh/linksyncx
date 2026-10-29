package codebuddy

import (
	"container/list"
	"sync"
)

// Literal prefilter. Every desensitization pattern is case-insensitive, and
// Go's regexp matches a letter under (?i) against its whole simple case-fold
// orbit. The literals only use letters whose orbit is their two ASCII cases
// (k and s, which also fold to U+212A and U+017F, are left out), so a match
// spells its literal in ASCII with case ignored, unless zero-width spaces sit
// between the letters (callers check for those first).

// containsFoldASCII reports whether s contains literal, a lower-case ASCII
// string, ignoring the case of ASCII letters.
func containsFoldASCII(s, literal string) bool {
	return containsAnyFoldASCII(s, []string{literal})
}

// containsAnyFoldASCII reports whether s contains one of literals (each lower
// case ASCII and starting with a letter) ignoring ASCII letter case, in one
// pass over s.
func containsAnyFoldASCII(s string, literals []string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i] | 0x20
		for _, literal := range literals {
			if c == literal[0] && len(s)-i >= len(literal) && equalFoldASCII(s[i:i+len(literal)], literal) {
				return true
			}
		}
	}
	return false
}

// equalFoldASCII compares s with the lower-case literal of the same length.
func equalFoldASCII(s, literal string) bool {
	for j := 0; j < len(literal); j++ {
		want, got := literal[j], s[j]
		if 'a' <= want && want <= 'z' {
			got |= 0x20
		}
		if got != want {
			return false
		}
	}
	return true
}

const (
	// desensitizeMemoMinBytes leaves short texts, whose passes are cheap, out
	// of the memo.
	desensitizeMemoMinBytes   = 1 << 10
	desensitizeMemoMaxEntries = 256
	desensitizeMemoMaxBytes   = 32 << 20
)

// desensitizeMemo remembers DesensitizeText results by input text. The result
// depends on the text alone, so a hit is exactly what the regex passes return.
// It is an LRU bounded by entry count and by the bytes it keeps.
type desensitizeMemo struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	lru     list.List
	bytes   int
}

type desensitizeMemoEntry struct {
	text, out string
	cost      int
}

var sharedDesensitizeMemo desensitizeMemo

func (m *desensitizeMemo) get(text string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	element, ok := m.entries[text]
	if !ok {
		return "", false
	}
	entry, ok := element.Value.(*desensitizeMemoEntry)
	if !ok {
		return "", false
	}
	m.lru.MoveToFront(element)
	return entry.out, true
}

func (m *desensitizeMemo) put(text, out string) {
	cost := len(text)
	if out == text {
		out = text // keep one copy of an unchanged text
	} else {
		cost += len(out)
	}
	// One huge prompt must not flush everything else.
	if cost > desensitizeMemoMaxBytes/8 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[text]; ok {
		return
	}
	if m.entries == nil {
		m.entries = make(map[string]*list.Element)
	}
	m.entries[text] = m.lru.PushFront(&desensitizeMemoEntry{text: text, out: out, cost: cost})
	m.bytes += cost
	for m.lru.Len() > 0 && (m.lru.Len() > desensitizeMemoMaxEntries || m.bytes > desensitizeMemoMaxBytes) {
		oldest := m.lru.Back()
		m.lru.Remove(oldest)
		if entry, ok := oldest.Value.(*desensitizeMemoEntry); ok {
			delete(m.entries, entry.text)
			m.bytes -= entry.cost
		}
	}
}
