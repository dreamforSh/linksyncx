//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var responsesStateTestGroup atomic.Int64

func nextResponsesStateTestGroup() int64 {
	return 900000 + responsesStateTestGroup.Add(1)
}

func responsesStateTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			OpenAIResponsesStateEnabled:    true,
			OpenAIResponsesStateTTLSeconds: 3600,
		}},
		httpUpstream: upstream,
	}
}

func responsesStateOAuthAccount() *Account {
	return &Account{
		ID: 4242, Name: "sub", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 4,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc"},
		Status:      StatusActive, Schedulable: true,
	}
}

func responsesStateSSE(responseID, text string) *http.Response {
	output := `[{"type":"reasoning","id":"rs_` + responseID + `","summary":[],"encrypted_content":"enc-` + responseID + `"},` +
		`{"type":"reasoning","id":"rs_plain_` + responseID + `","summary":[]},` +
		`{"type":"message","id":"msg_` + responseID + `","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + text + `","annotations":[]}]}]`
	sse := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + text + "\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"" + responseID + "\",\"object\":\"response\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":" + output + ",\"usage\":{\"input_tokens\":5,\"output_tokens\":1,\"total_tokens\":6}}}\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
}

type responsesStateTurn struct {
	recorder *httptest.ResponseRecorder
	ctx      *gin.Context
}

// runResponsesStateTurn mirrors the handler: expand or arm, then Forward.
func runResponsesStateTurn(t *testing.T, svc *OpenAIGatewayService, groupID, userID, apiKeyID int64, body string, headers map[string]string) responsesStateTurn {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "OpenAI/Python 2.8.1")
	for k, v := range headers {
		c.Request.Header.Set(k, v)
	}
	forwardBody := []byte(body)
	if gjson.Get(body, "previous_response_id").String() != "" {
		continuation, err := svc.ExpandOpenAIResponsesContinuation(context.Background(), groupID, userID, apiKeyID, forwardBody)
		require.NoError(t, err)
		require.NotNil(t, continuation)
		forwardBody = continuation.Body
		SetOpenAIResponsesStatePending(c, continuation.Pending)
	} else if pending := svc.NewOpenAIResponsesStatePending(groupID, userID, apiKeyID, forwardBody); pending != nil {
		SetOpenAIResponsesStatePending(c, pending)
	}
	_, err := svc.Forward(context.Background(), c, responsesStateOAuthAccount(), forwardBody)
	require.NoError(t, err)
	return responsesStateTurn{recorder: rec, ctx: c}
}

func TestOpenAIResponsesStateChainsTurnsForSubscriptionAccounts(t *testing.T) {
	groupID := nextResponsesStateTestGroup()
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		responsesStateSSE("resp_a", "4"),
		responsesStateSSE("resp_b", "12"),
		responsesStateSSE("resp_c", "done"),
	}}
	svc := responsesStateTestService(upstream)

	// Turn 1: non-streaming, string input, store omitted (defaults to true).
	turn1 := runResponsesStateTurn(t, svc, groupID, 7, 70, `{"model":"gpt-5.4","input":"What is 2+2?"}`, nil)
	require.Equal(t, http.StatusOK, turn1.recorder.Code)
	require.Equal(t, "resp_a", gjson.Get(turn1.recorder.Body.String(), "id").String())
	stored, found, err := svc.GetOpenAIResponsesStoredResponse(context.Background(), groupID, "resp_a", 7, 70)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "resp_a", gjson.GetBytes(stored, "id").String())
	sessionTurn1 := upstream.lastReq.Header.Get("session_id")
	require.NotEmpty(t, sessionTurn1, "SDK request must get a derived prompt cache session")
	require.NotEmpty(t, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())

	// Turn 2: streaming continuation.
	runResponsesStateTurn(t, svc, groupID, 7, 70, `{"model":"gpt-5.4","previous_response_id":"resp_a","input":[{"role":"user","content":"times 3?"}],"stream":true}`, nil)
	sent := upstream.lastBody
	require.False(t, gjson.GetBytes(sent, "previous_response_id").Exists())
	types := []string{}
	for _, item := range gjson.GetBytes(sent, "input").Array() {
		types = append(types, item.Get("type").String()+":"+item.Get("role").String())
	}
	require.Equal(t, []string{"message:user", "reasoning:", "message:assistant", ":user"}, types,
		"history = turn-1 input + replayable turn-1 output (reasoning without encrypted content dropped) + new input")
	require.Contains(t, string(sent), "What is 2+2?")
	require.Contains(t, string(sent), "enc-resp_a")
	require.NotContains(t, string(sent), "rs_plain_resp_a")
	require.Equal(t, sessionTurn1, upstream.lastReq.Header.Get("session_id"), "the derived cache key must stay stable across turns")

	recordB, err := svc.lookupOpenAIResponsesState(context.Background(), groupID, "resp_b", 7, 70)
	require.NoError(t, err)
	require.NotNil(t, recordB)
	require.Equal(t, []string{"resp_a"}, recordB.Ancestors)
	require.JSONEq(t, `[{"role":"user","content":"times 3?"}]`, string(recordB.Input), "only the new input is stored for a continuation")
	require.Equal(t, "resp_a", gjson.GetBytes(recordB.Response, "previous_response_id").String())

	// Turn 3 sees both earlier turns.
	runResponsesStateTurn(t, svc, groupID, 7, 70, `{"model":"gpt-5.4","previous_response_id":"resp_b","input":"and now?"}`, nil)
	sent = upstream.lastBody
	require.Contains(t, string(sent), "What is 2+2?")
	require.Contains(t, string(sent), "times 3?")
	require.Contains(t, string(sent), "enc-resp_b")
	require.Contains(t, string(sent), "and now?")
	require.Equal(t, sessionTurn1, upstream.lastReq.Header.Get("session_id"))

	items, found, err := svc.ListOpenAIResponsesStoredInputItems(context.Background(), groupID, "resp_c", 7, 70)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, items, 7, "turn1 in + 2 replayable out + turn2 in + 2 out + turn3 in")

	// Another user cannot continue, read or delete the chain.
	_, err = svc.ExpandOpenAIResponsesContinuation(context.Background(), groupID, 8, 80, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_b","input":"x"}`))
	require.ErrorIs(t, err, ErrOpenAIResponsesStateNotOwned)
	_, found, err = svc.GetOpenAIResponsesStoredResponse(context.Background(), groupID, "resp_b", 8, 80)
	require.NoError(t, err)
	require.False(t, found)
	deleted, err := svc.DeleteOpenAIResponsesStoredResponse(context.Background(), groupID, "resp_b", 8, 80)
	require.NoError(t, err)
	require.False(t, deleted)

	// Another group never sees it.
	continuation, err := svc.ExpandOpenAIResponsesContinuation(context.Background(), groupID+1, 7, 70, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_b","input":"x"}`))
	require.NoError(t, err)
	require.Nil(t, continuation)

	// Losing an ancestor breaks the chain instead of silently dropping context.
	deleted, err = svc.DeleteOpenAIResponsesStoredResponse(context.Background(), groupID, "resp_a", 7, 70)
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = svc.ExpandOpenAIResponsesContinuation(context.Background(), groupID, 7, 70, []byte(`{"model":"gpt-5.4","previous_response_id":"resp_c","input":"x"}`))
	require.ErrorIs(t, err, ErrOpenAIResponsesStateBroken)
}

func TestOpenAIResponsesStateRespectsStoreFalseAndCodexClients(t *testing.T) {
	svc := responsesStateTestService(&httpUpstreamRecorder{})
	require.Nil(t, svc.NewOpenAIResponsesStatePending(1, 1, 1, []byte(`{"model":"gpt-5.4","store":false,"input":"x"}`)))
	require.Nil(t, svc.NewOpenAIResponsesStatePending(1, 1, 1, []byte(`{"model":"gpt-5.4","background":true,"input":"x"}`)))
	require.NotNil(t, svc.NewOpenAIResponsesStatePending(1, 1, 1, []byte(`{"model":"gpt-5.4","store":true,"input":"x"}`)))

	disabled := &OpenAIGatewayService{cfg: &config.Config{}}
	require.Nil(t, disabled.NewOpenAIResponsesStatePending(1, 1, 1, []byte(`{"model":"gpt-5.4","input":"x"}`)))
	continuation, err := disabled.ExpandOpenAIResponsesContinuation(context.Background(), 1, 1, 1, []byte(`{"previous_response_id":"resp_x"}`))
	require.NoError(t, err)
	require.Nil(t, continuation)

	// API-key accounts keep their native state unless the request was expanded here.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	SetOpenAIResponsesStatePending(c, &OpenAIResponsesStatePending{})
	apiKeyAccount := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.False(t, shouldRecordOpenAIResponsesState(c, apiKeyAccount))
	require.True(t, shouldRecordOpenAIResponsesState(c, responsesStateOAuthAccount()))
	SetOpenAIResponsesStatePending(c, &OpenAIResponsesStatePending{Continuation: true})
	require.True(t, shouldRecordOpenAIResponsesState(c, apiKeyAccount))
}

func TestOpenAIResponsesStateCollapsesLongChains(t *testing.T) {
	groupID := nextResponsesStateTestGroup()
	svc := responsesStateTestService(&httpUpstreamRecorder{})
	store := svc.openAIResponsesStateStore()
	ancestors := make([]string, 0, openAIResponsesStateMaxAncestors)
	for i := 0; i < openAIResponsesStateMaxAncestors; i++ {
		id := "resp_" + strings.Repeat("x", i+1)
		payload, err := json.Marshal(openAIResponsesStateRecord{
			Version: openAIResponsesStateRecordVersion, UserID: 1, APIKeyID: 1,
			Ancestors: append([]string(nil), ancestors...),
			Input:     json.RawMessage(`[{"role":"user","content":"turn"}]`),
			Response:  json.RawMessage(`{"id":"` + id + `","output":[{"type":"message","role":"assistant","content":"ok"}]}`),
		})
		require.NoError(t, err)
		require.NoError(t, store.SetOpenAIResponsesState(context.Background(), openAIResponsesStateKey(groupID, id), payload, 3600e9))
		if len(ancestors)+1 < openAIResponsesStateMaxAncestors {
			ancestors = append(ancestors, id)
		}
	}
	last := "resp_" + strings.Repeat("x", openAIResponsesStateMaxAncestors)
	continuation, err := svc.ExpandOpenAIResponsesContinuation(context.Background(), groupID, 1, 1, []byte(`{"model":"gpt-5.4","previous_response_id":"`+last+`","input":"next"}`))
	require.NoError(t, err)
	require.NotNil(t, continuation)
	require.Empty(t, continuation.Pending.Ancestors, "a long chain is collapsed into a snapshot")
	require.Equal(t, gjson.GetBytes(continuation.Body, "input").Raw, string(continuation.Pending.Input))
	require.Len(t, gjson.GetBytes(continuation.Body, "input").Array(), 2*openAIResponsesStateMaxAncestors+1)
}

func TestOpenAIResponsesSDKPromptCacheKeyScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(headers map[string]string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		for k, v := range headers {
			c.Request.Header.Set(k, v)
		}
		return c
	}
	body := func(raw string) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		return m
	}
	base := `{"instructions":"be brief","input":[{"type":"message","role":"user","content":"hello"}]}`
	key := deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(base), "gpt-5.4")
	require.True(t, strings.HasPrefix(key, compatResponsesPromptCacheKeyPrefix))
	longer := `{"instructions":"be brief","input":[{"type":"message","role":"user","content":"hello"},{"type":"message","role":"assistant","content":"hi"},{"type":"message","role":"user","content":"more"}]}`
	require.Equal(t, key, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(longer), "gpt-5.4"), "later turns of one conversation share the key")
	require.NotEqual(t, key, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(`{"instructions":"be brief","input":[{"type":"message","role":"user","content":"other"}]}`), "gpt-5.4"))
	require.Empty(t, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(`{"prompt_cache_key":"client","input":[{"role":"user","content":"x"}]}`), "gpt-5.4"))
	require.Empty(t, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(`{"input":[]}`), "gpt-5.4"), "no conversation anchor")
	require.Empty(t, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(nil), body(base), "gpt-4o"))
	withSession := deriveOpenAIResponsesSDKPromptCacheKey(newCtx(map[string]string{"session_id": "abc"}), body(base), "gpt-5.4")
	require.NotEqual(t, key, withSession)
	require.Equal(t, withSession, deriveOpenAIResponsesSDKPromptCacheKey(newCtx(map[string]string{"session_id": "abc"}), body(`{"input":"different"}`), "gpt-5.4"))

	// Forward: Codex clients and explicit keys are untouched; SDK requests get one.
	for _, tc := range []struct {
		name      string
		headers   map[string]string
		body      string
		wantKey   bool
		wantExact string
	}{
		{"sdk", map[string]string{"User-Agent": "OpenAI/Python 2.8.1"}, `{"model":"gpt-5.4","input":"hi","stream":true}`, true, ""},
		{"codex", map[string]string{"User-Agent": "codex_cli_rs/0.130.0 (Mac OS 15.5.0; arm64) iTerm.app/3.5.14", "originator": "codex_cli_rs"}, `{"model":"gpt-5.4","input":"hi","stream":true}`, false, ""},
	} {
		upstream := &httpUpstreamRecorder{resp: responsesStateSSE("resp_k", "x")}
		svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(tc.body)))
		for k, v := range tc.headers {
			c.Request.Header.Set(k, v)
		}
		_, err := svc.Forward(context.Background(), c, responsesStateOAuthAccount(), []byte(tc.body))
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.wantKey, gjson.GetBytes(upstream.lastBody, "prompt_cache_key").Exists(), tc.name)
		require.Equal(t, tc.wantKey, upstream.lastReq.Header.Get("session_id") != "", tc.name)
	}
}

func TestOpenAIResponsesStateMemoryStoreBudgetAndTTL(t *testing.T) {
	store := newOpenAIResponsesStateMemoryStore(10)
	now := store.now()
	store.now = func() time.Time { return now }
	require.NoError(t, store.SetOpenAIResponsesState(context.Background(), "a", []byte("12345"), time.Minute))
	require.NoError(t, store.SetOpenAIResponsesState(context.Background(), "b", []byte("12345"), time.Minute))
	got, _ := store.GetOpenAIResponsesStates(context.Background(), []string{"a"})
	require.Equal(t, "12345", string(got[0]))
	require.NoError(t, store.SetOpenAIResponsesState(context.Background(), "c", []byte("123"), time.Minute))
	got, _ = store.GetOpenAIResponsesStates(context.Background(), []string{"a", "b", "c"})
	require.NotNil(t, got[0], "recently read entry survives")
	require.Nil(t, got[1], "least recently used entry is evicted over budget")
	require.NotNil(t, got[2])
	store.now = func() time.Time { return now.Add(2 * time.Minute) }
	got, _ = store.GetOpenAIResponsesStates(context.Background(), []string{"a", "c"})
	require.Nil(t, got[0])
	require.Nil(t, got[1])
	require.Error(t, store.SetOpenAIResponsesState(context.Background(), "big", make([]byte, 11), time.Minute))
}
