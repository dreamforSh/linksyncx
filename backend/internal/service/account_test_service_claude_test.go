//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newClaudeAccountTestStreamResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n" +
				"data: {\"type\":\"message_stop\"}\n\n",
		)),
	}
}

func TestAccountTestService_ClaudeOAuthAppliesModelRestriction(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		mapping     map[string]any
		model       string
		wantModel   string
	}{
		{
			name:        "OAuth 映射改写测试模型",
			accountType: AccountTypeOAuth,
			mapping:     map[string]any{"claude-opus-4-6": "claude-sonnet-4-6"},
			model:       "claude-opus-4-6",
			wantModel:   "claude-sonnet-4-6",
		},
		{
			name:        "SetupToken 映射目标短 ID 标准化",
			accountType: AccountTypeSetupToken,
			mapping:     map[string]any{"claude-opus-4-6": "claude-haiku-4-5"},
			model:       "claude-opus-4-6",
			wantModel:   "claude-haiku-4-5-20251001",
		},
		{
			name:        "未配置映射保持原测试模型",
			accountType: AccountTypeOAuth,
			model:       "claude-opus-4-6",
			wantModel:   "claude-opus-4-6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			credentials := map[string]any{"access_token": "oauth-token"}
			if tt.mapping != nil {
				credentials["model_mapping"] = tt.mapping
			}
			account := &Account{
				ID:          812,
				Name:        "claude-oauth-test",
				Platform:    PlatformAnthropic,
				Type:        tt.accountType,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: credentials,
			}
			repo := &mockAccountRepoForGemini{accountsByID: map[int64]*Account{account.ID: account}}
			upstream := &httpUpstreamRecorder{resp: newClaudeAccountTestStreamResponse()}
			svc := &AccountTestService{accountRepo: repo, httpUpstream: upstream}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/812/test", nil)

			err := svc.TestAccountConnection(c, account.ID, tt.model, "", AccountTestModeDefault)
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, tt.wantModel, gjson.GetBytes(upstream.lastBody, "model").String())
			require.Contains(t, rec.Body.String(), `"model":"`+tt.wantModel+`"`)
		})
	}
}
