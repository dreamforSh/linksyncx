package admin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestImportCodexSessionsRandomizeDeviceProfile(t *testing.T) {
	pinnedToken := buildCodexAccessToken(t, "workspace-1", "user-pinned", time.Now().Add(time.Hour))
	bareToken := buildCodexAccessToken(t, "workspace-1", "user-bare", time.Now().Add(time.Hour))
	pinnedUA := "codex-tui/0.147.0 (Mac OS 15.7.1; arm64) iTerm.app/3.6.5"
	svc := newCodexImportMemoryAdminService([]service.Account{
		{ID: 20, Name: "pinned", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1", "chatgpt_user_id": "user-pinned", "access_token": pinnedToken, "user_agent": pinnedUA,
		}},
		{ID: 21, Name: "bare", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{
			"chatgpt_account_id": "workspace-1", "chatgpt_user_id": "user-bare", "access_token": bareToken,
		}},
	})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	randomize := true
	req := CodexSessionImportRequest{RandomizeDeviceProfile: &randomize}
	entries := []codexImportEntry{
		{Index: 1, Value: map[string]any{"access_token": pinnedToken}},
		{Index: 2, Value: map[string]any{"access_token": bareToken}},
		{Index: 3, Value: buildCodexAccessOnlyImportValue(t, "workspace-2", "user-new-1")},
		{Index: 4, Value: buildCodexAccessOnlyImportValue(t, "workspace-3", "user-new-2")},
	}

	result, err := handler.importCodexSessions(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated, "%+v", result)
	require.Equal(t, 2, result.Created, "%+v", result)

	require.Len(t, svc.updatedAccounts, 2)
	// 已固定设备的账号重复导入时不换设备。
	require.Equal(t, pinnedUA, svc.updatedAccounts[0].input.Credentials["user_agent"])
	bareUA, _ := svc.updatedAccounts[1].input.Credentials["user_agent"].(string)
	require.True(t, strings.HasPrefix(bareUA, "codex-tui/"), bareUA)

	require.Len(t, svc.createdAccounts, 2)
	first, _ := svc.createdAccounts[0].Credentials["user_agent"].(string)
	second, _ := svc.createdAccounts[1].Credentials["user_agent"].(string)
	require.True(t, strings.HasPrefix(first, "codex-tui/"), first)
	require.True(t, strings.HasPrefix(second, "codex-tui/"), second)
	require.NotEqual(t, first, second)
}

func TestImportCodexSessionsDeviceProfileFromCredentialExtras(t *testing.T) {
	manualUA := "codex-tui/0.147.0 (Ubuntu 24.4.0; x86_64) xterm-256color"
	randomize := true
	for _, tc := range []struct {
		name      string
		randomize *bool
	}{
		{name: "manual only"},
		{name: "manual wins over randomize", randomize: &randomize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCodexImportMemoryAdminService(nil)
			handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			req := CodexSessionImportRequest{
				CredentialExtras:       map[string]any{"user_agent": manualUA},
				RandomizeDeviceProfile: tc.randomize,
			}
			entries := []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "workspace-1", "user-1")}}

			result, err := handler.importCodexSessions(context.Background(), req, entries)
			require.NoError(t, err)
			require.Equal(t, 1, result.Created, "%+v", result)
			require.Equal(t, manualUA, svc.createdAccounts[0].Credentials["user_agent"])
		})
	}
}

func TestImportCodexSessionsKeepsDefaultDeviceWithoutRandomize(t *testing.T) {
	svc := newCodexImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	entries := []codexImportEntry{{Index: 1, Value: buildCodexAccessOnlyImportValue(t, "workspace-1", "user-1")}}

	result, err := handler.importCodexSessions(context.Background(), CodexSessionImportRequest{}, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created, "%+v", result)
	require.NotContains(t, svc.createdAccounts[0].Credentials, "user_agent")
}
