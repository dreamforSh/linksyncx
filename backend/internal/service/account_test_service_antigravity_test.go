//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAntigravityConnectionTestModel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "claude-sonnet-4-6", antigravityConnectionTestModel(""))
	require.Equal(t, "gemini-3.1-pro-preview", antigravityConnectionTestModel("gemini-3.1-pro-preview"))
}

func TestBuildGeminiTestRequest_TextModelUsesPromptAndSufficientTokens(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	body, err := svc.buildGeminiTestRequest("proj-1", "gemini-3.1-pro", "hi there")
	require.NoError(t, err)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(body, &wrapped))
	require.Equal(t, "gemini-3.1-pro", wrapped["model"])

	req := wrapped["request"].(map[string]any)
	contents := req["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	require.Equal(t, "hi there", parts[0].(map[string]any)["text"])

	genConfig := req["generationConfig"].(map[string]any)
	require.Equal(t, float64(antigravityTestMaxOutputTokens), genConfig["maxOutputTokens"],
		"thinking 模型需要足够的输出额度才能给出可见回复")

	sys := req["systemInstruction"].(map[string]any)
	sysParts := sys["parts"].([]any)
	require.Contains(t, sysParts[0].(map[string]any)["text"], "You are Antigravity")
}

func TestBuildGeminiTestRequest_DefaultPromptWhenEmpty(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	body, err := svc.buildGeminiTestRequest("proj-1", "gemini-3.1-pro", "  ")
	require.NoError(t, err)
	require.Contains(t, string(body), defaultGeminiTextTestPrompt)
}

func TestBuildGeminiTestRequest_ImageModel(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	body, err := svc.buildGeminiTestRequest("proj-1", "gemini-3.1-flash-image", "draw a cat")
	require.NoError(t, err)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(body, &wrapped))

	req := wrapped["request"].(map[string]any)
	genConfig := req["generationConfig"].(map[string]any)
	require.Equal(t, []any{"TEXT", "IMAGE"}, genConfig["responseModalities"])
	_, hasCap := genConfig["maxOutputTokens"]
	require.False(t, hasCap, "图像模型不应限制 maxOutputTokens")

	contents := req["contents"].([]any)
	parts := contents[0].(map[string]any)["parts"].([]any)
	require.Equal(t, "draw a cat", parts[0].(map[string]any)["text"])
}

func TestBuildClaudeTestRequest_UsesPromptAndSufficientTokens(t *testing.T) {
	t.Parallel()

	svc := &AntigravityGatewayService{}
	body, err := svc.buildClaudeTestRequest("proj-1", "claude-sonnet-4-6", "hello")
	require.NoError(t, err)

	var wrapped map[string]any
	require.NoError(t, json.Unmarshal(body, &wrapped))
	req := wrapped["request"].(map[string]any)
	genConfig := req["generationConfig"].(map[string]any)
	require.Equal(t, float64(antigravityTestMaxOutputTokens), genConfig["maxOutputTokens"])
	require.Contains(t, string(body), "hello")
}

func TestExtractTestContentFromSSEResponse_PrefersAnswerOverThought(t *testing.T) {
	t.Parallel()

	sse := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking...\",\"thought\":true}]}}]}}\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello!\"}]}}]}}\n\n"
	text, images := extractTestContentFromSSEResponse([]byte(sse))
	require.Equal(t, "Hello!", text)
	require.Empty(t, images)
}

func TestExtractTestContentFromSSEResponse_ThoughtFallback(t *testing.T) {
	t.Parallel()

	sse := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ponder\",\"thought\":true}]}}]}}\n\n"
	text, _ := extractTestContentFromSSEResponse([]byte(sse))
	require.Equal(t, "ponder", text, "只有思考内容时退回思考文本，避免测试结果显示为空")
}

func TestExtractTestContentFromSSEResponse_Image(t *testing.T) {
	t.Parallel()

	sse := "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}}\n\n"
	text, images := extractTestContentFromSSEResponse([]byte(sse))
	require.Empty(t, text)
	require.Len(t, images, 1)
	require.Equal(t, "data:image/png;base64,QUJD", images[0].URL)
	require.Equal(t, "image/png", images[0].MimeType)
}
