package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// gatewayTransportErrorTempUnschedDuration is how long an account is temporarily
// unscheduled after a durable transport failure (matches the OpenAI-side
// openAITransportErrorTempUnschedDuration).
const gatewayTransportErrorTempUnschedDuration = 10 * time.Minute

// gatewayTransportFailoverBody is the Anthropic-format error body attached to
// the failover error for a transport-level failure. Kept identical to the
// legacy inline 502 body so the client-visible payload is unchanged if
// failover is ultimately exhausted.
var gatewayTransportFailoverBody = []byte(`{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`)

// handleUpstreamTransportError handles a transport-level upstream failure on
// the Anthropic/Bedrock forward paths (Do/DoWithTLS returned a non-HTTP error:
// proxy / DNS / TCP / TLS). It:
//  1. records the failure in Ops error logs (status 0, kind=request_error) —
//     the caller passes path-specific fields (UpstreamURL, Passthrough) via
//     event; identity, proxy attribution and classification fields are
//     filled here from the same account snapshot that built the transport;
//  2. for durable faults (expired/rejected proxy creds, dead proxy,
//     DNS/routing) temporarily unschedules the account and logs a stable warn
//     event that alert rules can key on; a broken proxy binding
//     (ErrAccountProxyUnavailable) only switches accounts (ProxyBindingUnavailable);
//  3. returns an error that is *UpstreamFailoverError (so the handler fails
//     over to a healthy account) for all non-canceled errors, or the original
//     error for context.Canceled (client gone — no failover, no eviction).
//
// It deliberately does NOT write to the response: the handler owns the
// response (failover, or a protocol-correct error once failover is exhausted).
func (s *GatewayService) handleUpstreamTransportError(ctx context.Context, c *gin.Context, account *Account, err error, event OpsUpstreamErrorEvent) error {
	safeErr := sanitizeUpstreamErrorMessage(err.Error())
	setOpsUpstreamError(c, 0, safeErr, "")
	event.ProxyID, event.ProxyName = opsUpstreamProxyAttribution(account)
	event.Platform = account.Platform
	event.AccountID = account.ID
	event.AccountName = account.Name
	event.UpstreamStatusCode = 0
	event.Kind = "request_error"
	event.Message = safeErr
	appendOpsUpstreamError(c, event)

	// Client disconnected: do NOT fail over to another account and do NOT
	// evict this one — the upstream never had a chance to exhibit a fault.
	if errors.Is(err, context.Canceled) || (errors.Is(err, context.DeadlineExceeded) && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return err
	}

	// 分配了代理却解析不出可用 URL：请求根本没有出站。只按账号不可用换号（粘性会话也换），不计 Ollama
	// 活跃、不临时摘号——与 OpenAI / Gemini / Antigravity 一致，绑定修好后立即恢复调度。不能落到下面的
	// 持久故障分类：按 ID 回查代理时数据库报的 connection refused 也会被包进这个错误，那是数据库的故障，
	// 不是这个账号的。
	if errors.Is(err, ErrAccountProxyUnavailable) {
		return &UpstreamFailoverError{
			StatusCode:              http.StatusBadGateway,
			ResponseBody:            gatewayTransportFailoverBody,
			AccountUnavailable:      true,
			ProxyBindingUnavailable: true,
		}
	}

	// Transport attempt left local validation; count Ollama Cloud activity.
	scheduleOllamaCloudUsageActivity(s.deferredService, account)

	persistent := classifyUpstreamTransportError(err).Persistent
	if persistent {
		s.tempUnscheduleTransportError(ctx, account, safeErr)
	}

	return &UpstreamFailoverError{
		StatusCode:         http.StatusBadGateway,
		ResponseBody:       gatewayTransportFailoverBody,
		AccountUnavailable: persistent,
	}
}

// handleAccessTokenTransportError 把 GetAccessToken 阶段的代理/传输层失败按数据面传输错误处理，
// 返回 handleUpstreamTransportError 的结果（客户端未断开时即 *UpstreamFailoverError）；其它错误
// （凭据缺失、坏 SA 私钥、token 端点拒绝等）返回 nil，由调用方保持原有的终止语义。
//
// 为什么需要：Vertex SA 账号在 GetAccessToken 里就要经账号代理去 Google 换 token。Forward 入口先做
// ProxyURLForOutbound 校验、再取 token，所以换 token 发生在绑定校验之后、DoWithTLS 之前：代理语法合法
// 但已失效（拨号失败）时，这里的传输错误若原样返回，handler 只认 *UpstreamFailoverError，请求会被直接
// 判死而不是换号——违背“代理故障要 failover、绝不回退直连”的约定。
// ErrAccountProxyUnavailable（关系缺失 / URL 无效）分支在先查绑定的入口上走不到，保留它作为与调用顺序
// 无关的兜底：哪个调用方先取 token、后查绑定，绑定失效时也照样按账号不可用换号。
func (s *GatewayService) handleAccessTokenTransportError(ctx context.Context, c *gin.Context, account *Account, err error) error {
	var exchangeErr *tokenExchangeTransportError
	switch {
	case errors.As(err, &exchangeErr):
		return s.handleUpstreamTransportError(ctx, c, account, err, OpsUpstreamErrorEvent{
			UpstreamURL: safeUpstreamURL(exchangeErr.tokenURL),
		})
	case errors.Is(err, ErrAccountProxyUnavailable):
		return s.handleUpstreamTransportError(ctx, c, account, err, OpsUpstreamErrorEvent{})
	default:
		return nil
	}
}

// tempUnscheduleTransportError marks an account temporarily unschedulable
// after a durable transport failure. Unlike the OpenAI side there is no
// in-memory scheduler block on this path: the Anthropic/Bedrock scheduler
// reads the persisted temp-unschedulable state, so the DB write is the single
// source of truth (same as tempUnscheduleGoogleConfigError /
// tempUnscheduleEmptyResponse).
//
// Log semantics:
//   - "gateway.account_temp_unscheduled_transport" — DB write succeeded.
//   - "gateway.account_temp_unschedule_transport_failed" — DB write attempted
//     but returned an error (the account remains schedulable).
func (s *GatewayService) tempUnscheduleTransportError(ctx context.Context, account *Account, safeErr string) {
	if s == nil {
		return
	}
	tempUnscheduleAccountForTransportError(ctx, s.accountRepo, account, safeErr)
}

// tempUnscheduleAccountForTransportError is the repo-level implementation
// shared by every forward path whose scheduler reads the persisted
// temp-unschedulable state (Anthropic/Bedrock and Gemini).
func tempUnscheduleAccountForTransportError(ctx context.Context, repo AccountRepository, account *Account, safeErr string) {
	if account == nil || repo == nil {
		return
	}
	until := time.Now().Add(gatewayTransportErrorTempUnschedDuration)
	reason := "upstream transport error (proxy/network): " + safeErr

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAIAccountStateUpdateTimeout)
	defer cancel()
	if err := repo.SetTempUnschedulable(bgCtx, account.ID, until, reason); err != nil {
		logger.L().With(zap.String("component", "service.gateway")).Warn(
			"gateway.account_temp_unschedule_transport_failed",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
		return
	}
	logger.L().With(zap.String("component", "service.gateway")).Warn(
		"gateway.account_temp_unscheduled_transport",
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("platform", account.Platform),
		zap.Time("until", until),
		zap.String("reason", reason),
	)
}
