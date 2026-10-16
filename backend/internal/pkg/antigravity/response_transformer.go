package antigravity

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

// TransformGeminiToClaude 将 Gemini 响应转换为 Claude 格式（非流式）
func TransformGeminiToClaude(geminiResp []byte, originalModel string) ([]byte, *ClaudeUsage, error) {
	v1Resp, err := parseGeminiResponse(geminiResp)
	if err != nil {
		return nil, nil, err
	}
	if len(v1Resp.Response.Candidates) == 0 {
		return nil, nil, fmt.Errorf("gemini response has no candidates")
	}

	// 使用处理器转换
	processor := NewNonStreamingProcessor()
	claudeResp := processor.Process(&v1Resp.Response, v1Resp.ResponseID, originalModel)

	// 序列化
	respBytes, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal claude response: %w", err)
	}

	return respBytes, &claudeResp.Usage, nil
}

// response 字段存在时必须按包装响应解析，不能因 candidates 为空而误读外层字段。
func parseGeminiResponse(data []byte) (*V1InternalResponse, error) {
	var envelope struct {
		Response     json.RawMessage `json:"response"`
		Error        json.RawMessage `json:"error"`
		ResponseID   string          `json:"responseId"`
		ModelVersion string          `json:"modelVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("parse gemini response: %w", err)
	}

	responseData := data
	if envelope.Response != nil {
		responseData = envelope.Response
	}
	var fields struct {
		Candidates    json.RawMessage `json:"candidates"`
		UsageMetadata json.RawMessage `json:"usageMetadata"`
		Error         json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(responseData, &fields); err != nil {
		return nil, fmt.Errorf("parse gemini response body: %w", err)
	}

	resp := &V1InternalResponse{
		ResponseID:   envelope.ResponseID,
		ModelVersion: envelope.ModelVersion,
	}
	if err := json.Unmarshal(responseData, &resp.Response); err != nil {
		return nil, fmt.Errorf("parse gemini response body: %w", err)
	}
	// 错误帧中的合法用量仍需计费，但调用方不得把响应内容当作成功输出。
	if hasGeminiError(envelope.Error) {
		return resp, fmt.Errorf("gemini upstream error: %s", envelope.Error)
	}
	if hasGeminiError(fields.Error) {
		return resp, fmt.Errorf("gemini upstream error: %s", fields.Error)
	}
	if fields.Candidates == nil && fields.UsageMetadata == nil {
		return nil, fmt.Errorf("gemini response has neither candidates nor usageMetadata")
	}
	return resp, nil
}

func hasGeminiError(data json.RawMessage) bool {
	return len(data) > 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("null"))
}

// NonStreamingProcessor 非流式响应处理器
type NonStreamingProcessor struct {
	contentBlocks     []ClaudeContentItem
	textBuilder       string
	thinkingBuilder   string
	thinkingSignature string
	trailingSignature string
	hasToolCall       bool
}

// NewNonStreamingProcessor 创建非流式响应处理器
func NewNonStreamingProcessor() *NonStreamingProcessor {
	return &NonStreamingProcessor{
		contentBlocks: make([]ClaudeContentItem, 0),
	}
}

// Process 处理 Gemini 响应
func (p *NonStreamingProcessor) Process(geminiResp *GeminiResponse, responseID, originalModel string) *ClaudeResponse {
	// 获取 parts
	var parts []GeminiPart
	if len(geminiResp.Candidates) > 0 && geminiResp.Candidates[0].Content != nil {
		parts = geminiResp.Candidates[0].Content.Parts
	}

	// 处理所有 parts
	for _, part := range parts {
		p.processPart(&part)
	}

	if len(geminiResp.Candidates) > 0 {
		if grounding := geminiResp.Candidates[0].GroundingMetadata; grounding != nil {
			p.processGrounding(grounding)
		}
	}

	// 刷新剩余内容
	p.flushThinking()
	p.flushText()

	// 处理 trailingSignature
	if p.trailingSignature != "" {
		p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
			Type:      "thinking",
			Thinking:  "",
			Signature: p.trailingSignature,
		})
	}

	// 构建响应
	return p.buildResponse(geminiResp, responseID, originalModel)
}

// processPart 处理单个 part
func (p *NonStreamingProcessor) processPart(part *GeminiPart) {
	signature := part.ThoughtSignature

	// 1. FunctionCall 处理
	if part.FunctionCall != nil {
		p.flushThinking()
		p.flushText()

		// 处理 trailingSignature
		if p.trailingSignature != "" {
			p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
				Type:      "thinking",
				Thinking:  "",
				Signature: p.trailingSignature,
			})
			p.trailingSignature = ""
		}

		p.hasToolCall = true

		// 生成 tool_use id
		toolID := part.FunctionCall.ID
		if toolID == "" {
			toolID = fmt.Sprintf("%s-%s", part.FunctionCall.Name, generateRandomID())
		}

		item := ClaudeContentItem{
			Type:  "tool_use",
			ID:    toolID,
			Name:  part.FunctionCall.Name,
			Input: part.FunctionCall.Args,
		}

		if signature != "" {
			item.Signature = signature
		}

		p.contentBlocks = append(p.contentBlocks, item)
		return
	}

	// 独立签名必须附着在尚未刷新的 thinking 上，不能另建空块代替。
	if part.Text == "" && !part.Thought && part.InlineData == nil && signature != "" {
		if p.thinkingBuilder != "" || p.thinkingSignature != "" {
			p.thinkingSignature = signature
		} else {
			if p.trailingSignature != "" {
				p.flushText()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Signature: p.trailingSignature,
				})
			}
			p.trailingSignature = signature
		}
		return
	}

	// 2. Text 处理
	if part.Text != "" || part.Thought {
		if part.Thought {
			// Thinking part
			p.flushText()

			// 处理 trailingSignature
			if p.trailingSignature != "" {
				p.flushThinking()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: p.trailingSignature,
				})
				p.trailingSignature = ""
			}

			p.thinkingBuilder += part.Text
			if signature != "" {
				p.thinkingSignature = signature
			}
		} else {
			// 普通 Text
			if part.Text == "" {
				// 空 text 带签名 - 暂存
				if signature != "" {
					p.trailingSignature = signature
				}
				return
			}

			p.flushThinking()

			// 处理之前的 trailingSignature
			if p.trailingSignature != "" {
				p.flushText()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: p.trailingSignature,
				})
				p.trailingSignature = ""
			}

			// 非空 text 带签名 - 特殊处理：先输出 text，再输出空 thinking 块
			if signature != "" {
				p.flushText()
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type: "text",
					Text: part.Text,
				})
				p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
					Type:      "thinking",
					Thinking:  "",
					Signature: signature,
				})
			} else {
				// 普通 text (无签名) - 累积到 builder
				p.textBuilder += part.Text
			}
		}
	}

	// 3. InlineData (Image) 处理
	if part.InlineData != nil && part.InlineData.Data != "" {
		p.flushThinking()
		markdownImg := fmt.Sprintf("![image](data:%s;base64,%s)",
			part.InlineData.MimeType, part.InlineData.Data)
		p.textBuilder += markdownImg
		p.flushText()
	}
}

func (p *NonStreamingProcessor) processGrounding(grounding *GeminiGroundingMetadata) {
	groundingText := buildGroundingText(grounding)
	if groundingText == "" {
		return
	}

	p.flushThinking()
	p.flushText()
	p.textBuilder += groundingText
	p.flushText()
}

// flushText 刷新 text builder
func (p *NonStreamingProcessor) flushText() {
	if p.textBuilder == "" {
		return
	}

	p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
		Type: "text",
		Text: p.textBuilder,
	})
	p.textBuilder = ""
}

// flushThinking 刷新 thinking builder
func (p *NonStreamingProcessor) flushThinking() {
	if p.thinkingBuilder == "" && p.thinkingSignature == "" {
		return
	}

	p.contentBlocks = append(p.contentBlocks, ClaudeContentItem{
		Type:      "thinking",
		Thinking:  p.thinkingBuilder,
		Signature: p.thinkingSignature,
	})
	p.thinkingBuilder = ""
	p.thinkingSignature = ""
}

// buildResponse 构建最终响应
func (p *NonStreamingProcessor) buildResponse(geminiResp *GeminiResponse, responseID, originalModel string) *ClaudeResponse {
	var finishReason string
	if len(geminiResp.Candidates) > 0 {
		finishReason = geminiResp.Candidates[0].FinishReason
		if finishReason == "MALFORMED_FUNCTION_CALL" {
			log.Printf("[Antigravity] MALFORMED_FUNCTION_CALL detected in response for model %s", originalModel)
			if geminiResp.Candidates[0].Content != nil {
				if b, err := json.Marshal(geminiResp.Candidates[0].Content); err == nil {
					log.Printf("[Antigravity] Malformed content: %s", string(b))
				}
			}
		}
	}

	stopReason := "end_turn"
	if p.hasToolCall {
		stopReason = "tool_use"
	} else if finishReason == "MAX_TOKENS" {
		stopReason = "max_tokens"
	}

	// 注意：Gemini 的 promptTokenCount 包含 cachedContentTokenCount，
	// 但 Claude 的 input_tokens 不包含 cache_read_input_tokens，需要减去
	usage := ClaudeUsage{}
	if geminiResp.UsageMetadata != nil {
		cached := geminiResp.UsageMetadata.CachedContentTokenCount
		usage.InputTokens = geminiResp.UsageMetadata.PromptTokenCount - cached
		usage.OutputTokens = geminiResp.UsageMetadata.CandidatesTokenCount + geminiResp.UsageMetadata.ThoughtsTokenCount
		usage.CacheReadInputTokens = cached
		usage.ImageOutputTokens = geminiResp.UsageMetadata.ImageOutputTokens()
	}

	// 生成响应 ID
	respID := responseID
	if respID == "" {
		respID = geminiResp.ResponseID
	}
	if respID == "" {
		respID = generateAnthropicMsgID()
	}

	return &ClaudeResponse{
		ID:         respID,
		Type:       "message",
		Role:       "assistant",
		Model:      originalModel,
		Content:    p.contentBlocks,
		StopReason: stopReason,
		Usage:      usage,
	}
}

func buildGroundingText(grounding *GeminiGroundingMetadata) string {
	if grounding == nil {
		return ""
	}

	var builder strings.Builder

	if len(grounding.WebSearchQueries) > 0 {
		_, _ = builder.WriteString("\n\n---\nWeb search queries: ")
		_, _ = builder.WriteString(strings.Join(grounding.WebSearchQueries, ", "))
	}

	if len(grounding.GroundingChunks) > 0 {
		var links []string
		for i, chunk := range grounding.GroundingChunks {
			if chunk.Web == nil {
				continue
			}
			title := strings.TrimSpace(chunk.Web.Title)
			if title == "" {
				title = "Source"
			}
			uri := strings.TrimSpace(chunk.Web.URI)
			if uri == "" {
				uri = "#"
			}
			links = append(links, fmt.Sprintf("[%d] [%s](%s)", i+1, title, uri))
		}

		if len(links) > 0 {
			_, _ = builder.WriteString("\n\nSources:\n")
			_, _ = builder.WriteString(strings.Join(links, "\n"))
		}
	}

	return builder.String()
}

// fallbackCounter 降级伪随机 ID 的全局计数器，混入 seed 避免高并发下 UnixNano 相同导致碰撞。
var fallbackCounter uint64

// generateRandomID 生成密码学安全的随机 ID
func generateRandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	id := make([]byte, 12)
	randBytes := make([]byte, 12)
	if _, err := rand.Read(randBytes); err != nil {
		// 避免在请求路径里 panic：极端情况下熵源不可用时降级为伪随机。
		// 这里主要用于生成响应/工具调用的临时 ID，安全要求不高但需尽量避免碰撞。
		cnt := atomic.AddUint64(&fallbackCounter, 1)
		seed := uint64(time.Now().UnixNano()) ^ cnt
		seed ^= uint64(len(err.Error())) << 32
		for i := range id {
			seed ^= seed << 13
			seed ^= seed >> 7
			seed ^= seed << 17
			id[i] = chars[int(seed)%len(chars)]
		}
		return string(id)
	}
	for i, b := range randBytes {
		id[i] = chars[int(b)%len(chars)]
	}
	return string(id)
}

// generateAnthropicMsgID 生成 Anthropic 官方格式的 message ID：msg_01 + 22 位 Base62
func generateAnthropicMsgID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		return "msg_01" + generateRandomID() + generateRandomID()[:10]
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}
