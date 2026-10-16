package antigravity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// BlockType 内容块类型
type BlockType int

const (
	BlockTypeNone BlockType = iota
	BlockTypeText
	BlockTypeThinking
	BlockTypeFunction
)

// UsageMapHook is a callback that can modify usage data before it's emitted in SSE events.
type UsageMapHook func(usageMap map[string]any)

// StreamingProcessor 流式响应处理器
type StreamingProcessor struct {
	blockType         BlockType
	blockIndex        int
	messageStartSent  bool
	messageStopSent   bool
	finished          bool
	finishReason      string
	responseID        string
	err               error
	usedTool          bool
	pendingSignature  string
	trailingSignature string
	originalModel     string
	webSearchQueries  []string
	groundingChunks   []GeminiGroundingChunk
	usageMapHook      UsageMapHook
	startUsage        any
	finalUsage        any

	// Gemini 的 prompt tokens 包含缓存命中，Claude 的 input tokens 不包含。
	inputTokens       int
	outputTokens      int
	cacheReadTokens   int
	imageOutputTokens int
}

// NewStreamingProcessor 创建流式响应处理器
func NewStreamingProcessor(originalModel string) *StreamingProcessor {
	return &StreamingProcessor{
		blockType:     BlockTypeNone,
		originalModel: originalModel,
	}
}

// SetUsageMapHook 的额外字段由字节 API 保留；结构化 API 保留 AnthropicUsage 支持的字段。
func (p *StreamingProcessor) SetUsageMapHook(fn UsageMapHook) {
	p.usageMapHook = fn
}

func usageToMap(u ClaudeUsage) map[string]any {
	m := map[string]any{
		"input_tokens":  u.InputTokens,
		"output_tokens": u.OutputTokens,
	}
	if u.CacheCreationInputTokens > 0 {
		m["cache_creation_input_tokens"] = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		m["cache_read_input_tokens"] = u.CacheReadInputTokens
	}
	if u.ImageOutputTokens > 0 {
		m["image_output_tokens"] = u.ImageOutputTokens
	}
	return m
}

// ProcessLine 保留原有的 SSE 字节接口，错误由 Err 返回。
func (p *StreamingProcessor) ProcessLine(line string) []byte {
	return p.formatEvents(p.ProcessLineEvents(line))
}

// ProcessLineEvents 不发送最终终止事件；调用方必须继续读取尾部用量并调用 FinishEvents。
func (p *StreamingProcessor) ProcessLineEvents(line string) []apicompat.AnthropicStreamEvent {
	if p.finished || p.err != nil {
		return nil
	}
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "data:") {
		return nil
	}

	data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}

	v1Resp, err := parseGeminiResponse([]byte(data))
	if v1Resp != nil && v1Resp.Response.UsageMetadata != nil {
		metadata := v1Resp.Response.UsageMetadata
		cached := metadata.CachedContentTokenCount
		p.inputTokens = metadata.PromptTokenCount - cached
		p.outputTokens = metadata.CandidatesTokenCount + metadata.ThoughtsTokenCount
		p.cacheReadTokens = cached
		p.imageOutputTokens = metadata.ImageOutputTokens()
	}
	if err != nil {
		p.err = err
		return nil
	}
	geminiResp := &v1Resp.Response
	if p.responseID == "" {
		p.responseID = v1Resp.ResponseID
		if p.responseID == "" {
			p.responseID = geminiResp.ResponseID
		}
	}

	var events []apicompat.AnthropicStreamEvent
	if len(geminiResp.Candidates) > 0 {
		candidate := &geminiResp.Candidates[0]
		if !p.messageStartSent && geminiCandidateHasContent(candidate) {
			events = append(events, p.emitMessageStart()...)
		}
		if candidate.Content != nil {
			for _, part := range candidate.Content.Parts {
				events = append(events, p.processPart(&part)...)
			}
		}
		p.captureGrounding(candidate.GroundingMetadata)
		if candidate.FinishReason != "" {
			p.finishReason = candidate.FinishReason
			if candidate.FinishReason == "MALFORMED_FUNCTION_CALL" {
				log.Printf("[Antigravity] MALFORMED_FUNCTION_CALL detected in stream for model %s", p.originalModel)
			}
		}
	}
	if p.err != nil {
		return nil
	}
	return events
}

// Finish 保留原有的 SSE 字节接口；调用方必须检查 Err，不能把 EOF 当作正常终止。
func (p *StreamingProcessor) Finish() ([]byte, *ClaudeUsage) {
	events, usage := p.FinishEvents()
	return p.formatEvents(events), usage
}

// FinishEvents 只在收到 finishReason 且没有错误时输出最终事件，重复调用不再输出。
func (p *StreamingProcessor) FinishEvents() ([]apicompat.AnthropicStreamEvent, *ClaudeUsage) {
	usage := p.Usage()
	if p.finished {
		return nil, usage
	}
	p.finished = true
	if p.err != nil {
		return nil, usage
	}
	if p.finishReason == "" {
		p.err = fmt.Errorf("gemini stream ended without finishReason: %w", io.ErrUnexpectedEOF)
		return nil, usage
	}
	if !p.messageStartSent {
		return nil, usage
	}
	events := p.emitFinish(p.finishReason)
	if p.err != nil {
		return nil, usage
	}
	return events, usage
}

// Usage 返回不受后续处理或调用方修改影响的上游用量快照。
func (p *StreamingProcessor) Usage() *ClaudeUsage {
	return &ClaudeUsage{
		InputTokens:          p.inputTokens,
		OutputTokens:         p.outputTokens,
		CacheReadInputTokens: p.cacheReadTokens,
		ImageOutputTokens:    p.imageOutputTokens,
	}
}

// Err 保留首个解析或编码错误；缺失 finishReason 的 EOF 由 FinishEvents 记录。
func (p *StreamingProcessor) Err() error {
	return p.err
}

// MessageStartSent 报告流中是否已发出过 message_start 事件。
func (p *StreamingProcessor) MessageStartSent() bool {
	return p.messageStartSent
}

func (p *StreamingProcessor) eventUsage() (apicompat.AnthropicUsage, any) {
	usage := *p.Usage()
	typedUsage := apicompat.AnthropicUsage{
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
	}
	if p.usageMapHook == nil {
		return typedUsage, usage
	}
	usageMap := usageToMap(usage)
	p.usageMapHook(usageMap)
	data, err := json.Marshal(usageMap)
	if err == nil {
		typedUsage = apicompat.AnthropicUsage{}
		err = json.Unmarshal(data, &typedUsage)
	}
	if err != nil && p.err == nil {
		p.err = fmt.Errorf("encode gemini stream usage: %w", err)
	}
	return typedUsage, usageMap
}

func geminiCandidateHasContent(candidate *GeminiCandidate) bool {
	if candidate.Content != nil {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" || part.Thought || part.ThoughtSignature != "" || part.FunctionCall != nil ||
				(part.InlineData != nil && part.InlineData.Data != "") {
				return true
			}
		}
	}
	return buildGroundingText(candidate.GroundingMetadata) != ""
}

func (p *StreamingProcessor) emitMessageStart() []apicompat.AnthropicStreamEvent {
	responseID := p.responseID
	if responseID == "" {
		responseID = generateAnthropicMsgID()
	}

	usage, wireUsage := p.eventUsage()
	p.startUsage = wireUsage
	p.messageStartSent = true
	return []apicompat.AnthropicStreamEvent{{
		Type: "message_start",
		Message: &apicompat.AnthropicResponse{
			ID:      responseID,
			Type:    "message",
			Role:    "assistant",
			Content: []apicompat.AnthropicContentBlock{},
			Model:   p.originalModel,
			Usage:   usage,
		},
	}}
}

func (p *StreamingProcessor) processPart(part *GeminiPart) []apicompat.AnthropicStreamEvent {
	var events []apicompat.AnthropicStreamEvent
	signature := part.ThoughtSignature
	if part.FunctionCall != nil {
		events = append(events, p.flushTrailingSignature()...)
		return append(events, p.processFunctionCall(part.FunctionCall, signature)...)
	}

	// 独立签名属于尚未关闭的 thinking，不能另建空块而留下原 thinking 无签名。
	if part.Text == "" && !part.Thought && part.InlineData == nil && signature != "" {
		if p.blockType == BlockTypeThinking {
			p.pendingSignature = signature
			return nil
		}
		events = append(events, p.flushTrailingSignature()...)
		p.trailingSignature = signature
		return events
	}

	if part.Text != "" || part.Thought {
		if part.Thought {
			events = append(events, p.processThinking(part.Text, signature)...)
		} else {
			events = append(events, p.processText(part.Text, signature)...)
		}
	}
	if part.InlineData != nil && part.InlineData.Data != "" {
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)", part.InlineData.MimeType, part.InlineData.Data)
		events = append(events, p.processText(markdownImg, "")...)
	}
	return events
}

func (p *StreamingProcessor) captureGrounding(grounding *GeminiGroundingMetadata) {
	if grounding == nil {
		return
	}
	if len(grounding.WebSearchQueries) > 0 && len(p.webSearchQueries) == 0 {
		p.webSearchQueries = append([]string(nil), grounding.WebSearchQueries...)
	}
	if len(grounding.GroundingChunks) > 0 && len(p.groundingChunks) == 0 {
		p.groundingChunks = append([]GeminiGroundingChunk(nil), grounding.GroundingChunks...)
	}
}

func (p *StreamingProcessor) processThinking(text, signature string) []apicompat.AnthropicStreamEvent {
	events := p.flushTrailingSignature()
	if p.blockType != BlockTypeThinking {
		events = append(events, p.startBlock(BlockTypeThinking, &apicompat.AnthropicContentBlock{Type: "thinking"})...)
	}
	if text != "" {
		events = append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "thinking_delta", Thinking: text}))
	}
	if signature != "" {
		p.pendingSignature = signature
	}
	return events
}

func (p *StreamingProcessor) processText(text, signature string) []apicompat.AnthropicStreamEvent {
	if text == "" {
		if signature != "" {
			p.trailingSignature = signature
		}
		return nil
	}
	events := p.flushTrailingSignature()
	if signature != "" {
		events = append(events, p.startBlock(BlockTypeText, &apicompat.AnthropicContentBlock{Type: "text"})...)
		events = append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "text_delta", Text: text}))
		events = append(events, p.endBlock()...)
		return append(events, p.emitEmptyThinkingWithSignature(signature)...)
	}
	if p.blockType != BlockTypeText {
		events = append(events, p.startBlock(BlockTypeText, &apicompat.AnthropicContentBlock{Type: "text"})...)
	}
	return append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "text_delta", Text: text}))
}

func (p *StreamingProcessor) processFunctionCall(fc *GeminiFunctionCall, signature string) []apicompat.AnthropicStreamEvent {
	var argsJSON []byte
	if fc.Args != nil {
		var err error
		argsJSON, err = json.Marshal(fc.Args)
		if err != nil {
			p.err = fmt.Errorf("marshal gemini function arguments: %w", err)
			return nil
		}
	}
	p.usedTool = true
	toolID := fc.ID
	if toolID == "" {
		toolID = fmt.Sprintf("%s-%s", fc.Name, generateRandomID())
	}
	toolUse := &apicompat.AnthropicContentBlock{
		Type:      "tool_use",
		ID:        toolID,
		Name:      fc.Name,
		Input:     json.RawMessage(`{}`),
		Signature: signature,
	}
	events := p.startBlock(BlockTypeFunction, toolUse)
	if argsJSON != nil {
		events = append(events, p.emitDelta(&apicompat.AnthropicDelta{
			Type:        "input_json_delta",
			PartialJSON: string(argsJSON),
		}))
	}
	return append(events, p.endBlock()...)
}

func (p *StreamingProcessor) startBlock(blockType BlockType, contentBlock *apicompat.AnthropicContentBlock) []apicompat.AnthropicStreamEvent {
	events := p.endBlock()
	events = append(events, apicompat.AnthropicStreamEvent{
		Type:         "content_block_start",
		Index:        streamBlockIndex(p.blockIndex),
		ContentBlock: contentBlock,
	})
	p.blockType = blockType
	return events
}

func (p *StreamingProcessor) endBlock() []apicompat.AnthropicStreamEvent {
	if p.blockType == BlockTypeNone {
		return nil
	}
	var events []apicompat.AnthropicStreamEvent
	if p.blockType == BlockTypeThinking && p.pendingSignature != "" {
		events = append(events, p.emitDelta(&apicompat.AnthropicDelta{
			Type:      "signature_delta",
			Signature: p.pendingSignature,
		}))
		p.pendingSignature = ""
	}
	events = append(events, apicompat.AnthropicStreamEvent{
		Type:  "content_block_stop",
		Index: streamBlockIndex(p.blockIndex),
	})
	p.blockIndex++
	p.blockType = BlockTypeNone
	return events
}

func streamBlockIndex(index int) *int {
	return &index
}

func (p *StreamingProcessor) emitDelta(delta *apicompat.AnthropicDelta) apicompat.AnthropicStreamEvent {
	return apicompat.AnthropicStreamEvent{
		Type:  "content_block_delta",
		Index: streamBlockIndex(p.blockIndex),
		Delta: delta,
	}
}

func (p *StreamingProcessor) flushTrailingSignature() []apicompat.AnthropicStreamEvent {
	if p.trailingSignature == "" {
		return nil
	}
	events := p.endBlock()
	events = append(events, p.emitEmptyThinkingWithSignature(p.trailingSignature)...)
	p.trailingSignature = ""
	return events
}

func (p *StreamingProcessor) emitEmptyThinkingWithSignature(signature string) []apicompat.AnthropicStreamEvent {
	events := p.startBlock(BlockTypeThinking, &apicompat.AnthropicContentBlock{Type: "thinking"})
	events = append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "thinking_delta"}))
	events = append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "signature_delta", Signature: signature}))
	return append(events, p.endBlock()...)
}

func (p *StreamingProcessor) emitFinish(finishReason string) []apicompat.AnthropicStreamEvent {
	events := p.endBlock()
	events = append(events, p.flushTrailingSignature()...)
	if len(p.webSearchQueries) > 0 || len(p.groundingChunks) > 0 {
		groundingText := buildGroundingText(&GeminiGroundingMetadata{
			WebSearchQueries: p.webSearchQueries,
			GroundingChunks:  p.groundingChunks,
		})
		if groundingText != "" {
			events = append(events, p.startBlock(BlockTypeText, &apicompat.AnthropicContentBlock{Type: "text"})...)
			events = append(events, p.emitDelta(&apicompat.AnthropicDelta{Type: "text_delta", Text: groundingText}))
			events = append(events, p.endBlock()...)
		}
	}

	stopReason := "end_turn"
	if p.usedTool {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}
	usage, wireUsage := p.eventUsage()
	p.finalUsage = wireUsage
	events = append(events, apicompat.AnthropicStreamEvent{
		Type: "message_delta",
		Delta: &apicompat.AnthropicDelta{
			StopReason: stopReason,
		},
		Usage: &usage,
	})
	if !p.messageStopSent {
		events = append(events, apicompat.AnthropicStreamEvent{Type: "message_stop"})
		p.messageStopSent = true
	}
	return events
}

// 字节接口保留空 text/thinking、null stop_sequence、图片用量和 hook 扩展字段。
func (p *StreamingProcessor) formatEvents(events []apicompat.AnthropicStreamEvent) []byte {
	var result bytes.Buffer
	for _, event := range events {
		data := map[string]any{"type": event.Type}
		if event.Index != nil {
			data["index"] = *event.Index
		}
		switch event.Type {
		case "message_start":
			data["message"] = map[string]any{
				"id":            event.Message.ID,
				"type":          event.Message.Type,
				"role":          event.Message.Role,
				"content":       []any{},
				"model":         event.Message.Model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         p.startUsage,
			}
		case "content_block_start":
			block := event.ContentBlock
			content := map[string]any{"type": block.Type}
			switch block.Type {
			case "text":
				content["text"] = block.Text
			case "thinking":
				content["thinking"] = block.Thinking
			case "tool_use":
				content["id"] = block.ID
				content["name"] = block.Name
				content["input"] = block.Input
				if block.Signature != "" {
					content["signature"] = block.Signature
				}
			}
			data["content_block"] = content
		case "content_block_delta":
			delta := event.Delta
			content := map[string]any{"type": delta.Type}
			switch delta.Type {
			case "text_delta":
				content["text"] = delta.Text
			case "thinking_delta":
				content["thinking"] = delta.Thinking
			case "signature_delta":
				content["signature"] = delta.Signature
			case "input_json_delta":
				content["partial_json"] = delta.PartialJSON
			}
			data["delta"] = content
		case "message_delta":
			data["delta"] = map[string]any{
				"stop_reason":   event.Delta.StopReason,
				"stop_sequence": nil,
			}
			data["usage"] = p.finalUsage
		}
		jsonData, err := json.Marshal(data)
		if err != nil {
			if p.err == nil {
				p.err = fmt.Errorf("marshal gemini stream event: %w", err)
			}
			return nil
		}
		_, _ = fmt.Fprintf(&result, "event: %s\ndata: %s\n\n", event.Type, jsonData)
	}
	return result.Bytes()
}
