package service

import (
	"strconv"
	"strings"
	"testing"
)

// BenchmarkPatchGrokResponsesBodyWithClientTools measures the request rewrite
// every Grok Responses turn pays before the upstream request is sent, so it
// lands directly on time-to-first-token for long replayed conversations.
func BenchmarkPatchGrokResponsesBodyWithClientTools(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{name: "64KB", bytes: 64 << 10},
		{name: "512KB", bytes: 512 << 10},
		{name: "2MB", bytes: 2 << 20},
	} {
		b.Run(size.name, func(b *testing.B) {
			body := buildLargeGrokResponsesBody(size.bytes)

			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				patched, _, err := patchGrokResponsesBodyWithClientTools(body, "grok-4.6")
				if err != nil {
					b.Fatalf("patch Grok Responses body: %v", err)
				}
				benchmarkIntSink = len(patched)
			}
		})
	}
}

// buildLargeGrokResponsesBody builds a Grok Build style turn: long
// instructions, function tools, and replayed history with encrypted reasoning
// and tool output.
func buildLargeGrokResponsesBody(targetBytes int) []byte {
	var builder strings.Builder
	builder.Grow(targetBytes + 64<<10)
	_, _ = builder.WriteString(`{"model":"grok-4.6","stream":true,"store":false,"prompt_cache_key":"019a1f2e-1111-7222-8333-944455556666","instructions":"`)
	_, _ = builder.WriteString(strings.Repeat("You are a coding agent. Follow the repository conventions. ", 180))
	_, _ = builder.WriteString(`","reasoning":{"effort":"high","summary":"auto"},"include":["reasoning.encrypted_content"],"tools":[`)
	for i := 0; i < 28; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		_, _ = builder.WriteString(`{"type":"function","name":"tool_` + strconv.Itoa(i) + `","description":"` + strings.Repeat("Run a tool. ", 20) + `","parameters":{"type":"object","properties":{"command":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}},"required":["command"]}}`)
	}
	_, _ = builder.WriteString(`,{"type":"web_search"}],"tool_choice":"auto","input":[`)
	for i := 0; builder.Len() < targetBytes; i++ {
		if i > 0 {
			_ = builder.WriteByte(',')
		}
		id := strconv.Itoa(i)
		_, _ = builder.WriteString(`{"type":"message","role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("please fix the failing test in module "+id+". ", 12) + `"}]},`)
		_, _ = builder.WriteString(`{"type":"reasoning","id":"rs_` + id + `","summary":[{"type":"summary_text","text":"thinking about ` + id + `"}],"encrypted_content":"` + strings.Repeat("QUJD", 400) + `"},`)
		_, _ = builder.WriteString(`{"type":"function_call","id":"fc_` + id + `","call_id":"call_` + id + `","name":"tool_1","arguments":"{\"command\":\"go test ./...\"}"},`)
		_, _ = builder.WriteString(`{"type":"function_call_output","call_id":"call_` + id + `","output":"` + strings.Repeat(`ok  \tgithub.com/x/y\t0.12s\n`, 30) + `"},`)
		_, _ = builder.WriteString(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat("Done with step "+id+". ", 10) + `"}]}`)
	}
	_, _ = builder.WriteString(`]}`)
	return []byte(builder.String())
}
