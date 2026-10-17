//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/channelmonitor"
	"github.com/Wei-Shaw/sub2api/ent/channelmonitorrequesttemplate"
	"github.com/stretchr/testify/require"
)

// These are the generated validators called by Ent builders before SQL writes.
// SQL CHECK permission alone does not make a new provider persistable.
func TestPerplexityMonitorEntProviderValidators(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "gemini", "grok", "perplexity", "antigravity", "kimi", "zhipu", "deepseek", "minimax", "opencode_go"} {
		require.NoError(t, channelmonitor.ProviderValidator(channelmonitor.Provider(provider)))
		require.NoError(t, channelmonitorrequesttemplate.ProviderValidator(channelmonitorrequesttemplate.Provider(provider)))
	}
	for _, provider := range []string{"bogus", "Perplexity", "pplx", "perplexity "} {
		require.Error(t, channelmonitor.ProviderValidator(channelmonitor.Provider(provider)))
		require.Error(t, channelmonitorrequesttemplate.ProviderValidator(channelmonitorrequesttemplate.Provider(provider)))
	}
}
