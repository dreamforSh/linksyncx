// Incremental/DONE and mirror-channel semantics were cross-checked against
// jamie950315/pplx-proxy (MIT), revision 5e40b654; this parser is implemented in Go.
package perplexity

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

const maxSSEEventBytes = 4 * 1024 * 1024

// MaxSources 限制可见来源数量，避免把整个检索结果集展开到回答中。
const MaxSources = 10

var (
	ErrIncompleteStream = errors.New("perplexity: stream ended before end_of_stream")
	ErrMalformedEvent   = errors.New("perplexity: malformed SSE JSON event")
	ErrUpstreamEvent    = errors.New("perplexity: upstream reported a stream error")
	ErrEmptyAnswer      = errors.New("perplexity: stream completed without answer content")
	ErrAnswerRewritten  = errors.New("perplexity: upstream rewrote text already sent in the stream")
)

// SSEFrame 是一个以空行结束的 SSE 事件；多个 data 行以换行连接。
type SSEFrame struct {
	Event   string
	Data    string
	HasData bool
}

// SSEReader 按事件而不是网络分块或单个 data 行解帧。末尾未闭合的帧不是终态。
type SSEReader struct {
	scanner *bufio.Scanner
	first   bool
}

// NewSSEReader 创建有单行及整帧大小上限的解帧器。
func NewSSEReader(r io.Reader) *SSEReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)
	return &SSEReader{scanner: scanner, first: true}
}

// Next 返回下一完整事件。无 data 的 end_of_stream 也会被保留。
func (r *SSEReader) Next() (SSEFrame, error) {
	frame := SSEFrame{Event: SSEEventMessage}
	var data strings.Builder
	size := 0
	hasFields := false
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if r.first {
			line = strings.TrimPrefix(line, string([]byte{0xef, 0xbb, 0xbf}))
			r.first = false
		}
		if line == "" {
			if frame.HasData || hasFields {
				frame.Data = data.String()
				return frame, nil
			}
			size = 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		size += len(line) + 1
		if size > maxSSEEventBytes {
			return SSEFrame{}, errors.New("perplexity: SSE event exceeds size limit")
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			hasFields = true
			frame.Event = value
			if frame.Event == "" {
				frame.Event = SSEEventMessage
			}
		case "data":
			if frame.HasData {
				_ = data.WriteByte('\n')
			}
			_, _ = data.WriteString(value)
			frame.HasData = true
		}
	}
	if err := r.scanner.Err(); err != nil {
		return SSEFrame{}, fmt.Errorf("perplexity: read SSE event: %w", err)
	}
	if frame.HasData || hasFields {
		return SSEFrame{}, io.ErrUnexpectedEOF
	}
	return SSEFrame{}, io.EOF
}

// Decoder 固定选择一个 ask_text 镜像，且只把明确 EOS 作为成功终态。
type Decoder struct {
	reader      *SSEReader
	answerUsage string
	complete    bool
}

// NewDecoder 创建 Perplexity 事件解码器。
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{reader: NewSSEReader(r)}
}

// Next 返回解析事件。错误载荷不回显，避免泄漏浏览器状态或凭据。
func (d *Decoder) Next() (Event, error) {
	if d.complete {
		return Event{}, io.EOF
	}
	for {
		frame, err := d.reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return Event{}, ErrIncompleteStream
			}
			return Event{}, err
		}
		switch frame.Event {
		case "error":
			return Event{}, ErrUpstreamEvent
		case SSEEventMessage, SSEEventEndOfStream:
			// EOS may have no data, an empty data field, or a final JSON payload.
			if frame.Event == SSEEventEndOfStream && strings.TrimSpace(frame.Data) == "" {
				d.complete = true
				return Event{EndOfStream: true}, nil
			}
			if !frame.HasData {
				continue
			}
			ev, ok := parseEvent([]byte(frame.Data), d.answerUsage)
			if !ok {
				return Event{}, ErrMalformedEvent
			}
			if ev.Error {
				return Event{}, ErrUpstreamEvent
			}
			if ev.HasText && d.answerUsage == "" {
				d.answerUsage = ev.AnswerUsage
			}
			if frame.Event == SSEEventEndOfStream {
				d.complete = true
				ev.EndOfStream = true
			}
			return ev, nil
		default:
			// Non-message events are keepalives or provider-specific notifications.
		}
	}
}

func parseEvent(data []byte, answerUsage string) (Event, bool) {
	if !gjson.ValidBytes(data) {
		return Event{}, false
	}
	root := gjson.ParseBytes(data)
	if !root.IsObject() {
		return Event{}, false
	}
	ev := Event{
		BackendUUID: strings.TrimSpace(root.Get("backend_uuid").String()),
		UUID:        strings.TrimSpace(root.Get("uuid").String()),
		Error:       hasEventError(root),
	}
	ev.Sources = sourcesFromResults(root.Get("web_results"))
	var selected gjson.Result
	for i, block := range root.Get("blocks").Array() {
		usage := block.Get("intended_usage").String()
		switch usage {
		case "pro_search_steps":
			for j, raw := range block.Get("plan_block.steps").Array() {
				if step, ok := parseStep(raw); ok {
					if step.UUID == "" {
						step.UUID = fmt.Sprintf("block-%d-step-%d", i, j)
					}
					ev.Steps = append(ev.Steps, step)
				}
			}
		case "web_results":
			ev.Sources = append(ev.Sources, sourcesFromResults(block.Get("web_result_block.web_results"))...)
		}
		ev.Sources = append(ev.Sources, sourcesFromResults(block.Get("markdown_block.web_results"))...)
		typeIsAnswer := usage == "ask_text" || strings.HasPrefix(usage, "ask_text_")
		md := block.Get("markdown_block")
		if !typeIsAnswer {
			continue
		}
		if status := md.Get("progress").String(); strings.EqualFold(status, "ERROR") || strings.EqualFold(status, "FAILED") || hasEventError(md) {
			ev.Error = true
		}
		if !md.IsObject() || (!md.Get("answer").Exists() && !md.Get("chunks").IsArray()) {
			continue
		}
		if answerUsage != "" {
			if usage == answerUsage && !selected.Exists() {
				selected = block
			}
		} else if !selected.Exists() || (usage == "ask_text" && selected.Get("intended_usage").String() != "ask_text") {
			selected = block
		}
	}
	if selected.Exists() {
		md := selected.Get("markdown_block")
		ev.AnswerUsage = selected.Get("intended_usage").String()
		if answer := md.Get("answer"); answer.Type == gjson.String {
			ev.Text, ev.HasText = answer.String(), true
		} else if chunks := md.Get("chunks"); chunks.IsArray() {
			var text strings.Builder
			for _, chunk := range chunks.Array() {
				if chunk.Type != gjson.String {
					return Event{}, false
				}
				_, _ = text.WriteString(chunk.String())
			}
			ev.Text, ev.HasText = text.String(), true
			ev.IsDelta = !strings.EqualFold(md.Get("progress").String(), "DONE")
		} else {
			return Event{}, false
		}
	}

	// Legacy FINAL stores another JSON object in content.answer. It is also a
	// snapshot, never another incremental channel alongside modern ask_text.
	rawText := root.Get("text")
	if rawText.Type == gjson.String && strings.TrimSpace(rawText.String()) != "" {
		if !gjson.Valid(rawText.String()) || !gjson.Parse(rawText.String()).IsObject() {
			return Event{}, false
		}
		inner := gjson.Parse(rawText.String())
		ev.Error = ev.Error || hasEventError(inner)
		for i, raw := range inner.Get("steps").Array() {
			if raw.Get("step_type").String() != "FINAL" {
				if step, ok := parseStep(raw); ok {
					if step.UUID == "" {
						step.UUID = "legacy-" + strconv.Itoa(i)
					}
					ev.Steps = append(ev.Steps, step)
				}
				continue
			}
			answer := raw.Get("content.answer")
			if answer.Type == gjson.String && gjson.Valid(answer.String()) && gjson.Parse(answer.String()).IsObject() {
				answer = gjson.Parse(answer.String())
			}
			if answer.IsObject() {
				ev.Sources = append(ev.Sources, sourcesFromResults(answer.Get("web_results"))...)
				answer = answer.Get("answer")
			}
			if answer.Type == gjson.String && !selected.Exists() && (answerUsage == "" || answerUsage == "legacy") {
				ev.Text, ev.HasText, ev.AnswerUsage = answer.String(), true, "legacy"
			}
		}
	}
	ev.Sources = MergeSources(nil, ev.Sources)
	return ev, true
}

func hasEventError(root gjson.Result) bool {
	err := root.Get("error")
	if err.Exists() && err.Type != gjson.Null && err.Type != gjson.False && (err.Type != gjson.String || err.String() != "") {
		return true
	}
	status := strings.ToUpper(root.Get("status").String())
	return status == "ERROR" || status == "FAILED" || strings.EqualFold(root.Get("type").String(), "error")
}

func parseStep(raw gjson.Result) (Step, bool) {
	stepType := strings.ToUpper(strings.TrimSpace(raw.Get("step_type").String()))
	if stepType == "" {
		return Step{}, false
	}
	step := Step{
		UUID:   strings.TrimSpace(raw.Get("uuid").String()),
		Type:   stepType,
		Status: raw.Get("status").String(),
	}
	if step.Status == "" {
		step.Status = raw.Get("progress").String()
	}
	content := raw.Get(strings.ToLower(stepType) + "_content")
	if !content.IsObject() {
		content = raw.Get("content")
	}
	if content.IsObject() {
		if err := json.Unmarshal([]byte(content.Raw), &step.Content); err != nil {
			return Step{}, false
		}
	}
	return step, true
}

// Answer 累积回答。非流式可采用最新权威快照覆盖，已输出的流式文本不可改写。
type Answer struct {
	text strings.Builder
}

// Apply 合并事件并返回新增文本；allowRewrite 仅可用于尚未输出的缓冲回答。
func (a *Answer) Apply(ev Event, allowRewrite bool) (string, error) {
	if !ev.HasText {
		return "", nil
	}
	if ev.IsDelta {
		_, _ = a.text.WriteString(ev.Text)
		return ev.Text, nil
	}
	delta, err := DeltaFromSnapshot(a.text.String(), ev.Text)
	if err != nil {
		if !allowRewrite {
			return "", err
		}
		a.text.Reset()
		_, _ = a.text.WriteString(ev.Text)
		return ev.Text, nil
	}
	_, _ = a.text.WriteString(delta)
	return delta, nil
}

// Content 返回当前权威正文，不包含来源 footer。
func (a *Answer) Content() string { return a.text.String() }

// Source 只含可公开的检索来源，不保留上游其余检索/浏览器数据。
type Source struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func sourcesFromResults(results gjson.Result) []Source {
	var sources []Source
	for _, result := range results.Array() {
		title := result.Get("title").String()
		if title == "" {
			title = result.Get("name").String()
		}
		sources = append(sources, Source{URL: result.Get("url").String(), Title: title})
	}
	return sources
}

// MergeSources 验证 http(s) URL，按 URL 去重保序，并更新同来源的新标题。
func MergeSources(current, incoming []Source) []Source {
	if len(current) == 0 && len(incoming) == 0 {
		return nil
	}
	out := make([]Source, 0, MaxSources)
	seen := make(map[string]int, MaxSources)
	for _, list := range [][]Source{current, incoming} {
		for _, source := range list {
			u, err := url.Parse(strings.TrimSpace(source.URL))
			if err != nil || u.Hostname() == "" || u.User != nil || (strings.ToLower(u.Scheme) != "http" && strings.ToLower(u.Scheme) != "https") {
				continue
			}
			if strings.ContainsFunc(source.URL, unicode.IsControl) {
				continue
			}
			u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
			u.Fragment = ""
			source.URL = strings.NewReplacer("<", "%3C", ">", "%3E", "\\", "%5C").Replace(u.String())
			source.Title = strings.Join(strings.FieldsFunc(source.Title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
			if title := []rune(source.Title); len(title) > 240 {
				source.Title = string(title[:240])
			}
			if i, ok := seen[source.URL]; ok {
				if source.Title != "" {
					out[i].Title = source.Title
				}
				continue
			}
			if len(out) == MaxSources {
				continue
			}
			if source.Title == "" {
				source.Title = u.Hostname()
			}
			seen[source.URL] = len(out)
			out = append(out, source)
		}
	}
	return out
}

// SourcesFooter 返回三种入站协议均可见的统一 Markdown 来源，不改动正文中的 [N]。
func SourcesFooter(sources []Source) string {
	sources = MergeSources(nil, sources)
	if len(sources) == 0 {
		return ""
	}
	escape := strings.NewReplacer("&", "&amp;", "\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`", "<", "&lt;", ">", "&gt;")
	var footer strings.Builder
	_, _ = footer.WriteString("\n\n### 来源\n")
	for i, source := range sources {
		_, _ = fmt.Fprintf(&footer, "\n%d. [%s](<%s>)", i+1, escape.Replace(source.Title), source.URL)
	}
	return footer.String()
}

// SearchProgress 是安全的检索状态，不是模型思维链或客户端可执行工具。
type SearchProgress struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Text   string `json:"text"`
}

// SafeSearchProgress 仅允许固定检索描述与枚举状态；不透传查询、代码、URL 或任意载荷。
func SafeSearchProgress(step Step) (SearchProgress, bool) {
	var label string
	switch step.Type {
	case "SEARCH_WEB", "SEARCHING":
		label = "正在搜索网页"
	case "READ_RESULTS", "BROWSER_OPEN_TAB":
		label = "正在阅读检索结果"
	default:
		return SearchProgress{}, false
	}
	status := strings.ToUpper(step.Status)
	if status == "" {
		for _, key := range []string{"status", "progress"} {
			if value, ok := step.Content[key].(string); ok && value != "" {
				status = strings.ToUpper(value)
				break
			}
		}
	}
	switch status {
	case "DONE", "COMPLETED":
		status = "DONE"
		label += "（已完成）"
	case "ERROR", "FAILED":
		status = "FAILED"
		label += "（未完成）"
	default:
		status = "IN_PROGRESS"
	}
	// UUID is only a deduplication key; hash it rather than exposing arbitrary
	// upstream identifiers in otherwise allowlisted metadata.
	id := sha256.Sum256([]byte(step.Type + ":" + step.UUID))
	return SearchProgress{ID: fmt.Sprintf("step-%x", id[:8]), Type: step.Type, Status: status, Text: label + "。\n"}, true
}
