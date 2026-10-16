package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

type antigravityStreamResult struct {
	usage            *ClaudeUsage
	firstTokenMs     *int
	clientDisconnect bool // 客户端是否在流式传输过程中断开
	usageIncomplete  bool
}

func (s *AntigravityGatewayService) observeAntigravityGeminiSSELine(c *gin.Context, line string) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return
	}
	// Observe the original payload: ObserveGemini supports both the v1internal
	// wrapper and direct Gemini response shapes. The main stream handler will
	// unwrap the same line for business processing, so unwrapping here would be
	// duplicate work on every SSE event.
	observer.ObserveGemini([]byte(payload))
}

// antigravityClientWriter 封装流式响应的客户端写入，自动检测断开并标记。
// 断开后所有写入操作变为 no-op，调用方通过 Disconnected() 判断是否继续 drain 上游。
type antigravityClientWriter struct {
	w                gin.ResponseWriter
	flusher          http.Flusher
	disconnected     bool
	prefix           string // 日志前缀，标识来源方法
	beforeFirstWrite func()
	writeTimeout     time.Duration
	onDisconnect     func()
}

func newAntigravityClientWriter(w gin.ResponseWriter, flusher http.Flusher, prefix string) *antigravityClientWriter {
	return &antigravityClientWriter{w: w, flusher: flusher, prefix: prefix, writeTimeout: defaultAntigravityWriteTimeout}
}

// Write 写入数据到客户端，写入失败时标记断开并返回 false
func (cw *antigravityClientWriter) Write(p []byte) bool {
	if cw.disconnected {
		return false
	}
	cw.prepareFirstWrite()
	controller := http.NewResponseController(cw.w)
	if cw.writeTimeout > 0 {
		if err := controller.SetWriteDeadline(time.Now().Add(cw.writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			cw.markDisconnected()
			return false
		}
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	}
	if n, err := cw.w.Write(p); err != nil || n != len(p) {
		cw.markDisconnected()
		return false
	}
	if flusher, ok := cw.flusher.(interface{ FlushError() error }); ok {
		if err := flusher.FlushError(); err != nil {
			cw.markDisconnected()
			return false
		}
	} else {
		cw.flusher.Flush()
	}
	return true
}

func (cw *antigravityClientWriter) Fprintf(format string, args ...any) bool {
	if cw.disconnected {
		return false
	}
	return cw.Write(fmt.Appendf(nil, format, args...))
}

func (cw *antigravityClientWriter) Disconnected() bool { return cw.disconnected }

func (cw *antigravityClientWriter) prepareFirstWrite() {
	if cw.beforeFirstWrite == nil {
		return
	}
	prepare := cw.beforeFirstWrite
	cw.beforeFirstWrite = nil
	prepare()
}

func (cw *antigravityClientWriter) markDisconnected() {
	if cw.disconnected {
		return
	}
	cw.disconnected = true
	if cw.onDisconnect != nil {
		cw.onDisconnect()
	}
	logger.LegacyPrintf("service.antigravity_gateway", "Client disconnected during streaming (%s), continuing to drain upstream for billing", cw.prefix)
}

// handleStreamReadError 处理上游读取错误的通用逻辑。
// 返回 (clientDisconnect, handled)：handled=true 表示错误已处理，调用方应返回已收集的 usage。
func handleStreamReadError(err error, clientDisconnected bool, prefix string) (disconnect bool, handled bool) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger.LegacyPrintf("service.antigravity_gateway", "Context canceled during streaming (%s), returning collected usage", prefix)
		return true, true
	}
	if clientDisconnected {
		logger.LegacyPrintf("service.antigravity_gateway", "Upstream read error after client disconnect (%s): %v, returning collected usage", prefix, err)
		return true, true
	}
	return false, false
}

// handleGeminiStreamToNonStreaming 读取上游流式响应，合并为非流式响应返回给客户端
// Gemini 流式响应是增量的，需要累积所有 chunk 的内容
func (s *AntigravityGatewayService) handleGeminiStreamToNonStreaming(c *gin.Context, resp *http.Response, startTime time.Time) (*antigravityStreamResult, error) {
	finalResponse, result, err := s.collectAntigravityStreamResponse(c, resp, startTime)
	if err != nil {
		return result, err
	}
	respBody, err := json.Marshal(finalResponse)
	if err != nil {
		result.usageIncomplete = true
		return result, fmt.Errorf("failed to marshal response: %w", err)
	}
	c.Data(http.StatusOK, "application/json", respBody)
	return result, nil
}

// getOrCreateGeminiParts copies the response paths it updates, leaving shared
// candidates/content maps and the caller's parts slice unchanged.
func getOrCreateGeminiParts(response map[string]any) (result map[string]any, existingParts []any, setParts func([]any)) {
	result = make(map[string]any, len(response)+1)
	for key, value := range response {
		result[key] = value
	}
	originalCandidates, _ := response["candidates"].([]any)
	candidates := append([]any(nil), originalCandidates...)
	if len(candidates) == 0 {
		candidates = make([]any, 1)
	}
	originalCandidate, _ := candidates[0].(map[string]any)
	candidate := make(map[string]any, len(originalCandidate)+1)
	for key, value := range originalCandidate {
		candidate[key] = value
	}
	candidates[0] = candidate
	originalContent, _ := candidate["content"].(map[string]any)
	content := make(map[string]any, len(originalContent)+1)
	for key, value := range originalContent {
		content[key] = value
	}
	if originalContent == nil {
		content["role"] = "model"
	}
	candidate["content"] = content
	parts, _ := content["parts"].([]any)
	existingParts = append([]any(nil), parts...)
	setParts = func(parts []any) {
		content["parts"] = parts
		result["candidates"] = candidates
	}
	return result, existingParts, setParts
}

// mergeCollectedPartsToResponse 将收集的所有 parts 合并到 Gemini 响应中
// 这个函数会合并所有类型的 parts：text、thinking、functionCall、inlineData 等
// 保持原始顺序，只合并连续的普通 text parts
func mergeCollectedPartsToResponse(response map[string]any, collectedParts []map[string]any) map[string]any {
	if len(collectedParts) == 0 {
		return response
	}

	result, _, setParts := getOrCreateGeminiParts(response)

	var mergedParts []any
	var textBuffer strings.Builder
	var hasText bool
	flushTextBuffer := func() {
		if hasText {
			mergedParts = append(mergedParts, map[string]any{"text": textBuffer.String()})
			textBuffer.Reset()
			hasText = false
		}
	}
	for _, part := range collectedParts {
		// Only a bare text part is mergeable. Signed text, explicit thought
		// flags, and unknown fields carry boundaries that must survive intact.
		if text, ok := part["text"].(string); ok && len(part) == 1 {
			_, _ = textBuffer.WriteString(text)
			hasText = true
			continue
		}
		flushTextBuffer()
		mergedParts = append(mergedParts, part)
	}
	flushTextBuffer()

	setParts(mergedParts)
	return result
}

func (s *AntigravityGatewayService) writeClaudeError(c *gin.Context, status int, errType, message string) error {
	MarkResponseCommitted(c)
	c.JSON(status, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": message},
	})
	return fmt.Errorf("%s", message)
}

// WriteMappedClaudeError 导出版本，供 handler 层使用（如 fallback 错误处理）
func (s *AntigravityGatewayService) WriteMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	return s.writeMappedClaudeError(c, account, upstreamStatus, upstreamRequestID, body)
}

func (s *AntigravityGatewayService) writeMappedClaudeError(c *gin.Context, account *Account, upstreamStatus int, upstreamRequestID string, body []byte) error {
	MarkResponseCommitted(c)
	upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(body))
	upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
	logBody, maxBytes := s.getLogConfig()
	upstreamDetail := s.getUpstreamErrorDetail(body)
	setOpsUpstreamError(c, upstreamStatus, upstreamMsg, upstreamDetail)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(account),
		ProxyName:          opsUpstreamProxyName(account),
		Platform:           account.Platform,
		AccountID:          account.ID,
		AccountName:        account.Name,
		UpstreamStatusCode: upstreamStatus,
		UpstreamRequestID:  upstreamRequestID,
		Kind:               "http_error",
		Message:            upstreamMsg,
		Detail:             upstreamDetail,
	})

	// 记录上游错误详情便于排障（可选：由配置控制；不回显到客户端）
	if logBody {
		logger.LegacyPrintf("service.antigravity_gateway", "[antigravity-Forward] upstream_error status=%d body=%s", upstreamStatus, truncateForLog(body, maxBytes))
	}

	// 检查错误透传规则
	if ptStatus, ptErrType, ptErrMsg, matched := applyErrorPassthroughRule(
		c, account.Platform, upstreamStatus, body,
		0, "", "",
	); matched {
		c.JSON(ptStatus, gin.H{
			"type":  "error",
			"error": gin.H{"type": ptErrType, "message": ptErrMsg},
		})
		if upstreamMsg == "" {
			return fmt.Errorf("upstream error: %d", upstreamStatus)
		}
		return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
	}

	var statusCode int
	var errType, errMsg string

	switch upstreamStatus {
	case 400:
		statusCode = http.StatusBadRequest
		errType = "invalid_request_error"
		errMsg = getPassthroughOrDefault(upstreamMsg, "Invalid request")
	case 401:
		statusCode = http.StatusBadGateway
		errType = "authentication_error"
		errMsg = "Upstream authentication failed"
	case 403:
		statusCode = http.StatusBadGateway
		errType = "permission_error"
		errMsg = "Upstream access forbidden"
	case 429:
		statusCode = http.StatusTooManyRequests
		errType = "rate_limit_error"
		errMsg = "Upstream rate limit exceeded"
	case 529:
		statusCode = http.StatusServiceUnavailable
		errType = "overloaded_error"
		errMsg = "Upstream service overloaded"
	default:
		statusCode = http.StatusBadGateway
		errType = "upstream_error"
		errMsg = "Upstream request failed"
	}

	c.JSON(statusCode, gin.H{
		"type":  "error",
		"error": gin.H{"type": errType, "message": errMsg},
	})
	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", upstreamStatus)
	}
	return fmt.Errorf("upstream error: %d message=%s", upstreamStatus, upstreamMsg)
}

func (s *AntigravityGatewayService) writeGoogleError(c *gin.Context, status int, message string) error {
	MarkResponseCommitted(c)
	statusStr := "UNKNOWN"
	switch status {
	case 400:
		statusStr = "INVALID_ARGUMENT"
	case 404:
		statusStr = "NOT_FOUND"
	case 429:
		statusStr = "RESOURCE_EXHAUSTED"
	case 500:
		statusStr = "INTERNAL"
	case 502, 503:
		statusStr = "UNAVAILABLE"
	}

	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  statusStr,
		},
	})
	return fmt.Errorf("%s", message)
}

// collectClaudeStreamResponse 收集上游流式响应，转换为 Claude 非流式格式返回
// 用于处理客户端非流式请求但上游只支持流式的情况
func (s *AntigravityGatewayService) collectClaudeStreamResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) ([]byte, *antigravityStreamResult, error) {
	finalResponse, result, err := s.collectAntigravityStreamResponse(c, resp, startTime)
	if err != nil {
		return nil, result, err
	}
	geminiBody, err := json.Marshal(finalResponse)
	if err != nil {
		result.usageIncomplete = true
		return nil, result, fmt.Errorf("failed to marshal gemini response: %w", err)
	}
	claudeResp, _, err := antigravity.TransformGeminiToClaude(geminiBody, originalModel)
	if err != nil {
		result.usageIncomplete = true
		return nil, result, fmt.Errorf("failed to parse upstream response: %w", err)
	}
	return claudeResp, result, nil
}

// handleClaudeStreamToNonStreaming 收集上游流式响应，转换为 Claude 非流式格式返回
// 用于处理客户端非流式请求但上游只支持流式的情况
func (s *AntigravityGatewayService) handleClaudeStreamToNonStreaming(c *gin.Context, resp *http.Response, startTime time.Time, originalModel string) (*antigravityStreamResult, error) {
	claudeResp, streamRes, err := s.collectClaudeStreamResponse(c, resp, startTime, originalModel)
	if err != nil {
		var failoverErr *UpstreamFailoverError
		if errors.As(err, &failoverErr) {
			return streamRes, err
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return streamRes, err
		}

		errMsg := "Failed to parse upstream response"
		errType := "upstream_error"
		if strings.Contains(err.Error(), "stream data interval timeout") {
			errMsg = "Upstream stream data interval timeout"
			errType = "upstream_timeout"
		} else if errors.Is(err, bufio.ErrTooLong) {
			errMsg = "Upstream response line too long"
			errType = "response_too_large"
		}

		_ = s.writeClaudeError(c, http.StatusBadGateway, errType, errMsg)
		return streamRes, err
	}
	c.Data(http.StatusOK, "application/json", claudeResp)
	return streamRes, nil
}

// handleClaudeStreamingResponse 处理 Claude 流式响应（Gemini SSE → Claude SSE 转换）
func (s *AntigravityGatewayService) extractImageInputSize(body []byte) string {
	var req antigravity.GeminiRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}

	if req.GenerationConfig != nil && req.GenerationConfig.ImageConfig != nil {
		return strings.TrimSpace(req.GenerationConfig.ImageConfig.ImageSize)
	}

	return ""
}

// isImageGenerationModel 判断模型是否为图片生成模型
// 支持的模型：gemini-3.1-flash-image, gemini-3-pro-image, gemini-2.5-flash-image 等
func isImageGenerationModel(model string) bool {
	modelLower := strings.ToLower(model)
	// 移除 models/ 前缀
	modelLower = strings.TrimPrefix(modelLower, "models/")

	// 精确匹配或前缀匹配
	return modelLower == "gemini-3.1-flash-image" ||
		modelLower == "gemini-3.1-flash-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-3.1-flash-image-") ||
		modelLower == "gemini-3-pro-image" ||
		modelLower == "gemini-3-pro-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-3-pro-image-") ||
		modelLower == "gemini-2.5-flash-image" ||
		modelLower == "gemini-2.5-flash-image-preview" ||
		strings.HasPrefix(modelLower, "gemini-2.5-flash-image-")
}
