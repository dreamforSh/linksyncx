package service

import (
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
// 网页端不提供客户端函数调用或原生 token 用量；搜索步骤不能作为工具指令，
// 本地估算仅供既有计费使用，不能记为真实上游计量。
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
	account, err := freshPerplexityForwardAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	_ = promptCacheKey // Continuation uses isolated visible-history keys, not a cache hint alone.
	startTime := time.Now()

	originalModel := gjson.GetBytes(body, "model").String()
	if originalModel == "" {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()
	opts, optionErr := perplexityOptionsForIngress(c, account, body, "chat_completions")
	if optionErr != nil {
		writePerplexityIngressError(c, "chat_completions", optionErr.Error())
		return nil, optionErr
	}

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

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, body, clientStream, upstreamModel, opts)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writePerplexityIngressError(c, "chat_completions", badReq.msg)
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
	return markPerplexityResult(result, forwardErr, resp)
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
	account, err := freshPerplexityForwardAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	opts, optionErr := perplexityOptionsForIngress(c, account, body, "responses")
	if optionErr != nil {
		writePerplexityIngressError(c, "responses", optionErr.Error())
		return nil, optionErr
	}
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
	// Client tools have already been validated before the typed conversion.

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

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, chatBody, clientStream, upstreamModel, opts)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writePerplexityIngressError(c, "responses", badReq.msg)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleErrorResponse(ctx, resp, c, account, chatBody, billingModel)
	}

	// Internal search activity is not a client function call.
	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamChatCompletionsAsResponses(c, resp, originalModel, nil, nil, false, nil, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	} else {
		result, forwardErr = s.bufferChatCompletionsAsResponses(c, resp, originalModel, nil, nil, false, nil, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return markPerplexityResult(result, forwardErr, resp)
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
	account, err := freshPerplexityForwardAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	startTime := time.Now()

	opts, optionErr := perplexityOptionsForIngress(c, account, body, "messages")
	if optionErr != nil {
		writePerplexityIngressError(c, "messages", optionErr.Error())
		return nil, optionErr
	}
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
	// Client tools have already been validated before the typed conversion.

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

	resp, err := s.sendPerplexityCCUpstreamRequest(ctx, c, account, chatBody, clientStream, upstreamModel, opts)
	if err != nil {
		var badReq *perplexityBadRequestError
		if errors.As(err, &badReq) {
			writePerplexityIngressError(c, "messages", badReq.msg)
		}
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	var result *OpenAIForwardResult
	var forwardErr error
	if clientStream {
		result, forwardErr = s.streamChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	} else {
		result, forwardErr = s.bufferChatCompletionsAsAnthropic(c, resp, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	return markPerplexityResult(result, forwardErr, resp)
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
	requestOptions ...perplexity.RequestOptions,
) (*http.Response, error) {
	var opts perplexity.RequestOptions
	if len(requestOptions) > 0 {
		opts = requestOptions[0]
	} else {
		var err error
		opts, err = perplexityOptionsForIngress(c, account, chatBody, "chat_completions")
		if err != nil {
			return nil, err
		}
	}
	creds := perplexityCredentialsFromAccount(account)
	if strings.TrimSpace(creds.SessionToken) == "" {
		return nil, fmt.Errorf("account %d missing perplexity session token (credentials.api_key)", account.ID)
	}
	if _, err := account.ProxyURLForOutbound(); err != nil {
		return nil, err
	}
	modelPref, mode := perplexity.ResolveModelPreference(upstreamModel)
	// 死模型自愈：目录快照显示该 preference 已下线且存在同族更新版本时自动升级
	// （须在思考链变体解析之前，使变体选择基于升级后的基座）。
	modelPref = upgradePerplexityDeadModelPreference(account, upstreamModel, modelPref)
	selectedPreference, selectionErr := resolvePerplexityThinkingPreference(account, modelPref, opts)
	if selectionErr != nil {
		return nil, selectionErr
	}
	modelPref = selectedPreference
	SetOpsUpstreamModel(c, modelPref)
	if reason := perplexityPreferenceHealthPauseReason(account, modelPref, mode); reason != "" {
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusServiceUnavailable,
			ResponseBody: []byte(`{"error":{"type":"api_error","message":"the selected Perplexity model is temporarily unavailable"}}`),
		}
	}
	if opts.Internal.ThinkingEnabled != nil {
		selection := "disabled"
		if *opts.Internal.ThinkingEnabled {
			selection = "enabled"
		}
		c.Header("X-Perplexity-Thinking-Selection", selection)
	}
	if bucket := perplexityExhaustedQuotaBucket(account, modelPref, mode, time.Now()); bucket != "" {
		return nil, perplexityQuotaFailover(bucket)
	}
	c.Header("X-Perplexity-Model-Preference", modelPref)
	c.Header("X-Perplexity-Mode", mode)
	upstreamCtx, cancelUpstream := context.WithTimeout(ctx, perplexityRequestMaxDuration)
	chatBody, attachments, err := s.preparePerplexityImageAttachments(upstreamCtx, account, chatBody)
	if err != nil {
		cancelUpstream()
		return nil, err
	}
	opts.Internal.Attachments = attachments
	fullQuery, err := perplexity.FoldMessagesToQueryWithOptions(gjson.GetBytes(chatBody, "messages"), perplexity.FoldOptions{
		StripSystem: !opts.Local.KeepSystemPrompt,
		MaxChars:    opts.Local.MaxQueryChars,
	})
	if err != nil {
		cancelUpstream()
		return nil, &perplexityBadRequestError{msg: err.Error()}
	}
	query := fullQuery
	continuation := s.preparePerplexityContinuation(account, getAPIKeyIDFromContext(c), opts.Internal.SessionScope, modelPref, mode, chatBody, opts)
	if continuation != nil {
		opts.Internal.FrontendContextUUID = continuation.contextUUID
		if continuation.backendUUID != "" {
			opts.Internal.LastBackendUUID = continuation.backendUUID
			query = continuation.query
		}
	}
	targetURL := creds.NormalizedBaseURL() + perplexity.AskPath
	SetActualOpenAIUpstreamEndpoint(c, perplexityUpstreamEndpoint)

	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		askBody, buildErr := perplexity.BuildAskBodyWithOptions(query, modelPref, mode, opts)
		if buildErr != nil {
			cancelUpstream()
			return nil, fmt.Errorf("build perplexity ask body: %w", buildErr)
		}
		upstreamReq, buildErr := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(askBody))
		if buildErr != nil {
			cancelUpstream()
			return nil, fmt.Errorf("build upstream request: %w", buildErr)
		}
		perplexityCredentialsFromAccount(account).SetFetchStyleHeaders(upstreamReq.Header, "text/event-stream")
		account.ApplyHeaderOverrides(upstreamReq.Header)
		resp, err = sendPerplexityAuthenticated(s.httpUpstream, s.accountRepo, account, upstreamReq, s.resolvePerplexityTLSProfile(account))
		if err == nil && resp == nil {
			err = errors.New("perplexity upstream returned no response")
		}
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			cancelUpstream()
			return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
		}
		if resp.StatusCode < 400 {
			break
		}
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if attempt == 0 && opts.Internal.LastBackendUUID != "" && perplexityContinuationRejected(resp.StatusCode, respBody) && ctx.Err() == nil {
			_ = resp.Body.Close()
			query = fullQuery
			opts.Internal.LastBackendUUID = ""
			continuation.backendUUID = ""
			continuation.contextUUID = uuid.NewString()
			opts.Internal.FrontendContextUUID = continuation.contextUUID
			continue
		}
		cancelUpstream()
		failover := perplexityShouldFailover(resp.StatusCode)
		kind := "http_error"
		if failover {
			kind = "failover"
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID: opsUpstreamProxyID(account), ProxyName: opsUpstreamProxyName(account),
			Platform: account.Platform, AccountID: account.ID, AccountName: account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("cf-ray")),
			Kind:               kind, Message: upstreamMsg,
		})
		s.handlePerplexityAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		if failover {
			_ = resp.Body.Close()
			return nil, &UpstreamFailoverError{
				StatusCode: resp.StatusCode, ResponseBody: respBody, ResponseHeaders: resp.Header.Clone(),
				RetryableOnSameAccount: false,
			}
		}
		return resp, nil
	}
	if resp.Body == nil {
		cancelUpstream()
		return nil, errors.New("perplexity upstream returned an empty body")
	}
	upstreamBody := &perplexityUpstreamBody{ReadCloser: resp.Body}
	reader, writer := io.Pipe()
	identity := &perplexityResponseIdentity{}
	identity.store(perplexity.ModelIdentity{RequestedPreference: modelPref, Status: "unknown"})
	metrics := &perplexityResponseMetrics{}
	params := perplexityTranslateParams{
		upstreamModel: upstreamModel, requestedPreference: modelPref, identity: identity, metrics: metrics,
		promptTokens: perplexity.EstimateTokens(fullQuery),
		stream:       stream, created: time.Now().Unix(),
		id:             "chatcmpl-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:24],
		IncludeSources: opts.Local.IncludeSources, IncludeSearchProgress: opts.Local.IncludeSearchProgress,
		StripCitations: opts.Local.StripCitations,
	}
	if continuation != nil {
		params.onComplete = continuation.complete
	}
	go translatePerplexitySSEToCC(upstreamCtx, upstreamBody, writer, params)
	header := resp.Header.Clone()
	if stream {
		header.Set("Content-Type", "text/event-stream")
	} else {
		header.Set("Content-Type", "application/json")
	}
	header.Del("Content-Length")
	header.Del("Transfer-Encoding")
	var onRead func()
	if !stream {
		onRead = func() {
			observed := identity.snapshot()
			c.Header("X-Perplexity-Model-Status", observed.Status)
			if observed.ActualModel != "" {
				c.Header("X-Perplexity-Actual-Model", observed.ActualModel)
			}
		}
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: header,
		Body: &perplexityCCBody{reader: reader, upstream: upstreamBody, cancel: cancelUpstream, identity: identity, metrics: metrics, onRead: onRead},
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
