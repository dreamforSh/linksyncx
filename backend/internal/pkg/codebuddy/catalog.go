package codebuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	CreditPolicyAll      = "all"
	CreditPolicyZeroOnly = "zero_only"
)

// DefaultModelIDs is advertised when an account has neither a synced catalog
// nor a manual whitelist. It mirrors the domestic CLI static fallback.
var DefaultModelIDs = []string{
	"auto", "hy4-preview", "hy3", "deepseek-v4-pro", "deepseek-v4-flash",
	"glm-5.3", "glm-5.2", "minimax-m3", "kimi-k3-1", "kimi-k2.7",
}

var multiplierRE = regexp.MustCompile(`(?i)^x\s*([0-9]+(?:\.[0-9]+)?)\s*(?:credits?)?$`)

// CatalogModel is one model of an account's synced catalog.
type CatalogModel struct {
	ID                string   `json:"id"`
	Name              string   `json:"name,omitempty"`
	Aliases           []string `json:"aliases,omitempty"`
	Credits           string   `json:"credits,omitempty"`
	CreditsValue      *float64 `json:"credits_value,omitempty"`
	SupportsImages    bool     `json:"supports_images,omitempty"`
	SupportsReasoning bool     `json:"supports_reasoning,omitempty"`
	MaxInputTokens    int64    `json:"max_input_tokens,omitempty"`
	MaxOutputTokens   int64    `json:"max_output_tokens,omitempty"`
}

// FetchCatalog requests the account's /v3/config catalog and returns the raw
// body alongside the parsed models.
func FetchCatalog(ctx context.Context, client HTTPDoer, creds Credentials) ([]CatalogModel, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ConfigURL(creds.Profile), nil)
	if err != nil {
		return nil, nil, err
	}
	ApplyHeaders(req.Header, CatalogHeaders(IdentityOf(creds)))
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, body, ErrAuthExpired
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, body, fmt.Errorf("catalog HTTP %d", resp.StatusCode)
	}
	models, err := ParseCatalog(body, ProfileProduct(creds.Profile))
	return models, body, err
}

// ParseCatalog selects the product's models from a /v3/config payload: the
// product agent's model references (cli agent for CLI, the default-tagged
// agent for WorkBuddy), minus disabled models, models outside a non-empty
// availableModels and models without tool-call support.
func ParseCatalog(payload []byte, product string) ([]CatalogModel, error) {
	var root struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	if code := strings.Trim(string(root.Code), `"`); code != "" && code != "0" && code != "200" && code != "null" {
		return nil, fmt.Errorf("catalog code %s: %s", code, root.Msg)
	}
	data := root.Data
	if len(data) == 0 {
		data = payload
	}
	var body struct {
		Models          []map[string]any `json:"models"`
		AvailableModels []string         `json:"availableModels"`
		Agents          []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("decode catalog data: %w", err)
	}
	if len(body.Models) == 0 {
		return nil, fmt.Errorf("catalog has no models")
	}

	byRef := map[string]map[string]any{}
	for _, model := range body.Models {
		for _, alias := range stringSlice(model["aliases"]) {
			byRef[alias] = model
		}
	}
	for _, model := range body.Models {
		if name := strings.TrimSpace(asString(model["name"])); name != "" {
			byRef[name] = model
		}
	}
	for _, model := range body.Models {
		if id := strings.TrimSpace(asString(model["id"])); id != "" {
			byRef[id] = model
		}
	}

	selected := body.Models
	if refs := agentModelRefs(body.Agents, product); len(refs) > 0 {
		picked := make([]map[string]any, 0, len(refs))
		seen := map[string]struct{}{}
		for _, ref := range refs {
			model := byRef[ref]
			id := strings.TrimSpace(asString(model["id"]))
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			picked = append(picked, model)
		}
		if len(picked) > 0 {
			selected = picked
		}
	}

	available := map[string]struct{}{}
	for _, id := range body.AvailableModels {
		if id = strings.TrimSpace(id); id != "" {
			available[id] = struct{}{}
		}
	}

	out := make([]CatalogModel, 0, len(selected))
	for _, model := range selected {
		id := strings.TrimSpace(asString(model["id"]))
		if id == "" || asBool(model["disabled"]) || !asBool(model["supportsToolCall"]) {
			continue
		}
		if len(available) > 0 {
			if _, ok := available[id]; !ok {
				continue
			}
		}
		credits := strings.TrimSpace(asString(model["credits"]))
		out = append(out, CatalogModel{
			ID:                id,
			Name:              strings.TrimSpace(asString(model["name"])),
			Aliases:           stringSlice(model["aliases"]),
			Credits:           credits,
			CreditsValue:      ParseMultiplier(credits),
			SupportsImages:    asBool(model["supportsImages"]) && !asBool(model["disabledMultimodal"]),
			SupportsReasoning: asBool(model["supportsReasoning"]),
			MaxInputTokens:    asInt(model["maxInputTokens"]),
			MaxOutputTokens:   asInt(model["maxOutputTokens"]),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("catalog produced no usable models")
	}
	return out, nil
}

func agentModelRefs(agents []map[string]any, product string) []string {
	var cli, tagged, fallback map[string]any
	for _, agent := range agents {
		if strings.TrimSpace(asString(agent["name"])) == "cli" && cli == nil {
			cli = agent
		}
		if contains(stringSlice(agent["tags"]), "default") && tagged == nil {
			tagged = agent
		}
		if _, ok := agent["models"]; ok && fallback == nil {
			fallback = agent
		}
	}
	chosen := cli
	if product == ProductWorkBuddy {
		switch {
		case tagged != nil:
			chosen = tagged
		case cli != nil:
			chosen = cli
		default:
			chosen = fallback
		}
	}
	if chosen == nil {
		return nil
	}
	items, _ := chosen["models"].([]any)
	refs := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				refs = append(refs, s)
			}
		case map[string]any:
			if ref := firstNonEmpty(asString(v["id"]), asString(v["name"])); ref != "" {
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

// ParseMultiplier parses "x0.5 credits" style prices.
func ParseMultiplier(credits string) *float64 {
	match := multiplierRE.FindStringSubmatch(strings.TrimSpace(credits))
	if match == nil {
		return nil
	}
	value, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	return &value
}

// IsZeroCredit reports an explicit x0 price.
func IsZeroCredit(credits string) bool {
	value := ParseMultiplier(credits)
	return value != nil && *value == 0
}

// FindModel resolves a catalog entry by ID, then alias. IDs win across the
// whole catalog so an alias can never shadow another model's real ID.
func FindModel(models []CatalogModel, modelID string) (CatalogModel, bool) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return CatalogModel{}, false
	}
	for _, model := range models {
		if strings.EqualFold(model.ID, modelID) {
			return model, true
		}
	}
	for _, model := range models {
		for _, alias := range model.Aliases {
			if strings.EqualFold(alias, modelID) {
				return model, true
			}
		}
	}
	return CatalogModel{}, false
}

// ModelMultiplier returns the credit price of modelID and whether the catalog
// priced it at all.
func ModelMultiplier(models []CatalogModel, modelID string) (float64, bool) {
	model, ok := FindModel(models, modelID)
	if !ok || model.CreditsValue == nil {
		return 0, false
	}
	return *model.CreditsValue, true
}

// FilterCatalog applies the account's credit policy.
func FilterCatalog(models []CatalogModel, policy string) []CatalogModel {
	if NormalizeCreditPolicy(policy) != CreditPolicyZeroOnly {
		return models
	}
	out := make([]CatalogModel, 0, len(models))
	for _, model := range models {
		if IsZeroCredit(model.Credits) {
			out = append(out, model)
		}
	}
	return out
}

func NormalizeCreditPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case CreditPolicyZeroOnly, "zero", "free":
		return CreditPolicyZeroOnly
	default:
		return CreditPolicyAll
	}
}

// CatalogIDs returns the public model ids (the international default-model is
// exposed as auto), de-duplicated in catalog order.
func CatalogIDs(models []CatalogModel) []string {
	ids := make([]string, 0, len(models))
	seen := map[string]struct{}{}
	for _, model := range models {
		id := PublicModel(strings.TrimSpace(model.ID))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asInt(v any) int64 {
	if f, ok := v.(float64); ok && f > 0 {
		return int64(f)
	}
	return 0
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := strings.TrimSpace(asString(item)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
