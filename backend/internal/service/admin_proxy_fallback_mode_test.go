//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// 过期改投直连（"direct"）已移除：创建 / 更新代理只接受 none / proxy，其它取值在写库前被拒绝。
func TestAdminProxyRejectsUnsupportedFallbackModes(t *testing.T) {
	for _, mode := range []string{"direct", "Direct", "bogus"} {
		t.Run("create "+mode, func(t *testing.T) {
			svc := &adminServiceImpl{proxyRepo: &proxyRepoStub{}} // Create panics if reached
			_, err := svc.CreateProxy(context.Background(), &CreateProxyInput{
				Name: "p", Protocol: "http", Host: "proxy.example.com", Port: 8080, FallbackMode: mode,
			})
			require.Error(t, err)
			require.Equal(t, "PROXY_FALLBACK_MODE_INVALID", infraerrors.Reason(err))
		})
		t.Run("update "+mode, func(t *testing.T) {
			repo := &updatingProxyRepoStub{proxyRepoStub: &proxyRepoStub{}, proxy: &Proxy{ID: 9, FallbackMode: FallbackModeNone}}
			svc := &adminServiceImpl{proxyRepo: repo}
			_, err := svc.UpdateProxy(context.Background(), 9, &UpdateProxyInput{FallbackMode: mode})
			require.Error(t, err)
			require.Equal(t, "PROXY_FALLBACK_MODE_INVALID", infraerrors.Reason(err))
			require.Zero(t, repo.updateCalls)
		})
	}
}

func TestIsValidProxyFallbackMode(t *testing.T) {
	require.True(t, IsValidProxyFallbackMode(FallbackModeNone))
	require.True(t, IsValidProxyFallbackMode(FallbackModeProxy))
	for _, mode := range []string{"", "direct", "NONE", "proxy "} {
		require.False(t, IsValidProxyFallbackMode(mode), mode)
	}
}
