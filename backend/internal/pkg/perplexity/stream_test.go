package perplexity

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestSSEReaderFraming(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		t.Run(fmt.Sprintf("ending-%q", ending), func(t *testing.T) {
			body := strings.Join([]string{
				": keepalive", "", "event: message", "id: ignored", `data: {"blocks":`,
				`data: [{"intended_usage":"ask_text","markdown_block":{"answer":"array[0]"}}]}`, "",
				"event: end_of_stream", "data:", "", "",
			}, ending)
			d := NewDecoder(iotest.OneByteReader(strings.NewReader(body)))
			ev, err := d.Next()
			if err != nil || ev.Text != "array[0]" || ev.IsDelta {
				t.Fatalf("first event = %+v, %v", ev, err)
			}
			ev, err = d.Next()
			if err != nil || !ev.EndOfStream {
				t.Fatalf("terminal event = %+v, %v", ev, err)
			}
			if _, err := d.Next(); !errors.Is(err, io.EOF) {
				t.Fatalf("after EOS = %v, want EOF", err)
			}
		})
	}
}

func TestDecoderRequiresExplicitCompleteTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"empty", "", ErrIncompleteStream},
		{"unterminated EOS", "event: end_of_stream\n", ErrIncompleteStream},
		{"unterminated message", "data: {}\n", ErrIncompleteStream},
		{"malformed JSON", "data: {broken\n\n", ErrMalformedEvent},
		{"non-object", "data: []\n\n", ErrMalformedEvent},
		{"empty data", "data:\n\n", ErrMalformedEvent},
		{"JSON error", "data: {\"error\":{\"private\":\"secret\"}}\n\n", ErrUpstreamEvent},
		{"JSON status", "data: {\"status\":\"FAILED\"}\n\n", ErrUpstreamEvent},
		{"error event", "event: error\ndata: private browser key\n\n", ErrUpstreamEvent},
		{"empty error event", "event: error\n\n", ErrUpstreamEvent},
		{"EOS JSON error", "event: end_of_stream\ndata: {\"error\":\"secret\"}\n\n", ErrUpstreamEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDecoder(strings.NewReader(tc.body)).Next()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Next() error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private browser key") {
				t.Fatalf("error echoed private payload: %v", err)
			}
		})
	}
}

// 推理模型不发独立 end_of_stream，用 final_sse_message=true 作终止，且遗留 text 字段
// 可能是步骤【数组】而非对象——两者都不得把流判为 malformed/incomplete。
func TestDecoderFinalSSEMessageTerminatesWithArrayText(t *testing.T) {
	legacy := `[{\"step_type\":\"FINAL\",\"content\":{\"answer\":\"ignored legacy\"}}]`
	body := `data: {"final_sse_message":true,"status":"COMPLETED",` +
		`"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"DONE","answer":"The ball costs $0.05."}}],` +
		`"text":"` + legacy + `"}` + "\n\n"
	d := NewDecoder(strings.NewReader(body))
	ev, err := d.Next()
	if err != nil {
		t.Fatalf("Next() error = %v, want nil (array text must not be malformed)", err)
	}
	if !ev.EndOfStream {
		t.Fatalf("final_sse_message must terminate the stream: %+v", ev)
	}
	if ev.Text != "The ball costs $0.05." {
		t.Fatalf("answer = %q", ev.Text)
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after final = %v, want EOF", err)
	}
}

// 计划块 goals[].description 被解析为 Event.Plan（思考部分来源）。
func TestParseEventPlanGoals(t *testing.T) {
	body := `data: {"blocks":[{"intended_usage":"plan","plan_block":{"goals":[` +
		`{"description":"Set up the equation","todo_task_status":"INCOMPLETE"},` +
		`{"description":"  ","todo_task_status":"INCOMPLETE"},` +
		`{"description":"Solve for x","todo_task_status":"COMPLETE"}]}},` +
		`{"intended_usage":"ask_text","markdown_block":{"answer":"x=0.05"}}]}` + "\n\n" +
		"event: end_of_stream\ndata:\n\n"
	ev, err := NewDecoder(strings.NewReader(body)).Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if len(ev.Plan) != 2 {
		t.Fatalf("plan goals = %+v, want 2 non-empty", ev.Plan)
	}
	if ev.Plan[0].Description != "Set up the equation" || ev.Plan[1].Status != "COMPLETE" {
		t.Fatalf("plan goals mismatch: %+v", ev.Plan)
	}
	if got := SafePlanReasoningText(ev.Plan[0]); got != "Set up the equation" {
		t.Fatalf("SafePlanReasoningText = %q", got)
	}
}

func TestDecoderIncrementalAndSnapshotMirrors(t *testing.T) {
	body := "data: {\"blocks\":[" +
		`{"intended_usage":"ask_text_0_markdown","markdown_block":{"progress":"IN_PROGRESS","chunks":["Hello"]}},` +
		`{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":["Hello"]}}]}` + "\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text_0_markdown","markdown_block":{"chunks":["ignored mirror"]}}]}` + "\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"IN_PROGRESS","chunks":[", ","世界"]}}]}` + "\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"progress":"DONE","chunks":["Hello, 世界!"]}}]}` + "\n\n" +
		"event: end_of_stream\n\n"
	d := NewDecoder(strings.NewReader(body))
	var answer Answer
	for {
		ev, err := d.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := answer.Apply(ev, false); err != nil {
			t.Fatal(err)
		}
		if ev.EndOfStream {
			break
		}
	}
	if answer.Content() != "Hello, 世界!" {
		t.Fatalf("answer = %q", answer.Content())
	}
}

func TestDecoderKeepsInitiallySelectedMirror(t *testing.T) {
	body := `data: {"blocks":[{"intended_usage":"ask_text_0_markdown","markdown_block":{"chunks":["A"]}}]}` + "\n\n" +
		`data: {"blocks":[{"intended_usage":"ask_text","markdown_block":{"chunks":["duplicate"]}},{"intended_usage":"ask_text_0_markdown","markdown_block":{"chunks":["B"]}}]}` + "\n\n"
	d := NewDecoder(strings.NewReader(body))
	var answer Answer
	for range 2 {
		ev, err := d.Next()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := answer.Apply(ev, false); err != nil {
			t.Fatal(err)
		}
	}
	if answer.Content() != "AB" {
		t.Fatalf("mirror selection changed: %q", answer.Content())
	}
}

func TestAnswerLatestSnapshotAndRewrite(t *testing.T) {
	for _, allowRewrite := range []bool{false, true} {
		var answer Answer
		if _, err := answer.Apply(Event{HasText: true, Text: "Hello world"}, allowRewrite); err != nil {
			t.Fatal(err)
		}
		_, err := answer.Apply(Event{HasText: true, Text: "Hello there"}, allowRewrite)
		if allowRewrite {
			if err != nil || answer.Content() != "Hello there" {
				t.Fatalf("buffered rewrite = %q, %v", answer.Content(), err)
			}
		} else if !errors.Is(err, ErrAnswerRewritten) || answer.Content() != "Hello world" {
			t.Fatalf("stream rewrite = %q, %v", answer.Content(), err)
		}
	}
	var answer Answer
	_, _ = answer.Apply(Event{HasText: true, Text: "previous"}, true)
	_, _ = answer.Apply(Event{HasText: true, Text: ""}, true)
	if answer.Content() != "" {
		t.Fatalf("empty authoritative snapshot did not replace stale content: %q", answer.Content())
	}
}

func TestSourcesExtractionAndSafeFooter(t *testing.T) {
	ev, ok := ParseEvent([]byte(`{"web_results":[{"url":"https://EXAMPLE.com/a#part","title":"old"}],"blocks":[{"intended_usage":"web_results","web_result_block":{"web_results":[{"url":"https://example.com/a","name":"[unsafe] <script> *title*"},{"url":"javascript:alert(1)","title":"bad"},{"url":"https://user:password@example.com/","title":"credentials"}]}}]}`))
	if !ok || len(ev.Sources) != 1 || ev.Sources[0].Title != "[unsafe] <script> *title*" {
		t.Fatalf("sources = %+v, ok=%v", ev.Sources, ok)
	}
	footer := SourcesFooter(ev.Sources)
	if !strings.Contains(footer, "https://example.com/a") || !strings.Contains(footer, "&lt;script&gt;") || !strings.Contains(footer, `\[unsafe\]`) {
		t.Fatalf("unsafe/unusable footer: %q", footer)
	}
	if strings.Contains(footer, "javascript:") || strings.Contains(footer, "password") || strings.Contains(footer, "<script>") {
		t.Fatalf("unsafe footer: %q", footer)
	}
	var sources []Source
	for i := range 20 {
		sources = append(sources, Source{URL: fmt.Sprintf("https://example.com/%d", i), Title: "title"})
	}
	if got := len(MergeSources(nil, sources)); got != MaxSources {
		t.Fatalf("source limit = %d", got)
	}
	if footer := SourcesFooter([]Source{{URL: "file:///private"}, {URL: "https://example.com/\nsecret"}}); footer != "" {
		t.Fatalf("unsafe URLs accepted: %q", footer)
	}
}

func TestLegacySourcesAndEmptySnapshots(t *testing.T) {
	ev, ok := ParseEvent([]byte(`{"text":"{\"steps\":[{\"step_type\":\"FINAL\",\"content\":{\"answer\":\"{\\\"answer\\\":\\\"legacy\\\",\\\"web_results\\\":[{\\\"url\\\":\\\"https://go.dev\\\",\\\"name\\\":\\\"Go\\\"}]}\"}}]}"}`))
	if !ok || ev.Text != "legacy" || len(ev.Sources) != 1 || ev.Sources[0].Title != "Go" {
		t.Fatalf("legacy event = %+v, ok=%v", ev, ok)
	}
	ev, ok = ParseEvent([]byte(`{"blocks":[{"intended_usage":"ask_text","markdown_block":{"answer":"","chunks":["stale"]}}]}`))
	if !ok || !ev.HasText || ev.Text != "" || ev.IsDelta {
		t.Fatalf("empty snapshot = %+v, ok=%v", ev, ok)
	}
}

func TestSafeSearchProgressNeverExposesInternalPayload(t *testing.T) {
	for _, kind := range []string{"CODE", "THOUGHT", "MCP_TOOL", "BROWSER_TYPE", "FINAL"} {
		if _, ok := SafeSearchProgress(Step{UUID: "id", Type: kind, Content: map[string]any{"code": "secret"}}); ok {
			t.Errorf("internal step %s was exposed", kind)
		}
	}
	update, ok := SafeSearchProgress(Step{UUID: "id", Type: "SEARCH_WEB", Status: "DONE", Content: map[string]any{"browser_key": "secret", "query": "private"}})
	if !ok || update.Status != "DONE" || strings.Contains(update.Text, "secret") || strings.Contains(update.Text, "private") {
		t.Fatalf("unsafe progress = %+v", update)
	}
}

func TestSSEReaderBoundsWholeEvent(t *testing.T) {
	line := "data: " + strings.Repeat("x", 1024) + "\n"
	_, err := NewSSEReader(strings.NewReader(strings.Repeat(line, maxSSEEventBytes/1024+1) + "\n")).Next()
	if err == nil {
		t.Fatal("oversize multi-line event accepted")
	}
}
