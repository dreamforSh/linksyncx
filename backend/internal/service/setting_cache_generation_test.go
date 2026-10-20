//go:build unit

package service

import (
	"context"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// blockingSettingRepo 模拟一次跨越管理员保存的在途回源：首次读取先拿到保存前的旧值，
// 再阻塞到测试放行，放行后才把结果交回给缓存加载逻辑。
type blockingSettingRepo struct {
	gatewayTTLSettingRepo
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func newBlockingSettingRepo(data map[string]string) *blockingSettingRepo {
	return &blockingSettingRepo{
		gatewayTTLSettingRepo: gatewayTTLSettingRepo{data: data},
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
}

func (r *blockingSettingRepo) pause() {
	r.once.Do(func() {
		close(r.entered)
		<-r.release
	})
}

func (r *blockingSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	v, err := r.gatewayTTLSettingRepo.GetValue(ctx, key)
	r.pause()
	return v, err
}

func (r *blockingSettingRepo) GetMultiple(ctx context.Context, keys []string) (map[string]string, error) {
	v, err := r.gatewayTTLSettingRepo.GetMultiple(ctx, keys)
	r.pause()
	return v, err
}

// resetGenerationGuardedCaches 让三份受代号保护的进程级缓存过期；refreshCachedSettings
// 还会写调度设置缓存，一并在结束时置为过期，避免把本测试的值带给后续用例。
func resetGenerationGuardedCaches(t *testing.T) {
	t.Helper()
	reset := func() {
		versionBoundsSF.Forget("version_bounds")
		versionBoundsCache.Store(&cachedVersionBounds{})
		backendModeSF.Forget("backend_mode")
		backendModeCache.Store(&cachedBackendMode{})
		gatewayForwardingSF.Forget("gateway_forwarding")
		gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	}
	reset()
	t.Cleanup(func() {
		reset()
		openAIAdvancedSchedulerSettingCache.Store(&cachedOpenAIAdvancedSchedulerSetting{})
	})
}

func TestRefreshCachedSettings_InFlightLoadDoesNotOverwriteSavedValues(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		stale map[string]string
		saved *SystemSettings
		read  func(*SettingService) any
		want  any
	}{
		{
			name:  "gateway forwarding",
			stale: map[string]string{SettingKeyEnableAnthropicCacheTTL1hInjection: "false"},
			saved: &SystemSettings{EnableAnthropicCacheTTL1hInjection: true},
			read:  func(s *SettingService) any { return s.IsAnthropicCacheTTL1hInjectionEnabled(ctx) },
			want:  true,
		},
		{
			name:  "backend mode",
			stale: map[string]string{SettingKeyBackendModeEnabled: "false"},
			saved: &SystemSettings{BackendModeEnabled: true},
			read:  func(s *SettingService) any { return s.IsBackendModeEnabled(ctx) },
			want:  true,
		},
		{
			name:  "version bounds",
			stale: map[string]string{SettingKeyMinClaudeCodeVersion: "2.0.0"},
			saved: &SystemSettings{MinClaudeCodeVersion: "2.1.0"},
			read: func(s *SettingService) any {
				minVersion, _ := s.GetClaudeCodeVersionBounds(ctx)
				return minVersion
			},
			want: "2.1.0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetGenerationGuardedCaches(t)
			repo := newBlockingSettingRepo(tc.stale)
			svc := NewSettingService(repo, &config.Config{})

			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = tc.read(svc)
			}()
			<-repo.entered // 回源已读到旧值，尚未写回缓存
			svc.refreshCachedSettings(tc.saved)
			close(repo.release)
			<-done

			require.Equal(t, tc.want, tc.read(svc), "在途回源不得把保存前读到的旧值写回缓存")
		})
	}
}
