package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 已绑定对话的软性上游错误：在原账号上退避重试，不把账号加入排除列表、不计切换次数；用尽后交给调用方。
func TestHandleClaudeStickyHoldFailureRetriesOnTheSameAccount(t *testing.T) {
	fs := NewFailoverState(10, true)
	failoverErr := &service.UpstreamFailoverError{StatusCode: 529}

	start := time.Now()
	require.Equal(t, FailoverContinue, fs.HandleClaudeStickyHoldFailure(context.Background(), 7, failoverErr, 2))
	require.Equal(t, FailoverContinue, fs.HandleClaudeStickyHoldFailure(context.Background(), 7, failoverErr, 2))
	require.GreaterOrEqual(t, time.Since(start), sameAccountRetryDelay*3, "retries back off: 500ms then 1s")
	require.Equal(t, FailoverExhausted, fs.HandleClaudeStickyHoldFailure(context.Background(), 7, failoverErr, 2))

	require.Empty(t, fs.FailedAccountIDs, "the bound account is never excluded")
	require.Zero(t, fs.SwitchCount)
	require.False(t, fs.ForceCacheBilling, "staying on the account keeps the prompt cache")
	require.Same(t, failoverErr, fs.LastFailoverErr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, FailoverCanceled, NewFailoverState(10, true).HandleClaudeStickyHoldFailure(ctx, 7, failoverErr, 2))
}

// 原账号暂时不能服务时返回与上游过载相同的可重试错误，Claude Code 会按 retry-after 退避重试。
func TestWriteClaudeStickyHoldError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	(&GatewayHandler{}).writeClaudeStickyHoldError(c, &service.ClaudeStickyHoldError{AccountID: 7, Reason: "rate_limited", RetryAfter: 2500 * time.Millisecond}, false)

	require.Equal(t, 529, rec.Code)
	require.Equal(t, "true", rec.Header().Get("x-should-retry"))
	require.Equal(t, "3", rec.Header().Get("retry-after"), "retry-after rounds up")
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "error", body.Type)
	require.Equal(t, "overloaded_error", body.Error.Type)
	require.Equal(t, "Overloaded", body.Error.Message)
}
