package service

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

// PlazaCatalogModel 模型广场「按模型」视图的条目：站点实际能服务的模型 + 计费同源的基础 API 价。
type PlazaCatalogModel struct {
	Name            string
	Platform        string
	OfficialPricing *PlazaOfficialPricing
	// GroupIDs 能服务该模型的活跃分组（账号所在分组 ∪ 配置了该模型的渠道关联分组），升序。
	GroupIDs []int64
	// Ungrouped 有未分组账号能服务该模型（只供未绑定分组的 API Key 调度）。
	Ungrouped bool
}

// PlazaCatalogGroup 目录引用到的分组摘要，供 handler 做可见性裁剪与展示倍率。
type PlazaCatalogGroup struct {
	ID             int64
	Name           string
	Platform       string
	RateMultiplier float64
	IsExclusive    bool
}

// PlazaModelCatalog 按模型汇总的价格目录。
type PlazaModelCatalog struct {
	Models []PlazaCatalogModel
	Groups map[int64]PlazaCatalogGroup
}

// ListModelCatalog 以模型为顶层汇总站点能服务的模型与基础 API 价，不依赖渠道配置。
//
// 模型枚举与 /v1/models 同口径：可调度账号的 model_mapping 精确键；无映射（或映射含通配符、
// OpenAI 透传）的账号贡献其平台默认模型中它能服务的部分。另并入活跃渠道配置的支持模型。
// 只保留至少有一个活跃分组或未分组账号可服务的模型；价格为无分组、无渠道口径的官方价
// （与计费同源），实付需再乘分组倍率。可见性裁剪由 handler 按登录态完成。
func (s *ModelPlazaService) ListModelCatalog(ctx context.Context) (*PlazaModelCatalog, error) {
	groups, err := s.groupRepo.ListActive(ctx)
	if err != nil {
		return nil, fmt.Errorf("list active groups: %w", err)
	}
	catalog := &PlazaModelCatalog{Groups: make(map[int64]PlazaCatalogGroup, len(groups))}
	for i := range groups {
		g := &groups[i]
		catalog.Groups[g.ID] = PlazaCatalogGroup{
			ID:             g.ID,
			Name:           g.Name,
			Platform:       g.Platform,
			RateMultiplier: g.RateMultiplier,
			IsExclusive:    g.IsExclusive,
		}
	}

	type modelKey struct {
		platform string
		name     string
	}
	entries := make(map[modelKey]*PlazaCatalogModel)
	add := func(platform, name string, groupIDs []int64, ungrouped bool) {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "*") {
			return
		}
		key := modelKey{platform: platform, name: name}
		entry := entries[key]
		if entry == nil {
			entry = &PlazaCatalogModel{Name: name, Platform: platform}
			entries[key] = entry
		}
		for _, gid := range groupIDs {
			if _, active := catalog.Groups[gid]; active && !containsInt64(entry.GroupIDs, gid) {
				entry.GroupIDs = append(entry.GroupIDs, gid)
			}
		}
		if ungrouped {
			entry.Ungrouped = true
		}
	}

	if s.accountRepo != nil {
		accounts, err := s.accountRepo.ListSchedulable(ctx)
		if err != nil {
			return nil, fmt.Errorf("list schedulable accounts: %w", err)
		}
		for i := range accounts {
			acc := &accounts[i]
			for _, model := range plazaAccountModels(acc) {
				add(acc.Platform, model, acc.GroupIDs, len(acc.GroupIDs) == 0)
			}
		}
	}

	if s.channelRepo != nil {
		channels, err := s.channelRepo.ListAll(ctx)
		if err != nil {
			return nil, fmt.Errorf("list channels: %w", err)
		}
		for i := range channels {
			ch := &channels[i]
			if ch.Status != StatusActive {
				continue
			}
			ch.normalizeBillingModelSource()
			for _, m := range ch.SupportedModels() {
				if isConcreteRequestPlatform(m.Platform) {
					add(m.Platform, m.Name, ch.GroupIDs, false)
				}
			}
		}
	}

	officialMemo := make(map[string]*PlazaOfficialPricing)
	catalog.Models = make([]PlazaCatalogModel, 0, len(entries))
	for _, entry := range entries {
		if len(entry.GroupIDs) == 0 && !entry.Ungrouped {
			continue
		}
		sort.Slice(entry.GroupIDs, func(i, j int) bool { return entry.GroupIDs[i] < entry.GroupIDs[j] })
		entry.OfficialPricing = s.lookupOfficialPricing(ctx, entry.Name, officialMemo)
		catalog.Models = append(catalog.Models, *entry)
	}
	sort.Slice(catalog.Models, func(i, j int) bool {
		if catalog.Models[i].Platform != catalog.Models[j].Platform {
			return catalog.Models[i].Platform < catalog.Models[j].Platform
		}
		return catalog.Models[i].Name < catalog.Models[j].Name
	})
	return catalog, nil
}

// plazaAccountModels 列出账号能服务、且可以明确列举的模型名。
// 精确映射键直接列出；无映射、映射含通配符或 OpenAI 透传时，再补上平台默认模型中
// 账号实际能服务的部分（与调度判定 IsModelSupported 一致）。
func plazaAccountModels(acc *Account) []string {
	mapping := acc.GetModelMapping()
	models := make([]string, 0, len(mapping))
	needDefaults := len(mapping) == 0 || acc.IsOpenAIPassthroughEnabled()
	for model := range mapping {
		if strings.Contains(model, "*") {
			needDefaults = true
			continue
		}
		models = append(models, model)
	}
	if needDefaults {
		for _, model := range PlatformDefaultModelIDs(acc.Platform) {
			if acc.IsModelSupported(model) {
				models = append(models, model)
			}
		}
	}
	return models
}

// PlatformDefaultModelIDs 返回平台对外宣告的默认模型 ID（与 /v1/models 的兜底列表一致）。
// 国产厂商等无固定模型清单的平台返回 nil。
func PlatformDefaultModelIDs(platform string) []string {
	switch platform {
	case PlatformAnthropic:
		return claude.DefaultModelIDs()
	case PlatformOpenAI:
		return openai.DefaultModelIDs()
	case PlatformGemini:
		ids := make([]string, 0, len(geminicli.DefaultModels))
		for _, model := range geminicli.DefaultModels {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformAntigravity:
		models := antigravity.DefaultModels()
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID)
		}
		return ids
	case PlatformGrok:
		return xai.DefaultModelIDs()
	case PlatformOpenCodeGo:
		return DefaultOpenCodeGoModelIDs()
	default:
		return nil
	}
}
