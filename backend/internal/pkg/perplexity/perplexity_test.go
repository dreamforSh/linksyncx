package perplexity

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestBuildAskBody(t *testing.T) {
	body, err := BuildAskBody("what is go", "turbo", ModeConcise)
	if err != nil {
		t.Fatalf("BuildAskBody: %v", err)
	}
	root := gjson.ParseBytes(body)
	if got := root.Get("query_str").String(); got != "what is go" {
		t.Fatalf("query_str = %q", got)
	}
	params := root.Get("params")
	for _, key := range []string{"attachments", "frontend_context_uuid", "frontend_uuid", "mode", "model_preference", "sources", "search_focus", "version"} {
		if !params.Get(key).Exists() {
			t.Errorf("params.%s missing", key)
		}
	}
	if got := params.Get("model_preference").String(); got != "turbo" {
		t.Errorf("model_preference = %q", got)
	}
	if got := params.Get("mode").String(); got != ModeConcise {
		t.Errorf("mode = %q", got)
	}
	if got := params.Get("search_focus").String(); got != "internet" {
		t.Errorf("search_focus = %q", got)
	}
	if got := params.Get("version").String(); got != "2.18" {
		t.Errorf("version = %q", got)
	}
	if params.Get("last_backend_uuid").Type != gjson.Null {
		t.Errorf("last_backend_uuid should be null")
	}
	if fid, cid := params.Get("frontend_uuid").String(), params.Get("frontend_context_uuid").String(); fid == "" || cid == "" || fid == cid {
		t.Errorf("frontend uuids should be distinct non-empty: %q %q", fid, cid)
	}
	if !params.Get("attachments").IsArray() || len(params.Get("attachments").Array()) != 0 {
		t.Errorf("attachments should be an empty array")
	}
}

func TestResolveModelPreference(t *testing.T) {
	cases := []struct {
		in       string
		wantPref string
		wantMode string
	}{
		{"sonar", "turbo", ModeConcise},
		{"sonar-pro", "pplx_pro", ModeCopilot},
		{"sonar-reasoning", "pplx_reasoning", ModeCopilot},
		{"sonar-deep-research", "pplx_alpha", ModeCopilot},
		{"turbo", "turbo", ModeConcise},
		// 裸 model_preference 透传（model_mapping 改写后到达）。
		{"pplx_pro", "pplx_pro", ModeCopilot},
		{"gpt54", "gpt54", ModeCopilot},
		{"claude46sonnetthinking", "claude46sonnetthinking", ModeCopilot},
		// 大小写不敏感。
		{"SONAR-PRO", "pplx_pro", ModeCopilot},
		// Computer 代理模式别名。
		{"sonar-computer", "turbo", ModeComputer},
		{"sonar-pro-computer", "pplx_pro", ModeComputer},
		// ":模式" 后缀覆盖（model_mapping 值里可用）。
		{"gpt54:computer", "gpt54", ModeComputer},
		{"pplx_pro:concise", "pplx_pro", ModeConcise},
		{"sonar-pro:computer", "pplx_pro", ModeComputer},
		// 未知后缀不当作模式，整体按未知模型回落。
		{"gpt54:banana", "turbo", ModeConcise},
		// 裸 preference 透传（上游新模型免发版，如 Opus 5.5 时代的 claude55opus）。
		{"claude55opus", "claude55opus", ModeCopilot},
		{"claude55opus:computer", "claude55opus", ModeComputer},
		// 带连字符的客户端拼写不透传，回落 auto。
		{"gpt-5", "turbo", ModeConcise},
		{"", "turbo", ModeConcise},
	}
	for _, tc := range cases {
		pref, mode := ResolveModelPreference(tc.in)
		if pref != tc.wantPref || mode != tc.wantMode {
			t.Errorf("ResolveModelPreference(%q) = (%q, %q), want (%q, %q)", tc.in, pref, mode, tc.wantPref, tc.wantMode)
		}
	}
}

func TestParseEventSteps(t *testing.T) {
	event := `{
	  "uuid": "evt-2",
	  "blocks": [
	    {"intended_usage": "pro_search_steps", "plan_block": {"steps": [
	      {"step_type": "INITIAL_QUERY", "uuid": "s0", "initial_query_content": {"query": "q0"}},
	      {"step_type": "SEARCH_WEB", "uuid": "s1", "search_web_content": {"query": "golang news"}},
	      {"step_type": "BROWSER_OPEN_TAB", "uuid": "s2", "browser_open_tab_content": {"url": "https://example.com"}}
	    ]}},
	    {"intended_usage": "ask_text", "markdown_block": {"answer": "a"}}
	  ]
	}`
	ev, ok := ParseEvent([]byte(event))
	if !ok {
		t.Fatal("ParseEvent returned !ok")
	}
	if !ev.HasText || ev.Text != "a" {
		t.Errorf("text parse broken: %+v", ev)
	}
	if len(ev.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(ev.Steps))
	}
	if ev.Steps[1].Type != "SEARCH_WEB" || ev.Steps[1].UUID != "s1" {
		t.Errorf("step[1] = %+v", ev.Steps[1])
	}
	if ev.Steps[1].Content["query"] != "golang news" {
		t.Errorf("step[1] content = %v", ev.Steps[1].Content)
	}
	if ToolNameForStep(ev.Steps[2].Type) != "pplx_browser_open_tab" {
		t.Errorf("tool name = %q", ToolNameForStep(ev.Steps[2].Type))
	}
	// 无 step_type 的步骤被跳过，无 _content 字段时 Content 为 nil 也合法。
}

func TestToolNameForStep(t *testing.T) {
	if got := ToolNameForStep("SEARCH_WEB"); got != "pplx_search_web" {
		t.Errorf("got %q", got)
	}
	if got := ToolNameForStep(""); got != "pplx_step" {
		t.Errorf("got %q", got)
	}
}

func TestParseModelsConfigIDs(t *testing.T) {
	body := `{"models":[
	  {"model":"turbo","label":"Auto","subscription_tier":"free"},
	  {"model":"pplx_pro","label":"Pro","subscription_tier":"pro"},
	  {"model":"claude55opus","label":"Claude Opus 5.5","subscription_tier":"max"},
	  {"model":"","label":"broken"},
	  {"model":"turbo","label":"dup"}
	]}`
	ids := ParseModelsConfigIDs([]byte(body))
	want := []string{"turbo", "pplx_pro", "claude55opus"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids = %v, want %v", ids, want)
		}
	}
	if got := ParseModelsConfigIDs([]byte(`{"nope":true}`)); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
	if got := ParseModelsConfigIDs([]byte(`not json`)); got != nil {
		t.Errorf("expected nil, got %v", got)
	}
}

func TestParseEventLegacyStepsExtracted(t *testing.T) {
	// 旧形态：FINAL 之前的步骤也应作为工具轨迹提取（uuid 缺失时按序号兜底）。
	inner := `{"steps":[
	  {"step_type":"INITIAL_QUERY","content":{"query":"q"}},
	  {"step_type":"SEARCH_WEB","uuid":"sw-1","content":{"query":"golang"}},
	  {"step_type":"FINAL","content":{"answer":"{\"answer\":\"A\",\"web_results\":[]}"}}
	]}`
	outer := `{"backend_uuid":"t-1","text":` + strconv.Quote(inner) + `}`
	ev, ok := ParseEvent([]byte(outer))
	if !ok || !ev.HasText || ev.Text != "A" {
		t.Fatalf("text broken: ok=%v text=%q", ok, ev.Text)
	}
	if len(ev.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(ev.Steps), ev.Steps)
	}
	if ev.Steps[0].UUID != "legacy-0" || ev.Steps[0].Type != "INITIAL_QUERY" {
		t.Errorf("step[0] = %+v", ev.Steps[0])
	}
	if ev.Steps[1].UUID != "sw-1" || ev.Steps[1].Content["query"] != "golang" {
		t.Errorf("step[1] = %+v", ev.Steps[1])
	}
}

const blocksShapeEvent = `{
  "uuid": "evt-1",
  "backend_uuid": "thread-abc",
  "blocks": [
    {"intended_usage": "ask_text", "markdown_block": {"answer": "The capital of France is Paris", "chunks": ["The capital", " of France", " is Paris"]}},
    {"intended_usage": "sources", "markdown_block": {"chunks": ["ignored"]}}
  ]
}`

func TestParseEventBlocksShape(t *testing.T) {
	ev, ok := ParseEvent([]byte(blocksShapeEvent))
	if !ok {
		t.Fatal("ParseEvent returned !ok")
	}
	if !ev.HasText {
		t.Fatal("expected HasText")
	}
	if ev.Text != "The capital of France is Paris" {
		t.Errorf("Text = %q", ev.Text)
	}
	if ev.BackendUUID != "thread-abc" {
		t.Errorf("BackendUUID = %q", ev.BackendUUID)
	}
	if ev.UUID != "evt-1" {
		t.Errorf("UUID = %q", ev.UUID)
	}
}

func TestParseEventBlocksShapeChunksFallback(t *testing.T) {
	// answer 缺失时拼接 chunks。
	event := `{"blocks": [{"intended_usage": "ask_text", "markdown_block": {"chunks": ["Hello", ", world"]}}]}`
	ev, ok := ParseEvent([]byte(event))
	if !ok || !ev.HasText {
		t.Fatalf("ParseEvent ok=%v HasText=%v", ok, ev.HasText)
	}
	if ev.Text != "Hello, world" {
		t.Errorf("Text = %q", ev.Text)
	}
}

func TestParseEventLegacyNestedTextShape(t *testing.T) {
	answerJSON, _ := json.Marshal(map[string]any{
		"answer":      "Go is a programming language.",
		"web_results": []any{map[string]any{"url": "https://go.dev"}},
	})
	stepsJSON, _ := json.Marshal(map[string]any{
		"steps": []any{
			map[string]any{"step_type": "SEARCHING", "content": map[string]any{"queries": []string{"go language"}}},
			map[string]any{"step_type": "FINAL", "content": map[string]any{"answer": string(answerJSON)}},
		},
	})
	outer, _ := json.Marshal(map[string]any{
		"backend_uuid": "thread-xyz",
		"text":         string(stepsJSON),
	})
	ev, ok := ParseEvent(outer)
	if !ok {
		t.Fatal("ParseEvent returned !ok")
	}
	if !ev.HasText {
		t.Fatal("expected HasText")
	}
	if ev.Text != "Go is a programming language." {
		t.Errorf("Text = %q", ev.Text)
	}
	if ev.BackendUUID != "thread-xyz" {
		t.Errorf("BackendUUID = %q", ev.BackendUUID)
	}
}

func TestParseEventNonAnswerEvents(t *testing.T) {
	// 状态/心跳事件：合法 JSON 但无回答文本。
	for _, payload := range []string{
		`{"status": "searching"}`,
		`{"blocks": [{"intended_usage": "sources", "markdown_block": {"chunks": ["x"]}}]}`,
	} {
		ev, ok := ParseEvent([]byte(payload))
		if !ok {
			t.Errorf("ParseEvent(%s) returned !ok", payload)
		}
		if ev.HasText {
			t.Errorf("ParseEvent(%s) unexpectedly HasText", payload)
		}
	}
	if _, ok := ParseEvent([]byte("")); ok {
		t.Error("empty payload should be !ok")
	}
	if _, ok := ParseEvent([]byte("not json")); ok {
		t.Error("invalid json should be !ok")
	}
}

func TestDeltaFromSnapshot(t *testing.T) {
	if got := DeltaFromSnapshot("Hello", "Hello, world"); got != ", world" {
		t.Errorf("delta = %q", got)
	}
	if got := DeltaFromSnapshot("abc", "abc"); got != "" {
		t.Errorf("delta = %q, want empty", got)
	}
	// 快照不以 prev 为前缀（上游重写）：返回整段。
	if got := DeltaFromSnapshot("zzz", "Hello"); got != "Hello" {
		t.Errorf("delta = %q", got)
	}
	// 上游重写中段：只重复分叉点之后的内容，而不是整段。
	if got := DeltaFromSnapshot("Hello world", "Hello there"); got != "there" {
		t.Errorf("delta = %q", got)
	}
	// 多字节字符：不在 rune 中间切开。
	if got := DeltaFromSnapshot("你好，世界", "你好，朋友"); got != "朋友" {
		t.Errorf("delta = %q", got)
	}
}

func TestFoldMessagesToQuery(t *testing.T) {
	parse := func(s string) gjson.Result { return gjson.Parse(s).Get("messages") }

	t.Run("single user message verbatim", func(t *testing.T) {
		q, err := FoldMessagesToQuery(parse(`{"messages":[{"role":"user","content":"What is Go?"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if q != "What is Go?" {
			t.Errorf("q = %q", q)
		}
	})

	t.Run("system + history folded", func(t *testing.T) {
		q, err := FoldMessagesToQuery(parse(`{"messages":[
			{"role":"system","content":"You are terse."},
			{"role":"user","content":"Hi"},
			{"role":"assistant","content":"Hello!"},
			{"role":"user","content":"What is Go?"}
		]}`))
		if err != nil {
			t.Fatal(err)
		}
		want := "Instructions: You are terse.\n\nUser: Hi\nAssistant: Hello!\nWhat is Go?"
		if q != want {
			t.Errorf("q = %q, want %q", q, want)
		}
	})

	t.Run("tool-call-only assistant stripped", func(t *testing.T) {
		q, err := FoldMessagesToQuery(parse(`{"messages":[
			{"role":"user","content":"Hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"search","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"result"},
			{"role":"user","content":"And Go?"}
		]}`))
		if err != nil {
			t.Fatal(err)
		}
		want := "User: Hi\nAnd Go?"
		if q != want {
			t.Errorf("q = %q, want %q", q, want)
		}
	})

	t.Run("multipart content concatenated", func(t *testing.T) {
		q, err := FoldMessagesToQuery(parse(`{"messages":[{"role":"user","content":[{"type":"text","text":"Describe "},{"type":"text","text":"Go."}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if q != "Describe Go." {
			t.Errorf("q = %q", q)
		}
	})

	t.Run("images ignored when text present", func(t *testing.T) {
		q, err := FoldMessagesToQuery(parse(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,xx"}},{"type":"text","text":"what is this?"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if q != "what is this?" {
			t.Errorf("q = %q", q)
		}
	})

	t.Run("image-only final message rejected", func(t *testing.T) {
		_, err := FoldMessagesToQuery(parse(`{"messages":[
			{"role":"user","content":"hi"},
			{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,xx"}}]}
		]}`))
		if !errors.Is(err, ErrImageOnlyFinalMessage) {
			t.Errorf("err = %v, want ErrImageOnlyFinalMessage", err)
		}
	})

	t.Run("no user message", func(t *testing.T) {
		if _, err := FoldMessagesToQuery(parse(`{"messages":[{"role":"system","content":"x"}]}`)); err == nil {
			t.Error("expected error")
		}
	})

	t.Run("empty user message", func(t *testing.T) {
		if _, err := FoldMessagesToQuery(parse(`{"messages":[{"role":"user","content":"  "}]}`)); err == nil {
			t.Error("expected error")
		}
	})
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("empty = %d", got)
	}
	if got := EstimateTokens("hi"); got != 1 {
		t.Errorf("short text = %d, want 1", got)
	}
	// 40 个 ASCII 字符 → 10
	if got := EstimateTokens(strings.Repeat("a", 40)); got != 10 {
		t.Errorf("40 ascii = %d, want 10", got)
	}
	// rune-aware：8 个中文 rune（24 字节）→ 2
	if got := EstimateTokens("人工智能搜索问答"); got != 2 {
		t.Errorf("8 CJK runes = %d, want 2", got)
	}
}

func TestClassifyHTTPError(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		headers     http.Header
		body        string
		wantClass   ErrorClass
		wantRetryGt time.Duration
	}{
		{"401 auth", http.StatusUnauthorized, nil, ``, ErrClassAuth, 0},
		{"403 auth", http.StatusForbidden, nil, `{"error":"forbidden"}`, ErrClassAuth, 0},
		{"403 cloudflare", http.StatusForbidden, http.Header{"Cf-Ray": {"abc"}}, ``, ErrClassCloudflare, 0},
		{"429 with retry-after", http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, ``, ErrClassRateLimited, 29 * time.Second},
		{"429 no retry-after", http.StatusTooManyRequests, nil, ``, ErrClassRateLimited, 0},
		{"500 server", http.StatusInternalServerError, nil, ``, ErrClassServer, 0},
		{"503 cloudflare challenge", http.StatusServiceUnavailable, http.Header{"Server": {"cloudflare"}}, `<html>cf-chl</html>`, ErrClassCloudflare, 0},
		{"400 other", http.StatusBadRequest, nil, ``, ErrClassOther, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			class, retry := ClassifyHTTPError(tc.status, tc.headers, []byte(tc.body))
			if class != tc.wantClass {
				t.Errorf("class = %v, want %v", class, tc.wantClass)
			}
			if tc.wantRetryGt > 0 && retry <= tc.wantRetryGt {
				t.Errorf("retry = %v, want > %v", retry, tc.wantRetryGt)
			}
			if tc.wantRetryGt == 0 && retry != 0 {
				t.Errorf("retry = %v, want 0", retry)
			}
		})
	}
}

func TestCredentialsCookieHeaderAndHeaders(t *testing.T) {
	creds := Credentials{SessionToken: "tok", CSRFToken: "csrf"}
	if got := creds.CookieHeader(); got != "__Secure-next-auth.session-token=tok; next-auth.csrf-token=csrf" {
		t.Errorf("CookieHeader = %q", got)
	}
	creds.CSRFToken = ""
	if got := creds.CookieHeader(); got != "__Secure-next-auth.session-token=tok" {
		t.Errorf("CookieHeader without csrf = %q", got)
	}
	if got := creds.NormalizedBaseURL(); got != DefaultBaseURL {
		t.Errorf("NormalizedBaseURL = %q", got)
	}
	creds.BaseURL = "https://pplx.example.com/"
	if got := creds.NormalizedBaseURL(); got != "https://pplx.example.com" {
		t.Errorf("NormalizedBaseURL custom = %q", got)
	}

	h := http.Header{}
	creds.SetFetchStyleHeaders(h, "text/event-stream")
	if got := h.Get("Origin"); got != "https://pplx.example.com" {
		t.Errorf("Origin = %q", got)
	}
	if got := h.Get("Referer"); got != "https://pplx.example.com/" {
		t.Errorf("Referer = %q", got)
	}
	if got := h.Get("sec-fetch-site"); got != "same-origin" {
		t.Errorf("sec-fetch-site = %q", got)
	}
	if got := h.Get("User-Agent"); got != DefaultUserAgent {
		t.Errorf("User-Agent = %q", got)
	}
	if got := h.Get("Cookie"); !strings.Contains(got, "__Secure-next-auth.session-token=tok") {
		t.Errorf("Cookie = %q", got)
	}
}

func TestDefaultModelIDs(t *testing.T) {
	ids := DefaultModelIDs()
	want := []string{"sonar", "sonar-pro", "sonar-reasoning", "sonar-deep-research", "sonar-computer", "sonar-pro-computer"}
	if len(ids) != len(want) {
		t.Fatalf("DefaultModelIDs = %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("DefaultModelIDs = %v, want %v", ids, want)
		}
	}
}
