package perplexity

import (
	"strings"
)

// familyPreference 是裸 model_preference 的族谱视图：剥掉版本数字与 thinking
// 变体后缀后的骨架（族键）、版本号序列、以及是否为 thinking 变体。
type familyPreference struct {
	key      string
	version  []int
	thinking bool
}

// parseFamilyPreference 解析裸 model_preference：
//   - 先剥掉 thinking 变体后缀（"_thinking" 或词尾直接相连的 "thinking"）
//   - 族键 = 剩余部分的非数字字符（"gpt54"→"gpt"，"claude46sonnet"→"claudesonnet"）
//   - 版本 = 依次出现的数字串（"gpt54"→[54]，"claude46sonnet"→[46]）
//
// 不含版本数字的取值（turbo、pplx_pro 等非版本化 preference）不参与族升级，
// 返回 false。
func parseFamilyPreference(pref string) (familyPreference, bool) {
	name := strings.ToLower(strings.TrimSpace(pref))
	if name == "" {
		return familyPreference{}, false
	}
	thinking := false
	if strings.HasSuffix(name, "_thinking") {
		name, thinking = name[:len(name)-len("_thinking")], true
	} else if strings.HasSuffix(name, "thinking") {
		name, thinking = name[:len(name)-len("thinking")], true
	}
	var key strings.Builder
	var version []int
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < '0' || c > '9' {
			key.WriteByte(c)
			continue
		}
		value := 0
		for i < len(name) && name[i] >= '0' && name[i] <= '9' {
			value = value*10 + int(name[i]-'0')
			i++
		}
		i--
		version = append(version, value)
	}
	if key.Len() == 0 || len(version) == 0 {
		return familyPreference{}, false
	}
	return familyPreference{key: key.String(), version: version, thinking: thinking}, true
}

// comparePreferenceVersions 逐段比较版本号序列；公共前缀相同时段数更多者更新。
func comparePreferenceVersions(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

// preferFamilyCandidate 报告版本并列时 candidate 是否优于 current：
// 优先保持与请求相同的 thinking 变体形态，再取字典序较小者保证确定性。
func preferFamilyCandidate(candidate, current familyPreference, thinking bool, candidateName, currentName string) bool {
	if candidate.thinking != current.thinking {
		return candidate.thinking == thinking
	}
	return candidateName < currentName
}

// NewestFamilyPreference 在 catalog（服务端下发的裸 model_preference 列表）中
// 寻找与 pref 同族且版本更新的取值，返回版本最高者，用于上游下线旧版本后的
// 死模型自动升级（如 gpt54→gpt55）。
//
// pref 不含版本数字（turbo、pplx_pro 等）或无同族更高版本时返回 false；
// catalog 仍包含 pref 本身不属于升级场景（调用方应先排除），此处也只会返回
// 严格更高版本的候选。
func NewestFamilyPreference(pref string, catalog []string) (string, bool) {
	base, ok := parseFamilyPreference(pref)
	if !ok {
		return "", false
	}
	best := ""
	var bestParsed familyPreference
	for _, entry := range catalog {
		candidate, ok := parseFamilyPreference(entry)
		if !ok || candidate.key != base.key {
			continue
		}
		if comparePreferenceVersions(candidate.version, base.version) <= 0 {
			continue
		}
		if best != "" {
			order := comparePreferenceVersions(candidate.version, bestParsed.version)
			if order < 0 || order == 0 && !preferFamilyCandidate(candidate, bestParsed, base.thinking, entry, best) {
				continue
			}
		}
		best, bestParsed = entry, candidate
	}
	return best, best != ""
}

// IsStaticAliasPreference 报告 mappedModel 是否命中静态别名表
// （aliasToPreference，如 sonar→turbo）。别名是长期稳定的产品名而非版本化
// preference，不参与目录自愈的版本族改写。
func IsStaticAliasPreference(mappedModel string) bool {
	name := strings.ToLower(strings.TrimSpace(mappedModel))
	if i := strings.LastIndex(name, ":"); i >= 0 {
		switch name[i+1:] {
		case ModeConcise, ModeCopilot, ModeComputer:
			name = name[:i]
		}
	}
	_, ok := aliasToPreference[name]
	return ok
}
