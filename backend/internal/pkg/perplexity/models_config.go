package perplexity

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ConfigModel is one explicit web model choice, not proof of model entitlement.
// Definition preserves upstream capability fields for the account catalog.
type ConfigModel struct {
	ID               string
	Label            string
	SubscriptionTier string
	Reasoning        *bool
	Definition       json.RawMessage
}

type configModelDefinition struct {
	Model            string `json:"model"`
	Label            string `json:"label"`
	Mode             string `json:"mode"`
	SubscriptionTier string `json:"subscription_tier"`
	Reasoning        *bool  `json:"reasoning"`
}

type searchModelChoice struct {
	Label             string `json:"label"`
	SubscriptionTier  string `json:"subscription_tier"`
	NonReasoningModel string `json:"non_reasoning_model"`
	ReasoningModel    string `json:"reasoning_model"`
}

// ModelsConfigURL includes the same client version used by web Ask requests.
// Callers must validate baseURL before sending credentials to it.
func ModelsConfigURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + ModelsConfigPath + "?version=" + askVersion + "&source=default"
}

// ParseModelsConfig normalizes the v2 model dictionary and its search picker,
// while retaining compatibility with the older models[].model shape.
func ParseModelsConfig(body []byte) ([]ConfigModel, error) {
	var config struct {
		Models        json.RawMessage   `json:"models"`
		SearchConfig  json.RawMessage   `json:"search_config"`
		DefaultModels map[string]string `json:"default_models"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return nil, fmt.Errorf("parse perplexity model configuration: %w", err)
	}
	rawModels := strings.TrimSpace(string(config.Models))
	if rawModels == "" || rawModels == "null" {
		return nil, fmt.Errorf("perplexity model configuration has no models")
	}

	definitions := make(map[string]configModelDefinition)
	models := make(map[string]ConfigModel)
	var order []string
	addDefinition := func(id string, raw json.RawMessage) {
		if !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
			return
		}
		var definition configModelDefinition
		if err := json.Unmarshal(raw, &definition); err != nil {
			return
		}
		if id == "" {
			id = definition.Model
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := models[id]; exists {
			return
		}
		definitions[id] = definition
		models[id] = ConfigModel{
			ID: id, Label: strings.TrimSpace(definition.Label),
			SubscriptionTier: strings.TrimSpace(definition.SubscriptionTier),
			Reasoning:        definition.Reasoning, Definition: raw,
		}
		order = append(order, id)
	}

	switch rawModels[0] {
	case '[':
		var entries []json.RawMessage
		if err := json.Unmarshal(config.Models, &entries); err != nil {
			return nil, fmt.Errorf("parse perplexity model list: %w", err)
		}
		for _, entry := range entries {
			addDefinition("", entry)
		}
	case '{':
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(config.Models, &entries); err != nil {
			return nil, fmt.Errorf("parse perplexity model dictionary: %w", err)
		}
		keys := make([]string, 0, len(entries))
		for id := range entries {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		for _, id := range keys {
			addDefinition(id, entries[id])
		}
	default:
		return nil, fmt.Errorf("perplexity models must be an object or array")
	}

	var result []ConfigModel
	seen := make(map[string]bool)
	appendChoice := func(id, label, tier string, reasoning *bool) {
		id = strings.TrimSpace(id)
		model, exists := models[id]
		if !exists || seen[id] {
			return
		}
		// Browser agents and Computer workflows are not ordinary chat models.
		switch definitions[id].Mode {
		case "", "search", "research", "concise", "copilot", "reasoning":
		default:
			return
		}
		if model.Label == "" {
			model.Label = strings.TrimSpace(label)
		}
		if model.SubscriptionTier == "" {
			model.SubscriptionTier = strings.TrimSpace(tier)
		}
		if model.Reasoning == nil {
			model.Reasoning = reasoning
		}
		seen[id] = true
		result = append(result, model)
	}

	if rawModels[0] == '{' && len(config.SearchConfig) > 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(config.SearchConfig)), "[") {
			return nil, fmt.Errorf("perplexity search_config must be an array")
		}
		var choices []searchModelChoice
		if err := json.Unmarshal(config.SearchConfig, &choices); err != nil {
			return nil, fmt.Errorf("parse perplexity search model choices: %w", err)
		}
		for _, choice := range choices {
			nonReasoning, reasoning := false, true
			appendChoice(choice.NonReasoningModel, choice.Label, choice.SubscriptionTier, &nonReasoning)
			appendChoice(choice.ReasoningModel, choice.Label, choice.SubscriptionTier, &reasoning)
		}
		for _, mode := range []string{"search", "research"} {
			appendChoice(config.DefaultModels[mode], "", "", nil)
		}
	} else {
		for _, id := range order {
			appendChoice(id, "", "", nil)
		}
	}
	return result, nil
}
