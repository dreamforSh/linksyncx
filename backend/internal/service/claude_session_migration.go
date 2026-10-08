package service

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// 换号后按新会话处理（D10）：对话换到新账号时记录迁移水位线（换号那一刻的消息条数），此后该对话在
// 新账号上的请求都删掉水位线之前 assistant 消息里的 thinking / redacted_thinking——这些签名由旧账号签发，
// 带到新账号既可能校验失败，也是两个账号之间的关联信号；Anthropic API 本就丢弃往轮 thinking，不损失上下文。

// claudeHistoryRewrite 是剥离旧账号 thinking 的结果。
type claudeHistoryRewrite int

const (
	claudeHistoryUnchanged claudeHistoryRewrite = iota
	claudeHistoryStripped
	// claudeHistoryRetryFilter：工具调用循环跨了换号——最后一条 assistant 消息（带 tool_use）在水位线之前，
	// API 要求原样回传它的 thinking。这一个请求改用 FilterThinkingBlocksForRetry（关闭 thinking、
	// thinking 转文本），下一轮 assistant 消息由新账号产生后恢复正常。
	claudeHistoryRetryFilter
)

const claudeAssistantContentRemovedPlaceholder = `[{"type":"text","text":"(assistant content removed)"}]`

// PrepareClaudeSessionBody 在转发到 Claude OAuth / setup-token 账号前处理换号留下的旧 thinking 签名，
// 返回（可能改写后的）请求体与是否改写。只在「额度未耗尽不换号」生效时启用：
//   - 对话绑定在别的账号、本次换到 account：记录水位线；
//   - 对话在 account 上已有水位线：请求的消息条数少于水位线，说明客户端改写或压缩了历史，删除水位线；
//
// 然后剥离水位线之前的 thinking。
func (s *GatewayService) PrepareClaudeSessionBody(ctx context.Context, account *Account, sessionKey string, boundAccountID int64, body []byte) ([]byte, bool) {
	if sessionKey == "" || boundAccountID <= 0 || s.identityService == nil || !s.ClaudeStickyHoldApplies(ctx, account) {
		return body, false
	}
	cache := s.identityService.cache
	messageCount := int(gjson.GetBytes(body, "messages.#").Int())

	var watermark int
	if boundAccountID != account.ID {
		migration := ClaudeSessionMigration{FromAccountID: boundAccountID, MessageCount: messageCount, At: time.Now().Unix()}
		if err := cache.SetClaudeSessionMigration(ctx, account.ID, sessionKey, migration, stickySessionTTL); err != nil {
			slog.Warn("claude_session_migration_record_failed", "account_id", account.ID, "from_account_id", boundAccountID, "error", err)
		}
		watermark = messageCount
	} else {
		migration, err := cache.GetClaudeSessionMigration(ctx, account.ID, sessionKey, stickySessionTTL)
		if err != nil || migration == nil {
			return body, false
		}
		if messageCount < migration.MessageCount {
			_ = cache.DeleteClaudeSessionMigration(ctx, account.ID, sessionKey)
			return body, false
		}
		watermark = migration.MessageCount
	}

	out, rewrite := stripClaudeThinkingBefore(body, watermark)
	switch rewrite {
	case claudeHistoryStripped:
		return out, true
	case claudeHistoryRetryFilter:
		filtered := FilterThinkingBlocksForRetry(body, gjson.GetBytes(body, "model").String())
		return filtered, !bytes.Equal(filtered, body)
	default:
		return body, false
	}
}

// stripClaudeThinkingBefore 删除 messages[0:count) 中 assistant 消息的 thinking / redacted_thinking 块。
// 按路径精确删除，其余字节原样保留（不重新序列化，不改变键序与转义）；删空的消息换成占位文本。
func stripClaudeThinkingBefore(body []byte, count int) ([]byte, claudeHistoryRewrite) {
	messages := gjson.GetBytes(body, "messages")
	if count <= 0 || !messages.IsArray() {
		return body, claudeHistoryUnchanged
	}
	list := messages.Array()
	if count > len(list) {
		count = len(list)
	}

	lastAssistant := -1
	for i, msg := range list {
		if msg.Get("role").String() == "assistant" {
			lastAssistant = i
		}
	}
	if thinkingType := gjson.GetBytes(body, "thinking.type").String(); lastAssistant >= 0 && lastAssistant < count &&
		(thinkingType == "enabled" || thinkingType == "adaptive") {
		hasToolUse, hasThinking := false, false
		list[lastAssistant].Get("content").ForEach(func(_, block gjson.Result) bool {
			switch block.Get("type").String() {
			case "tool_use":
				hasToolUse = true
			case "thinking", "redacted_thinking":
				hasThinking = true
			}
			return true
		})
		if hasToolUse && hasThinking {
			return body, claudeHistoryRetryFilter
		}
	}

	out := body
	changed := false
	for i := count - 1; i >= 0; i-- {
		content := list[i].Get("content")
		if list[i].Get("role").String() != "assistant" || !content.IsArray() {
			continue
		}
		blocks := content.Array()
		removed := 0
		for j := len(blocks) - 1; j >= 0; j-- {
			if t := blocks[j].Get("type").String(); t != "thinking" && t != "redacted_thinking" {
				continue
			}
			next, err := sjson.DeleteBytes(out, "messages."+strconv.Itoa(i)+".content."+strconv.Itoa(j))
			if err != nil {
				return body, claudeHistoryUnchanged
			}
			out, removed, changed = next, removed+1, true
		}
		if removed > 0 && removed == len(blocks) {
			next, err := sjson.SetRawBytes(out, "messages."+strconv.Itoa(i)+".content", []byte(claudeAssistantContentRemovedPlaceholder))
			if err != nil {
				return body, claudeHistoryUnchanged
			}
			out = next
		}
	}
	if !changed {
		return body, claudeHistoryUnchanged
	}
	return out, claudeHistoryStripped
}
