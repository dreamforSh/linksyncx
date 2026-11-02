package service

import "testing"

func TestRewriteOpenAIRequestEnvironment(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		timezone   string
		want       string
		wantReason string
	}{
		{
			name:       "replaces timezone only",
			text:       "<environment_context><cwd>/tmp</cwd><timezone>Asia/Shanghai</timezone></environment_context>",
			timezone:   "Asia/Tokyo",
			want:       "<environment_context><cwd>/tmp</cwd><timezone>Asia/Tokyo</timezone></environment_context>",
			wantReason: "replaced",
		},
		{
			name:       "already target",
			text:       "<environment_context><timezone>Asia/Tokyo</timezone></environment_context>",
			timezone:   "Asia/Tokyo",
			want:       "<environment_context><timezone>Asia/Tokyo</timezone></environment_context>",
			wantReason: "already_target",
		},
		{
			name:       "no environment context",
			text:       "hello",
			timezone:   "Asia/Tokyo",
			want:       "hello",
			wantReason: "no_environment_context",
		},
		{
			name:       "no timezone field",
			text:       "<environment_context><cwd>/tmp</cwd></environment_context>",
			timezone:   "Asia/Tokyo",
			want:       "<environment_context><cwd>/tmp</cwd></environment_context>",
			wantReason: "no_timezone",
		},
		{
			name:       "preserves surrounding text",
			text:       "before <environment_context><timezone>UTC</timezone></environment_context> after",
			timezone:   "Europe/Berlin",
			want:       "before <environment_context><timezone>Europe/Berlin</timezone></environment_context> after",
			wantReason: "replaced",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, reason := rewriteOpenAIRequestEnvironment(tc.text, tc.timezone)
			if got != tc.want {
				t.Fatalf("rewrite = %q, want %q", got, tc.want)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestNormalizeOpenAIRequestLocaleRewritesInputText(t *testing.T) {
	account := &Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIRequestTimezoneExtraKey: "Asia/Tokyo"}}
	body := []byte(`{"model":"gpt-6-astra","input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context><timezone>Asia/Shanghai</timezone></environment_context>"}]}]}`)
	got := string(normalizeOpenAIRequestLocale(account, body))
	if !contains(got, "Asia/Tokyo") || contains(got, "Asia/Shanghai") {
		t.Fatalf("timezone not rewritten: %s", got)
	}
}

func TestCodexTurnTicketTTL(t *testing.T) {
	account := &Account{Extra: map[string]any{
		codexTurnTicketExtraKey:      "ticket",
		codexTurnTicketAtExtraKey:    "2020-01-01T00:00:00Z",
		codexTurnTicketModelExtraKey: "gpt-6-astra",
	}}
	if _, ok := account.CodexTurnTicket("gpt-6-astra"); ok {
		t.Fatal("expired ticket must not be returned")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
