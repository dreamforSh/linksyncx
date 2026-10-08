package admin

import (
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 账号列表与详情返回「需重新授权」提示：refresh token 期限固定且不足 3 天、或已过期时返回，
// 其余账号不返回。
func TestAccountListReportsReauthNotice(t *testing.T) {
	now := time.Now()
	rtExpiry := strconv.FormatInt(now.Add(36*time.Hour).Unix(), 10)
	adminSvc := newStubAdminService()
	adminSvc.accounts = []service.Account{
		{ID: 1, Name: "fixed", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Credentials: map[string]any{"refresh_token": "rt", "refresh_token_expires_at": rtExpiry, "refresh_token_expiry_fixed_at": rtExpiry}},
		{ID: 2, Name: "pending", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive,
			Credentials: map[string]any{"refresh_token": "rt", "refresh_token_expires_at": rtExpiry}},
		{ID: 3, Name: "expired", Platform: service.PlatformAnthropic, Type: service.AccountTypeSetupToken, Status: service.StatusActive,
			Credentials: map[string]any{"refresh_token": "rt", "refresh_token_expires_at": strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)}},
	}
	router := newSessionBudgetAccountHandler(t, adminSvc, &sessionBudgetCountStub{counts: map[int64]int{}}, "")

	for _, lite := range []string{"", "&lite=1"} {
		items, ok := asJSONObject(t, getData(t, router, "/api/v1/admin/accounts?page=1&page_size=10"+lite))["items"].([]any)
		require.True(t, ok)
		notices := map[any]any{}
		for _, raw := range items {
			item := asJSONObject(t, raw)
			notices[item["id"]] = item["reauth_notice"]
		}
		require.Nil(t, notices[float64(2)], "keep-alive pending: no notice (lite=%q)", lite)
		fixed := asJSONObject(t, notices[float64(1)])
		require.Equal(t, float64(2), fixed["days_left"])
		require.Equal(t, false, fixed["expired"])
		expired := asJSONObject(t, notices[float64(3)])
		require.Equal(t, true, expired["expired"])
	}

	adminSvc.getAccountResult = &adminSvc.accounts[0]
	detail := asJSONObject(t, getData(t, router, "/api/v1/admin/accounts/1"))
	require.Equal(t, float64(2), asJSONObject(t, detail["reauth_notice"])["days_left"])
}
