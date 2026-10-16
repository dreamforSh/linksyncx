package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func perplexityConversationHint(c *gin.Context, body []byte) string {
	for _, path := range []string{"prompt_cache_key", "metadata.session_id", "metadata.conversation_id"} {
		if value := gjson.GetBytes(body, path); value.Type == gjson.String {
			if id := sanitizeSessionID(value.String()); id != "" {
				return id
			}
		}
	}
	if metadata := gjson.GetBytes(body, "metadata.user_id"); metadata.Type == gjson.String {
		if parsed := ParseMetadataUserID(metadata.String()); parsed != nil {
			if id := sanitizeSessionID(parsed.SessionID); id != "" {
				return id
			}
		}
	}
	return MessagesClientSessionIDFromHeader(c)
}

func writePerplexityIngressError(c *gin.Context, protocol, message string) {
	if !c.Writer.Written() {
		switch protocol {
		case "responses":
			writeOpenAIResponsesFallbackError(c, http.StatusBadRequest, "invalid_request_error", message)
		case "messages":
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", message)
		default:
			writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", message)
		}
		return
	}
	if protocol == "responses" {
		state := apicompat.NewChatCompletionsToResponsesStreamState(requestModelFromContext(c))
		event := perplexityResponsesFailure(state, message)
		event.Response.Error.Code = "invalid_request_error"
		if data, err := apicompat.ResponsesEventToSSE(event); err == nil {
			_, _ = fmt.Fprint(c.Writer, data)
		}
	} else {
		payload := map[string]any{"error": map[string]any{"type": "invalid_request_error", "message": message}}
		if protocol == "messages" {
			payload["type"] = "error"
		}
		data, _ := json.Marshal(payload)
		if protocol == "messages" {
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", data)
		} else {
			_, _ = fmt.Fprintf(c.Writer, "data: %s\n\n", data)
		}
	}
	c.Writer.Flush()
	MarkResponseCommitted(c)
}

func writePerplexityMessagesStreamFailure(c *gin.Context, reason string) {
	payload, _ := json.Marshal(map[string]any{
		"type": "error", "error": map[string]any{"type": "api_error", "message": reason},
	})
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", payload)
	c.Writer.Flush()
	MarkResponseCommitted(c)
}

func requestModelFromContext(c *gin.Context) string {
	if value, exists := c.Get("original_model"); exists {
		model, _ := value.(string)
		return strings.TrimSpace(model)
	}
	return ""
}
