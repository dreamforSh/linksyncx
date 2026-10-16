package handler

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func (h *GatewayHandler) recordPartialAntigravityUsage(c *gin.Context, forwardErr error, body []byte, input *service.RecordUsageInput) {
	if input == nil || input.Account == nil || input.Account.Platform != service.PlatformAntigravity ||
		input.APIKey == nil || !input.Result.HasObservedUsage() {
		return
	}
	var failoverErr *service.UpstreamFailoverError
	if errors.As(forwardErr, &failoverErr) {
		return
	}
	input.User = input.APIKey.User
	input.QuotaPlatform = service.QuotaPlatform(c.Request.Context(), input.APIKey)
	input.InboundEndpoint = GetInboundEndpoint(c)
	input.UpstreamEndpoint = GetUpstreamEndpoint(c, input.Account.Platform)
	input.UserAgent = c.GetHeader("User-Agent")
	input.IPAddress = ip.GetClientIP(c)
	input.SessionID = service.ExtractClientSessionID(c)
	input.RequestPayloadHash = service.HashUsageRequestPayload(body)
	input.APIKeyService = h.apiKeyService
	stampForwardRequestedReasoningEffort(input.Result, service.RequestedReasoningEffortFromContext(c.Request.Context()))
	h.submitUsageRecordTask(c.Request.Context(), func(ctx context.Context) {
		if err := h.gatewayService.RecordUsage(ctx, input); err != nil {
			logger.FromContext(ctx).Error("gateway.partial_antigravity_usage_failed",
				zap.Int64("account_id", input.Account.ID), zap.Error(err))
		}
	})
}
