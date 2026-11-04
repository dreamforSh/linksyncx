//go:build unit

package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

const (
	legacyII_openAIResponsesEndpoint          = "/v1/responses"
	legacyII_openAIResponsesCompactEndpoint   = "/v1/responses/compact"
	legacyII_responsesLiteHeader              = "X-OpenAI-Internal-Codex-Responses-Lite"
	legacyII_responsesLiteHeaderKey           = "x-openai-internal-codex-responses-lite"
	legacyII_responsesLiteWSMetadataKey       = "ws_request_header_x_openai_internal_codex_responses_lite"
	legacyII_imageGenerationPermissionMessage = "Image generation is not enabled for this group"
)

func legacyII_isOpenAIResponsesLiteHeader(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

func legacyII_isOpenAIResponsesLiteWebSocketPayload(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return legacyII_isOpenAIResponsesLiteHeader(gjson.GetBytes(body, "client_metadata."+legacyII_responsesLiteWSMetadataKey).String())
}

// ImageGenerationPermissionMessage returns the stable end-user error text for disabled groups.
func legacyII_ImageGenerationPermissionMessage() string {
	return legacyII_imageGenerationPermissionMessage
}

// GroupAllowsImageGeneration preserves ungrouped-key behavior and enforces the flag when a group is present.
func legacyII_GroupAllowsImageGeneration(group *Group) bool {
	return group == nil || group.AllowImageGeneration
}

// IsImageGenerationIntent classifies requests that can produce generated images.
func legacyII_IsImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if legacyII_IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}

	var modelSeen, toolsSeen, inputSeen, toolChoiceSeen bool
	imageIntent := false
	parseRawJSONView(body).ForEach(func(key, value gjson.Result) bool {
		// GetBytes returns the first duplicate key; retain that behavior while walking the root once.
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = legacyII_openAIJSONToolsContainImageGeneration(value)
			}
		case "input":
			if !inputSeen {
				inputSeen = true
				imageIntent = legacyII_openAIJSONInputContainsImageGenTool(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = legacyII_openAIJSONToolChoiceSelectsImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !inputSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsExplicitImageGenerationIntent 仅检测原生 image_generation 工具、图片模型和显式 tool_choice，
// 不检测被动的 image_gen namespace 声明。用于 capability 路由决策——被动 namespace 不应
// 强制要求原生 Responses 能力，否则 Chat Completions-only 账号会被误过滤（#4476）。
func legacyII_IsExplicitImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if legacyII_IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	var modelSeen, toolsSeen, toolChoiceSeen bool
	imageIntent := false
	parseRawJSONView(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				imageIntent = legacyII_openAIJSONToolsContainNativeImageGeneration(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = legacyII_openAIJSONToolChoiceSelectsExplicitImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsImageGenerationIntentForPlatform applies platform-specific intent rules.
//
// Codex advertises the image_gen namespace on ordinary Responses requests so
// that it is available if the model needs it. Grok strips namespace and
// Responses Lite additional_tools declarations before forwarding, so those
// declarations alone must not turn every Codex request into an image request.
// Native image_generation tools, explicit image selection and image models
// remain image intent. Other platforms retain the original declaration rule.
func legacyII_IsImageGenerationIntentForPlatform(endpoint string, requestedModel string, body []byte, platform string) bool {
	if !strings.EqualFold(strings.TrimSpace(platform), PlatformGrok) {
		return legacyII_IsImageGenerationIntent(endpoint, requestedModel, body)
	}
	return legacyII_isExplicitGrokImageGenerationIntent(endpoint, requestedModel, body)
}

func legacyII_isExplicitGrokImageGenerationIntent(endpoint string, requestedModel string, body []byte) bool {
	if legacyII_IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}

	var modelSeen, toolsSeen, toolChoiceSeen bool
	imageIntent := false
	parseRawJSONView(body).ForEach(func(key, value gjson.Result) bool {
		switch key.Str {
		case "model":
			if !modelSeen {
				modelSeen = true
				imageIntent = isOpenAIImageGenerationModel(strings.TrimSpace(value.String()))
			}
		case "tools":
			if !toolsSeen {
				toolsSeen = true
				// Grok removes namespace catalogs before forwarding. Native
				// image_generation remains an explicit capability request.
				imageIntent = legacyII_openAIJSONToolsContainNativeImageGeneration(value)
			}
		case "tool_choice":
			if !toolChoiceSeen {
				toolChoiceSeen = true
				imageIntent = legacyII_openAIJSONToolChoiceSelectsExplicitImageGeneration(value)
			}
		}
		return !imageIntent && (!modelSeen || !toolsSeen || !toolChoiceSeen)
	})
	return imageIntent
}

// IsImageGenerationIntentMap is the map-backed variant used after service-side request mutation.
func legacyII_IsImageGenerationIntentMap(endpoint string, requestedModel string, reqBody map[string]any) bool {
	if legacyII_IsImageGenerationEndpoint(endpoint) {
		return true
	}
	if isOpenAIImageGenerationModel(requestedModel) {
		return true
	}
	if reqBody == nil {
		return false
	}
	if isOpenAIImageGenerationModel(firstNonEmptyString(reqBody["model"])) {
		return true
	}
	if hasOpenAIImageGenerationTool(reqBody) {
		return true
	}
	return legacyII_openAIAnyToolChoiceSelectsImageGeneration(reqBody["tool_choice"])
}

// IsImageGenerationEndpoint identifies dedicated generated-image endpoints.
func legacyII_IsImageGenerationEndpoint(endpoint string) bool {
	switch legacyII_normalizeImageGenerationEndpoint(endpoint) {
	case "/v1/images/generations", "/v1/images/edits", "/images/generations", "/images/edits":
		return true
	default:
		return false
	}
}

func legacyII_normalizeImageGenerationEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.ToLower(endpoint))
	if endpoint == "" {
		return ""
	}
	endpoint = strings.TrimPrefix(endpoint, "https://api.openai.com")
	if idx := strings.IndexByte(endpoint, '?'); idx >= 0 {
		endpoint = endpoint[:idx]
	}
	return strings.TrimRight(endpoint, "/")
}

func legacyII_openAIJSONToolsContainImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if legacyII_isOpenAIImageGenerationType(legacyII_openAIJSONString(item.Get("type"))) {
			found = true
			return false
		}
		if legacyII_isImageGenNamespaceTool(item) {
			found = true
			return false
		}
		return true
	})
	return found
}

func legacyII_openAIJSONToolsContainNativeImageGeneration(tools gjson.Result) bool {
	if !tools.IsArray() {
		return false
	}
	found := false
	tools.ForEach(func(_, item gjson.Result) bool {
		found = legacyII_isOpenAIImageGenerationType(legacyII_openAIJSONString(item.Get("type")))
		return !found
	})
	return found
}

func legacyII_isOpenAIImageGenerationType(value string) bool {
	return strings.TrimSpace(value) == "image_generation"
}

func legacyII_isOpenAIImageGenNamespaceName(value string) bool {
	return strings.TrimSpace(value) == "image_gen"
}

// isImageGenNamespaceTool detects the namespace advertised by Codex's built-in
// image-generation extension instead of a hosted image_generation tool.
func legacyII_isImageGenNamespaceTool(tool gjson.Result) bool {
	return legacyII_openAIJSONString(tool.Get("type")) == "namespace" &&
		legacyII_isOpenAIImageGenNamespaceName(legacyII_openAIJSONString(tool.Get("name")))
}

// openAIJSONInputContainsImageGenTool scans Responses input items for
// additional_tools entries that declare the image_gen namespace. This covers
// the "Responses Lite" format where tools are embedded inside input items
// rather than top-level tools.
func legacyII_openAIJSONInputContainsImageGenTool(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if legacyII_openAIJSONString(item.Get("type")) != "additional_tools" {
			return true
		}
		found = legacyII_openAIJSONToolsContainImageGeneration(item.Get("tools"))
		return !found
	})
	return found
}

func legacyII_openAIRequestBodyHasImageGenerationDeclaration(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	return legacyII_openAIJSONToolsContainImageGeneration(gjson.GetBytes(body, "tools")) ||
		legacyII_openAIJSONInputContainsImageGenTool(gjson.GetBytes(body, "input")) ||
		legacyII_openAIJSONToolChoiceSelectsImageGeneration(gjson.GetBytes(body, "tool_choice"))
}

func legacyII_openAIRequestBodyImageGenerationToolNeedsNormalization(body []byte) bool {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return false
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	needsNormalization := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if legacyII_openAIJSONString(item.Get("type")) != "image_generation" {
			return true
		}
		// 只有旧字段或明确的模型不兼容字段需要修正时才进入 map 修改。
		if item.Get("format").Exists() || item.Get("compression").Exists() {
			needsNormalization = true
			return false
		}
		imageModel := strings.ToLower(strings.TrimSpace(item.Get("model").String()))
		if strings.HasPrefix(imageModel, "gpt-image-2") && item.Get("input_fidelity").Exists() {
			needsNormalization = true
			return false
		}
		return true
	})
	return needsNormalization
}

func legacyII_openAIJSONToolChoiceSelectsImageGeneration(choice gjson.Result) bool {
	if !choice.Exists() {
		return false
	}
	if choice.Type == gjson.String {
		return legacyII_isOpenAIImageGenerationType(choice.String())
	}
	if !choice.IsObject() {
		return false
	}
	choiceType := legacyII_openAIJSONString(choice.Get("type"))
	if legacyII_isOpenAIImageGenerationType(choiceType) {
		return true
	}
	if choiceType == "namespace" &&
		(legacyII_isOpenAIImageGenNamespaceName(legacyII_openAIJSONString(choice.Get("name"))) ||
			legacyII_isOpenAIImageGenNamespaceName(legacyII_openAIJSONString(choice.Get("namespace")))) {
		return true
	}
	if tool := choice.Get("tool"); tool.IsObject() && legacyII_openAIJSONToolChoiceSelectsImageGeneration(tool) {
		return true
	}
	if legacyII_isOpenAIImageGenerationType(legacyII_openAIJSONString(choice.Get("function.name"))) {
		return true
	}
	return false
}

func legacyII_openAIJSONToolChoiceSelectsExplicitImageGeneration(choice gjson.Result) bool {
	if legacyII_openAIJSONToolChoiceSelectsImageGeneration(choice) {
		return true
	}
	if !choice.IsObject() {
		return false
	}
	if tool := choice.Get("tool"); tool.IsObject() && legacyII_openAIJSONToolChoiceSelectsExplicitImageGeneration(tool) {
		return true
	}
	if legacyII_isOpenAIImageGenFunctionReference(
		legacyII_openAIJSONString(choice.Get("namespace")),
		legacyII_openAIJSONString(choice.Get("name")),
	) {
		return true
	}
	if fn := choice.Get("function"); fn.IsObject() {
		return legacyII_isOpenAIImageGenFunctionReference(
			legacyII_openAIJSONString(fn.Get("namespace")),
			legacyII_openAIJSONString(fn.Get("name")),
		)
	}
	return false
}

func legacyII_isOpenAIImageGenFunctionReference(namespace string, name string) bool {
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "image_gen" && name == "imagegen" {
		return true
	}
	switch name {
	case "image_gen.imagegen", "image_gen__imagegen":
		return true
	default:
		return false
	}
}

func legacyII_openAIAnyToolChoiceSelectsImageGeneration(choice any) bool {
	switch v := choice.(type) {
	case string:
		return legacyII_isOpenAIImageGenerationType(v)
	case map[string]any:
		choiceType := strings.TrimSpace(firstNonEmptyString(v["type"]))
		if legacyII_isOpenAIImageGenerationType(choiceType) {
			return true
		}
		if choiceType == "namespace" &&
			(legacyII_isOpenAIImageGenNamespaceName(firstNonEmptyString(v["name"])) ||
				legacyII_isOpenAIImageGenNamespaceName(firstNonEmptyString(v["namespace"]))) {
			return true
		}
		if tool, ok := v["tool"].(map[string]any); ok && legacyII_openAIAnyToolChoiceSelectsImageGeneration(tool) {
			return true
		}
		if fn, ok := v["function"].(map[string]any); ok && legacyII_isOpenAIImageGenerationType(firstNonEmptyString(fn["name"])) {
			return true
		}
	}
	return false
}

func legacyII_getAPIKeyFromContext(c interface{ Get(string) (any, bool) }) *APIKey {
	if c == nil {
		return nil
	}
	v, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, _ := v.(*APIKey)
	return apiKey
}

func legacyII_apiKeyGroup(apiKey *APIKey) *Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

type legacyII_OpenAIResponsesImageBillingConfig struct {
	Model     string
	SizeTier  string
	InputSize string
}

func legacyII_resolveOpenAIResponsesImageBillingConfigDetailed(reqBody map[string]any, fallbackModel string) (legacyII_OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if reqBody != nil {
		rawTools, _ := reqBody["tools"].([]any)
		for _, rawTool := range rawTools {
			toolMap, ok := rawTool.(map[string]any)
			if !ok || strings.TrimSpace(firstNonEmptyString(toolMap["type"])) != "image_generation" {
				continue
			}
			hasImageTool = true
			imageModel = strings.TrimSpace(firstNonEmptyString(toolMap["model"]))
			imageSize = strings.TrimSpace(firstNonEmptyString(toolMap["size"]))
			break
		}
		if imageSize == "" {
			imageSize = strings.TrimSpace(firstNonEmptyString(reqBody["size"]))
		}
	}
	if imageModel == "" && reqBody != nil {
		bodyModel := strings.TrimSpace(firstNonEmptyString(reqBody["model"]))
		if legacyII_isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
			imageModel = bodyModel
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	sizeTier := normalizeOpenAIImageSizeTier(imageSize)
	return legacyII_OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  sizeTier,
		InputSize: imageSize,
	}, nil
}

func legacyII_resolveOpenAIResponsesImageBillingConfigFromBody(body []byte, fallbackModel string) (string, string, error) {
	cfg, err := legacyII_resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body, fallbackModel)
	if err != nil {
		return "", "", err
	}
	return cfg.Model, cfg.SizeTier, nil
}

func legacyII_resolveOpenAIResponsesImageBillingConfigDetailedFromBody(body []byte, fallbackModel string) (legacyII_OpenAIResponsesImageBillingConfig, error) {
	imageModel := ""
	imageSize := ""
	hasImageTool := false
	if len(body) > 0 && gjson.ValidBytes(body) {
		tools := gjson.GetBytes(body, "tools")
		if tools.IsArray() {
			tools.ForEach(func(_, item gjson.Result) bool {
				if legacyII_openAIJSONString(item.Get("type")) != "image_generation" {
					return true
				}
				hasImageTool = true
				imageModel = legacyII_openAIJSONString(item.Get("model"))
				imageSize = legacyII_openAIJSONString(item.Get("size"))
				return false
			})
		}
		if imageSize == "" {
			imageSize = legacyII_openAIJSONString(gjson.GetBytes(body, "size"))
		}
		if imageModel == "" {
			bodyModel := legacyII_openAIJSONString(gjson.GetBytes(body, "model"))
			if legacyII_isOpenAIImageBillingModelAlias(bodyModel) || !hasImageTool {
				imageModel = bodyModel
			}
		}
	}
	if imageModel == "" && hasImageTool {
		imageModel = "gpt-image-2"
	}
	if imageModel == "" {
		imageModel = strings.TrimSpace(fallbackModel)
	}
	return legacyII_OpenAIResponsesImageBillingConfig{
		Model:     imageModel,
		SizeTier:  normalizeOpenAIImageSizeTier(imageSize),
		InputSize: imageSize,
	}, nil
}

func legacyII_isOpenAIImageBillingModelAlias(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return isOpenAIImageGenerationModel(normalized) || strings.Contains(normalized, "image")
}

func legacyII_openAIJSONString(value gjson.Result) string {
	if value.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(value.String())
}
