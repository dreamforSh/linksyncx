package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
)

// perplexityTranslateParams 是翻译协程的运行参数。来源和进度开关由 send 的
// 默认请求选项显式传入；不把内部执行步骤映射为客户端可执行的 tool_calls。
type perplexityTranslateParams struct {
	upstreamModel         string
	promptTokens          int
	stream                bool
	created               int64
	id                    string
	IncludeSources        bool
	IncludeSearchProgress bool
	// StripCitations 剥离答案正文里的引用角标（[1][2]…）；来源 footer 不受影响。
	StripCitations      bool
	onComplete          func(perplexityCompletion)
	requestedPreference string
	identity            *perplexityResponseIdentity
	// metrics 在翻译完成后携带真实指标（搜索引用数）回转发层，供计费使用。
	metrics *perplexityResponseMetrics
}

// perplexityCompletion 仅在明确 EOS、非空回答且成功写出之后发布。
// Content 与用户可见 assistant 正文完全一致（包括本地来源 footer）。
type perplexityCompletion struct {
	Content     string
	BackendUUID string
	Sources     []perplexity.Source
}

// translatePerplexitySSEToCC 翻译真实 SSE 事件。首个语义事件前不发送 role，
// 出错保留已输出内容但绝不补 stop/[DONE]，以便调用方区分安全重试与部分失败。
func translatePerplexitySSEToCC(ctx context.Context, upstream io.ReadCloser, pw *io.PipeWriter, p perplexityTranslateParams) {
	defer func() { _ = upstream.Close() }()
	// A canceled context must interrupt both blocked reads and pipe backpressure.
	// The response-body owner cancels this context when its pipe reader closes.
	stop := context.AfterFunc(ctx, func() {
		_ = pw.CloseWithError(ctx.Err())
		_ = upstream.Close()
	})
	defer stop()
	_ = pw.CloseWithError(writePerplexityTranslation(ctx, upstream, pw, p))
}

func writePerplexityTranslation(ctx context.Context, upstream io.Reader, pw io.Writer, p perplexityTranslateParams) error {
	var answer perplexity.Answer
	var sources []perplexity.Source
	var progress []perplexity.SearchProgress
	progressIndex := make(map[string]int)
	backendUUID := ""
	roleSent := false
	sentBytes := 0
	preference := p.requestedPreference
	if preference == "" {
		preference, _ = perplexity.ResolveModelPreference(p.upstreamModel)
	}
	identity := perplexity.NewModelIdentityTracker(preference)
	defer func() {
		if p.identity != nil {
			p.identity.store(identity.Identity())
		}
	}()

	baseChunk := func() map[string]any {
		return map[string]any{
			"id":      p.id,
			"object":  "chat.completion.chunk",
			"created": p.created,
			"model":   p.upstreamModel,
		}
	}
	writeChunk := func(chunk map[string]any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(pw, "data: %s\n\n", b)
		return err
	}
	emitDelta := func(delta map[string]any, finishReason any, metadata map[string]any) error {
		chunk := baseChunk()
		chunk["choices"] = []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}}
		if metadata != nil {
			chunk["perplexity"] = metadata
		}
		return writeChunk(chunk)
	}
	ensureRole := func() error {
		if roleSent {
			return nil
		}
		if err := emitDelta(map[string]any{"role": "assistant"}, nil, nil); err != nil {
			return err
		}
		roleSent = true
		return nil
	}
	// 引用角标清洗（流式安全）：完整角标立即清洗，结尾处可能未完成的角标
	// 前缀（"["、"[1"）暂存到下一段；收尾时按普通文本下发残余。
	var citePending strings.Builder
	emitContent := func(text string) error {
		if !p.StripCitations {
			return emitDelta(map[string]any{"content": text}, nil, nil)
		}
		_, _ = citePending.WriteString(text)
		emit, rest := perplexity.StripCitationMarkersStreaming(citePending.String())
		citePending.Reset()
		_, _ = citePending.WriteString(rest)
		if emit == "" {
			return nil
		}
		return emitDelta(map[string]any{"content": emit}, nil, nil)
	}
	flushCitations := func() error {
		if !p.StripCitations || citePending.Len() == 0 {
			return nil
		}
		rest := citePending.String()
		citePending.Reset()
		return emitDelta(map[string]any{"content": perplexity.StripCitationMarkers(rest)}, nil, nil)
	}

	decoder := perplexity.NewDecoder(upstream)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, err := decoder.Next()
		if err != nil {
			return err
		}
		if ev.BackendUUID != "" {
			backendUUID = ev.BackendUUID
		}
		if len(ev.Sources) > 0 {
			sources = perplexity.MergeSources(sources, ev.Sources)
		}
		rewritten := ev.HasText && !ev.IsDelta && !strings.HasPrefix(ev.Text, answer.Content())
		newText, err := answer.Apply(ev, !p.stream || sentBytes == 0)
		if err != nil {
			return err
		}
		if rewritten {
			identity.ResetAnswer()
		}
		identity.Observe(ev, newText)
		if p.identity != nil {
			p.identity.store(identity.Identity())
		}
		if p.IncludeSearchProgress {
			for _, step := range ev.Steps {
				update, ok := perplexity.SafeSearchProgress(step)
				if !ok {
					continue
				}
				if i, exists := progressIndex[update.ID]; exists {
					if progress[i] == update {
						continue
					}
					progress[i] = update
				} else {
					// Bound state even if an upstream sends arbitrary new step IDs.
					if len(progress) == 128 {
						continue
					}
					progressIndex[update.ID] = len(progress)
					progress = append(progress, update)
				}
				if p.stream {
					if err := ensureRole(); err != nil {
						return err
					}
					if err := emitDelta(map[string]any{"reasoning_content": update.Text}, nil,
						map[string]any{"search_progress": []perplexity.SearchProgress{update}}); err != nil {
						return err
					}
				}
			}
		}
		if p.stream && (sentBytes > 0 || strings.TrimSpace(answer.Content()) != "") {
			// Apply already checks snapshot rewrites exactly. Incremental chunks
			// append to Answer's builder; neither copy nor rescan their old prefix.
			delta := answer.Content()[sentBytes:]
			if delta != "" {
				if err := ensureRole(); err != nil {
					return err
				}
				if err := emitContent(delta); err != nil {
					return err
				}
				sentBytes = len(answer.Content())
			}
		}
		if ev.EndOfStream {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rawAnswer := answer.Content()
	if strings.TrimSpace(rawAnswer) == "" {
		return perplexity.ErrEmptyAnswer
	}
	// 引用清洗只作用于答案正文；来源 footer 与代模型提示保持原样。
	answerText := rawAnswer
	if p.StripCitations {
		answerText = perplexity.StripCitationMarkers(rawAnswer)
	}
	footer := ""
	if p.IncludeSources {
		footer = perplexity.SourcesFooter(sources)
	}
	modelIdentity := identity.Identity()
	footer += modelIdentity.Notice()
	full := answerText + footer
	// These are local billing estimates, not upstream-reported token usage.
	// The footer and search progress are local presentation, not model output.
	completionTokens := perplexity.EstimateTokens(answerText)
	usage := map[string]any{
		"prompt_tokens":     p.promptTokens,
		"completion_tokens": completionTokens,
		"total_tokens":      p.promptTokens + completionTokens,
	}
	metadata := map[string]any{"usage_estimated": true, "usage_source": "local_estimate", "model_identity": modelIdentity}
	if len(progress) > 0 {
		metadata["search_progress"] = progress
	}
	addSources := func(result map[string]any) {
		if !p.IncludeSources || len(sources) == 0 {
			return
		}
		urls := make([]string, 0, len(sources))
		for _, source := range sources {
			urls = append(urls, source.URL)
		}
		result["citations"] = urls
		result["search_results"] = sources
	}
	if p.stream {
		if err := flushCitations(); err != nil {
			return err
		}
		if footer != "" {
			if err := emitDelta(map[string]any{"content": footer}, nil, nil); err != nil {
				return err
			}
		}
		if err := emitDelta(map[string]any{}, "stop", nil); err != nil {
			return err
		}
		usageChunk := baseChunk()
		usageChunk["choices"] = []any{}
		usageChunk["usage"] = usage
		usageChunk["perplexity"] = metadata
		addSources(usageChunk)
		if err := writeChunk(usageChunk); err != nil {
			return err
		}
		if _, err := io.WriteString(pw, "data: [DONE]\n\n"); err != nil {
			return err
		}
	} else {
		message := map[string]any{"role": "assistant", "content": full}
		if len(progress) > 0 {
			var text strings.Builder
			for _, update := range progress {
				_, _ = text.WriteString(update.Text)
			}
			message["reasoning_content"] = text.String()
		}
		result := map[string]any{
			"id":      p.id,
			"object":  "chat.completion",
			"created": p.created,
			"model":   p.upstreamModel,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": "stop",
			}},
			"usage":      usage,
			"perplexity": metadata,
		}
		addSources(result)
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if _, err := pw.Write(data); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 真实搜索引用数（上游可观测的最贴近 search 计费维度的指标）回传转发层。
	if p.metrics != nil {
		p.metrics.storeSearchCount(len(sources))
	}
	if p.onComplete != nil {
		p.onComplete(perplexityCompletion{Content: full, BackendUUID: backendUUID, Sources: sources})
	}
	return nil
}
