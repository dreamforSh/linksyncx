//go:build unit

package service

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func localeTestAccount(timezone string) *Account {
	return &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: timezone}}
}

// localeCodexBody is a Codex-CLI-shaped history whose user turns carry an
// environment_context every envEvery items.
func localeCodexBody(target, envEvery int) []byte {
	env := `<environment_context>\n  <cwd>/repo</cwd>\n  <shell>zsh</shell>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>`
	var b bytes.Buffer
	b.WriteString(`{"model":"gpt-5.4","instructions":"You are Codex.","input":[`)
	b.WriteString(`{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>"}]}`)
	for i := 0; b.Len() < target; i++ {
		text := fmt.Sprintf("fix bug %d", i)
		if i%envEvery == 0 {
			text = env
		}
		b.WriteString(fmt.Sprintf(`,{"type":"message","role":"user","content":[{"type":"input_text","text":"%s"}]}`, text))
		b.WriteString(fmt.Sprintf(`,{"type":"reasoning","summary":[{"type":"summary_text","text":"step %d"}],"encrypted_content":"gAAA%s"}`, i, strings.Repeat("Zm9v", 300)))
		b.WriteString(fmt.Sprintf(`,{"type":"function_call","name":"shell","arguments":"{\"cmd\":\"rg %d\"}","call_id":"call_%d"}`, i, i))
		b.WriteString(fmt.Sprintf(`,{"type":"function_call_output","call_id":"call_%d","output":"%s"}`, i, strings.Repeat("src/x.go:1: ok <a&b>\\n", 20)))
		b.WriteString(fmt.Sprintf(`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done %d"}]}`, i))
	}
	b.WriteString(`],"store":false,"stream":true}`)
	return b.Bytes()
}

func localeDiffSeeds() []string {
	env := func(tz string) string {
		return `<environment_context><cwd>/r</cwd><timezone>` + tz + `</timezone></environment_context>`
	}
	return []string{
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}]}`,
		`{"instructions":"` + env("Asia/Shanghai") + `","input":"plain environment_context"}`,
		`{"input":[{"content":[{"text":"` + env("America/Los_Angeles") + `"}]}]}`,
		`{"input":[{"content":[{"text":"` + env("") + `"},{"text":"` + env("A<b") + `"},{"text":"<environment_context><timezone>X</timezone>"}]}]}`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `","text":"` + env("Europe/Paris") + `"}]}]}`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}],"content":[{"text":"` + env("Europe/Paris") + `"}]}]}`,
		`{"input":[{"content":"` + env("Asia/Shanghai") + `"}],"input":[{"content":[{"text":"` + env("Europe/Paris") + `"}]}]}`,
		`{"input":[{"x":1},{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}],"input":[{"content":[{"text":"` + env("Europe/Paris") + `"}]}]}`,
		`{"instructions":"` + env("Asia/Shanghai") + `","instructions":"` + env("Europe/Paris") + `"}`,
		`{"in\u0070ut":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}]}`,
		`{"input":[{"cont\u0065nt":[{"t\u0065xt":"` + env("Asia/Shanghai") + `"}]}]}`,
		`{"input":[{"content":[{"text":"` + env(`Asia/\u0053hanghai`) + `\u2028 <&>"}]}]}`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + ` \"quoted\" \u00e9"}]}]}`,
		`{"input":[[{"text":"` + env("Asia/Shanghai") + `"}],"x",{"content":{"text":"` + env("Asia/Shanghai") + `"}},{"content":[1,null,{"text":5},{"text":"` + env("Asia/Shanghai") + `"}]}]}`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}]} trailing`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}]`,
		`[{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]}]}]`,
		`{"input":[{"content":[{"text":"` + env("Asia/Shanghai") + `"}]},{"content":[{"text":"` + env("Asia/Tokyo") + `"}]}],"instructions":"` + env("UTC") + `"}`,
		string(localeCodexBody(16<<10, 3)),
		// The tag itself escaped: only the \u prefilter lets the fast path see it.
		`{"instructions":"mentions environment_context","input":[{"content":[{"text":"<environment\u005fcontext><timezone>Asia/Shanghai</timezone></environment\u005fcontext>"}]}]}`,
		// Instructions before input with both rewritten: edits must apply in body order.
		`{"instructions":"` + env("Asia/Shanghai") + `","input":[{"content":[{"text":"` + env("Asia/Tokyo") + `"}]}]}`,
	}
}

func TestNormalizeOpenAIRequestLocaleMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(20261104))
	accounts := []*Account{
		localeTestAccount("America/Los_Angeles"),
		localeTestAccount("Asia/Shanghai"),
		localeTestAccount("Not/AZone"),
		{Platform: PlatformOpenAI},
		{Platform: PlatformGrok, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Europe/Paris"}},
		nil,
	}
	fastHits := 0
	for i, seed := range localeDiffSeeds() {
		bodies := [][]byte{[]byte(seed)}
		for j := 0; j < 300; j++ {
			bodies = append(bodies, mutateJSONish(rng, []byte(seed)))
		}
		for k, body := range bodies {
			for _, account := range accounts {
				want := legacyNormalizeOpenAIRequestLocale(account, body)
				got := normalizeOpenAIRequestLocale(account, body)
				require.True(t, bytes.Equal(want, got), "seed %d body %d %q\nwant %q\ngot  %q", i, k, body, want, got)
				if account != nil && account.Platform == PlatformOpenAI {
					if _, ok := normalizeOpenAIRequestLocaleSinglePass(body, account.OpenAIRequestTimezone()); ok {
						fastHits++
					}
				}
			}
		}
	}
	require.Greater(t, fastHits, 1000, "the single-pass path must actually be exercised")
}

func BenchmarkNormalizeOpenAIRequestLocale(b *testing.B) {
	account := localeTestAccount("America/Los_Angeles")
	for _, size := range []int{150 << 10, 600 << 10, 2 << 20} {
		body := localeCodexBody(size, 20)
		for _, impl := range []struct {
			name string
			fn   func(*Account, []byte) []byte
		}{
			{"legacy", legacyNormalizeOpenAIRequestLocale},
			{"single_pass", normalizeOpenAIRequestLocale},
		} {
			b.Run(fmt.Sprintf("%s/%dKB", impl.name, len(body)>>10), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if out := impl.fn(account, body); bytes.Equal(out, body) {
						b.Fatal("expected a rewrite")
					}
				}
			})
		}
	}
}
