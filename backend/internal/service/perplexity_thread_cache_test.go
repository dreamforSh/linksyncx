//go:build unit

package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const (
	perplexityThreadTestBackend = "11111111-1111-4111-8111-111111111111"
	perplexityThreadTestChild   = "22222222-2222-4222-8222-222222222222"
	perplexityThreadTestAnswer  = "fake assistant answer\n\nSources:\n[1] https://source.example.invalid/thread-test"
)

func perplexityThreadTestAccount() *Account {
	proxyID := int64(17)
	return &Account{
		ID: 23, Platform: PlatformPerplexity, Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "fake-thread-cookie-secret", "csrf_token": "fake-thread-csrf-secret",
			"base_url":                   "https://perplexity.example.invalid",
			credKeyHeaderOverrideEnabled: true,
			credKeyHeaderOverrides:       map[string]string{"x-thread-test": "fake-header-secret"},
		},
		ProxyID: &proxyID,
		Proxy: &Proxy{
			ID: proxyID, Protocol: "http", Host: "proxy.example.invalid", Port: 18080,
			Username: "fake-proxy-user", Password: "fake-proxy-secret",
		},
	}
}

func perplexityThreadTestBody(t testing.TB, messages ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"messages": messages})
	require.NoError(t, err)
	return body
}

func perplexityThreadTestTurn(role, text string) map[string]any {
	return map[string]any{"role": role, "content": text}
}

func perplexityThreadTestFollowup(t testing.TB, answer, question string) []byte {
	t.Helper()
	return perplexityThreadTestBody(t,
		perplexityThreadTestTurn("user", "fake first question"),
		perplexityThreadTestTurn("assistant", answer),
		perplexityThreadTestTurn("user", question),
	)
}

func perplexityThreadTestSeed(t testing.TB, svc *OpenAIGatewayService, account *Account, opts perplexity.RequestOptions) *perplexityContinuation {
	t.Helper()
	first := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot,
		perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "fake first question")), opts)
	require.NotNil(t, first)
	require.Empty(t, first.backendUUID, "a new conversation must send full history")
	require.NoError(t, uuid.Validate(first.contextUUID))
	first.complete(perplexityCompletion{Content: perplexityThreadTestAnswer, BackendUUID: perplexityThreadTestBackend})
	return first
}

func perplexityThreadTestRecords(cache *perplexityThreadCache) map[[32]byte]perplexityThreadRecord {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	out := make(map[[32]byte]perplexityThreadRecord, len(cache.entries))
	for key, record := range cache.entries {
		out[key] = record
	}
	return out
}

func TestPerplexityThreadFirstCompletionAndSingleUseFollowup(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	opts := perplexity.DefaultRequestOptions()
	first := perplexityThreadTestSeed(t, svc, account, opts)
	require.Len(t, perplexityThreadTestRecords(first.store), 1)

	body := perplexityThreadTestFollowup(t, perplexityThreadTestAnswer, "  fake second question  ")
	second := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
	require.NotNil(t, second)
	require.Equal(t, perplexityThreadTestBackend, second.backendUUID)
	require.Equal(t, first.contextUUID, second.contextUUID)
	require.Equal(t, "fake second question", second.query)
	require.Empty(t, perplexityThreadTestRecords(first.store), "taking a parent must consume it atomically")

	branch := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
	require.NotNil(t, branch)
	require.Empty(t, branch.backendUUID, "a second claim must fall back to full history")
	require.NotEqual(t, first.contextUUID, branch.contextUUID)

	second.complete(perplexityCompletion{Content: "fake second answer", BackendUUID: perplexityThreadTestChild})
	thirdBody := perplexityThreadTestBody(t,
		perplexityThreadTestTurn("user", "fake first question"),
		perplexityThreadTestTurn("assistant", perplexityThreadTestAnswer),
		perplexityThreadTestTurn("user", "  fake second question  "),
		perplexityThreadTestTurn("assistant", "fake second answer"),
		perplexityThreadTestTurn("user", "fake third question"),
	)
	third := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, thirdBody, opts)
	require.NotNil(t, third)
	require.Equal(t, perplexityThreadTestChild, third.backendUUID)
	require.Equal(t, first.contextUUID, third.contextUUID)
	require.Equal(t, "fake third question", third.query)
}

func TestPerplexityThreadConcurrentClaimsHaveExactlyOneNativeFollowup(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	opts := perplexity.DefaultRequestOptions()
	first := perplexityThreadTestSeed(t, svc, account, opts)
	body := perplexityThreadTestFollowup(t, perplexityThreadTestAnswer, "fake concurrent question")

	const claimants = 100
	start := make(chan struct{})
	plans := make(chan *perplexityContinuation, claimants)
	var wg sync.WaitGroup
	for range claimants {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			plans <- svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
		}()
	}
	close(start)
	wg.Wait()
	close(plans)

	native, full := 0, 0
	for plan := range plans {
		require.NotNil(t, plan)
		if plan.backendUUID == "" {
			full++
			require.NotEqual(t, first.contextUUID, plan.contextUUID)
			continue
		}
		native++
		require.Equal(t, perplexityThreadTestBackend, plan.backendUUID)
		require.Equal(t, first.contextUUID, plan.contextUUID)
		require.Equal(t, "fake concurrent question", plan.query)
	}
	require.Equal(t, 1, native)
	require.Equal(t, claimants-1, full)
	require.Empty(t, perplexityThreadTestRecords(first.store))
}

func TestPerplexityThreadScopeIsolation(t *testing.T) {
	type scopeInput struct {
		account *Account
		apiKey  int64
		session string
		model   string
		mode    string
		opts    perplexity.RequestOptions
	}
	for _, test := range []struct {
		name   string
		change func(*scopeInput)
	}{
		{"tenant API key", func(in *scopeInput) { in.apiKey++ }},
		{"account", func(in *scopeInput) { in.account.ID++ }},
		{"cookie", func(in *scopeInput) { in.account.Credentials["api_key"] = "fake-other-cookie" }},
		{"CSRF", func(in *scopeInput) { in.account.Credentials["csrf_token"] = "fake-other-csrf" }},
		{"base URL", func(in *scopeInput) { in.account.Credentials["base_url"] = "https://other.example.invalid" }},
		{"proxy ID", func(in *scopeInput) { id := int64(18); in.account.ProxyID = &id; in.account.Proxy.ID = id }},
		{"proxy removed", func(in *scopeInput) { in.account.ProxyID = nil; in.account.Proxy = nil }},
		{"proxy host same ID", func(in *scopeInput) { in.account.Proxy.Host = "other-proxy.example.invalid" }},
		{"proxy credentials same ID", func(in *scopeInput) { in.account.Proxy.Password = "fake-other-proxy-secret" }},
		{"header value", func(in *scopeInput) {
			in.account.Credentials[credKeyHeaderOverrides] = map[string]string{"x-thread-test": "fake-other-header"}
		}},
		{"headers disabled", func(in *scopeInput) { in.account.Credentials[credKeyHeaderOverrideEnabled] = false }},
		{"model", func(in *scopeInput) { in.model = "pplx_reasoning" }},
		{"mode", func(in *scopeInput) { in.mode = perplexity.ModeConcise }},
		{"sources", func(in *scopeInput) { in.opts.Sources = []string{"scholar"} }},
		{"language", func(in *scopeInput) { in.opts.Language = "zh-CN" }},
		{"timezone", func(in *scopeInput) { in.opts.Timezone = "Asia/Shanghai" }},
		{"incognito", func(in *scopeInput) { in.opts.Incognito = true }},
		{"source footer option", func(in *scopeInput) { in.opts.Local.IncludeSources = false }},
		{"progress option", func(in *scopeInput) { in.opts.Local.IncludeSearchProgress = false }},
		{"strict option", func(in *scopeInput) { in.opts.Local.StrictParameters = true }},
		{"session", func(in *scopeInput) { in.session = "fake-other-session" }},
		{"session removed", func(in *scopeInput) { in.session = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			opts := perplexity.DefaultRequestOptions()
			original := perplexityThreadTestAccount()
			first := perplexityThreadTestSeed(t, svc, original, opts)
			in := scopeInput{
				account: perplexityThreadTestAccount(), apiKey: 41, session: "fake-session",
				model: "pplx_pro", mode: perplexity.ModeCopilot, opts: perplexity.DefaultRequestOptions(),
			}
			test.change(&in)
			body := perplexityThreadTestFollowup(t, perplexityThreadTestAnswer, "fake second question")
			miss := svc.preparePerplexityContinuation(in.account, in.apiKey, in.session, in.model, in.mode, body, in.opts)
			if in.opts.Incognito {
				// 无痕会话绝不落本地缓存：不产生续链计划，也不消费他人句柄。
				require.Nil(t, miss, "incognito conversations must never be cached or chained")
				return
			}
			require.NotNil(t, miss)
			require.Empty(t, miss.backendUUID, "changed scope must replay full history")
			require.NotEqual(t, first.contextUUID, miss.contextUUID)
			// A miss in another scope must not consume the original tenant's handle.
			hit := svc.preparePerplexityContinuation(original, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
			require.NotNil(t, hit)
			require.Equal(t, perplexityThreadTestBackend, hit.backendUUID)
		})
	}
}

func TestPerplexityThreadHistoryRequiresExactVisiblePrefix(t *testing.T) {
	for _, test := range []struct {
		name string
		body func(*testing.T) []byte
	}{
		{"missing footer", func(t *testing.T) []byte { return perplexityThreadTestFollowup(t, "fake assistant answer", "next") }},
		{"rewritten answer", func(t *testing.T) []byte {
			return perplexityThreadTestFollowup(t, perplexityThreadTestAnswer+" changed", "next")
		}},
		{"edited first question", func(t *testing.T) []byte {
			return perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "edited first question"),
				perplexityThreadTestTurn("assistant", perplexityThreadTestAnswer), perplexityThreadTestTurn("user", "next"))
		}},
		{"compressed history", func(t *testing.T) []byte {
			return perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "summary of earlier turns"),
				perplexityThreadTestTurn("assistant", perplexityThreadTestAnswer), perplexityThreadTestTurn("user", "next"))
		}},
		{"history dropped", func(t *testing.T) []byte {
			return perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "next"))
		}},
		{"history whitespace changed", func(t *testing.T) []byte {
			return perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "fake first question "),
				perplexityThreadTestTurn("assistant", perplexityThreadTestAnswer), perplexityThreadTestTurn("user", "next"))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := perplexityThreadTestAccount()
			opts := perplexity.DefaultRequestOptions()
			perplexityThreadTestSeed(t, svc, account, opts)
			plan := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, test.body(t), opts)
			require.NotNil(t, plan)
			require.Empty(t, plan.backendUUID)
			require.Len(t, perplexityThreadTestRecords(plan.store), 1, "a branch mismatch must not consume its parent")
		})
	}
}

func TestPerplexityThreadTextPartArraysPreserveHistory(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	opts := perplexity.DefaultRequestOptions()
	perplexityThreadTestSeed(t, svc, account, opts)
	body := perplexityThreadTestBody(t,
		map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "fake first "}, {"type": "text", "text": "question"}}},
		map[string]any{"role": "assistant", "content": []map[string]any{{"type": "text", "text": perplexityThreadTestAnswer}}},
		map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "next "}, {"type": "text", "text": "question"}}},
	)
	plan := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
	require.NotNil(t, plan)
	require.Equal(t, perplexityThreadTestBackend, plan.backendUUID)
	require.Equal(t, "next question", plan.query)
}

func TestPerplexityThreadIneligibleHistoryDoesNotConsumeParent(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"developer","content":"instructions"},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"tool","content":"result"},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"function","content":"result"},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"user","name":"someone","content":"next"}]}`,
		`{"messages":[{"role":"assistant","content":"answer","tool_calls":[{"id":"fake-call"}]},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"assistant","content":"answer","function_call":{"name":"f"}},{"role":"user","content":"next"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"next"},{"type":"image_url","image_url":{"url":"https://image.example.invalid/test.png"}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"input_text","text":"next"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"text","text":17}]}]}`,
		`{"messages":[{"role":"user","content":{"text":"next"}}]}`,
		`{"messages":[{"role":"user","content":null}]}`,
		`{"messages":[{"role":"user","content":"   "}]}`,
		`{"messages":[{"role":"assistant","content":"last turn is not a user"}]}`,
		`{"messages":[]}`, `{"messages":{}}`, `{}`, `null`, `not json`,
		`{"tools":[{"type":"function","function":{"name":"fake_tool"}}],"messages":[{"role":"user","content":"next"}]}`,
		`{"functions":[{"name":"fake_tool"}],"messages":[{"role":"user","content":"next"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account := perplexityThreadTestAccount()
			opts := perplexity.DefaultRequestOptions()
			first := perplexityThreadTestSeed(t, svc, account, opts)
			plan := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, []byte(body), opts)
			require.Nil(t, plan)
			require.Len(t, perplexityThreadTestRecords(first.store), 1)
		})
	}
}

func TestPerplexityThreadEligibilityLimits(t *testing.T) {
	baseBody := perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "fake question"))
	for _, test := range []struct {
		name   string
		change func(*Account, *int64, *string, *perplexity.RequestOptions) *Account
	}{
		{"missing account", func(_ *Account, _ *int64, _ *string, _ *perplexity.RequestOptions) *Account { return nil }},
		{"account without ID", func(a *Account, _ *int64, _ *string, _ *perplexity.RequestOptions) *Account { a.ID = 0; return a }},
		{"missing tenant", func(a *Account, key *int64, _ *string, _ *perplexity.RequestOptions) *Account { *key = 0; return a }},
		{"native followups disabled", func(a *Account, _ *int64, _ *string, o *perplexity.RequestOptions) *Account {
			o.Local.NativeFollowups = false
			return a
		}},
		{"computer", func(a *Account, _ *int64, mode *string, _ *perplexity.RequestOptions) *Account {
			*mode = perplexity.ModeComputer
			return a
		}},
		{"attachments", func(a *Account, _ *int64, _ *string, o *perplexity.RequestOptions) *Account {
			o.Internal.Attachments = []string{"fake-upload-id"}
			return a
		}},
		{"assigned proxy relation missing", func(a *Account, _ *int64, _ *string, _ *perplexity.RequestOptions) *Account {
			a.Proxy = nil
			return a
		}},
		{"assigned proxy relation mismatched", func(a *Account, _ *int64, _ *string, _ *perplexity.RequestOptions) *Account {
			a.Proxy.ID++
			return a
		}},
		{"unserializable credentials", func(a *Account, _ *int64, _ *string, _ *perplexity.RequestOptions) *Account {
			a.Credentials["invalid"] = make(chan int)
			return a
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			account, apiKey, mode, opts := perplexityThreadTestAccount(), int64(41), perplexity.ModeCopilot, perplexity.DefaultRequestOptions()
			account = test.change(account, &apiKey, &mode, &opts)
			require.Nil(t, svc.preparePerplexityContinuation(account, apiKey, "fake-session", "pplx_pro", mode, baseBody, opts))
		})
	}

	t.Run("body size boundary", func(t *testing.T) {
		svc := &OpenAIGatewayService{}
		body := perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "x"))
		body = append(body, []byte(strings.Repeat(" ", perplexityThreadBodyLimit-len(body)))...)
		require.Len(t, body, perplexityThreadBodyLimit)
		require.NotNil(t, svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, perplexity.DefaultRequestOptions()))
		require.Nil(t, svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, append(body, ' '), perplexity.DefaultRequestOptions()))
	})

	t.Run("message count boundary", func(t *testing.T) {
		messages := make([]map[string]any, perplexityThreadTurnLimit)
		for i := range messages {
			messages[i] = perplexityThreadTestTurn("user", fmt.Sprintf("fake question %d", i))
		}
		svc := &OpenAIGatewayService{}
		require.NotNil(t, svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, perplexityThreadTestBody(t, messages...), perplexity.DefaultRequestOptions()))
		messages = append(messages, perplexityThreadTestTurn("user", "over limit"))
		require.Nil(t, svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, perplexityThreadTestBody(t, messages...), perplexity.DefaultRequestOptions()))
	})
}

func TestPerplexityThreadUnknownHandleAndExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cache := &perplexityThreadCache{}
	unknown := sha256.Sum256([]byte("unknown fake history"))
	_, ok := cache.take(unknown, now)
	require.False(t, ok)
	require.Empty(t, perplexityThreadTestRecords(cache))

	key := sha256.Sum256([]byte("known fake history"))
	cache.put(key, perplexityThreadRecord{backendUUID: perplexityThreadTestBackend, contextUUID: perplexityThreadTestChild}, now)
	record := perplexityThreadTestRecords(cache)[key]
	require.Equal(t, now.Add(time.Hour), record.expiresAt)
	_, ok = cache.take(key, record.expiresAt)
	require.False(t, ok, "TTL expiry is exclusive, including the exact boundary")
	require.Empty(t, perplexityThreadTestRecords(cache), "expired handles must be deleted rather than resurrected")

	cache.put(key, perplexityThreadRecord{backendUUID: perplexityThreadTestBackend}, now)
	record, ok = cache.take(key, now.Add(perplexityThreadTTL-time.Nanosecond))
	require.True(t, ok)
	require.Equal(t, perplexityThreadTestBackend, record.backendUUID)
	_, ok = cache.take(key, now)
	require.False(t, ok, "a consumed handle remains consumed even with an earlier clock")
}

func TestPerplexityThreadFullCacheEvictsOldestAndPrunesExpired(t *testing.T) {
	cache := &perplexityThreadCache{}
	base := time.Unix(1_800_000_000, 0)
	keys := make([][32]byte, perplexityThreadCacheLimit+1)
	for i := range keys {
		keys[i] = sha256.Sum256([]byte(fmt.Sprintf("fake cache entry %d", i)))
	}
	for i := range perplexityThreadCacheLimit {
		cache.put(keys[i], perplexityThreadRecord{backendUUID: perplexityThreadTestBackend}, base.Add(time.Duration(i)*time.Millisecond))
	}
	require.Len(t, perplexityThreadTestRecords(cache), 1024)
	cache.put(keys[perplexityThreadCacheLimit], perplexityThreadRecord{backendUUID: perplexityThreadTestChild}, base.Add(2*time.Second))
	records := perplexityThreadTestRecords(cache)
	require.Len(t, records, 1024)
	require.NotContains(t, records, keys[0])
	require.Contains(t, records, keys[1])
	require.Equal(t, perplexityThreadTestChild, records[keys[perplexityThreadCacheLimit]].backendUUID)

	freshKey := sha256.Sum256([]byte("fake post expiry entry"))
	cache.put(freshKey, perplexityThreadRecord{backendUUID: perplexityThreadTestBackend}, base.Add(perplexityThreadTTL+3*time.Second))
	records = perplexityThreadTestRecords(cache)
	require.Len(t, records, 1)
	require.Contains(t, records, freshKey)
}

func TestPerplexityThreadCompletionRequiresValidUUIDAndContent(t *testing.T) {
	for _, completion := range []perplexityCompletion{
		{Content: "answer", BackendUUID: ""},
		{Content: "answer", BackendUUID: "not-a-uuid"},
		{Content: "answer", BackendUUID: "11111111-1111-4111-8111-11111111111z"},
		{Content: "answer", BackendUUID: " " + perplexityThreadTestBackend},
		{Content: "", BackendUUID: perplexityThreadTestBackend},
		{Content: " \n\t", BackendUUID: perplexityThreadTestBackend},
	} {
		t.Run(fmt.Sprintf("%q %q", completion.BackendUUID, completion.Content), func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			plan := svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot,
				perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "fake question")), perplexity.DefaultRequestOptions())
			require.NotNil(t, plan)
			plan.complete(completion)
			require.Empty(t, perplexityThreadTestRecords(plan.store))
			// Rejected callbacks must not burn the once guard for a valid completion.
			plan.complete(perplexityCompletion{Content: "valid answer", BackendUUID: perplexityThreadTestBackend})
			require.Len(t, perplexityThreadTestRecords(plan.store), 1)
		})
	}
	var absent *perplexityContinuation
	require.NotPanics(t, func() {
		absent.complete(perplexityCompletion{Content: "answer", BackendUUID: perplexityThreadTestBackend})
	})
}

func TestPerplexityThreadDuplicateCompletionCannotRefreshOrResurrectHandle(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	opts := perplexity.DefaultRequestOptions()
	plan := perplexityThreadTestSeed(t, svc, account, opts)
	before := perplexityThreadTestRecords(plan.store)
	for range 3 {
		plan.complete(perplexityCompletion{Content: "different answer", BackendUUID: perplexityThreadTestChild})
	}
	require.Equal(t, before, perplexityThreadTestRecords(plan.store), "duplicate callback must not mutate history, handle, or expiry")

	body := perplexityThreadTestFollowup(t, perplexityThreadTestAnswer, "next")
	claimed := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, body, opts)
	require.NotNil(t, claimed)
	require.Equal(t, perplexityThreadTestBackend, claimed.backendUUID)
	plan.complete(perplexityCompletion{Content: perplexityThreadTestAnswer, BackendUUID: perplexityThreadTestBackend})
	require.Empty(t, perplexityThreadTestRecords(plan.store), "late duplicate callback must not reinsert a consumed handle")
}

func TestPerplexityThreadConcurrentDuplicateCompletion(t *testing.T) {
	svc := &OpenAIGatewayService{}
	plan := svc.preparePerplexityContinuation(perplexityThreadTestAccount(), 41, "fake-session", "pplx_pro", perplexity.ModeCopilot,
		perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "fake question")), perplexity.DefaultRequestOptions())
	require.NotNil(t, plan)
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			plan.complete(perplexityCompletion{Content: "fake concurrent answer", BackendUUID: perplexityThreadTestBackend})
		}()
	}
	wg.Wait()
	records := perplexityThreadTestRecords(plan.store)
	require.Len(t, records, 1)
	for _, record := range records {
		require.Equal(t, perplexityThreadTestBackend, record.backendUUID)
	}
}

func TestPerplexityThreadCacheDoesNotRetainPlaintextOrCredentials(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	plan := perplexityThreadTestSeed(t, svc, account, perplexity.DefaultRequestOptions())
	records := perplexityThreadTestRecords(plan.store)
	require.Len(t, records, 1)

	// Inspect only persisted record fields, not the short-lived plan or Account:
	// they legitimately hold the current query and credentials during a request.
	recordType := reflect.TypeOf(perplexityThreadRecord{})
	require.Equal(t, 3, recordType.NumField())
	for i := range recordType.NumField() {
		field := recordType.Field(i)
		require.NotEmpty(t, field.PkgPath, "cached record field %s must remain private", field.Name)
		switch field.Name {
		case "backendUUID", "contextUUID":
			require.Equal(t, reflect.TypeOf(""), field.Type)
		case "expiresAt":
			require.Equal(t, reflect.TypeOf(time.Time{}), field.Type)
		default:
			t.Fatalf("unexpected cached record field %s may retain request data", field.Name)
		}
	}
	require.Equal(t, reflect.TypeOf([32]byte{}), reflect.TypeOf(records).Key(), "history and scope keys must be digests")
	stored := fmt.Sprintf("%#v", records)
	for _, secret := range []string{
		"fake first question", perplexityThreadTestAnswer, "fake-session", "fake-thread-cookie-secret",
		"fake-thread-csrf-secret", "fake-header-secret", "https://perplexity.example.invalid",
		"proxy.example.invalid", "fake-proxy-user", "fake-proxy-secret",
	} {
		require.NotContains(t, stored, secret)
	}
}

func TestPerplexityThreadHistoryDigestUsesUnambiguousTurnBoundaries(t *testing.T) {
	var scope [32]byte
	keyFor := func(turns ...[2]string) [32]byte {
		history := sha256.New()
		for _, turn := range turns {
			writePerplexityTurn(history, turn[0], turn[1])
		}
		return perplexityHistoryKey(scope, history)
	}
	require.NotEqual(t, keyFor([2]string{"user", "ab"}, [2]string{"assistant", "c"}), keyFor([2]string{"user", "a"}, [2]string{"assistant", "bc"}))
	require.NotEqual(t, keyFor([2]string{"user", "assistant"}), keyFor([2]string{"assistant", "user"}))
	require.NotEqual(t, keyFor([2]string{"user", "question"}), keyFor([2]string{"user", "question "}))
}

func TestPerplexityThreadUnknownIDRejectionClassification(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"error":{"code":"invalid_backend_uuid"}}`, true},
		{404, `{"error":{"code":"backend_uuid_not_found"}}`, true},
		{409, `{"error":{"code":"backend_uuid_expired"}}`, true},
		{400, `{"detail":"backend_uuid is invalid"}`, true},
		{404, `{"message":"backend_uuid not found"}`, true},
		{400, `{"error":{"message":"backend_uuid expired"}}`, true},
		{401, `{"error":{"code":"backend_uuid_not_found"}}`, false},
		{429, `{"error":{"code":"backend_uuid_expired"}}`, false},
		{500, `{"error":{"code":"invalid_backend_uuid"}}`, false},
		{400, `{"error":{"message":"invalid model"}}`, false},
		{400, `{"error":{"message":"unknown request"}}`, false},
		{400, `<html>backend_uuid invalid</html>`, false},
	} {
		t.Run(fmt.Sprintf("%d %s", test.status, test.body), func(t *testing.T) {
			require.Equal(t, test.want, perplexityContinuationRejected(test.status, []byte(test.body)))
		})
	}
}

func TestPerplexityThreadStoreInitializationIsConcurrentAndServiceLocal(t *testing.T) {
	svc := &OpenAIGatewayService{}
	var first atomic.Pointer[perplexityThreadCache]
	var different atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store := svc.perplexityThreadStore()
			first.CompareAndSwap(nil, store)
			if first.Load() != store {
				different.Add(1)
			}
		}()
	}
	wg.Wait()
	require.NotNil(t, first.Load())
	require.Zero(t, different.Load())
	other := (&OpenAIGatewayService{}).perplexityThreadStore()
	require.NotSame(t, first.Load(), other, "handles must not be process-global across service instances")
}

func TestPerplexityThreadIncognitoNeverCached(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := perplexityThreadTestAccount()
	opts := perplexity.DefaultRequestOptions()
	opts.Incognito = true

	// 无痕会话：不生成续链计划（不读缓存）。
	plan := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot,
		perplexityThreadTestBody(t, perplexityThreadTestTurn("user", "private question")), opts)
	require.Nil(t, plan, "incognito requests must never touch the thread cache")

	// 即使有可命中的既有线程，无痕请求也不许接续它。
	normalOpts := perplexity.DefaultRequestOptions()
	seed := perplexityThreadTestSeed(t, svc, account, normalOpts)
	followup := perplexityThreadTestFollowup(t, perplexityThreadTestAnswer, "private followup")
	plan = svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, followup, opts)
	require.Nil(t, plan, "incognito followup must not chain onto a cached thread")
	// 既有线程句柄未被消费（仍可供非无痕请求使用）。
	hit := svc.preparePerplexityContinuation(account, 41, "fake-session", "pplx_pro", perplexity.ModeCopilot, followup, normalOpts)
	require.NotNil(t, hit)
	require.Equal(t, perplexityThreadTestBackend, hit.backendUUID)
	_ = seed
}
