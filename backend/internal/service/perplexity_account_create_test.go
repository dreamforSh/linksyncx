//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreatePerplexityAccountWithoutRelayBillingProbe(t *testing.T) {
	disabled := false
	for _, probeEnabled := range []*bool{nil, &disabled} {
		repo := &upstreamBillingProbeAccountRepo{}
		svc := &adminServiceImpl{accountRepo: repo}
		account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
			Name: "Perplexity web subscription", Platform: PlatformPerplexity, Type: AccountTypeAPIKey,
			Credentials: map[string]any{"api_key": "fake-session"}, ProbeEnabled: probeEnabled,
		})
		require.NoError(t, err)
		require.Equal(t, PlatformPerplexity, account.Platform)
		require.Equal(t, "fake-session", account.GetCredential("api_key"))
		require.NotContains(t, account.Extra, UpstreamBillingProbeEnabledExtraKey)
	}
}

func TestCreatePerplexityAccountRejectsRelayBillingProbe(t *testing.T) {
	enabled := true
	svc := &adminServiceImpl{accountRepo: &upstreamBillingProbeAccountRepo{}}
	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "Perplexity web subscription", Platform: PlatformPerplexity, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "fake-session"}, ProbeEnabled: &enabled,
	})
	require.ErrorIs(t, err, ErrUpstreamBillingProbeAccountInvalid)
}
