package service

import "strings"

// Command Code Provider API 按模型把请求分流到原生协议端点：Claude 使用
// Anthropic Messages，GPT 优先 Responses，其余模型使用 Chat Completions。

const DefaultCommandCodeTestModel = "deepseek/deepseek-v4-flash"

// CommandCodeProtocolRule 是 Command Code 的模型匹配规则。
type CommandCodeProtocolRule struct {
	Pattern   string   `json:"pattern"`
	Protocol  string   `json:"protocol"`
	Protocols []string `json:"protocols,omitempty"`
}

func DefaultCommandCodeProtocolRules() []CommandCodeProtocolRule {
	return []CommandCodeProtocolRule{
		{Pattern: "claude-*", Protocol: APIProtocolAnthropic},
		{Pattern: "gpt-*", Protocol: APIProtocolResponses, Protocols: []string{APIProtocolResponses, APIProtocolChatCompletions}},
	}
}

func commandCodePatternMatches(pattern, model string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "command-code/")
	model = strings.TrimPrefix(model, "command_code/")
	if pattern == "" || model == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return model == pattern
}

func matchCommandCodeProtocolRules(model string, rules []CommandCodeProtocolRule) string {
	for _, rule := range rules {
		if rule.Protocol != APIProtocolChatCompletions && rule.Protocol != APIProtocolAnthropic && rule.Protocol != APIProtocolResponses {
			continue
		}
		if commandCodePatternMatches(rule.Pattern, model) {
			return rule.Protocol
		}
	}
	return APIProtocolChatCompletions
}

func CommandCodeModelProtocol(model string) string {
	return matchCommandCodeProtocolRules(model, DefaultCommandCodeProtocolRules())
}

func (a *Account) ResolveCommandCodeUpstreamProtocol(model string) string {
	if a == nil || !a.IsCommandCode() {
		return ""
	}
	switch a.GetAPIProtocol() {
	case APIProtocolChatCompletions, APIProtocolAnthropic, APIProtocolResponses:
		return a.GetAPIProtocol()
	default:
		return CommandCodeModelProtocol(model)
	}
}

func commandCodeNativeProtocol(account *Account, model string) string {
	if account == nil {
		return APIProtocolChatCompletions
	}
	switch protocol := account.ResolveCommandCodeUpstreamProtocol(model); protocol {
	case APIProtocolAnthropic, APIProtocolResponses:
		return protocol
	default:
		return APIProtocolChatCompletions
	}
}
