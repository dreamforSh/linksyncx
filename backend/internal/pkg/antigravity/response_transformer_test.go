//go:build unit

package antigravity

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Task 7: 验证 generateRandomID 和降级碰撞防护 ---

func TestGenerateRandomID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := generateRandomID()
		require.Len(t, id, 12, "ID 长度应为 12")
		_, dup := seen[id]
		require.False(t, dup, "第 %d 次调用生成了重复 ID: %s", i, id)
		seen[id] = struct{}{}
	}
}

func TestFallbackCounter_Increments(t *testing.T) {
	// 验证 fallbackCounter 的原子递增行为确保降级分支不会生成相同 seed
	before := atomic.LoadUint64(&fallbackCounter)
	cnt1 := atomic.AddUint64(&fallbackCounter, 1)
	cnt2 := atomic.AddUint64(&fallbackCounter, 1)
	require.Equal(t, before+1, cnt1, "第一次递增应为 before+1")
	require.Equal(t, before+2, cnt2, "第二次递增应为 before+2")
	require.NotEqual(t, cnt1, cnt2, "连续两次递增的计数器值应不同")
}

func TestFallbackCounter_ConcurrentIncrements(t *testing.T) {
	// 验证并发递增的原子性 — 每次递增都应产生唯一值
	const goroutines = 50
	results := make([]uint64, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = atomic.AddUint64(&fallbackCounter, 1)
		}(i)
	}
	wg.Wait()

	// 所有结果应唯一
	seen := make(map[uint64]bool, goroutines)
	for _, v := range results {
		assert.False(t, seen[v], "并发递增产生了重复值: %d", v)
		seen[v] = true
	}
}

func TestGenerateRandomID_Charset(t *testing.T) {
	const validChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	validSet := make(map[byte]struct{}, len(validChars))
	for i := 0; i < len(validChars); i++ {
		validSet[validChars[i]] = struct{}{}
	}

	for i := 0; i < 50; i++ {
		id := generateRandomID()
		for j := 0; j < len(id); j++ {
			_, ok := validSet[id[j]]
			require.True(t, ok, "ID 包含非法字符: %c (ID=%s)", id[j], id)
		}
	}
}

func TestGenerateRandomID_Length(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := generateRandomID()
		assert.Len(t, id, 12, "每次生成的 ID 长度应为 12")
	}
}

func TestGenerateRandomID_ConcurrentUniqueness(t *testing.T) {
	// 验证并发调用不会产生重复 ID
	const goroutines = 100
	results := make([]string, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = generateRandomID()
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool, goroutines)
	for _, id := range results {
		assert.False(t, seen[id], "并发调用产生了重复 ID: %s", id)
		seen[id] = true
	}
}

func BenchmarkGenerateRandomID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = generateRandomID()
	}
}

func TestTransformGeminiToClaude_ResponseShapes(t *testing.T) {
	const body = `{"responseId":"msg_direct","candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"cachedContentTokenCount":4,"candidatesTokenCount":3}}`
	for _, data := range []string{body, `{"response":` + body + `}`} {
		out, usage, err := TransformGeminiToClaude([]byte(data), "gemini-test")
		require.NoError(t, err)
		var response ClaudeResponse
		require.NoError(t, json.Unmarshal(out, &response))
		assert.Equal(t, "msg_direct", response.ID)
		require.Len(t, response.Content, 1)
		assert.Equal(t, "hello", response.Content[0].Text)
		assert.Equal(t, "end_turn", response.StopReason)
		assert.Equal(t, 16, usage.InputTokens)
		assert.Equal(t, 4, usage.CacheReadInputTokens)
	}
}

func TestParseGeminiResponse_UsesResponseFieldPresence(t *testing.T) {
	resp, err := parseGeminiResponse([]byte(`{"response":{"usageMetadata":{"promptTokenCount":42}},"candidates":[{"content":{"parts":[{"text":"outer decoy"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7}}`))
	require.NoError(t, err)
	assert.Empty(t, resp.Response.Candidates)
	require.NotNil(t, resp.Response.UsageMetadata)
	assert.Equal(t, 42, resp.Response.UsageMetadata.PromptTokenCount)
}

func TestParseGeminiResponse_ErrorRetainsTypedUsage(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":500},"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}`,
		`{"error":{"code":500},"response":{"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}`,
		`{"response":{"error":{"code":500},"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":3}}}`,
	} {
		resp, err := parseGeminiResponse([]byte(body))
		require.Error(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.Response.UsageMetadata)
		assert.Equal(t, 8, resp.Response.UsageMetadata.PromptTokenCount)
		assert.Equal(t, 3, resp.Response.UsageMetadata.CandidatesTokenCount)
		out, usage, err := TransformGeminiToClaude([]byte(body), "gemini-test")
		require.Error(t, err)
		assert.Nil(t, out)
		assert.Nil(t, usage)
	}
}

func TestTransformGeminiToClaude_RejectsInvalidOrErrorResponses(t *testing.T) {
	for _, data := range []string{
		`{"response":`,
		`null`,
		`[]`,
		`{}`,
		`{"usageMetadata":{"promptTokenCount":5}}`,
		`{"response":null,"candidates":[{"finishReason":"STOP"}]}`,
		`{"response":{"candidates":[]},"candidates":[{"finishReason":"STOP"}]}`,
		`{"response":{"candidates":"invalid"},"candidates":[{"finishReason":"STOP"}]}`,
		`{"error":{"code":429,"message":"quota exceeded"}}`,
		`{"response":{"error":{"message":"failed"}}}`,
		`{"error":{"code":500},"response":{"candidates":[{"finishReason":"STOP"}]}}`,
	} {
		out, usage, err := TransformGeminiToClaude([]byte(data), "gemini-test")
		require.Error(t, err, "body=%s", data)
		assert.Nil(t, out)
		assert.Nil(t, usage)
	}
}

func TestNonStreamingProcessor_SignatureOnlyBindsThinking(t *testing.T) {
	for _, textField := range []string{"", `"text":"",`} {
		body := `{"candidates":[{"content":{"parts":[{"text":"plan","thought":true},{` + textField + `"thoughtSignature":"sig"},{"text":"answer"}]},"finishReason":"STOP"}]}`
		out, _, err := TransformGeminiToClaude([]byte(body), "gemini-test")
		require.NoError(t, err)
		var response ClaudeResponse
		require.NoError(t, json.Unmarshal(out, &response))
		require.Len(t, response.Content, 2)
		assert.Equal(t, "thinking", response.Content[0].Type)
		assert.Equal(t, "plan", response.Content[0].Thinking)
		assert.Equal(t, "sig", response.Content[0].Signature)
		assert.Equal(t, "answer", response.Content[1].Text)
	}
}

func TestNonStreamingProcessor_StandaloneSignatureIsPreserved(t *testing.T) {
	out, _, err := TransformGeminiToClaude([]byte(`{"candidates":[{"content":{"parts":[{"thoughtSignature":"sig"}]},"finishReason":"STOP"}]}`), "gemini-test")
	require.NoError(t, err)
	var response ClaudeResponse
	require.NoError(t, json.Unmarshal(out, &response))
	require.Len(t, response.Content, 1)
	assert.Equal(t, "thinking", response.Content[0].Type)
	assert.Empty(t, response.Content[0].Thinking)
	assert.Equal(t, "sig", response.Content[0].Signature)
}

func TestNonStreamingProcessor_SignedTextPreservesPreviousTextOrder(t *testing.T) {
	out, _, err := TransformGeminiToClaude([]byte(`{"candidates":[{"content":{"parts":[{"text":"before"},{"text":"signed","thoughtSignature":"sig"},{"text":"after"}]},"finishReason":"STOP"}]}`), "gemini-test")
	require.NoError(t, err)
	var response ClaudeResponse
	require.NoError(t, json.Unmarshal(out, &response))
	require.Len(t, response.Content, 4)
	assert.Equal(t, "before", response.Content[0].Text)
	assert.Equal(t, "signed", response.Content[1].Text)
	assert.Equal(t, "thinking", response.Content[2].Type)
	assert.Equal(t, "sig", response.Content[2].Signature)
	assert.Equal(t, "after", response.Content[3].Text)
}
