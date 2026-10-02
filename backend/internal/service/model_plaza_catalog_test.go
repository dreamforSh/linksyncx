//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

type plazaCatalogAccountRepoStub struct {
	AccountRepository
	accounts []Account
}

func (r *plazaCatalogAccountRepoStub) ListSchedulable(context.Context) ([]Account, error) {
	return r.accounts, nil
}

func newPlazaCatalogService(accounts []Account, channels []Channel, groups []Group, pricing *PricingService) *ModelPlazaService {
	repo := &mockChannelRepository{
		listAllFn: func(ctx context.Context) ([]Channel, error) { return channels, nil },
	}
	svc := NewModelPlazaService(repo, &stubGroupRepoForAvailable{activeGroups: groups}, pricing, nil, nil,
		&plazaCatalogAccountRepoStub{accounts: accounts})
	if pricing != nil {
		svc.billingService = NewBillingService(&config.Config{}, pricing)
	}
	return svc
}

func plazaCatalogByKey(models []PlazaCatalogModel) map[string]PlazaCatalogModel {
	out := make(map[string]PlazaCatalogModel, len(models))
	for _, m := range models {
		out[m.Platform+"/"+m.Name] = m
	}
	return out
}

func TestListModelCatalog_AccountsWithoutChannels(t *testing.T) {
	defaultModel := claude.DefaultModelIDs()[0]
	pricing := newStubPricingServiceFromMap(map[string]*LiteLLMModelPricing{
		defaultModel: {Mode: "chat", InputCostPerToken: 3e-6, OutputCostPerToken: 1.5e-5, CacheReadInputTokenCost: 3e-7},
	})
	accounts := []Account{
		// 未分组、精确映射 + 通配符映射：精确键直接列出，通配符不作为模型名
		{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{defaultModel: defaultModel, "custom-claude-alias": defaultModel, "claude-*": defaultModel},
		}},
		// 分组账号、无映射：贡献平台默认模型
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeOAuth, GroupIDs: []int64{10, 99}, Credentials: map[string]any{}},
	}
	groups := []Group{{ID: 10, Name: "claude-pool", Platform: PlatformAnthropic, RateMultiplier: 0.8}}
	svc := newPlazaCatalogService(accounts, nil, groups, pricing)

	catalog, err := svc.ListModelCatalog(context.Background())
	require.NoError(t, err)
	byKey := plazaCatalogByKey(catalog.Models)

	priced, ok := byKey["anthropic/"+defaultModel]
	require.True(t, ok)
	require.True(t, priced.Ungrouped, "未分组账号能服务")
	require.Equal(t, []int64{10}, priced.GroupIDs, "非活跃分组 99 不计入")
	require.NotNil(t, priced.OfficialPricing)
	require.InDelta(t, 3e-6, *priced.OfficialPricing.InputPrice, 1e-15)
	require.InDelta(t, 1.5e-5, *priced.OfficialPricing.OutputPrice, 1e-15)

	alias, ok := byKey["anthropic/custom-claude-alias"]
	require.True(t, ok, "精确映射键应列出")
	require.True(t, alias.Ungrouped)
	require.Empty(t, alias.GroupIDs)

	for key := range byKey {
		require.NotContains(t, key, "*", "通配符映射键不应作为模型列出")
	}
	for _, id := range claude.DefaultModelIDs() {
		entry, ok := byKey["anthropic/"+id]
		require.True(t, ok, "无映射账号应贡献默认模型 %s", id)
		require.Equal(t, []int64{10}, entry.GroupIDs)
	}
	require.Equal(t, "claude-pool", catalog.Groups[10].Name)
}

func TestListModelCatalog_MergesChannelModelsAndDropsUnservable(t *testing.T) {
	channels := []Channel{
		plazaPricedChannel(1, "ch", []int64{20}, PlatformOpenAI, "gpt-5"),
		// 渠道只挂在非活跃分组上：没有可用途径，不展示
		plazaPricedChannel(2, "stale", []int64{30}, PlatformOpenAI, "gpt-legacy"),
	}
	groups := []Group{{ID: 20, Name: "openai", Platform: PlatformOpenAI, RateMultiplier: 1, IsExclusive: true}}
	svc := newPlazaCatalogService(nil, channels, groups, nil)

	catalog, err := svc.ListModelCatalog(context.Background())
	require.NoError(t, err)
	byKey := plazaCatalogByKey(catalog.Models)

	require.Contains(t, byKey, "openai/gpt-5")
	require.Equal(t, []int64{20}, byKey["openai/gpt-5"].GroupIDs)
	require.False(t, byKey["openai/gpt-5"].Ungrouped)
	require.NotContains(t, byKey, "openai/gpt-legacy")
	require.True(t, catalog.Groups[20].IsExclusive)
}

func TestListModelCatalog_SortedByPlatformThenName(t *testing.T) {
	accounts := []Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-b": "gpt-b", "gpt-a": "gpt-a"},
		}},
		{ID: 2, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{"claude-x": "claude-x"},
		}},
	}
	svc := newPlazaCatalogService(accounts, nil, nil, nil)

	catalog, err := svc.ListModelCatalog(context.Background())
	require.NoError(t, err)
	names := make([]string, 0, len(catalog.Models))
	for _, m := range catalog.Models {
		names = append(names, m.Platform+"/"+m.Name)
	}
	require.Equal(t, []string{"anthropic/claude-x", "openai/gpt-a", "openai/gpt-b"}, names)
}
