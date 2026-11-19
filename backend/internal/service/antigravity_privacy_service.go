package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

const (
	AntigravityPrivacySet    = "privacy_set"
	AntigravityPrivacyFailed = "privacy_set_failed"
)

// setAntigravityAccountPrivacy 针对已落库账号设置隐私，严格对齐账号设备画像、代理与专属 TLS 连接池。
func setAntigravityAccountPrivacy(
	ctx context.Context,
	account *Account,
	accessToken, projectID, proxyURL string,
	tlsSvc *TLSFingerprintProfileService,
) string {
	if accessToken == "" {
		return ""
	}
	ctx = withAntigravityDevice(ctx, account)
	var accountID int64
	if account != nil {
		accountID = account.ID
	}
	client, err := antigravity.NewClientForAccount(proxyURL, accountID, resolveAntigravityAccountTLSProfile(tlsSvc, account))
	if err != nil {
		slog.Warn("antigravity_privacy_client_error", "account_id", accountID, "error", err.Error())
		return AntigravityPrivacyFailed
	}
	if accountID <= 0 {
		defer client.CloseIdleConnections()
	}
	return setAntigravityPrivacyWithClient(ctx, client, accessToken, projectID)
}

func setAntigravityPrivacyWithClient(ctx context.Context, client *antigravity.Client, accessToken, projectID string) string {
	if accessToken == "" || client == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 第 1 步：调用 setUserSettings，检查返回值
	setResp, err := client.SetUserSettings(ctx, accessToken)
	if err != nil {
		slog.Warn("antigravity_privacy_set_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	if !setResp.IsSuccess() {
		slog.Warn("antigravity_privacy_set_response_not_empty",
			"user_settings", setResp.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	// 第 2 步：调用 fetchUserInfo 二次验证隐私是否已生效
	if strings.TrimSpace(projectID) == "" {
		slog.Warn("antigravity_privacy_missing_project_id")
		return AntigravityPrivacyFailed
	}
	userInfo, err := client.FetchUserInfo(ctx, accessToken, projectID)
	if err != nil {
		slog.Warn("antigravity_privacy_verify_failed", "error", err.Error())
		return AntigravityPrivacyFailed
	}
	if !userInfo.IsPrivate() {
		slog.Warn("antigravity_privacy_verify_not_private",
			"user_settings", userInfo.UserSettings,
		)
		return AntigravityPrivacyFailed
	}

	slog.Info("antigravity_privacy_set_success")
	return AntigravityPrivacySet
}

func applyAntigravityPrivacyMode(account *Account, mode string) {
	if account == nil || strings.TrimSpace(mode) == "" {
		return
	}
	extra := make(map[string]any, len(account.Extra)+1)
	for k, v := range account.Extra {
		extra[k] = v
	}
	extra["privacy_mode"] = mode
	account.Extra = extra
}
