//go:build unit

package service

import (
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/stretchr/testify/require"
)

// newStickyTLSProfileTestService 直接装好本地模板缓存，不经仓储与 Redis。
// 模板按给定 ID 顺序传入，便于验证结果与传入顺序 / map 遍历顺序无关。
func newStickyTLSProfileTestService(ids ...int64) *TLSFingerprintProfileService {
	profiles := make([]*model.TLSFingerprintProfile, 0, len(ids))
	for _, id := range ids {
		profiles = append(profiles, &model.TLSFingerprintProfile{
			ID:             id,
			Name:           fmt.Sprintf("template-%d", id),
			Curves:         []uint16{29},
			KeyShareGroups: []uint16{29},
		})
	}
	svc := &TLSFingerprintProfileService{}
	svc.setLocalCache(profiles)
	return svc
}

func newRandomTLSProfileTestAccount(id int64) *Account {
	return &Account{
		ID:       id,
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"tls_fingerprint_profile_id": float64(-1)},
	}
}

// resolveRandomTLSTemplates 返回每个账号在“随机”模式下解析到的模板名。
func resolveRandomTLSTemplates(t *testing.T, svc *TLSFingerprintProfileService, accounts int) map[int64]string {
	t.Helper()
	picks := make(map[int64]string, accounts)
	for id := int64(1); id <= int64(accounts); id++ {
		profile := svc.ResolveTLSProfile(newRandomTLSProfileTestAccount(id))
		require.NotNil(t, profile)
		picks[id] = profile.Name
	}
	return picks
}

// 一个账号就是一台固定设备：绑定“随机”的账号每次解析都必须落在同一个模板上，
// 否则相邻请求的 JA3 会变，连接池也会按模板摘要拆成多个。多个网关实例各自加载模板
// （加载顺序不同）时，同一账号也要选中同一个模板。整体上各模板分到的账号大致均匀。
func TestResolveTLSProfileRandomTemplateIsStickyPerAccount(t *testing.T) {
	const accounts = 4000
	svc := newStickyTLSProfileTestService(1, 2, 3, 4)
	otherInstance := newStickyTLSProfileTestService(4, 3, 2, 1)

	first := resolveRandomTLSTemplates(t, svc, accounts)
	for round := 0; round < 3; round++ {
		require.Equal(t, first, resolveRandomTLSTemplates(t, svc, accounts), "round %d re-rolled a template", round)
	}
	require.Equal(t, first, resolveRandomTLSTemplates(t, otherInstance, accounts), "instances must agree on each account's template")

	counts := map[string]int{}
	for _, name := range first {
		counts[name]++
	}
	t.Logf("accounts per template: %v", counts)
	require.Len(t, counts, 4)
	for name, count := range counts {
		// 期望 1000，标准差约 27；区间放得很宽，只拦住明显偏斜。
		require.InDelta(t, accounts/4, count, 200, "template %s got %d of %d accounts", name, count, accounts)
	}
}

// rendezvous 哈希的关键性质：新增一个模板只会把约 1/N 的账号迁走，而且只迁到新模板上；
// 删除模板只影响原本落在它上面的账号。管理员加删模板不会让整个账号池集体换指纹。
func TestResolveTLSProfileRandomTemplateMovesFewAccountsWhenTemplatesChange(t *testing.T) {
	const accounts = 4000
	svc := newStickyTLSProfileTestService(1, 2, 3, 4)
	before := resolveRandomTLSTemplates(t, svc, accounts)

	svc.setLocalCache(newStickyTLSProfileTestService(1, 2, 3, 4, 5).snapshotLocalCacheForTest())
	added := resolveRandomTLSTemplates(t, svc, accounts)
	moved := 0
	for id, name := range added {
		if name != before[id] {
			moved++
			require.Equal(t, "template-5", name, "account %d moved between existing templates", id)
		}
	}
	// 期望 1/5 = 800，标准差约 25。
	t.Logf("adding a 5th template moved %d of %d accounts", moved, accounts)
	require.InDelta(t, accounts/5, moved, 200, "adding a 5th template moved %d of %d accounts", moved, accounts)

	svc.setLocalCache(newStickyTLSProfileTestService(1, 2, 4, 5).snapshotLocalCacheForTest())
	removed := resolveRandomTLSTemplates(t, svc, accounts)
	for id, name := range removed {
		if added[id] != "template-3" {
			require.Equal(t, added[id], name, "account %d was not on the removed template but moved", id)
		} else {
			require.NotEqual(t, "template-3", name)
		}
	}
}

// 没有任何模板时，“随机”回落到内置默认 profile；未绑定模板（id = 0）的路径不访问
// 接收者，大量测试用的 &GatewayService{} 没有注入 tlsFPProfileService 也能工作。
func TestResolveTLSProfileFallsBackWithoutTemplatesAndToleratesNilService(t *testing.T) {
	empty := newStickyTLSProfileTestService()
	profile := empty.ResolveTLSProfile(newRandomTLSProfileTestAccount(1))
	require.NotNil(t, profile)
	require.Equal(t, "Built-in Default (Node.js 24.x)", profile.Name)

	var nilService *TLSFingerprintProfileService
	unbound := &Account{ID: 1, Platform: PlatformAnthropic, Type: AccountTypeOAuth, Extra: map[string]any{}}
	profile = nilService.ResolveTLSProfile(unbound)
	require.NotNil(t, profile)
	require.Equal(t, "Built-in Default (Node.js 24.x)", profile.Name)
}

func (s *TLSFingerprintProfileService) snapshotLocalCacheForTest() []*model.TLSFingerprintProfile {
	s.localMu.RLock()
	defer s.localMu.RUnlock()
	profiles := make([]*model.TLSFingerprintProfile, 0, len(s.localCache))
	for _, p := range s.localCache {
		profiles = append(profiles, p)
	}
	return profiles
}
