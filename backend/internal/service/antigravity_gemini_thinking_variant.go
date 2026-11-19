package service

import (
	"bytes"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
)

// Gemini 原生请求（/v1beta/models/{model}:generateContent 等）以及兼容协议请求
// （/v1/chat/completions、/v1/responses、/v1/messages）经 Antigravity 账号转发时，
// 客户端习惯发"裸"模型名（如 gemini-4-argon / gemini-3.8-flash）并用
// generationConfig.thinkingConfig / reasoning_effort / thinking 表达思考深度；
// 而 Antigravity 上游的模型目录往往只提供带后缀的变体（例如只返回 gemini-4-argon-high）。
//
// 这里负责把"模型名"与"思考深度"彻底解耦并双向适配：
//  1. 裸名请求（gemini-4-argon）优先按账号已观测到的上游目录解析到真实存在的变体
//     （例如上游仅有 gemini-4-argon-high 时落到 gemini-4-argon-high），并把客户端
//     请求的思考深度（minimal / low / medium / high）独立透传到 thinkingConfig.thinkingLevel；
//  2. 带其他档位后缀的请求（如 gemini-4-argon-low / -medium / -minimal）若上游实际仅提供
//     gemini-4-argon-high，也会路由到 gemini-4-argon-high 并把对应 thinkingLevel 注入请求体，
//     避免 404 "Requested entity was not found."；
//  3. 显式自定义映射（裸名或变体本身在 credentials.model_mapping 中被重映射）始终优先。

const (
	geminiThinkingBudgetLowMax    = 1024
	geminiThinkingBudgetMediumMax = 8192
)

var geminiThinkingVariantDefaultOrder = []string{
	"high", "medium", "low", "minimal", "extra-low", "xhigh", "max", "tiered",
}

// geminiThinkingIntent 表示从请求协议或模型后缀中拆分出来的思考意图。
type geminiThinkingIntent struct {
	Level           string // "minimal" | "low" | "medium" | "high"
	Disabled        bool
	Explicit        bool // 客户端显式传入了 reasoning_effort / output_config.effort / thinking
	FromModelSuffix bool // 思考档位来源于模型名后缀（如 gemini-4-argon-high）
	Budget          *int // 若由 Claude thinking.budget_tokens 显式提供
}

func geminiThinkingVariantOrder(preferred string) []string {
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred == "" {
		preferred = "high"
	}
	order := make([]string, 0, len(geminiThinkingVariantDefaultOrder)+1)
	order = append(order, preferred)
	for _, level := range geminiThinkingVariantDefaultOrder {
		if level != preferred {
			order = append(order, level)
		}
	}
	return order
}

// geminiThinkingLevelFromBody 从 Gemini 原生请求体零反序列化提取期望的思考档位（minimal/low/medium/high）。
// 解析失败或未携带 thinkingConfig 时返回 "high"。
func geminiThinkingLevelFromBody(body []byte) string {
	if len(body) == 0 || (!bytes.Contains(body, []byte("thinkingConfig")) && !bytes.Contains(body, []byte("thinking_config"))) {
		return "high"
	}
	if !gjson.ValidBytes(body) {
		return "high"
	}
	tc := gjson.GetBytes(body, "generationConfig.thinkingConfig")
	if !tc.Exists() || !tc.IsObject() {
		tc = gjson.GetBytes(body, "generation_config.thinking_config")
	}
	if !tc.Exists() || !tc.IsObject() {
		return "high"
	}
	levelRes := tc.Get("thinkingLevel")
	if !levelRes.Exists() {
		levelRes = tc.Get("thinking_level")
	}
	if level, disabled, ok := antigravity.NormalizeGeminiThinkingLevel(levelRes.String()); ok {
		if disabled {
			return "low"
		}
		return level
	}
	budgetRes := tc.Get("thinkingBudget")
	if !budgetRes.Exists() {
		budgetRes = tc.Get("thinking_budget")
	}
	if !budgetRes.Exists() || budgetRes.Type != gjson.Number {
		return "high"
	}
	budget := budgetRes.Float()
	switch {
	case budget < 0:
		return "high" // -1 = 动态思考
	case budget <= geminiThinkingBudgetLowMax:
		return "low" // 含 0（关闭思考）：上游没有"无思考"变体，取最浅档，budget 本身仍原样透传
	case budget <= geminiThinkingBudgetMediumMax:
		return "medium"
	default:
		return "high"
	}
}

// geminiThinkingLevelFromClaudeThinking 用 Claude Messages 协议的 thinking 配置推导档位，
// 阈值与 geminiThinkingLevelFromBody 保持一致，使同一请求无论走 Gemini 原生还是
// Chat Completions / Messages 兼容层都落到同一个上游变体。
func geminiThinkingLevelFromClaudeThinking(thinking *antigravity.ThinkingConfig) string {
	if thinking == nil {
		return "high"
	}
	if strings.EqualFold(strings.TrimSpace(thinking.Type), "disabled") {
		return "low"
	}
	budget := thinking.BudgetTokens
	switch {
	case budget <= 0:
		return "high" // 动态思考 / 未指定预算
	case budget <= geminiThinkingBudgetLowMax:
		return "low"
	case budget <= geminiThinkingBudgetMediumMax:
		return "medium"
	default:
		return "high"
	}
}

// resolveGeminiThinkingIntent 统一从 OpenAI reasoning_effort / Claude output_config.effort /
// Claude thinking / 模型名思考后缀中拆分出目标思考档位。
func resolveGeminiThinkingIntent(
	effort *string,
	thinking *antigravity.ThinkingConfig,
	outputConfig *antigravity.ClaudeOutputConfig,
	requestedModel string,
) geminiThinkingIntent {
	variant, hasVariant := antigravity.SplitGeminiThinkingVariant(requestedModel)

	if effort != nil && strings.TrimSpace(*effort) != "" {
		if level, disabled, ok := antigravity.NormalizeGeminiThinkingLevel(*effort); ok {
			if disabled {
				return geminiThinkingIntent{Level: "minimal", Disabled: true, Explicit: true}
			}
			return geminiThinkingIntent{Level: level, Explicit: true}
		}
	}

	if outputConfig != nil && strings.TrimSpace(outputConfig.Effort) != "" {
		if level, disabled, ok := antigravity.NormalizeGeminiThinkingLevel(outputConfig.Effort); ok {
			if disabled {
				return geminiThinkingIntent{Level: "minimal", Disabled: true, Explicit: true}
			}
			return geminiThinkingIntent{Level: level, Explicit: true}
		}
	}

	if thinking != nil {
		switch strings.ToLower(strings.TrimSpace(thinking.Type)) {
		case "disabled":
			return geminiThinkingIntent{Level: "minimal", Disabled: true, Explicit: true}
		case "enabled", "adaptive":
			if thinking.BudgetTokens > 0 {
				b := thinking.BudgetTokens
				return geminiThinkingIntent{
					Level:    antigravity.GeminiThinkingLevelFromBudget(b),
					Explicit: true,
					Budget:   &b,
				}
			}
			level := "high"
			if hasVariant && variant.ThinkingLevel != "" {
				level = variant.ThinkingLevel
			}
			return geminiThinkingIntent{Level: level, Explicit: true}
		}
	}

	if hasVariant && variant.ThinkingLevel != "" {
		return geminiThinkingIntent{
			Level:           variant.ThinkingLevel,
			FromModelSuffix: true,
		}
	}

	return geminiThinkingIntent{Level: "high"}
}

// accountRawModelMappingHasKey 判断 key 是否由用户写在账号 credentials.model_mapping 里
// （而不是 resolveModelMapping 运行时补进来的默认透传条目）。
func accountRawModelMappingHasKey(account *Account, key string) bool {
	if account == nil || account.Credentials == nil {
		return false
	}
	switch raw := account.Credentials["model_mapping"].(type) {
	case map[string]any:
		_, ok := raw[key]
		return ok
	case map[string]string:
		_, ok := raw[key]
		return ok
	}
	return false
}

// resolveGeminiThinkingVariant 为裸 Gemini 模型名或需降级适配的思考变体挑选上游真实存在的模型 ID。
// 返回 (映射后的上游模型名, 是否命中)。未命中时调用方应回退到常规 getMappedModel 流程。
func resolveGeminiThinkingVariant(account *Account, requestedModel string, body []byte) (string, bool) {
	return resolveGeminiThinkingVariantForLevel(account, requestedModel, geminiThinkingLevelFromBody(body))
}

// resolveGeminiThinkingVariantForLevel 是 resolveGeminiThinkingVariant 的协议无关内核：
// 调用方负责按自身协议推导 preferred 档位（Gemini 原生读 generationConfig.thinkingConfig，
// Claude/OpenAI 兼容层读 reasoning_effort / output_config.effort / thinking.budget_tokens），
// 此处优先依据账号已观测到的上游真实模型目录做精确匹配与降级，其次再查账号映射表。
func resolveGeminiThinkingVariantForLevel(account *Account, requestedModel string, preferred string) (string, bool) {
	if account == nil {
		return "", false
	}
	model := strings.TrimSpace(strings.TrimPrefix(requestedModel, "models/"))
	if !strings.HasPrefix(strings.ToLower(model), "gemini-") {
		return "", false
	}
	raw := rawAntigravityModelMapping(account)
	strictWhitelist := account.antigravityWhitelistEnabled() && len(raw) > 0
	observed, _ := account.AntigravityObservedModelIDs()

	// 若请求本身已经带了思考档位后缀（例如 gemini-4-argon-low / gemini-4-argon-medium），
	// 在没有用户显式自定义改写、且已观测到上游真实目录不包含该后缀但包含同族其他变体
	// （例如上游仅返回 gemini-4-argon-high）时，自动路由到真实存在的上游变体，
	// 思考档位则由调用方通过 generationConfig.thinkingConfig.thinkingLevel 独立适配。
	if variant, hasSuffix := antigravity.SplitGeminiThinkingVariant(model); hasSuffix {
		if len(observed) == 0 || slices.Contains(observed, model) {
			return "", false
		}
		if strictWhitelist && !mappingSupportsRequestedModel(raw, model) {
			return "", false
		}
		if mapped, matched := resolveRequestedModelInMapping(raw, model); matched && strings.TrimSpace(mapped) != model {
			return "", false
		}
		levelPref := strings.TrimSpace(preferred)
		if levelPref == "" {
			levelPref = variant.ThinkingLevel
		}
		if resolved, ok := findObservedGeminiVariant(observed, variant.BaseModel, levelPref); ok {
			return resolved, true
		}
		return "", false
	}

	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return "", false
	}

	// 裸名本身已有用户显式重映射（目标 != 自己）时，始终尊重用户配置。
	// 当用户把裸名写成自映射（例如白名单里选了 gemini-4-argon）时：
	// - 若尚未观测到上游目录，尊重自映射（保持历史行为与测试预期）；
	// - 若已观测到上游目录且上游没有裸名但有同族思考变体（如 gemini-4-argon-high），
	//   则解析到该真实上游变体，避免直接发裸名 404。
	if mapped, matched := resolveRequestedModelInMapping(mapping, model); matched {
		if strings.TrimSpace(mapped) != model {
			return "", false
		}
		if accountRawModelMappingHasKey(account, model) {
			if len(observed) == 0 || slices.Contains(observed, model) {
				return "", false
			}
			if resolved, ok := findObservedGeminiVariant(observed, model, preferred); ok {
				return resolved, true
			}
			return "", false
		}
	}

	if strictWhitelist {
		return "", false
	}

	order := geminiThinkingVariantOrder(preferred)

	// 优先以账号实际从上游观测到的真实模型目录为准（覆盖 OAuth / APIKey / Upstream），
	// 防止静态默认表里有 gemini-4-argon-low 而上游实际只有 gemini-4-argon-high 时发错 ID 导致 404。
	if len(observed) > 0 {
		if slices.Contains(observed, model) {
			return model, true
		}
		if resolved, ok := findObservedGeminiVariant(observed, model, preferred); ok {
			return resolved, true
		}
	}

	for _, level := range order {
		candidate := model + "-" + level
		if mapped, matched := resolveRequestedModelInMapping(mapping, candidate); matched && strings.TrimSpace(mapped) != "" {
			return mapped, true
		}
	}
	return "", false
}

func findObservedGeminiVariant(observed []string, baseModel string, preferred string) (string, bool) {
	if len(observed) == 0 || strings.TrimSpace(baseModel) == "" {
		return "", false
	}
	order := geminiThinkingVariantOrder(preferred)
	for _, level := range order {
		candidate := baseModel + "-" + level
		if slices.Contains(observed, candidate) {
			return candidate, true
		}
	}
	if slices.Contains(observed, baseModel) {
		return baseModel, true
	}
	return "", false
}

// applyAntigravityGeminiThinkingConfigToMap 将拆分后的思考配置写入 Gemini 请求体 map。
//   - onlyIfRedirected=false（Chat Completions / Responses 兼容层）：当请求显式表达了思考意图、
//     使用带思考后缀的模型、或目标是 Gemini 4 模型时写入适配后的 thinkingConfig。
//   - onlyIfRedirected=true（Gemini 原生 /v1beta）：保留客户端原生 thinkingConfig，
//     仅当客户端请求了不同档位后缀并被重定向到另一上游变体（如 gemini-4-argon-low -> gemini-4-argon-high）
//     且未在 body 里显式传 thinkingLevel / thinkingBudget 时，补齐对应的 thinkingLevel。
func applyAntigravityGeminiThinkingConfigToMap(
	reqMap map[string]any,
	originalModel string,
	mappedModel string,
	intent geminiThinkingIntent,
	onlyIfRedirected bool,
) {
	if reqMap == nil || !isAntigravityGeminiModel(mappedModel) {
		return
	}
	if onlyIfRedirected {
		origClean := strings.TrimSpace(strings.TrimPrefix(originalModel, "models/"))
		mappedClean := strings.TrimSpace(strings.TrimPrefix(mappedModel, "models/"))
		if strings.EqualFold(origClean, mappedClean) {
			return
		}
		variant, hasSuffix := antigravity.SplitGeminiThinkingVariant(origClean)
		if !hasSuffix || variant.ThinkingLevel == "" {
			return
		}
		if geminiRequestMapHasExplicitThinkingConfig(reqMap) {
			return
		}
		setGeminiThinkingConfigOnMap(reqMap, antigravity.BuildGeminiThinkingConfigMap(mappedClean, variant.ThinkingLevel, false, nil, true))
		return
	}

	if !intent.Explicit && !intent.FromModelSuffix && !antigravity.IsGemini4Model(mappedModel) {
		return
	}
	tc := antigravity.BuildGeminiThinkingConfigMap(mappedModel, intent.Level, intent.Disabled, intent.Budget, !intent.Disabled)
	if tc != nil {
		setGeminiThinkingConfigOnMap(reqMap, tc)
	}
}

func geminiRequestMapHasExplicitThinkingConfig(reqMap map[string]any) bool {
	for _, genKey := range []string{"generationConfig", "generation_config"} {
		genCfg, ok := reqMap[genKey].(map[string]any)
		if !ok {
			continue
		}
		for _, tcKey := range []string{"thinkingConfig", "thinking_config"} {
			tc, ok := genCfg[tcKey].(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"thinkingLevel", "thinking_level", "thinkingBudget", "thinking_budget"} {
				if val, exists := tc[field]; exists && val != nil {
					if s, isStr := val.(string); isStr && strings.TrimSpace(s) == "" {
						continue
					}
					return true
				}
			}
		}
	}
	return false
}

func setGeminiThinkingConfigOnMap(reqMap map[string]any, thinkingConfig map[string]any) {
	if reqMap == nil || len(thinkingConfig) == 0 {
		return
	}
	genCfg, ok := reqMap["generationConfig"].(map[string]any)
	if !ok || genCfg == nil {
		genCfg = make(map[string]any)
		reqMap["generationConfig"] = genCfg
	}
	genCfg["thinkingConfig"] = thinkingConfig
}
