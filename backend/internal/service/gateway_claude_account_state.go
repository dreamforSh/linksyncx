package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

// claudeIdentityUnavailableBody 是账号客户端身份不可用、换号也耗尽时返回给客户端的
// Anthropic 格式错误体。overloaded_error 让 Anthropic SDK / Claude Code 按退避自动重试。
var claudeIdentityUnavailableBody = []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Upstream account temporarily unavailable"}}`)

// claudeIdentityUnavailableFailover 把 ErrClientIdentityUnavailable 转成可换号的错误：
// 先在同一账号上重试一次（身份存储多为瞬时抖动），仍失败再换号。故障在身份存储而不在
// 账号本身，标记 RequestScopedTransient，避免据此临时封禁账号。
func claudeIdentityUnavailableFailover(account *Account, err error) error {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	logger.LegacyPrintf("service.gateway", "Account %d: client identity unavailable, refusing to send without a persisted identity: %v", accountID, err)
	return &UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ResponseBody:           claudeIdentityUnavailableBody,
		RetryableOnSameAccount: true,
		SameAccountRetryMax:    1,
		RequestScopedTransient: true,
	}
}

// claudeUpstreamSessionID 选出 OAuth 请求的上游会话 ID，供 X-Claude-Code-Session-Id 使用：
// 请求体 metadata.user_id 已带（已映射的）会话时用它，并记为账号最近活跃会话；
// 否则按 IdentityService.ResolveSessionIDWithoutMetadata 选择，绝不逐请求随机。
// identityEpoch 是账号当前身份代次，客户端会话头按同一代次映射。
func (s *GatewayService) claudeUpstreamSessionID(ctx context.Context, account *Account, identityEpoch int64, clientHeaders http.Header, metadataSessionID string) string {
	if metadataSessionID != "" {
		sessionID := strings.Clone(metadataSessionID)
		if s.identityService != nil && account != nil {
			s.identityService.TouchActiveSession(ctx, account.ID, sessionID)
		}
		return sessionID
	}
	clientSession := ""
	if clientHeaders != nil {
		clientSession = getHeaderRaw(clientHeaders, "X-Claude-Code-Session-Id")
	}
	if s.identityService == nil {
		return uuid.NewString()
	}
	return s.identityService.ResolveSessionIDWithoutMetadata(ctx, account, identityEpoch, clientSession)
}

// syncClaudeSessionHeader 写入 X-Claude-Code-Session-Id：真实 CLI 每个请求都带，且与
// metadata.user_id 的 session_id 一致。先删除任意大小写的残留值（白名单透传可能写入
// 未映射的客户端原值），再写入选出的上游会话 ID。
func (s *GatewayService) syncClaudeSessionHeader(ctx context.Context, account *Account, identityEpoch int64, header, clientHeaders http.Header, metadataUserID string) {
	metadataSessionID := ""
	if metadataUserID != "" {
		if parsed := ParseMetadataUserID(metadataUserID); parsed != nil {
			metadataSessionID = parsed.SessionID
		}
	}
	sessionID := s.claudeUpstreamSessionID(ctx, account, identityEpoch, clientHeaders, metadataSessionID)
	deleteHeaderAllForms(header, "X-Claude-Code-Session-Id")
	setHeaderRaw(header, "X-Claude-Code-Session-Id", sessionID)
}
