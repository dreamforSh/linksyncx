//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func deviceProfileClaudeAccount(credentials map[string]any) *Account {
	return &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Credentials: credentials}
}

func TestClaudeDeviceProfile_EligibilityAndDefensiveRead(t *testing.T) {
	creds := map[string]any{
		credKeyDeviceUserAgent: "claude-cli/2.1.100 (external, cli)",
		credKeyDeviceOS:        "macos",
		credKeyDeviceArch:      "X64",
	}
	require.Equal(t, DeviceProfile{UserAgent: "claude-cli/2.1.100 (external, cli)", OS: "MacOS", Arch: "x64"},
		deviceProfileClaudeAccount(creds).ClaudeDeviceProfile())

	setupToken := deviceProfileClaudeAccount(creds)
	setupToken.Type = AccountTypeSetupToken
	require.Equal(t, "MacOS", setupToken.ClaudeDeviceProfile().OS)

	apiKey := deviceProfileClaudeAccount(creds)
	apiKey.Type = AccountTypeAPIKey
	require.Equal(t, DeviceProfile{}, apiKey.ClaudeDeviceProfile())

	openAI := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: creds}
	require.Equal(t, DeviceProfile{}, openAI.ClaudeDeviceProfile())

	// 历史或手工写入的非法值一律忽略，不能出站。
	bad := deviceProfileClaudeAccount(map[string]any{
		credKeyDeviceUserAgent: "Mozilla/5.0",
		credKeyDeviceOS:        "Solaris",
		credKeyDeviceArch:      "riscv",
	})
	require.Equal(t, DeviceProfile{}, bad.ClaudeDeviceProfile())
	var nilAccount *Account
	require.Equal(t, DeviceProfile{}, nilAccount.ClaudeDeviceProfile())
}

func TestApplyClaudeDeviceProfileHeaders(t *testing.T) {
	account := deviceProfileClaudeAccount(map[string]any{
		credKeyDeviceUserAgent: "claude-cli/2.1.100 (external, claude-vscode)",
		credKeyDeviceOS:        "Windows",
		credKeyDeviceArch:      "x64",
	})

	t.Run("version follows the current outbound UA", func(t *testing.T) {
		h := http.Header{}
		setHeaderRaw(h, "User-Agent", "claude-cli/2.1.296 (external, cli)")
		// 透传路径可能留下 Go canonical 形态的键，覆盖后不能并存。
		h.Set("X-Stainless-OS", "Linux")
		h.Set("X-Stainless-Arch", "arm64")

		applyClaudeDeviceProfileHeaders(h, account)

		require.Equal(t, "claude-cli/2.1.296 (external, claude-vscode)", getHeaderRaw(h, "User-Agent"))
		require.Equal(t, []string{"Windows"}, h["X-Stainless-OS"])
		require.Equal(t, []string{"x64"}, h["X-Stainless-Arch"])
		require.NotContains(t, h, "X-Stainless-Os")
	})

	t.Run("falls back to the effective version", func(t *testing.T) {
		h := http.Header{}
		applyClaudeDeviceProfileHeaders(h, account)
		require.Equal(t, "claude-cli/"+claude.EffectiveCLIVersion()+" (external, claude-vscode)", getHeaderRaw(h, "User-Agent"))
	})

	t.Run("partial profile only touches configured headers", func(t *testing.T) {
		h := http.Header{}
		setHeaderRaw(h, "User-Agent", "claude-cli/2.1.296 (external, cli)")
		setHeaderRaw(h, "X-Stainless-Arch", "arm64")
		applyClaudeDeviceProfileHeaders(h, deviceProfileClaudeAccount(map[string]any{credKeyDeviceOS: "MacOS"}))
		require.Equal(t, "claude-cli/2.1.296 (external, cli)", getHeaderRaw(h, "User-Agent"))
		require.Equal(t, "MacOS", getHeaderRaw(h, "X-Stainless-OS"))
		require.Equal(t, "arm64", getHeaderRaw(h, "X-Stainless-Arch"))
	})

	t.Run("no profile is a no-op", func(t *testing.T) {
		h := http.Header{}
		setHeaderRaw(h, "X-Stainless-OS", "Linux")
		applyClaudeDeviceProfileHeaders(h, deviceProfileClaudeAccount(nil))
		require.Equal(t, http.Header{"X-Stainless-OS": {"Linux"}}, h)
	})
}

// 设备画像在 messages / count_tokens 两条出站路径、mimic 与透传两种模式下都生效，
// 且 UA 版本号与请求体 billing 块 cc_version 保持同源。
func TestBuildClaudeRequest_AppliesDeviceProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, mimic := range []bool{true, false} {
			name := endpoint + "/passthrough"
			if mimic {
				name = endpoint + "/mimic"
			}
			t.Run(name, func(t *testing.T) {
				resetGatewayForwardingSettingsCacheForTest(t)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				body := []byte(`{"model":"claude-haiku-4-5","system":[{"type":"text","text":""}],"messages":[{"role":"user","content":"hello world"}]}`)
				billing, err := buildBillingAttributionText(body, "2.1.81")
				require.NoError(t, err)
				body, err = sjson.SetBytes(body, "system.0.text", billing)
				require.NoError(t, err)

				svc := &GatewayService{cfg: &config.Config{}}
				cachedUA := "claude-cli/2.9.0 (external, cli)"
				svc.identityService = NewIdentityService(&stubIdentityCache{fingerprint: &Fingerprint{
					UserAgent: cachedUA, ClientID: "test-client", StainlessOS: "Linux", StainlessArch: "arm64",
					UpdatedAt: time.Now().Unix(),
				}})
				account := deviceProfileClaudeAccount(map[string]any{
					credKeyDeviceUserAgent: "claude-cli/1.0.0 (external, cli)",
					credKeyDeviceOS:        "MacOS",
					credKeyDeviceArch:      "x64",
				})

				var req *http.Request
				var wireBody []byte
				if endpoint == "messages" {
					req, wireBody, err = svc.buildUpstreamRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", false, mimic)
				} else {
					req, wireBody, err = svc.buildCountTokensRequest(context.Background(), c, account,
						body, "test-token", "oauth", "claude-haiku-4-5", mimic)
				}
				require.NoError(t, err)
				t.Cleanup(func() { _ = req.Body.Close() })

				wantVersion := ExtractCLIVersion(cachedUA)
				if mimic {
					wantVersion = ExtractCLIVersion(claude.DefaultUserAgent())
				}
				require.Equal(t, "claude-cli/"+wantVersion+" (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
				require.Equal(t, []string{"MacOS"}, req.Header["X-Stainless-OS"])
				require.Equal(t, []string{"x64"}, req.Header["X-Stainless-Arch"])
				require.NotContains(t, req.Header, "X-Stainless-Os")
				if endpoint == "messages" {
					require.Contains(t, gjson.GetBytes(wireBody, "system.0.text").String(), "cc_version="+wantVersion+".")
				}
			})
		}
	}
}

func TestNormalizeDeviceProfileCredentials(t *testing.T) {
	validCodexUA := "codex-tui/0.150.0 (Mac OS 15.7.1; arm64) iTerm.app/3.6.5"

	t.Run("claude values are trimmed and canonicalized", func(t *testing.T) {
		creds := map[string]any{
			credKeyDeviceUserAgent: "  claude-cli/2.1.100 (external, cli)  ",
			credKeyDeviceOS:        "linux",
			credKeyDeviceArch:      " ARM64 ",
		}
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAnthropic, AccountTypeOAuth, nil, creds))
		require.Equal(t, "claude-cli/2.1.100 (external, cli)", creds[credKeyDeviceUserAgent])
		require.Equal(t, "Linux", creds[credKeyDeviceOS])
		require.Equal(t, "arm64", creds[credKeyDeviceArch])
	})

	t.Run("codex UA must pair with an official client", func(t *testing.T) {
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformOpenAI, AccountTypeOAuth, nil,
			map[string]any{credKeyDeviceUserAgent: validCodexUA}))
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformOpenAI, AccountTypeSetupToken, nil,
			map[string]any{credKeyDeviceUserAgent: validCodexUA}))
		err := NormalizeDeviceProfileCredentials(PlatformOpenAI, AccountTypeOAuth, nil,
			map[string]any{credKeyDeviceUserAgent: "curl/8.0.0"})
		requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	})

	for _, tc := range []struct {
		name                  string
		platform, accountType string
		creds                 map[string]any
	}{
		{"claude UA must be claude-cli shaped", PlatformAnthropic, AccountTypeOAuth, map[string]any{credKeyDeviceUserAgent: "claude-code/2.1.100"}},
		{"claude UA needs the external clause", PlatformAnthropic, AccountTypeOAuth, map[string]any{credKeyDeviceUserAgent: "claude-cli/2.1.100"}},
		{"unknown OS", PlatformAnthropic, AccountTypeOAuth, map[string]any{credKeyDeviceOS: "Darwin"}},
		{"unknown arch", PlatformAnthropic, AccountTypeSetupToken, map[string]any{credKeyDeviceArch: "x86_64"}},
		{"OS only applies to Claude", PlatformOpenAI, AccountTypeOAuth, map[string]any{credKeyDeviceOS: "MacOS"}},
		{"claude api key has no device UA", PlatformAnthropic, AccountTypeAPIKey, map[string]any{credKeyDeviceUserAgent: "claude-cli/2.1.100 (external, cli)"}},
		{"control characters", PlatformOpenAI, AccountTypeAPIKey, map[string]any{credKeyDeviceUserAgent: "agent/1.0\r\nX-Evil: 1"}},
		{"non ASCII", PlatformOpenAI, AccountTypeAPIKey, map[string]any{credKeyDeviceUserAgent: "agent/1.0 设备"}},
		{"too long", PlatformOpenAI, AccountTypeAPIKey, map[string]any{credKeyDeviceUserAgent: "agent/" + strings.Repeat("a", maxDeviceProfileUserAgentLength)}},
		{"non string", PlatformAnthropic, AccountTypeOAuth, map[string]any{credKeyDeviceOS: 1}},
		{"bulk still checks the OS set", "", "", map[string]any{credKeyDeviceOS: "Plan9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := NormalizeDeviceProfileCredentials(tc.platform, tc.accountType, nil, tc.creds)
			requireApplicationErrorReason(t, err, deviceProfileErrorCode)
		})
	}

	t.Run("openai api key UA keeps generic semantics", func(t *testing.T) {
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformOpenAI, AccountTypeAPIKey, nil,
			map[string]any{credKeyDeviceUserAgent: "my-agent/1.0"}))
	})

	t.Run("unchanged legacy values do not block saving", func(t *testing.T) {
		prev := map[string]any{credKeyDeviceUserAgent: "legacy ua", credKeyDeviceOS: "Plan9"}
		next := map[string]any{credKeyDeviceUserAgent: "legacy ua", credKeyDeviceOS: "Plan9", "model_mapping": map[string]any{}}
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAnthropic, AccountTypeOAuth, prev, next))
	})

	t.Run("empty values clear", func(t *testing.T) {
		creds := map[string]any{credKeyDeviceUserAgent: " ", credKeyDeviceOS: "", credKeyDeviceArch: ""}
		require.NoError(t, NormalizeDeviceProfileCredentials("", "", nil, creds))
		require.Equal(t, map[string]any{credKeyDeviceUserAgent: "", credKeyDeviceOS: "", credKeyDeviceArch: ""}, creds)
	})

	require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAnthropic, AccountTypeOAuth, nil, nil))
}

func TestGenerateDeviceProfile(t *testing.T) {
	t.Run("anthropic randomizes OS and arch only", func(t *testing.T) {
		seen := map[DeviceProfile]bool{}
		for i := 0; i < 300; i++ {
			p, err := GenerateDeviceProfile(PlatformAnthropic)
			require.NoError(t, err)
			require.Empty(t, p.UserAgent)
			require.Contains(t, claudeDeviceOSValues, p.OS)
			require.Contains(t, claudeDeviceArchValues, p.Arch)
			creds := deviceProfileCredentialUpdates(PlatformAnthropic, p)
			require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAnthropic, AccountTypeOAuth, nil, creds))
			seen[p] = true
		}
		require.Greater(t, len(seen), 2, "generator must produce diverse devices")
	})

	t.Run("openai builds a valid codex UA on the effective version", func(t *testing.T) {
		seen := map[string]bool{}
		version := CodexCanonicalClientVersion()
		for i := 0; i < 300; i++ {
			p, err := GenerateDeviceProfile(PlatformOpenAI)
			require.NoError(t, err)
			require.Empty(t, p.OS)
			require.Empty(t, p.Arch)
			require.True(t, strings.HasPrefix(p.UserAgent, openai.CodexDefaultOriginator+"/"+version+" ("), p.UserAgent)
			require.NoError(t, NormalizeDeviceProfileCredentials(PlatformOpenAI, AccountTypeOAuth, nil,
				deviceProfileCredentialUpdates(PlatformOpenAI, p)))

			// 终端池与 OS 保持一致。
			switch {
			case strings.Contains(p.UserAgent, "(Windows "):
				require.NotContains(t, p.UserAgent, "iTerm")
				require.Contains(t, p.UserAgent, "; x86_64)")
			case strings.Contains(p.UserAgent, "(Mac OS "):
				require.NotContains(t, p.UserAgent, "WindowsTerminal")
				require.NotContains(t, p.UserAgent, "aarch64")
			case strings.Contains(p.UserAgent, "(Ubuntu "):
				require.NotContains(t, p.UserAgent, "WindowsTerminal")
				require.NotContains(t, p.UserAgent, "iTerm")
			default:
				t.Fatalf("unexpected OS in %q", p.UserAgent)
			}

			// 出站时与强制统一身份的收口逻辑配合：保留设备段、版本仍为生效版本。
			h := http.Header{}
			h.Set("originator", "codex-tui")
			enforceCodexIdentityHeadersWithUA(h, p.UserAgent)
			require.Equal(t, p.UserAgent, h.Get("user-agent"))
			seen[p.UserAgent] = true
		}
		require.Greater(t, len(seen), 20, "generator must produce diverse devices")
	})

	_, err := GenerateDeviceProfile(PlatformGemini)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)
}

func TestGenerateDistinctDeviceProfiles(t *testing.T) {
	profiles, err := GenerateDistinctDeviceProfiles(PlatformOpenAI, 30)
	require.NoError(t, err)
	require.Len(t, profiles, 30)
	seen := map[DeviceProfile]struct{}{}
	for _, p := range profiles {
		seen[p] = struct{}{}
	}
	require.Len(t, seen, 30, "codex profiles in one set should not repeat")

	// Claude 只有少数 OS / 架构组合：取值空间内不重复，超出后允许重复，但不能报错。
	claudeSpace := len(allDeviceProfiles(PlatformAnthropic))
	profiles, err = GenerateDistinctDeviceProfiles(PlatformAnthropic, 50)
	require.NoError(t, err)
	require.Len(t, profiles, 50)
	seen = map[DeviceProfile]struct{}{}
	for _, p := range profiles[:claudeSpace] {
		seen[p] = struct{}{}
	}
	require.Len(t, seen, claudeSpace)

	// 兜底枚举出的每个组合都必须是合法画像。
	for _, platform := range []string{PlatformAnthropic, PlatformOpenAI, PlatformAntigravity} {
		for _, p := range allDeviceProfiles(platform) {
			require.NoError(t, NormalizeDeviceProfileCredentials(platform, AccountTypeOAuth, nil,
				deviceProfileCredentialUpdates(platform, p)), p)
		}
	}

	for _, count := range []int{0, MaxDeviceProfileBatchAccounts + 1} {
		_, err = GenerateDistinctDeviceProfiles(PlatformOpenAI, count)
		requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	}
	_, err = GenerateDistinctDeviceProfiles(PlatformGemini, 2)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)
}

func TestCodexIdentityOverrideUA_UsesCredentialSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	parentUA := "codex-tui/0.150.0 (Ubuntu 24.4.0; x86_64) xterm-256color"
	parent := &Account{ID: 10, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{credKeyDeviceUserAgent: parentUA}}
	parentID := parent.ID
	shadow := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID}

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Empty(t, svc.codexIdentityOverrideUA(c, shadow), "shadow holds no credentials")

	c.Set(codexAccountIdentitySourceContextKey, parent)
	require.Equal(t, parentUA, svc.codexIdentityOverrideUA(c, shadow))
	require.Equal(t, parentUA, svc.codexIdentityOverrideUA(nil, parent))

	forced := &OpenAIGatewayService{cfg: &config.Config{}}
	forced.cfg.Gateway.ForceCodexCLI = true
	require.Empty(t, forced.codexIdentityOverrideUA(c, shadow))
	require.Empty(t, forced.codexIdentityOverrideUAForAccount(context.Background(), parent))

	require.Equal(t, parentUA, svc.codexIdentityOverrideUAForAccount(context.Background(), parent))
}

func TestApplyLiveUpstreamIdentityHeaders_UsesAccountUA(t *testing.T) {
	h := http.Header{}
	applyLiveUpstreamIdentityHeaders(h, "codex-tui/0.150.0 (Windows 10.0.26100; x86_64) WindowsTerminal")
	require.Equal(t, "codex-tui/"+CodexCanonicalClientVersion()+" (Windows 10.0.26100; x86_64) WindowsTerminal", h.Get("user-agent"))
	require.Equal(t, CodexCanonicalClientVersion(), h.Get("version"))
}

type deviceProfileBatchRepo struct {
	accountRepoStub
	accounts []*Account
	updates  map[int64]map[string]any
	failIDs  map[int64]bool
}

func (r *deviceProfileBatchRepo) GetByIDs(_ context.Context, _ []int64) ([]*Account, error) {
	return r.accounts, nil
}

func (r *deviceProfileBatchRepo) BulkUpdate(_ context.Context, ids []int64, updates AccountBulkUpdate) (int64, error) {
	if len(ids) != 1 {
		return 0, context.Canceled
	}
	if r.failIDs[ids[0]] {
		return 0, context.DeadlineExceeded
	}
	if r.updates == nil {
		r.updates = map[int64]map[string]any{}
	}
	r.updates[ids[0]] = updates.Credentials
	return 1, nil
}

func TestBatchUpdateDeviceProfiles(t *testing.T) {
	parentID := int64(2)
	repo := &deviceProfileBatchRepo{
		accounts: []*Account{
			{ID: 1, Name: "claude", Platform: PlatformAnthropic, Type: AccountTypeOAuth},
			{ID: 2, Name: "codex", Platform: PlatformOpenAI, Type: AccountTypeOAuth},
			{ID: 3, Name: "spark", Platform: PlatformOpenAI, Type: AccountTypeOAuth, ParentAccountID: &parentID},
			{ID: 4, Name: "apikey", Platform: PlatformAnthropic, Type: AccountTypeAPIKey},
			{ID: 5, Name: "broken", Platform: PlatformAnthropic, Type: AccountTypeSetupToken},
		},
		failIDs: map[int64]bool{5: true},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	result, err := svc.BatchUpdateDeviceProfiles(context.Background(), &DeviceProfileBatchInput{
		AccountIDs: []int64{1, 2, 3, 4, 5, 99, 1},
		Action:     DeviceProfileBatchActionRandomize,
	})
	require.NoError(t, err)
	require.Equal(t, 2, result.Updated)
	require.Equal(t, 3, result.Skipped)
	require.Equal(t, 1, result.Failed)
	require.Len(t, result.Results, 6, "duplicate IDs are processed once")

	byID := map[int64]DeviceProfileBatchItem{}
	for _, item := range result.Results {
		byID[item.AccountID] = item
	}
	require.Equal(t, DeviceProfileSkipShadow, byID[3].Reason)
	require.Equal(t, DeviceProfileSkipUnsupported, byID[4].Reason)
	require.Equal(t, DeviceProfileSkipNotFound, byID[99].Reason)
	require.Equal(t, DeviceProfileBatchStatusFailed, byID[5].Status)

	claudeUpdate := repo.updates[1]
	require.Equal(t, "", claudeUpdate[credKeyDeviceUserAgent])
	require.Contains(t, claudeDeviceOSValues, claudeUpdate[credKeyDeviceOS])
	require.Contains(t, claudeDeviceArchValues, claudeUpdate[credKeyDeviceArch])
	require.Equal(t, byID[1].Profile.OS, claudeUpdate[credKeyDeviceOS])

	codexUpdate := repo.updates[2]
	require.Equal(t, map[string]any{credKeyDeviceUserAgent: byID[2].Profile.UserAgent}, codexUpdate,
		"codex accounts only store the UA")

	t.Run("clear writes empty values", func(t *testing.T) {
		repo.updates = nil
		result, err := svc.BatchUpdateDeviceProfiles(context.Background(), &DeviceProfileBatchInput{
			AccountIDs: []int64{1, 2},
			Action:     DeviceProfileBatchActionClear,
		})
		require.NoError(t, err)
		require.Equal(t, 2, result.Updated)
		require.Equal(t, map[string]any{credKeyDeviceUserAgent: "", credKeyDeviceOS: "", credKeyDeviceArch: ""}, repo.updates[1])
		require.Equal(t, map[string]any{credKeyDeviceUserAgent: ""}, repo.updates[2])
		require.Nil(t, result.Results[0].Profile)
	})

	t.Run("input validation", func(t *testing.T) {
		_, err := svc.BatchUpdateDeviceProfiles(context.Background(), &DeviceProfileBatchInput{Action: DeviceProfileBatchActionClear})
		requireApplicationErrorReason(t, err, deviceProfileErrorCode)
		_, err = svc.BatchUpdateDeviceProfiles(context.Background(), &DeviceProfileBatchInput{AccountIDs: []int64{1}, Action: "rotate"})
		requireApplicationErrorReason(t, err, deviceProfileErrorCode)
		_, err = svc.BatchUpdateDeviceProfiles(context.Background(), &DeviceProfileBatchInput{
			AccountIDs: make([]int64, MaxDeviceProfileBatchAccounts+1), Action: DeviceProfileBatchActionClear,
		})
		requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	})
}

func TestAntigravityDeviceProfile(t *testing.T) {
	creds := map[string]any{credKeyDeviceOS: "macos", credKeyDeviceArch: "arm64"}
	require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, nil, creds))
	require.Equal(t, "MacOS", creds[credKeyDeviceOS])
	require.Equal(t, "arm64", creds[credKeyDeviceArch])

	account := &Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: creds}
	require.Equal(t, DeviceProfile{OS: "MacOS", Arch: "arm64"}, account.AntigravityDeviceProfile())
	ctx := withAntigravityDevice(context.Background(), account)
	require.Equal(t, "DARWIN_ARM64", antigravity.DevicePlatformFromContext(ctx))
	require.Contains(t, antigravity.GetUserAgentForContext(ctx), " darwin/arm64")

	setup := &Account{Platform: PlatformAntigravity, Type: AccountTypeSetupToken, Credentials: map[string]any{
		credKeyDeviceOS: "Linux", credKeyDeviceArch: "x64",
	}}
	require.Equal(t, DeviceProfile{OS: "Linux", Arch: "x64"}, setup.AntigravityDeviceProfile())

	require.Empty(t, (&Account{Platform: PlatformAntigravity, Type: AccountTypeOAuth}).AntigravityDeviceProfile().OS)
	require.Empty(t, (&Account{Platform: PlatformAntigravity, Type: AccountTypeAPIKey, Credentials: creds}).AntigravityDeviceProfile().OS)
	require.Empty(t, (&Account{Platform: PlatformAntigravity, Type: AccountTypeUpstream, Credentials: creds}).AntigravityDeviceProfile().OS)
	require.Empty(t, (*Account)(nil).AntigravityDeviceProfile().OS)
	require.Empty(t, antigravity.DevicePlatformFromContext(withAntigravityDevice(nil, nil)))

	badPair := map[string]any{credKeyDeviceOS: "Windows", credKeyDeviceArch: "arm64"}
	err := NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, nil, badPair)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	half := map[string]any{credKeyDeviceOS: "MacOS"}
	err = NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, nil, half)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	changedUA := map[string]any{credKeyDeviceUserAgent: "antigravity/2.23.1 windows/amd64"}
	err = NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, nil, changedUA)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)
	apiKey := map[string]any{credKeyDeviceOS: "Linux", credKeyDeviceArch: "x64"}
	err = NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeAPIKey, nil, apiKey)
	requireApplicationErrorReason(t, err, deviceProfileErrorCode)

	legacy := map[string]any{credKeyDeviceUserAgent: "legacy-ua", credKeyDeviceOS: "Linux"}
	next := map[string]any{credKeyDeviceUserAgent: "legacy-ua", credKeyDeviceOS: "linux", credKeyDeviceArch: "x64"}
	require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, legacy, next))
	require.Equal(t, "Linux", next[credKeyDeviceOS])
	cleared := map[string]any{credKeyDeviceOS: "", credKeyDeviceArch: ""}
	require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeSetupToken, nil, cleared))

	seen := map[DeviceProfile]bool{}
	for i := 0; i < 100; i++ {
		p, genErr := GenerateDeviceProfile(PlatformAntigravity)
		require.NoError(t, genErr)
		require.Empty(t, p.UserAgent)
		updates := deviceProfileCredentialUpdates(PlatformAntigravity, p)
		require.Equal(t, "", updates[credKeyDeviceUserAgent])
		require.NoError(t, NormalizeDeviceProfileCredentials(PlatformAntigravity, AccountTypeOAuth, nil, updates))
		goos, goarch, enumName := antigravityGoDevice(p.OS, p.Arch)
		require.NotEmpty(t, enumName)
		stamped := withAntigravityDevice(context.Background(), &Account{
			Platform: PlatformAntigravity, Type: AccountTypeOAuth, Credentials: updates,
		})
		require.Equal(t, enumName, antigravity.DevicePlatformFromContext(stamped))
		require.Contains(t, antigravity.GetUserAgentForContext(stamped), " "+goos+"/"+goarch)
		seen[p] = true
	}
	require.Greater(t, len(seen), 2, "generator must produce diverse devices")
}
