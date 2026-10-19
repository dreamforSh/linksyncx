package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandCodeModelProtocol(t *testing.T) {
	require.Equal(t, APIProtocolAnthropic, CommandCodeModelProtocol("claude-sonnet-4-6"))
	require.Equal(t, APIProtocolResponses, CommandCodeModelProtocol("gpt-5.4"))
	require.Equal(t, APIProtocolChatCompletions, CommandCodeModelProtocol("deepseek/deepseek-v4-flash"))
}

func TestCommandCodeAccountDefaults(t *testing.T) {
	account := &Account{Platform: PlatformCommandCode, Type: AccountTypeAPIKey}
	require.Equal(t, DefaultCommandCodeBaseURL, account.GetOpenAIBaseURL())
	require.Equal(t, DefaultCommandCodeAnthropicBaseURL, account.GetCNProtocolBaseURL(APIProtocolAnthropic))
	require.Equal(t, APIProtocolAdaptive, account.GetAPIProtocol())
	require.Equal(t, APIProtocolAnthropic, account.ResolveCommandCodeUpstreamProtocol("claude-opus-4-6"))
}

func TestCommandCodeAnthropicTargetURLAvoidsDuplicateVersion(t *testing.T) {
	service := &OpenAIGatewayService{}
	account := &Account{
		ID:       2,
		Platform: PlatformCommandCode,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key": "key",
			"api_base_urls": map[string]any{
				APIProtocolAnthropic: "https://api.commandcode.ai/provider/v1",
			},
		},
	}
	target, err := service.nativeAnthropicTargetURL(account)
	require.NoError(t, err)
	require.Equal(t, "https://api.commandcode.ai/provider/v1/messages", target)
}
