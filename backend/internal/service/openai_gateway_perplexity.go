package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 本文件实现 Perplexity.ai 网页订阅 API（Cookie 会话认证）的三条入站转发路径。
//
// 核心技巧：对上游 /rest/sse/perplexity_ask 只发一种请求，用一个翻译协程把
// Perplexity SSE 流转成 OpenAI Chat Completions 协议并灌进 io.Pipe，包装成
// 合成的 *http.Response 交给既有的下游 lifter：
//
//   - CC 入站：       streamRawChatCompletions / bufferRawChatCompletions
//   - Responses 入站：streamChatCompletionsAsResponses / bufferChatCompletionsAsResponses
//   - Messages 入站： streamChatCompletionsAsAnthropic / bufferChatCompletionsAsAnthropic
//
// Perplexity 是单次搜索问答：无 tool calling（转换后的 tools 静默丢弃）、无 token
// 用量（按 ~4 rune/token 估算兜底计费）、无多轮（消息历史折叠为单个 query_str）。
const perplexityUpstreamEndpoint = "/rest/sse/perplexity_ask"

// perplexityBadRequestError 表示客户端请求无法折叠为 Perplexity 查询
// （如最终用户消息仅含图片），映射为 400 invalid_request_error，不触发换号。
type perplexityBadRequestError struct{ msg string }

func (e *perplexityBadRequestError) Error() string { return e.msg }

// forwardPerplexityChatCompletions 处理 /v1/chat/completions 入站。
func (s *OpenAIGatewayService) forwardPerplexityChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	_ = promptCacheKey // Perplexity 无会话缓存语义，忽略。
	startTime := time.Now()

	originalModel := gjson.GetBytes(body, "model").String()
	if originalModel == "" {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	serviceTier := extractOpenAIServiceTierFromBody(body)
	SetOpsUpstreamModel(c, upstreamModel)

	logger.L().Debug("perplexity chat_completions: forwarding via web subscription API",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, body, clientStream, upstreamModel)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", badReq.msg)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
	}

	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, len(body))
	} else {
		result, forwardErr = s.bufferRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	if result != nil {
		result.UpstreamEndpoint = perplexityUpstreamEndpoint
	}
	return result, forwardErr
}

// forwardPerplexityResponses 处理 /v1/responses 入站：Responses → CC 折叠后
// 走 Perplexity 网页端，回程由既有 CC→Responses lifter 转换。
// 模板：forwardResponsesViaRawChatCompletions（仅替换上游发送部分）。
func (s *OpenAIGatewayService) forwardPerplexityResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel string,
	reqStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse responses request: %w", err)
	}
	if strings.TrimSpace(originalModel) == "" {
		originalModel = strings.TrimSpace(responsesReq.Model)
	}
	if originalModel == "" {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := responsesReq.Stream

	// 自愈回写：与 raw CC 回退路径一致的 reasoning item 缓存刷新。
	s.recacheReasoningItemsFromInput(responsesReq.Input)

	chatReq, err := apicompat.ResponsesToChatCompletionsRequestWithOptions(&responsesReq, &apicompat.ResponsesToChatOptions{
		ReasoningContentByID: s.reasoningContentByID,
	})
	if err != nil {
		writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}
	// Perplexity 无 tool calling：转换引入的 tools 在折叠阶段静默丢弃（不报错）。

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	chatReq.Model = upstreamModel

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions fallback request: %w", err)
	}
	chatBody, err = s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, chatBody)
	if err != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(err, &blocked) {
			writeOpenAIFastPolicyBlockedResponse(c, blocked)
		}
		return nil, err
	}
	serviceTier := extractOpenAIServiceTierFromBody(chatBody)

	logger.L().Debug("perplexity responses: forwarding via web subscription API",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)
	SetOpsUpstreamModel(c, upstreamModel)

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, chatBody, clientStream, upstreamModel)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", badReq.msg)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	// Perplexity 不产出 tool call，tool 映射参数恒为 nil/empty。
	if clientStream {
		return s.streamChatCompletionsAsResponses(c, resp, originalModel, nil, nil, false, nil, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return s.bufferChatCompletionsAsResponses(c, resp, originalModel, nil, nil, false, nil, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
}

// forwardPerplexityMessages 处理 /v1/messages 入站：Anthropic Messages → CC
// 折叠后走 Perplexity 网页端，回程由既有 CC→Anthropic lifter 转换。
// 模板：forwardAnthropicViaRawChatCompletions（仅替换上游发送部分）。
func (s *OpenAIGatewayService) forwardPerplexityMessages(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	originalModel := anthropicReq.Model
	if strings.TrimSpace(originalModel) == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	applyOpenAICompatModelNormalization(&anthropicReq)
	clientStream := anthropicReq.Stream

	chatReq, err := apicompat.AnthropicToChatCompletionsRequest(&anthropicReq)
	if err != nil {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, fmt.Errorf("convert anthropic to chat completions: %w", err)
	}
	// Perplexity 无 tool calling：tools 在折叠阶段静默丢弃。

	billingModel := resolveOpenAIForwardModel(account, anthropicReq.Model, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	chatReq.Model = upstreamModel
	chatReq.ReasoningEffort = openAICompatAnthropicReasoningEffort(&anthropicReq, upstreamModel, chatReq.ReasoningEffort)
	chatReq.Stream = clientStream

	serviceTier := extractOpenAIServiceTierFromBody(body)

	chatBody, err := json.Marshal(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal chat completions request: %w", err)
	}
	reasoningEffort := extractOpenAIReasoningEffortFromBody(chatBody, upstreamModel, billingModel, originalModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, chatBody, upstreamModel)

	logger.L().Debug("perplexity messages: forwarding via web subscription API",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)
	SetOpsUpstreamModel(c, upstreamModel)

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, chatBody, clientStream, upstreamModel)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", badReq.msg)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if clientStream {
		return s.streamChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return s.bufferChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
}

// perplexityCredentialsFromAccount 提取账号的 Perplexity 网页会话凭证。
func perplexityCredentialsFromAccount(account *Account) perplexity.Credentials {
	return perplexity.Credentials{
		SessionToken: account.GetPerplexitySessionToken(),
		CSRFToken:    account.GetPerplexityCSRFToken(),
		BaseURL:      account.GetPerplexityBaseURL(),
	}
}

// resolvePerplexityTLSProfile 解析出站 TLS 指纹：优先账号绑定/随机 profile，
// 否则使用内置默认（空字段 → dialer 内置抓包指纹）。
// Perplexity 网页端有反爬：纯 Go TLS 是强 bot 信号，本平台绝不裸发。
func (s *OpenAIGatewayService) resolvePerplexityTLSProfile(account *Account) *tlsfingerprint.Profile {
	if s != nil && s.tlsFPProfileService != nil {
		if p := s.tlsFPProfileService.ResolveTLSProfile(account); p != nil {
			return p
		}
	}
	return perplexityDefaultTLSProfile()
}

// sendPerplexityCCUpstreamRequest 把（已转换为 CC 协议的）请求体折叠为
// Perplexity query_str，发到 /rest/sse/perplexity_ask，并把上游 SSE 流翻译为
// OpenAI CC 协议后包装成合成 *http.Response 返回。
//
// 返回约定与 sendCCUpstreamRequest 一致：
//   - 可换号的上游错误（401/403/429/5xx）→ nil, *UpstreamFailoverError
//   - 不可换号的其他 4xx → 返回 resp（Body 已回卷），调用方按入站协议写错误
//   - 请求折叠失败 → *perplexityBadRequestError（调用方写 400）
func (s *OpenAIGatewayService) sendPerplexityCCUpstreamRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	chatBody []byte,
	stream bool,
	upstreamModel string,
) (*http.Response, error) {
	// 1. 折叠消息历史为单个 query_str（tools 被静默忽略）。
	query, err := perplexity.FoldMessagesToQuery(gjson.GetBytes(chatBody, "messages"))
	if err != nil {
		return nil, &perplexityBadRequestError{msg: err.Error()}
	}

	creds := perplexityCredentialsFromAccount(account)
	if strings.TrimSpace(creds.SessionToken) == "" {
		return nil, fmt.Errorf("account %d missing perplexity session token (credentials.api_key)", account.ID)
	}
	modelPref, mode := perplexity.ResolveModelPreference(upstreamModel)
	askBody, err := perplexity.BuildAskBody(query, modelPref, mode)
	if err != nil {
		return nil, fmt.Errorf("build perplexity ask body: %w", err)
	}
	targetURL := creds.NormalizedBaseURL() + perplexity.AskPath

	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(askBody))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	SetActualOpenAIUpstreamEndpoint(c, perplexityUpstreamEndpoint)
	creds.SetFetchStyleHeaders(upstreamReq.Header, "text/event-stream")
	// 账号级请求头覆写最后应用，与共享 CC 管线一致。
	account.ApplyHeaderOverrides(upstreamReq.Header)

	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return nil, proxyErr
	}
	resp, err := s.httpUpstream.DoWithTLS(upstreamReq, proxyURL, account.ID, account.Concurrency, s.resolvePerplexityTLSProfile(account))
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		failover := perplexityShouldFailover(resp.StatusCode)
		kind := "http_error"
		if failover {
			kind = "failover"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("cf-ray")),
			Kind:               kind,
			Message:            upstreamMsg,
		})
		s.handlePerplexityAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		if failover {
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				ResponseHeaders:        resp.Header.Clone(),
				RetryableOnSameAccount: false,
			}
		}
		// 非换号 4xx：Body 已由 readOpenAIUpstreamError 回卷，交给调用方按入站协议写错误。
		return resp, nil
	}

	// 2. 成功：启动翻译协程，把 Perplexity SSE 流转成 CC 协议灌入 pipe。
	pr, pw := io.Pipe()
	params := perplexityTranslateParams{
		upstreamModel: upstreamModel,
		promptTokens:  perplexity.EstimateTokens(query),
		stream:        stream,
		created:       time.Now().Unix(),
		id:            "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24],
	}
	go translatePerplexitySSEToCC(upstreamReq.Context(), resp.Body, pw, params)

	header := resp.Header.Clone()
	if stream {
		header.Set("Content-Type", "text/event-stream")
	} else {
		header.Set("Content-Type", "application/json")
	}
	header.Del("Content-Length")
	header.Del("Transfer-Encoding")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       pr,
	}, nil
}

// perplexityShouldFailover 判定上游状态码是否应换号重试：
// 401/403（会话失效）、429（限流）、5xx（服务端/Cloudflare）。
func perplexityShouldFailover(statusCode int) bool {
	return statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= 500
}

// perplexityTranslateParams 是翻译协程的运行参数。
type perplexityTranslateParams struct {
	upstreamModel string
	promptTokens  int
	stream        bool
	created       int64
	id            string
}

// translatePerplexitySSEToCC 是 Perplexity SSE → OpenAI CC 的翻译协程。
// 文本以累积快照到达，按后缀差分产出 content delta。收尾语义：
//   - end_of_stream 或 EOF（已有内容）：补 final chunk + usage chunk + [DONE]；
//   - 上游读错误 / 无任何内容的 EOF：CloseWithError，由下游截断处理接管；
//   - 任何路径都保证关闭 pipe writer 与上游 body。
func translatePerplexitySSEToCC(ctx context.Context, upstream io.ReadCloser, pw *io.PipeWriter, p perplexityTranslateParams) {
	defer func() { _ = upstream.Close() }()

	var accumulated strings.Builder
	lastSnapshot := ""
	produced := false
	roleSent := false
	backendUUID := ""
	// 工具执行轨迹：按 step uuid 去重，收集为 OpenAI tool_calls（上游已执行，
	// 仅作展示/审计，finish_reason 恒为 stop，客户端无需执行）。
	seenSteps := map[string]bool{}
	var toolCalls []any

	writeChunk := func(chunk map[string]any) error {
		b, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(pw, "data: %s\n\n", b)
		return err
	}
	baseChunk := func() map[string]any {
		return map[string]any{
			"id":      p.id,
			"object":  "chat.completion.chunk",
			"created": p.created,
			"model":   p.upstreamModel,
		}
	}
	emitDelta := func(delta map[string]any, finishReason any) error {
		chunk := baseChunk()
		chunk["choices"] = []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}}
		return writeChunk(chunk)
	}

	// finalize 在流式模式补终止 chunk + usage + [DONE]；非流式写单个完整 JSON。
	finalize := func() error {
		full := accumulated.String()
		completionTokens := perplexity.EstimateTokens(full)
		usage := map[string]any{
			"prompt_tokens":     p.promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      p.promptTokens + completionTokens,
		}
		if !p.stream {
			message := map[string]any{"role": "assistant", "content": full}
			if len(toolCalls) > 0 {
				message["tool_calls"] = toolCalls
			}
			resp := map[string]any{
				"id":      p.id,
				"object":  "chat.completion",
				"created": p.created,
				"model":   p.upstreamModel,
				"choices": []any{map[string]any{
					"index":         0,
					"message":       message,
					"finish_reason": "stop",
				}},
				"usage": usage,
			}
			b, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			_, err = pw.Write(b)
			return err
		}
		if !roleSent {
			if err := emitDelta(map[string]any{"role": "assistant"}, nil); err != nil {
				return err
			}
			roleSent = true
		}
		if err := emitDelta(map[string]any{}, "stop"); err != nil {
			return err
		}
		usageChunk := baseChunk()
		usageChunk["choices"] = []any{}
		usageChunk["usage"] = usage
		if err := writeChunk(usageChunk); err != nil {
			return err
		}
		_, err := io.WriteString(pw, "data: [DONE]\n\n")
		return err
	}

	scanner := newPerplexitySSEScanner(upstream)
	curEvent := ""
	var scanErr error
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			curEvent = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			curEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if curEvent == perplexity.SSEEventEndOfStream {
			break
		}
		if data == "" {
			continue
		}
		ev, ok := perplexity.ParseEvent([]byte(data))
		if !ok {
			continue
		}
		if ev.BackendUUID != "" {
			backendUUID = ev.BackendUUID
		}
		// 工具执行轨迹（Computer/代理模式的 pro_search_steps）：按 uuid 去重，
		// 首次出现即作为一个完整 tool_call 下发。步骤由上游代理自主执行，
		// 与客户端声明的 tools 无关（入站 tools 在折叠阶段已丢弃）。
		for _, step := range ev.Steps {
			if step.UUID == "" || seenSteps[step.UUID] {
				continue
			}
			seenSteps[step.UUID] = true
			args, _ := json.Marshal(step.Content)
			toolCalls = append(toolCalls, map[string]any{
				"id":   "call_" + strings.ReplaceAll(step.UUID, "-", ""),
				"type": "function",
				"function": map[string]any{
					"name":      perplexity.ToolNameForStep(step.Type),
					"arguments": string(args),
				},
			})
			if p.stream {
				if !roleSent {
					if err := emitDelta(map[string]any{"role": "assistant"}, nil); err != nil {
						_ = pw.CloseWithError(err)
						return
					}
					roleSent = true
				}
				tc := toolCalls[len(toolCalls)-1]
				if err := emitDelta(map[string]any{"tool_calls": []any{tc}}, nil); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
		}
		if !ev.HasText {
			continue
		}
		delta := perplexity.DeltaFromSnapshot(lastSnapshot, ev.Text)
		lastSnapshot = ev.Text
		if delta == "" {
			continue
		}
		if p.stream {
			if !roleSent {
				if err := emitDelta(map[string]any{"role": "assistant"}, nil); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				roleSent = true
			}
			if err := emitDelta(map[string]any{"content": delta}, nil); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		}
		_, _ = accumulated.WriteString(delta)
		produced = true
	}
	scanErr = scanner.Err()

	if scanErr != nil {
		if errors.Is(scanErr, context.Canceled) || errors.Is(scanErr, context.DeadlineExceeded) || errors.Is(scanErr, io.ErrClosedPipe) {
			// 客户端断连/取消：静默收尾，不计为上游故障。
			_ = pw.CloseWithError(scanErr)
			return
		}
		logger.L().Warn("perplexity: upstream stream read error",
			zap.Error(scanErr),
			zap.String("backend_uuid", backendUUID),
			zap.String("upstream_model", p.upstreamModel),
		)
		// 中途读错：直接关 pipe，下游截断处理（failover 或带类型错误）接管。
		_ = pw.CloseWithError(scanErr)
		return
	}
	if ctx.Err() != nil {
		_ = pw.CloseWithError(ctx.Err())
		return
	}
	if !produced {
		// 干净 EOF 但没有任何回答：按截断处理，让下游可换号。
		_ = pw.CloseWithError(errors.New("perplexity: stream ended without answer content"))
		return
	}
	if err := finalize(); err != nil {
		_ = pw.CloseWithError(err)
		return
	}
	_ = pw.Close()
}

// newPerplexitySSEScanner 按行扫描上游 SSE（事件以 \r\n\r\n 分隔，行级解析足够；
// 单行上限放大到 4MB，与网关 SSE 扫描惯例一致）。
func newPerplexitySSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return scanner
}

// handlePerplexityAccountUpstreamError 对 >=400 的上游响应做账号级副作用：
//   - 401/403（非 Cloudflare）：会话 Cookie 失效，临时停调（10m/30m）
//   - 429：按 Retry-After 冷却（缺省 5m）
//   - Cloudflare 质询：短冷却（5m）
//   - 5xx：短冷却（2m）
//
// 模式参考 handleGrokAccountUpstreamError 的 tempUnschedule/rateLimit 路径，
// 但保持单一冷却 setter，不做 grok 的配额窗口解析。
func (s *OpenAIGatewayService) handlePerplexityAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte) {
	if s == nil || account == nil {
		return
	}
	class, retryAfter := perplexity.ClassifyHTTPError(statusCode, headers, responseBody)
	if account.IsPoolMode() {
		logger.L().Debug("perplexity: pool-mode account error state skipped",
			zap.Int64("account_id", account.ID),
			zap.Int("status_code", statusCode),
			zap.String("class", class.String()),
		)
		return
	}
	switch class {
	case perplexity.ErrClassAuth:
		cooldown := 30 * time.Minute
		reason := "perplexity session access denied"
		if statusCode == http.StatusUnauthorized {
			cooldown = 10 * time.Minute
			reason = "perplexity session token unauthorized"
		}
		s.tempUnschedulePerplexity(ctx, account, cooldown, reason)
	case perplexity.ErrClassRateLimited:
		if retryAfter <= 0 {
			retryAfter = 5 * time.Minute
		}
		s.rateLimitPerplexity(ctx, account, time.Now().Add(retryAfter))
	case perplexity.ErrClassCloudflare:
		s.tempUnschedulePerplexity(ctx, account, 5*time.Minute, "perplexity cloudflare challenge")
	case perplexity.ErrClassServer:
		s.tempUnschedulePerplexity(ctx, account, 2*time.Minute, "perplexity upstream temporary error")
	}
}

// tempUnschedulePerplexity 临时停调账号（内存运行时 + 落库），镜像 tempUnscheduleGrok。
func (s *OpenAIGatewayService) tempUnschedulePerplexity(ctx context.Context, account *Account, cooldown time.Duration, reason string) {
	if s == nil || account == nil {
		return
	}
	until := time.Now().Add(cooldown)
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(until) {
		until = *account.TempUnschedulableUntil
	}
	s.BlockAccountScheduling(account, until, reason)
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason)
	}
}

// rateLimitPerplexity 按上游 Retry-After 安装限流冷却（内存运行时 + 落库）。
func (s *OpenAIGatewayService) rateLimitPerplexity(ctx context.Context, account *Account, resetAt time.Time) {
	if s == nil || account == nil {
		return
	}
	runtimeUntil := resetAt
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(runtimeUntil) {
		runtimeUntil = *account.TempUnschedulableUntil
	}
	s.BlockAccountScheduling(account, runtimeUntil, "429")
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		if err := s.accountRepo.SetRateLimited(stateCtx, account.ID, resetAt); err != nil {
			logger.L().Warn("perplexity: persist rate limit failed",
				zap.Error(err),
				zap.Int64("account_id", account.ID),
				zap.Time("reset_at", resetAt.UTC()),
			)
		}
	}
}
