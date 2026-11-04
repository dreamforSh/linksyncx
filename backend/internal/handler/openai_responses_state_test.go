package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestStoredResponseEndpointsAnswerNotFoundForUnknownIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, User: &service.User{ID: 1}})
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
	})
	router.GET("/v1/responses/:response_id", h.GetStoredResponse)
	router.DELETE("/v1/responses/:response_id", h.DeleteStoredResponse)
	router.GET("/v1/responses/:response_id/input_items", h.ListStoredResponseInputItems)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/responses/resp_missing"},
		{http.MethodDelete, "/v1/responses/resp_missing"},
		{http.MethodGet, "/v1/responses/resp_missing/input_items"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, tc.path)
		require.Equal(t, "Response with id 'resp_missing' not found.", gjson.Get(rec.Body.String(), "error.message").String())
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/responses/resp_x/input_items?limit=0", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPaginateStoredResponseInputItems(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"a"}`),
		json.RawMessage(`{"id":"msg_2","type":"message","role":"assistant","content":"b"}`),
		json.RawMessage(`{"type":"message","role":"user","content":"c"}`),
	}
	ids := func(page []json.RawMessage) []string {
		out := make([]string, len(page))
		for i, item := range page {
			out[i] = gjson.GetBytes(item, "id").String()
		}
		return out
	}

	page, more := paginateStoredResponseInputItems("resp_1", items, "desc", 20, "", "")
	require.False(t, more)
	require.Equal(t, []string{"resp_1_item_2", "msg_2", "resp_1_item_0"}, ids(page))

	page, more = paginateStoredResponseInputItems("resp_1", items, "asc", 2, "", "")
	require.True(t, more)
	require.Equal(t, []string{"resp_1_item_0", "msg_2"}, ids(page))

	page, more = paginateStoredResponseInputItems("resp_1", items, "asc", 2, "msg_2", "")
	require.False(t, more)
	require.Equal(t, []string{"resp_1_item_2"}, ids(page))

	page, _ = paginateStoredResponseInputItems("resp_1", items, "asc", 20, "", "resp_1_item_2")
	require.Equal(t, []string{"resp_1_item_0", "msg_2"}, ids(page))
	require.Equal(t, `{"type":"message","role":"user","content":"a"}`, string(items[0]), "input items are not mutated")
}
