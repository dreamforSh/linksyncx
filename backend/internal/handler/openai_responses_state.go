package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// Retrieval endpoints for Responses. Turns the gateway stored on behalf of
// subscription accounts are answered locally (see
// service/openai_responses_state.go); responses an OpenAI API-key account
// created are fetched from that account's upstream while the gateway still
// knows the account. Unknown ids and other users' ids answer 404, as OpenAI
// does for a response the caller cannot see.

type storedResponseCaller struct {
	groupID  int64
	userID   int64
	apiKeyID int64
}

func (h *OpenAIGatewayHandler) storedResponseCaller(c *gin.Context) (storedResponseCaller, bool) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return storedResponseCaller{}, false
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return storedResponseCaller{}, false
	}
	caller := storedResponseCaller{userID: subject.UserID, apiKeyID: apiKey.ID}
	if apiKey.GroupID != nil {
		caller.groupID = *apiKey.GroupID
	}
	return caller, true
}

// forwardUpstreamResponseLookup asks the API-key account that created the
// response, answering 404 when the gateway cannot place it.
func (h *OpenAIGatewayHandler) forwardUpstreamResponseLookup(c *gin.Context, caller storedResponseCaller, responseID, method, suffix string) {
	handled, err := h.gatewayService.ForwardOpenAIResponsesLookup(c.Request.Context(), c, caller.groupID, responseID, caller.userID, caller.apiKeyID, method, suffix)
	if err != nil {
		requestLogger(c, "handler.openai_gateway.responses_lookup").Warn("openai.responses_upstream_lookup_failed", zap.Error(err))
		if !c.Writer.Written() {
			h.errorResponse(c, http.StatusBadGateway, "upstream_error", "Upstream response lookup failed")
		}
		return
	}
	if !handled {
		storedResponseNotFound(c, responseID)
	}
}

func storedResponseNotFound(c *gin.Context, responseID string) {
	c.JSON(http.StatusNotFound, gin.H{"error": gin.H{
		"type":    "invalid_request_error",
		"message": fmt.Sprintf("Response with id '%s' not found.", responseID),
		"param":   nil,
		"code":    nil,
	}})
}

// GetStoredResponse handles GET /v1/responses/:response_id.
func (h *OpenAIGatewayHandler) GetStoredResponse(c *gin.Context) {
	caller, ok := h.storedResponseCaller(c)
	if !ok {
		return
	}
	responseID := strings.TrimSpace(c.Param("response_id"))
	response, found, err := h.gatewayService.GetOpenAIResponsesStoredResponse(c.Request.Context(), caller.groupID, responseID, caller.userID, caller.apiKeyID)
	if err != nil {
		requestLogger(c, "handler.openai_gateway.responses_get").Warn("openai.responses_state_get_failed", zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Stored responses are temporarily unavailable")
		return
	}
	if !found {
		h.forwardUpstreamResponseLookup(c, caller, responseID, http.MethodGet, "")
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", response)
}

// DeleteStoredResponse handles DELETE /v1/responses/:response_id.
func (h *OpenAIGatewayHandler) DeleteStoredResponse(c *gin.Context) {
	caller, ok := h.storedResponseCaller(c)
	if !ok {
		return
	}
	responseID := strings.TrimSpace(c.Param("response_id"))
	deleted, err := h.gatewayService.DeleteOpenAIResponsesStoredResponse(c.Request.Context(), caller.groupID, responseID, caller.userID, caller.apiKeyID)
	if err != nil {
		requestLogger(c, "handler.openai_gateway.responses_delete").Warn("openai.responses_state_delete_failed", zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Stored responses are temporarily unavailable")
		return
	}
	if !deleted {
		h.forwardUpstreamResponseLookup(c, caller, responseID, http.MethodDelete, "")
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": responseID, "object": "response.deleted", "deleted": true})
}

// ListStoredResponseInputItems handles GET /v1/responses/:response_id/input_items
// with OpenAI's list shape and its order/limit/after/before parameters.
func (h *OpenAIGatewayHandler) ListStoredResponseInputItems(c *gin.Context) {
	caller, ok := h.storedResponseCaller(c)
	if !ok {
		return
	}
	responseID := strings.TrimSpace(c.Param("response_id"))
	order := strings.ToLower(strings.TrimSpace(c.DefaultQuery("order", "desc")))
	if order != "asc" && order != "desc" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "order must be 'asc' or 'desc'")
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "limit must be an integer between 1 and 100")
			return
		}
		limit = parsed
	}
	items, found, err := h.gatewayService.ListOpenAIResponsesStoredInputItems(c.Request.Context(), caller.groupID, responseID, caller.userID, caller.apiKeyID)
	if err != nil {
		requestLogger(c, "handler.openai_gateway.responses_input_items").Warn("openai.responses_state_list_failed", zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", "Stored responses are temporarily unavailable")
		return
	}
	if !found {
		h.forwardUpstreamResponseLookup(c, caller, responseID, http.MethodGet, "/input_items")
		return
	}
	page, hasMore := paginateStoredResponseInputItems(responseID, items, order, limit, c.Query("after"), c.Query("before"))
	body := map[string]any{"object": "list", "data": page, "has_more": hasMore, "first_id": nil, "last_id": nil}
	if len(page) > 0 {
		body["first_id"] = gjson.GetBytes(page[0], "id").String()
		body["last_id"] = gjson.GetBytes(page[len(page)-1], "id").String()
	}
	c.JSON(http.StatusOK, body)
}

// paginateStoredResponseInputItems gives every item an id (a stable synthetic
// one when the client sent none) so cursors work, then applies the order and
// the after/before cursors.
func paginateStoredResponseInputItems(responseID string, items []json.RawMessage, order string, limit int, after, before string) ([]json.RawMessage, bool) {
	withIDs := make([]json.RawMessage, len(items))
	for i, item := range items {
		withIDs[i] = item
		if id := gjson.GetBytes(item, "id"); id.Type == gjson.String && id.String() != "" {
			continue
		}
		if gjson.ParseBytes(item).IsObject() {
			if patched, err := sjson.SetBytes(item, "id", fmt.Sprintf("%s_item_%d", responseID, i)); err == nil {
				withIDs[i] = patched
			}
		}
	}
	if order == "desc" {
		for i, j := 0, len(withIDs)-1; i < j; i, j = i+1, j-1 {
			withIDs[i], withIDs[j] = withIDs[j], withIDs[i]
		}
	}
	start, end := 0, len(withIDs)
	for i, item := range withIDs {
		id := gjson.GetBytes(item, "id").String()
		if after != "" && id == after {
			start = i + 1
		}
		if before != "" && id == before {
			end = i
		}
	}
	if start > end {
		start = end
	}
	window := withIDs[start:end]
	if len(window) > limit {
		return window[:limit], true
	}
	return window, false
}
