package service

import (
	"context"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// 账号级设备画像（device profile）：每个账号固定一台“设备”——同一账号的出站请求恒定
// 呈现同一设备形态，不同账号之间呈现不同设备，避免整个账号池共用同一份默认指纹。
//
//   - Claude（anthropic oauth / setup-token）：固定 X-Stainless-OS / X-Stainless-Arch，
//     可选固定 User-Agent 模板（claude-cli/<ver> (external, ...)）。mimic 路径同样生效。
//   - Antigravity（oauth / setup-token）：固定官方 ClientMetadata.platform
//     （DARWIN_ARM64 / DARWIN_AMD64 / LINUX_ARM64 / LINUX_AMD64 / WINDOWS_AMD64），
//     出站 User-Agent 为 antigravity/<当前版本> <goos>/<goarch>。未配置时仍是 windows/amd64。
//   - Codex（openai oauth / setup-token）：沿用既有 credentials.user_agent，只贡献
//     客户端名与 OS / 架构 / 终端段（见 resolveCodexOutboundIdentity）。
//
// 版本段一律由运行期生效版本（面板 / 自动同步）重建，账号上填写的版本号不生效：
// 钉死历史版本会落到上游客户端版本闸门之下（426 / 429）。
const (
	credKeyDeviceUserAgent = "user_agent"
	credKeyDeviceOS        = "device_os"
	credKeyDeviceArch      = "device_arch"

	maxDeviceProfileUserAgentLength = 512

	deviceProfileErrorCode = "INVALID_DEVICE_PROFILE"
)

// claudeDeviceUserAgentPattern 校验 Claude 账号的 UA 模板：必须是官方 CLI 形态
// `claude-cli/<x.y.z> (external, <entrypoint>[, ...])`。版本号只占位，出站时被替换。
var claudeDeviceUserAgentPattern = regexp.MustCompile(`^claude-cli/\d+\.\d+\.\d+ \(external, [^()]+\)$`)

// claudeDeviceOSValues / claudeDeviceArchValues 是 Stainless SDK 在真实 CLI 上会上报的取值
// （X-Stainless-OS: MacOS / Linux / Windows；X-Stainless-Arch: x64 / arm64）。
var (
	claudeDeviceOSValues   = []string{"MacOS", "Linux", "Windows"}
	claudeDeviceArchValues = []string{"arm64", "x64"}
)

// DeviceProfile 是设备画像的对外形态（随机生成接口 / 批量分配结果）。
type DeviceProfile struct {
	UserAgent string `json:"user_agent"`
	OS        string `json:"device_os"`
	Arch      string `json:"device_arch"`
}

// IsDeviceProfileEligible 报告平台 / 账号类型是否支持设备画像。
func IsDeviceProfileEligible(platform, accountType string) bool {
	if accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken {
		return false
	}
	return platform == PlatformAnthropic || platform == PlatformOpenAI || platform == PlatformAntigravity
}

// ClaudeDeviceProfile 返回 Claude OAuth / setup-token 账号配置的设备画像；
// 不适用或未配置时返回零值。读取时再做一次防御性校验：历史或手工写入的非法值一律忽略，
// 不能让一条坏配置把畸形头带到上游。
func (a *Account) ClaudeDeviceProfile() DeviceProfile {
	if a == nil || !a.IsAnthropic() || !a.IsOAuth() {
		return DeviceProfile{}
	}
	var p DeviceProfile
	if ua := strings.TrimSpace(a.GetCredential(credKeyDeviceUserAgent)); ua != "" &&
		len(ua) <= maxDeviceProfileUserAgentLength && claudeDeviceUserAgentPattern.MatchString(ua) {
		p.UserAgent = ua
	}
	p.OS = canonicalDeviceValue(a.GetCredential(credKeyDeviceOS), claudeDeviceOSValues)
	p.Arch = canonicalDeviceValue(a.GetCredential(credKeyDeviceArch), claudeDeviceArchValues)
	return p
}

// applyClaudeDeviceProfileHeaders 把账号设备画像覆盖到 Claude 出站请求头。
// 必须在指纹 / mimic 头写入之后调用，账号显式配置对这几个头拥有最终决定权。
//
// UA 模板只贡献括号内的客户端形态，版本号取当前出站 UA 的版本（与请求体 billing 块
// cc_version 同源，见 effectiveBillingUserAgent），取不到时退回运行期生效版本。
func applyClaudeDeviceProfileHeaders(h http.Header, account *Account) {
	if h == nil {
		return
	}
	p := account.ClaudeDeviceProfile()
	if p.UserAgent != "" {
		version := ExtractCLIVersion(getHeaderRaw(h, "User-Agent"))
		if version == "" {
			version = claude.EffectiveCLIVersion()
		}
		setHeaderRaw(h, "User-Agent", claudeCLIUAVersionPrefixRegex.ReplaceAllString(p.UserAgent, "${1}/"+version))
	}
	if p.OS != "" {
		setHeaderRaw(h, "X-Stainless-OS", p.OS)
	}
	if p.Arch != "" {
		setHeaderRaw(h, "X-Stainless-Arch", p.Arch)
	}
}

// canonicalDeviceValue 不区分大小写地把 value 归一为 allowed 中的规范写法，不在集合内返回空串。
func canonicalDeviceValue(value string, allowed []string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	for _, candidate := range allowed {
		if strings.EqualFold(candidate, value) {
			return candidate
		}
	}
	return ""
}

func deviceProfileCredentialString(m map[string]any, key string) (string, bool, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	s, isString := raw.(string)
	if !isString {
		return "", true, infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode, "%s must be a string", key)
	}
	return strings.TrimSpace(s), true, nil
}

// NormalizeDeviceProfileCredentials 校验并规范化 next 中的设备画像字段（就地修改）。
//
// platform / accountType 为空表示平台未知的批量路径，只做通用的头值安全校验与取值集合校验。
// 只校验相对 prev 发生变化的字段：存量值即便不合规也不阻断其它字段的保存
// （读取侧另有防御性校验，非法值不会出站）。
func NormalizeDeviceProfileCredentials(platform, accountType string, prev, next map[string]any) error {
	if next == nil {
		return nil
	}
	knownPlatform := platform != ""
	eligible := IsDeviceProfileEligible(platform, accountType)
	allowsDeviceOSArch := eligible && (platform == PlatformAnthropic || platform == PlatformAntigravity)

	ua, present, err := deviceProfileCredentialString(next, credKeyDeviceUserAgent)
	if err != nil {
		return err
	}
	if present {
		next[credKeyDeviceUserAgent] = ua
		prevUA, _, _ := deviceProfileCredentialString(prev, credKeyDeviceUserAgent)
		if ua != "" && ua != prevUA {
			if err := validateDeviceUserAgent(platform, accountType, ua); err != nil {
				return err
			}
		}
	}

	for _, field := range []struct {
		key     string
		allowed []string
	}{
		{credKeyDeviceOS, claudeDeviceOSValues},
		{credKeyDeviceArch, claudeDeviceArchValues},
	} {
		value, present, err := deviceProfileCredentialString(next, field.key)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		prevValue, _, _ := deviceProfileCredentialString(prev, field.key)
		if value == "" || value == prevValue {
			next[field.key] = value
			continue
		}
		if knownPlatform && !allowsDeviceOSArch {
			return infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
				"%s is only supported for Claude or Antigravity OAuth / setup-token accounts", field.key)
		}
		canonical := canonicalDeviceValue(value, field.allowed)
		if canonical == "" {
			return infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
				"%s must be one of %s", field.key, strings.Join(field.allowed, " / "))
		}
		next[field.key] = canonical
	}
	if err := validateAntigravityDevicePair(platform, accountType, next); err != nil {
		return err
	}
	return nil
}

func validateDeviceUserAgent(platform, accountType, ua string) error {
	if len(ua) > maxDeviceProfileUserAgentLength {
		return infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
			"user_agent must be at most %d characters", maxDeviceProfileUserAgentLength)
	}
	if !validDeviceUserAgentValue(ua) {
		return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
			"user_agent contains invalid header characters")
	}
	switch {
	case platform == PlatformAnthropic && IsDeviceProfileEligible(platform, accountType):
		if !claudeDeviceUserAgentPattern.MatchString(ua) {
			return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
				"Claude user_agent must look like: claude-cli/<x.y.z> (external, cli)")
		}
	case platform == PlatformAnthropic:
		return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
			"user_agent is only supported for Claude OAuth / setup-token accounts; use header overrides for API key accounts")
	case platform == PlatformAntigravity && IsDeviceProfileEligible(platform, accountType):
		return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
			"Antigravity accounts pin device_os and device_arch; user_agent is derived and cannot be set")
	case platform == PlatformOpenAI && IsDeviceProfileEligible(platform, accountType):
		_, paired, ok := openai.PairCodexClientIdentity(ua)
		if !ok || openai.SetCodexUserAgentVersion(paired, CodexCanonicalClientVersion()) == "" {
			return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
				"Codex user_agent must look like: codex-tui/<version> (<OS> <version>; <arch>) <terminal>")
		}
	}
	return nil
}

// validDeviceUserAgentValue 只接受可打印 ASCII：真实客户端 UA 不含控制字符或非 ASCII 字节，
// 这类值也不能逐字转发给上游。
func validDeviceUserAgentValue(ua string) bool {
	for i := 0; i < len(ua); i++ {
		if c := ua[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}

type weightedDeviceChoice[T any] struct {
	weight int
	value  T
}

func pickWeightedDevice[T any](choices []weightedDeviceChoice[T]) T {
	total := 0
	for _, c := range choices {
		total += c.weight
	}
	n := rand.IntN(total)
	for _, c := range choices {
		if n < c.weight {
			return c.value
		}
		n -= c.weight
	}
	return choices[len(choices)-1].value
}

func pickDevice[T any](values []T) T {
	return values[rand.IntN(len(values))]
}

type claudeDeviceShape struct{ os, arch string }

// claudeDeviceShapes 按 Claude Code 用户群的大致设备分布加权（Apple Silicon Mac 为主）。
var claudeDeviceShapes = []weightedDeviceChoice[claudeDeviceShape]{
	{45, claudeDeviceShape{"MacOS", "arm64"}},
	{5, claudeDeviceShape{"MacOS", "x64"}},
	{30, claudeDeviceShape{"Linux", "x64"}},
	{5, claudeDeviceShape{"Linux", "arm64"}},
	{15, claudeDeviceShape{"Windows", "x64"}},
}

// codexDeviceShape 描述一类 Codex 客户端主机：OS 名与版本取 os_info 的展示形态
// （Ubuntu 24.04 → "24.4.0"），终端标识取 codex-rs terminal-detection 的 UA 令牌形态
// （TERM_PROGRAM[/TERM_PROGRAM_VERSION]，无则回退 TERM）。终端池与 OS 保持一致。
type codexDeviceShape struct {
	osName    string
	versions  []string
	arches    []weightedDeviceChoice[string]
	terminals []weightedDeviceChoice[string]
}

var codexDeviceShapes = []weightedDeviceChoice[codexDeviceShape]{
	{45, codexDeviceShape{
		osName:   "Mac OS",
		versions: []string{"15.6.1", "15.7.1", "26.0.1", "26.1.0", "26.2.0"},
		arches:   []weightedDeviceChoice[string]{{9, "arm64"}, {1, "x86_64"}},
		terminals: []weightedDeviceChoice[string]{
			{4, "iTerm.app/3.6.5"}, {2, "iTerm.app/3.6.4"}, {3, "vscode/1.105.1"},
			{2, "ghostty/1.2.3"}, {1, "xterm-256color"},
		},
	}},
	{35, codexDeviceShape{
		osName:   "Ubuntu",
		versions: []string{"22.4.0", "24.4.0", "24.10.0"},
		arches:   []weightedDeviceChoice[string]{{9, "x86_64"}, {1, "aarch64"}},
		terminals: []weightedDeviceChoice[string]{
			{6, "xterm-256color"}, {2, "vscode/1.105.1"}, {1, "gnome-terminal"}, {1, "tmux-256color"},
		},
	}},
	{20, codexDeviceShape{
		osName:    "Windows",
		versions:  []string{"10.0.26100", "10.0.22631", "10.0.19045"},
		arches:    []weightedDeviceChoice[string]{{1, "x86_64"}},
		terminals: []weightedDeviceChoice[string]{{3, "WindowsTerminal"}, {2, "vscode/1.105.1"}},
	}},
}

// GenerateDeviceProfile 为指定平台随机生成一份设备画像。
//   - anthropic：只随机 OS / 架构，UA 留空（沿用默认 claude-cli/<ver> (external, cli)，
//     与 billing 块 cc_entrypoint=cli 保持一致）；
//   - antigravity：同一组 OS / 架构，UA 留空（出站 User-Agent 与 platform 枚举由这对值派生）；
//   - openai：生成完整 Codex UA，版本取当前生效版本（出站时仍会按生效版本重建）。
func GenerateDeviceProfile(platform string) (DeviceProfile, error) {
	switch platform {
	case PlatformAnthropic:
		shape := pickWeightedDevice(claudeDeviceShapes)
		return DeviceProfile{OS: shape.os, Arch: shape.arch}, nil
	case PlatformAntigravity:
		shape := pickWeightedDevice(claudeDeviceShapes)
		return DeviceProfile{OS: shape.os, Arch: shape.arch}, nil
	case PlatformOpenAI:
		shape := pickWeightedDevice(codexDeviceShapes)
		ua := codexDeviceUserAgent(CodexCanonicalClientVersion(), shape.osName, pickDevice(shape.versions),
			pickWeightedDevice(shape.arches), pickWeightedDevice(shape.terminals))
		return DeviceProfile{UserAgent: ua}, nil
	default:
		return DeviceProfile{}, infraerrors.Newf(http.StatusBadRequest, deviceProfileErrorCode,
			"device profile is not supported for platform %q", platform)
	}
}

func codexDeviceUserAgent(version, osName, osVersion, arch, terminal string) string {
	return openai.CodexDefaultOriginator + "/" + version + " (" + osName + " " + osVersion + "; " + arch + ") " + terminal
}

// allDeviceProfiles 枚举平台的全部候选画像（Codex 版本取当前生效版本），
// 供批量分配在加权抽样连续撞重时兜底挑选未用过的组合。
func allDeviceProfiles(platform string) []DeviceProfile {
	var out []DeviceProfile
	switch platform {
	case PlatformAnthropic:
		for _, c := range claudeDeviceShapes {
			out = append(out, DeviceProfile{OS: c.value.os, Arch: c.value.arch})
		}
	case PlatformAntigravity:
		for _, c := range claudeDeviceShapes {
			out = append(out, DeviceProfile{OS: c.value.os, Arch: c.value.arch})
		}
	case PlatformOpenAI:
		version := CodexCanonicalClientVersion()
		for _, c := range codexDeviceShapes {
			for _, osVersion := range c.value.versions {
				for _, arch := range c.value.arches {
					for _, terminal := range c.value.terminals {
						out = append(out, DeviceProfile{
							UserAgent: codexDeviceUserAgent(version, c.value.osName, osVersion, arch.value, terminal.value),
						})
					}
				}
			}
		}
	}
	return out
}

// deviceProfileCredentialUpdates 把画像转成该平台需要写入的凭据键（JSONB 合并语义，
// 空串即清除）。Codex 账号的 device_os / device_arch 不参与出站，不写。
func deviceProfileCredentialUpdates(platform string, p DeviceProfile) map[string]any {
	updates := map[string]any{credKeyDeviceUserAgent: p.UserAgent}
	if platform == PlatformAnthropic || platform == PlatformAntigravity {
		updates[credKeyDeviceOS] = p.OS
		updates[credKeyDeviceArch] = p.Arch
	}
	return updates
}

// AntigravityDeviceProfile 返回已钉住且属于官方平台枚举的设备。
// 缺一项、或 Windows/arm64 这类枚举里没有的组合，一律当成未配置。
func (a *Account) AntigravityDeviceProfile() DeviceProfile {
	if a == nil || a.Platform != PlatformAntigravity || !a.IsOAuth() {
		return DeviceProfile{}
	}
	osName := canonicalDeviceValue(a.GetCredential(credKeyDeviceOS), claudeDeviceOSValues)
	arch := canonicalDeviceValue(a.GetCredential(credKeyDeviceArch), claudeDeviceArchValues)
	if _, _, platform := antigravityGoDevice(osName, arch); platform == "" {
		return DeviceProfile{}
	}
	return DeviceProfile{OS: osName, Arch: arch}
}

func antigravityGoDevice(osName, arch string) (goos, goarch, platform string) {
	switch osName + "/" + arch {
	case "MacOS/arm64":
		return "darwin", "arm64", "DARWIN_ARM64"
	case "MacOS/x64":
		return "darwin", "amd64", "DARWIN_AMD64"
	case "Linux/arm64":
		return "linux", "arm64", "LINUX_ARM64"
	case "Linux/x64":
		return "linux", "amd64", "LINUX_AMD64"
	case "Windows/x64":
		return "windows", "amd64", "WINDOWS_AMD64"
	default:
		return "", "", ""
	}
}

// withAntigravityDevice 把账号设备画像放进出站 context。未配置时 context 不变，
// User-Agent 仍是 antigravity/<版本> windows/amd64。
func withAntigravityDevice(ctx context.Context, account *Account) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	p := account.AntigravityDeviceProfile()
	goos, goarch, platform := antigravityGoDevice(p.OS, p.Arch)
	if goos == "" {
		return ctx
	}
	return antigravity.WithDeviceIdentity(ctx, goos, goarch, platform)
}

func validateAntigravityDevicePair(platform, accountType string, next map[string]any) error {
	if platform != PlatformAntigravity || !IsDeviceProfileEligible(platform, accountType) || next == nil {
		return nil
	}
	_, osPresent, err := deviceProfileCredentialString(next, credKeyDeviceOS)
	if err != nil {
		return err
	}
	_, archPresent, err := deviceProfileCredentialString(next, credKeyDeviceArch)
	if err != nil {
		return err
	}
	if !osPresent && !archPresent {
		return nil
	}
	osName, _, _ := deviceProfileCredentialString(next, credKeyDeviceOS)
	arch, _, _ := deviceProfileCredentialString(next, credKeyDeviceArch)
	if osName == "" && arch == "" {
		return nil
	}
	if _, _, enumName := antigravityGoDevice(osName, arch); enumName == "" {
		return infraerrors.New(http.StatusBadRequest, deviceProfileErrorCode,
			"Antigravity device_os and device_arch must be one of MacOS/arm64, MacOS/x64, Linux/arm64, Linux/x64, Windows/x64")
	}
	return nil
}
