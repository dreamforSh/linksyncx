package service

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"go.uber.org/zap"
)

// perplexityCatalogMaxAge 是目录快照参与请求改写的保鲜期：超过该时长的
// 快照不再可信（上游模型清单会持续漂移）。
const perplexityCatalogMaxAge = 24 * time.Hour

// freshPerplexityCatalog 返回账号当前凭证/代理身份下保鲜期内的模型目录
// （snapshot.Models 键的小写裸 model_preference 列表）。快照缺失、来源不符、
// 身份指纹不符或过期时返回 nil——过期目录不能作为请求改写的依据。
func freshPerplexityCatalog(account *Account) []string {
	if account == nil {
		return nil
	}
	snapshot := account.GetUpstreamModelMetadataSnapshot()
	if snapshot == nil || snapshot.Source != "perplexity" || snapshot.IdentityFingerprint == "" || snapshot.IdentityFingerprint != perplexityAccountFingerprint(account) {
		return nil
	}
	syncedAt, err := time.Parse(time.RFC3339, snapshot.SyncedAt)
	if err != nil || syncedAt.After(time.Now()) || time.Since(syncedAt) >= perplexityCatalogMaxAge {
		return nil
	}
	catalog := make([]string, 0, len(snapshot.Models))
	for id := range snapshot.Models {
		catalog = append(catalog, strings.ToLower(id))
	}
	return catalog
}

// upgradePerplexityDeadModelPreference 是请求路径上的死模型自愈：账号持有新鲜
// 目录快照、解析出的 preference 已不在目录中、但目录里存在同族（剥掉版本数字
// 与 thinking 后缀后骨架相同）的更高版本时，把 preference 改写为最新同族版本
// （如 gpt54→gpt55），避免上游下架旧版本后该族模型整体不可用。
//
// 永不改写：静态别名（sonar→turbo 等，别名是稳定产品名而非版本号）、快照
// 缺失/过期/身份不符、以及目录无同族候选的情况。
func upgradePerplexityDeadModelPreference(account *Account, upstreamModel, modelPref string) string {
	if modelPref == "" || perplexity.IsStaticAliasPreference(upstreamModel) {
		return modelPref
	}
	catalog := freshPerplexityCatalog(account)
	if len(catalog) == 0 {
		return modelPref
	}
	for _, id := range catalog {
		if id == modelPref {
			return modelPref
		}
	}
	candidate, ok := perplexity.NewestFamilyPreference(modelPref, catalog)
	if !ok {
		return modelPref
	}
	logger.L().Info("perplexity: dead model preference auto-upgraded from catalog",
		zap.Int64("account_id", account.ID),
		zap.String("from", modelPref),
		zap.String("to", candidate),
	)
	return candidate
}
