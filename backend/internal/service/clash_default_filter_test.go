//go:build unit

package service

import (
	"regexp"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	"github.com/stretchr/testify/require"
)

func TestDefaultClashExcludePatternKeepsPlanNodeNames(t *testing.T) {
	pattern := regexp.MustCompile(DefaultClashExcludePattern)
	for _, name := range []string{
		"美国AT&T家宽-03套餐hy2",
		"cf加速|美国AT&T家宽-03套餐🇺🇸",
		"日本套餐03-高速",
		"套餐03 香港",
		"套餐 香港 01",
		"ℹ️　套餐 香港 01",
		"US Premium Plan-03",
		"Plan Tokyo 02",
		"Subscription US 01",
		"TrafficLight US",
		"Remainington UK",
		"Unexpired JP",
		"WebsiteProxy SG",
	} {
		t.Run(name, func(t *testing.T) {
			require.False(t, pattern.MatchString(name))
		})
	}
}

func TestDefaultClashExcludePatternRemovesInformationEntries(t *testing.T) {
	pattern := regexp.MustCompile(DefaultClashExcludePattern)
	for _, name := range []string{
		"剩余流量 10GB",
		"到期时间：2027-01-01",
		"已过期",
		"官网：https://example.com",
		"重置时间：下月",
		"套餐：高级版",
		"📦 套餐名称：高级版",
		"⚠️　套餐：高级版",
		"　套餐　：高级版",
		"[套餐信息]",
		"当前套餐 = 高级版",
		"我的套餐详情：高级版",
		"Remaining traffic: 10 GB",
		"TRAFFIC: 10 GB",
		"Expires: 2027-01-01",
		"Expired",
		"Expiry date: 2027-01-01",
		"Expiration: 2027-01-01",
		"Website: https://example.com",
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, pattern.MatchString(name))
		})
	}
}

func TestClashDefaultFilterPreservesNodesThroughSyncPlan(t *testing.T) {
	main := parsedNode("美国AT&T家宽-03套餐hy2", "hysteria2", "main.example.com", 443, "auth")
	backup := parsedNode("美国AT&T家宽备用ushy2", "hysteria2", "backup.example.com", 443, "auth")
	nodes := []clashsub.Node{
		main, backup,
		parsedNode("cf加速|美国AT&T家宽-03套餐🇺🇸", "vless", "other.example.com", 443, "auth"),
		parsedNode("套餐信息：高级版", "hysteria2", "info.example.com", 443, "auth"),
		parsedNode("Remaining traffic: 10GB", "hysteria2", "quota.example.com", 443, "auth"),
	}
	existing := []ClashNode{storedNode(7, backup, ClashNodeStatusDisabled)}
	plan, stats, err := BuildClashNodeSyncPlan(existing, nodes, ClashSyncOptions{
		Exclude: regexp.MustCompile(DefaultClashExcludePattern),
	})
	require.NoError(t, err)
	require.Equal(t, 2, stats.Filtered)
	require.Len(t, plan.Inserts, 2)
	require.Equal(t, main.Name, plan.Inserts[0].Name)
	require.Equal(t, nodes[2].Name, plan.Inserts[1].Name)
	require.Len(t, plan.Updates, 1)
	require.Equal(t, ClashNodeStatusDisabled, plan.Updates[0].Status, "refresh must preserve administrator disabling")
	require.Empty(t, plan.Missing)
}
