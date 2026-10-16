package service

import (
	"strings"
	"testing"
)

func TestPerplexitySessionSummary(t *testing.T) {
	t.Run("email tier and expiry extracted", func(t *testing.T) {
		body := `{"user":{"email":"u@example.com","subscription_state":"max"},"expires":"2026-11-01T00:00:00Z"}`
		got := perplexitySessionSummary([]byte(body))
		if got["email"] != "u@example.com" {
			t.Errorf("email = %v", got["email"])
		}
		if got["subscription_tier"] != "max" {
			t.Errorf("subscription_tier = %v", got["subscription_tier"])
		}
		if got["session_expires"] != "2026-11-01T00:00:00Z" {
			t.Errorf("session_expires = %v", got["session_expires"])
		}
	})

	t.Run("alternate tier path", func(t *testing.T) {
		body := `{"user":{"email":"u@example.com","subscription":{"tier":"pro"}}}`
		got := perplexitySessionSummary([]byte(body))
		if got["subscription_tier"] != "pro" {
			t.Errorf("subscription_tier = %v", got["subscription_tier"])
		}
	})

	t.Run("anonymous session yields no tier", func(t *testing.T) {
		got := perplexitySessionSummary([]byte(`{"expires":"2026-11-01T00:00:00Z"}`))
		if _, ok := got["email"]; ok {
			t.Errorf("unexpected email: %v", got["email"])
		}
		if _, ok := got["subscription_tier"]; ok {
			t.Errorf("unexpected tier: %v", got["subscription_tier"])
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if got := perplexitySessionSummary([]byte(`not json`)); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func TestPerplexityCreditsSummary(t *testing.T) {
	t.Run("scalar fields only", func(t *testing.T) {
		body := `{"balance":4000,"total":10000,"currency":"credits","auto_topup":false,"history":[{"x":1}],"note":"` + strings.Repeat("a", 100) + `"}`
		got := perplexityCreditsSummary([]byte(body))
		if got["balance"] != 4000.0 {
			t.Errorf("balance = %v", got["balance"])
		}
		if got["total"] != 10000.0 {
			t.Errorf("total = %v", got["total"])
		}
		if got["currency"] != "credits" {
			t.Errorf("currency = %v", got["currency"])
		}
		if got["auto_topup"] != false {
			t.Errorf("auto_topup = %v", got["auto_topup"])
		}
		if _, ok := got["history"]; ok {
			t.Error("nested array must be skipped")
		}
		if _, ok := got["note"]; ok {
			t.Error("overlong string must be skipped")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		if got := perplexityCreditsSummary([]byte(`{`)); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func TestPerplexityAccountInfoText(t *testing.T) {
	snapshot := map[string]any{
		"session":     map[string]any{"subscription_tier": "max", "email": "u@example.com"},
		"credits":     map[string]any{"balance": 4000.0},
		"rate_limits": map[string]any{},
	}
	text := perplexityAccountInfoText(snapshot)
	for _, want := range []string{"session is valid", "tier=max", "u@example.com", "balance=4000", "rate-limit snapshot updated"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q: %s", want, text)
		}
	}

	bare := perplexityAccountInfoText(map[string]any{})
	if !strings.Contains(bare, "session is valid") || strings.Contains(bare, "tier=") {
		t.Errorf("unexpected bare text: %s", bare)
	}
}
