package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// deriveAntigravityQuotaGroups 把共享重置时间与剩余比例（保留 4 位小数）的模型收成一组。
func deriveAntigravityQuotaGroups(models map[string]antigravity.ModelInfo) []AntigravityQuotaGroup {
	type item struct {
		id      string
		display string
	}
	type bucket struct {
		remaining float64
		reset     string
		items     []item
	}
	grouped := make(map[string]*bucket)
	for modelName, modelInfo := range models {
		if modelInfo.QuotaInfo == nil {
			continue
		}
		key := fmt.Sprintf("%.4f\n%s", modelInfo.QuotaInfo.RemainingFraction, modelInfo.QuotaInfo.ResetTime)
		entry := grouped[key]
		if entry == nil {
			entry = &bucket{
				remaining: modelInfo.QuotaInfo.RemainingFraction,
				reset:     modelInfo.QuotaInfo.ResetTime,
			}
			grouped[key] = entry
		}
		entry.items = append(entry.items, item{id: modelName, display: strings.TrimSpace(modelInfo.DisplayName)})
	}
	if len(grouped) == 0 {
		return nil
	}
	groups := make([]AntigravityQuotaGroup, 0, len(grouped))
	for _, entry := range grouped {
		sort.Slice(entry.items, func(i, j int) bool { return entry.items[i].id < entry.items[j].id })
		ids := make([]string, len(entry.items))
		names := make([]string, len(entry.items))
		for i, model := range entry.items {
			ids[i] = model.id
			if model.display != "" {
				names[i] = model.display
			} else {
				names[i] = model.id
			}
		}
		sort.Strings(names)
		groups = append(groups, AntigravityQuotaGroup{
			Label:             antigravityQuotaGroupLabel(names),
			Models:            ids,
			RemainingFraction: entry.remaining,
			ResetTime:         entry.reset,
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
	return groups
}

func antigravityQuotaGroupLabel(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	limit := len(names)
	if limit > 3 {
		limit = 3
	}
	label := strings.Join(names[:limit], " / ")
	if len(names) > 3 {
		label += "..."
	}
	return label
}

// applyAntigravityQuotaSummary 在 summary 有可用桶时替换推导分组，并重算 FiveHour。
// 没有可用桶时保留 fetchAvailableModels 推导出的分组。
func applyAntigravityQuotaSummary(info *UsageInfo, summary *antigravity.RetrieveUserQuotaSummaryResponse) {
	groups := quotaGroupsFromSummary(summary)
	if info == nil || len(groups) == 0 {
		return
	}
	info.AntigravityQuotaGroups = groups
	fillFiveHourFromQuotaGroups(info)
}

func quotaGroupsFromSummary(summary *antigravity.RetrieveUserQuotaSummaryResponse) []AntigravityQuotaGroup {
	if summary == nil {
		return nil
	}
	buckets := make([]antigravity.QuotaSummaryBucket, 0)
	if len(summary.Groups) > 0 {
		for _, group := range summary.Groups {
			for _, bucket := range group.Buckets {
				if strings.TrimSpace(bucket.DisplayName) == "" {
					bucket.DisplayName = group.DisplayName
				}
				buckets = append(buckets, bucket)
			}
		}
	} else {
		buckets = append(buckets, summary.Buckets...)
	}
	groups := make([]AntigravityQuotaGroup, 0, len(buckets))
	for _, bucket := range buckets {
		if bucket.Disabled {
			continue
		}
		label := strings.TrimSpace(bucket.DisplayName)
		if label == "" {
			label = strings.TrimSpace(bucket.BucketID)
		}
		if label == "" || bucket.RemainingFraction == nil {
			// 没给剩余比例不能当成满额，否则会盖掉模型接口已经拿到的真实用量。
			continue
		}
		remaining := *bucket.RemainingFraction
		models := append([]string(nil), bucket.Models...)
		if len(models) == 0 && strings.TrimSpace(bucket.BucketID) != "" {
			models = []string{strings.TrimSpace(bucket.BucketID)}
		}
		groups = append(groups, AntigravityQuotaGroup{
			Label:             label,
			Models:            models,
			RemainingFraction: remaining,
			ResetTime:         bucket.ResetTime,
		})
	}
	if len(groups) == 0 {
		return nil
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Label < groups[j].Label })
	return groups
}

// fillFiveHourFromQuotaGroups 用剩余比例最低的分组填充兼容字段 FiveHour。
func fillFiveHourFromQuotaGroups(info *UsageInfo) {
	if info == nil {
		return
	}
	if len(info.AntigravityQuotaGroups) == 0 {
		info.FiveHour = nil
		return
	}
	best := info.AntigravityQuotaGroups[0]
	for _, group := range info.AntigravityQuotaGroups[1:] {
		if group.RemainingFraction < best.RemainingFraction {
			best = group
		}
	}
	progress := &UsageProgress{Utilization: (1 - best.RemainingFraction) * 100}
	if best.ResetTime != "" {
		if resetTime, err := time.Parse(time.RFC3339, best.ResetTime); err == nil {
			progress.ResetsAt = &resetTime
			progress.RemainingSeconds = int(time.Until(resetTime).Seconds())
		}
	}
	info.FiveHour = progress
}
