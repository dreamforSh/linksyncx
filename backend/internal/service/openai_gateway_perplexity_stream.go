package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
)

// errPerplexityToolChoiceUnsatisfied 表示 tool_choice=required/具名 要求调用工具，但
// 模型没有产出满足约束的工具调用。调用方据此返回 400（而非把模型文本当成功）。
// 仅在 required/function 的同步翻译路径触发；绝不伪造调用。
var errPerplexityToolChoiceUnsatisfied = errors.New("perplexity: tool_choice required a tool call but none was produced")

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
	// toolAllowed 非空时启用函数工具模拟：用 ToolCallParser 识别上游回答开头的
	// <tool_call>{...}</tool_call> 块，还原为 CC tool_calls；解析失败整段按文本下发，
	// 绝不伪造调用。toolChoice 仅用于记录本轮约束（required/function 的兑现由模型
	// 决定，不满足时不编造，按文本返回）。
	toolAllowed []perplexity.ToolSpec
	toolChoice  perplexity.ToolChoice
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
	// 推理模型的计划目标去重后作为 reasoning_content 下发（"思考部分"）。
	planSeen := make(map[string]bool)
	var planReasoning []string
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

	// 函数工具模拟：toolParser 非 nil 时，回答增量先过 parser 再下发文本；
	// parser 吃未清洗原文（StripCitationMarkers 会破坏 {"ids":[1]} 这类参数），
	// 引用清洗只作用于 parser 放行的文本部分。
	var toolParser *perplexity.ToolCallParser
	if len(p.toolAllowed) > 0 {
		toolParser = perplexity.NewToolCallParser(p.toolAllowed)
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
			// 推理模型的计划目标（去重）作为思考内容下发，先于最终答案。
			for _, goal := range ev.Plan {
				text := perplexity.SafePlanReasoningText(goal)
				if text == "" || planSeen[text] {
					continue
				}
				if len(planSeen) >= 128 {
					break
				}
				planSeen[text] = true
				planReasoning = append(planReasoning, text)
				if p.stream {
					if err := ensureRole(); err != nil {
						return err
					}
					if err := emitDelta(map[string]any{"reasoning_content": text + "\n"}, nil,
						map[string]any{"reasoning_plan": []string{text}}); err != nil {
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
				emitText := delta
				if toolParser != nil {
					// 疑似工具调用会被 parser 暂存（返回空串），确认是文本才放行。
					emitText = toolParser.Feed(delta)
				}
				if emitText != "" {
					if err := emitContent(emitText); err != nil {
						return err
					}
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
	// 函数工具模拟收尾：流式已逐段 Feed，非流式在此一次性喂入；Finish 给出解析到的
	// 工具调用（与剩余文本互斥）。解析失败时 toolCalls 为空、toolRest 是回退文本。
	var toolRest string
	var toolCalls []perplexity.ToolCall
	if toolParser != nil {
		if !p.stream {
			_ = toolParser.Feed(rawAnswer)
		}
		toolRest, toolCalls = toolParser.Finish()
		// required/具名 tool_choice 未兑现：返回哨兵错误，由同步路径转 400，不伪造、
		// 也不把模型文本当成功。auto/none 永远满足，此处不受影响。
		if !p.toolChoice.Satisfied(toolCalls) {
			return errPerplexityToolChoiceUnsatisfied
		}
	}
	if len(toolCalls) > 0 {
		modelIdentity := identity.Identity()
		var argChars strings.Builder
		for _, call := range toolCalls {
			_, _ = argChars.WriteString(call.Arguments)
		}
		completionTokens := perplexity.EstimateTokens(argChars.String())
		usage := map[string]any{
			"prompt_tokens":     p.promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      p.promptTokens + completionTokens,
		}
		metadata := map[string]any{
			"usage_estimated": true, "usage_source": "local_estimate",
			"model_identity": modelIdentity, "tool_emulation": true,
		}
		if p.stream {
			if err := ensureRole(); err != nil {
				return err
			}
			if err := emitDelta(map[string]any{"tool_calls": perplexity.ToolCallsChatDelta(toolCalls)}, nil, nil); err != nil {
				return err
			}
			if err := emitDelta(map[string]any{}, "tool_calls", nil); err != nil {
				return err
			}
			usageChunk := baseChunk()
			usageChunk["choices"] = []any{}
			usageChunk["usage"] = usage
			usageChunk["perplexity"] = metadata
			if err := writeChunk(usageChunk); err != nil {
				return err
			}
			if _, err := io.WriteString(pw, "data: [DONE]\n\n"); err != nil {
				return err
			}
		} else {
			result := map[string]any{
				"id":      p.id,
				"object":  "chat.completion",
				"created": p.created,
				"model":   p.upstreamModel,
				"choices": []any{map[string]any{
					"index":         0,
					"message":       map[string]any{"role": "assistant", "content": nil, "tool_calls": perplexity.ToolCallsChatMessage(toolCalls)},
					"finish_reason": "tool_calls",
				}},
				"usage":      usage,
				"perplexity": metadata,
			}
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
		if p.metrics != nil {
			p.metrics.storeSearchCount(len(sources))
		}
		// 工具调用轮次不缓存可见正文续聊：下一轮客户端会带工具历史，由
		// NormalizeToolHistory 折叠为文本，无需在此缓存 assistant 文本。
		return nil
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
		// 工具模拟回退：疑似工具调用最终解析失败时，暂存的原文在此作为普通文本补发。
		if toolRest != "" {
			if err := ensureRole(); err != nil {
				return err
			}
			if err := emitContent(toolRest); err != nil {
				return err
			}
		}
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
		if len(planReasoning) > 0 || len(progress) > 0 {
			var text strings.Builder
			// 思考（计划目标）在前，检索进度在后，聚合为 reasoning_content。
			for _, plan := range planReasoning {
				_, _ = text.WriteString(plan)
				_, _ = text.WriteString("\n")
			}
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
