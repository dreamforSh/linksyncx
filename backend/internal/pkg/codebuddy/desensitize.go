package codebuddy

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Desensitization adapts the system prompt so Tencent's channel risk control
// (error code 11128 "Illegal API invocation from an unapproved channel") stops
// fingerprinting the request as a third-party coding agent. The gateway matches
// string signatures on the system prompt's opening identity line; a request that
// carries, e.g., "You are Claude Code, Anthropic's official CLI for Claude" is
// rejected before the first token.
//
// Two mechanisms, applied to system-role content only (real user input is never
// touched):
//   - identity openings are rewritten to a generic CodeBuddy identity;
//   - residual vendor-identity fingerprints (the Claude / Anthropic family) get a
//     zero-width space after their first rune, so the signature no longer matches
//     while the text stays human-readable.
//
// Scope note: maiphucgiang/codebuddy2api's app/desensitize.py also cloaks
// safety/compliance terms (malware, weapon, exploit, self-harm, …) that appear in
// Claude Code's own refusal policy, purely to slip them past the upstream filter.
// That is defeating a content-safety filter, so it is intentionally NOT ported
// here — only vendor-identity fingerprints are adapted. The documented 11128
// trigger is the identity opening line, which the rewrite below already removes.
//
// The official gitStatus block is handled specially: its branch names, status
// and commit lines are preserved verbatim, only the identity line ahead of it is
// rewritten.

// zwsp is the zero-width space inserted inside a matched term.
var zwsp = string(rune(0x200b)) // zero-width space

// sensitiveTerms are the vendor-identity fingerprints (Claude / Anthropic family)
// the upstream signature-matches inside the Claude Code system prompt. Only
// identity/attribution markers are listed — never safety or harm terms.
var sensitiveTerms = []string{
	"Claude Code", "Claude Opus", "Claude Sonnet", "Claude Haiku", "Claude Fable",
	"Anthropic", "Co-Authored-By", "noreply@anthropic.com",
}

const (
	workBuddyIdentity = "You are CodeBuddy, Tencent's official CLI"
	claudeIdentity    = "You are Claude Code, Anthropic's official CLI for Claude"
)

type templateReplacement struct {
	pattern *regexp.Regexp
	target  string
}

// templateReplacements rewrite known identity openings. Order is significant:
// the "+ Agent SDK" variant must precede the bare identity so the longer line
// wins. Entry index 3 (the "Main branch" line) is only rewritten inside the
// gitStatus block, guarded by the suffix check in replaceGitStatusText.
var templateReplacements = []templateReplacement{
	{templatePattern(claudeIdentity + ", running within the Claude Agent SDK"), workBuddyIdentity},
	{templatePattern(claudeIdentity), workBuddyIdentity},
	{templatePattern("You are a Claude agent, built on Anthropic's Claude Agent SDK"), workBuddyIdentity},
	{templatePattern("Main branch (you will usually use this for PRs)"), "Main branch (you will usually use this for PR)"},
}

var (
	sensitiveTermRE = buildSensitiveTermRE()
	// gitStatusContextRE marks the start of the official Claude Code gitStatus
	// block so its verbatim content is protected from term cloaking.
	gitStatusContextRE = regexp.MustCompile(`(?im)^\s*(?:gitStatus:\s*|#\s*gitStatus\s*\n)This is the git status at the start of the conversation\.`)
	// statusOrCommitsLineRE flips the gitStatus block into protected mode: every
	// line from "Status:" / "Recent commits:" onward is left untouched.
	statusOrCommitsLineRE = regexp.MustCompile(`(?i)^(?:Status|Recent commits):`)
)

// templatePattern builds a case-insensitive matcher tolerant of whitespace and
// already-inserted zero-width spaces between words and characters, mirroring
// desensitize.py's _template_pattern (minus RE2-unsupported lookarounds).
func templatePattern(source string) *regexp.Regexp {
	words := strings.Split(source, " ")
	parts := make([]string, 0, len(words))
	for _, word := range words {
		chars := make([]string, 0, len(word))
		for _, r := range word {
			chars = append(chars, regexp.QuoteMeta(string(r)))
		}
		parts = append(parts, strings.Join(chars, zwsp+"*"))
	}
	body := strings.Join(parts, "[\\s"+zwsp+"]+")
	return regexp.MustCompile("(?i)" + body)
}

// buildSensitiveTermRE compiles the term list into one case-insensitive matcher,
// longest term first so overlapping terms (e.g. "escalation" vs "escalated
// privileges") resolve to the longer match. Word boundaries use Go's \b, whose
// \w = [0-9A-Za-z_] matches the Python negative-lookaround class exactly.
func buildSensitiveTermRE() *regexp.Regexp {
	terms := append([]string(nil), sensitiveTerms...)
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	quoted := make([]string, len(terms))
	for i, term := range terms {
		quoted[i] = regexp.QuoteMeta(term)
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`)
}

// zeroWidthSplit inserts one zero-width space after the first rune of a match.
func zeroWidthSplit(term string) string {
	runes := []rune(term)
	if len(runes) <= 1 {
		return term
	}
	return string(runes[0]) + zwsp + string(runes[1:])
}

func replaceWorkBuddyTemplates(text string) string {
	for _, tr := range templateReplacements {
		text = tr.pattern.ReplaceAllLiteralString(text, tr.target)
	}
	return text
}

// DesensitizeText applies the identity rewrites and term cloaking to a single
// string, protecting any official gitStatus block it carries.
func DesensitizeText(text string) string {
	if text == "" {
		return text
	}
	if loc := gitStatusContextRE.FindStringIndex(text); loc != nil {
		return DesensitizeText(text[:loc[0]]) + replaceGitStatusText(text[loc[0]:], false)
	}
	text = replaceWorkBuddyTemplates(text)
	return sensitiveTermRE.ReplaceAllStringFunc(text, zeroWidthSplit)
}

// replaceGitStatusText rewrites only the identity lines inside the gitStatus
// block and leaves everything from Status:/Recent commits: onward verbatim, so
// branch names and commit hashes are never cloaked or altered.
func replaceGitStatusText(text string, protected bool) string {
	var b strings.Builder
	for _, line := range splitLinesKeepEnds(text) {
		label := strings.TrimLeft(line, " \t")
		if statusOrCommitsLineRE.MatchString(label) {
			protected = true
		}
		if !protected {
			for index, tr := range templateReplacements {
				loc := tr.pattern.FindStringIndex(label)
				if loc == nil || loc[0] != 0 {
					continue
				}
				rest := label[loc[1]:]
				matched := false
				if index == 3 {
					matched = strings.HasPrefix(strings.TrimLeft(rest, " \t"), ":")
				} else {
					trimmed := strings.TrimSpace(rest)
					matched = trimmed == "" || trimmed == "."
				}
				if matched {
					indent := line[:len(line)-len(label)]
					line = indent + tr.target + rest
					break
				}
			}
		}
		_, _ = b.WriteString(line)
	}
	return b.String()
}

// splitLinesKeepEnds splits on "\n" keeping the trailing newline on each piece,
// mirroring Python's str.splitlines(keepends=True) for the "\n" case.
func splitLinesKeepEnds(text string) []string {
	if text == "" {
		return nil
	}
	return strings.SplitAfter(text, "\n")
}

// desensitizeSystemMessages rewrites the content of every system-role message in
// place. Content may be a JSON string or an array of content parts; only "text"
// parts are touched. Non-system roles and non-text parts are left untouched.
func desensitizeSystemMessages(messages []map[string]json.RawMessage) {
	for _, message := range messages {
		if rawRole(message) != "system" {
			continue
		}
		content, ok := message["content"]
		if !ok {
			continue
		}
		if updated, changed := desensitizeContent(content); changed {
			message["content"] = updated
		}
	}
}

// desensitizeContent desensitizes a message content value, returning the new
// RawMessage and whether anything changed.
func desensitizeContent(raw json.RawMessage) (json.RawMessage, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		desensitized := DesensitizeText(text)
		if desensitized == text {
			return raw, false
		}
		encoded, err := marshalNoEscape(desensitized)
		if err != nil {
			return raw, false
		}
		return encoded, true
	}

	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return raw, false
	}
	changed := false
	for _, part := range parts {
		var partType string
		_ = json.Unmarshal(part["type"], &partType)
		if partType != "text" {
			continue
		}
		var partText string
		if json.Unmarshal(part["text"], &partText) != nil {
			continue
		}
		desensitized := DesensitizeText(partText)
		if desensitized == partText {
			continue
		}
		encoded, err := marshalNoEscape(desensitized)
		if err != nil {
			continue
		}
		part["text"] = encoded
		changed = true
	}
	if !changed {
		return raw, false
	}
	encoded, err := marshalNoEscape(parts)
	if err != nil {
		return raw, false
	}
	return encoded, true
}
