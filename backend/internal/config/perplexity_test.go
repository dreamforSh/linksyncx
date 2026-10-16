package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPerplexityMaintenanceConfigDefaultsAndEnvironment(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		cfg, err := Load()
		require.NoError(t, err)
		require.True(t, cfg.Gateway.Perplexity.MaintenanceEnabled)
		require.Equal(t, 6, cfg.Gateway.Perplexity.SessionKeepaliveHours)
		require.Equal(t, 5, cfg.Gateway.Perplexity.QuotaRefreshMinutes)
	})
	t.Run("environment overrides", func(t *testing.T) {
		resetViperWithJWTSecret(t)
		t.Setenv("GATEWAY_PERPLEXITY_MAINTENANCE_ENABLED", "false")
		t.Setenv("GATEWAY_PERPLEXITY_SESSION_KEEPALIVE_HOURS", "2")
		t.Setenv("GATEWAY_PERPLEXITY_QUOTA_REFRESH_MINUTES", "10")
		cfg, err := Load()
		require.NoError(t, err)
		require.False(t, cfg.Gateway.Perplexity.MaintenanceEnabled)
		require.Equal(t, 2, cfg.Gateway.Perplexity.SessionKeepaliveHours)
		require.Equal(t, 10, cfg.Gateway.Perplexity.QuotaRefreshMinutes)
	})
}

func TestPerplexityMaintenanceConfigValidation(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"GATEWAY_PERPLEXITY_SESSION_KEEPALIVE_HOURS", "-1"},
		{"GATEWAY_PERPLEXITY_SESSION_KEEPALIVE_HOURS", "169"},
		{"GATEWAY_PERPLEXITY_QUOTA_REFRESH_MINUTES", "-1"},
		{"GATEWAY_PERPLEXITY_QUOTA_REFRESH_MINUTES", "16"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			resetViperWithJWTSecret(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			require.Error(t, err)
			require.Contains(t, err.Error(), "gateway.perplexity.")
		})
	}
}
