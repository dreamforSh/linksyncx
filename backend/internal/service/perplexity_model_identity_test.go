//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func perplexityModelAnswer(text, model string) string {
	payload, _ := json.Marshal(map[string]any{
		"display_model": model,
		"blocks":        []any{map[string]any{"intended_usage": "ask_text", "markdown_block": map[string]any{"answer": text}}},
	})
	return "data: " + string(payload) + "\n\n"
}

func TestPerplexityActualModelAcrossIngressProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			for _, reported := range []string{"", "gpt54", "claude46sonnet"} {
				t.Run(protocol+"/"+map[bool]string{false: "buffer", true: "stream"}[stream]+"/"+reported, func(t *testing.T) {
					svc, _, account := perplexityIngressService(t, func(int, *http.Request) *http.Response {
						return perplexityAttachmentResponse(200, perplexityModelAnswer("actual answer", reported)+"event: end_of_stream\n\n")
					})
					account.Credentials["model_mapping"] = map[string]any{"alias": "gpt54"}
					bodyObject := map[string]any{"model": "alias", "stream": stream}
					if protocol == "responses" {
						bodyObject["input"] = "question"
					} else {
						bodyObject["messages"] = []any{map[string]any{"role": "user", "content": "question"}}
						if protocol == "messages" {
							bodyObject["max_tokens"] = 100
						}
					}
					body, err := json.Marshal(bodyObject)
					require.NoError(t, err)
					c, recorder := perplexityIngressContext(t, body)
					var result *OpenAIForwardResult
					switch protocol {
					case "chat":
						result, err = svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
					case "responses":
						result, err = svc.forwardPerplexityResponses(context.Background(), c, account, body, "alias", stream, time.Now())
					case "messages":
						result, err = svc.forwardPerplexityMessages(context.Background(), c, account, body, "")
					}
					require.NoError(t, err)
					require.Equal(t, 200, recorder.Code)
					require.NotNil(t, result)
					require.Equal(t, "gpt54", result.UpstreamModel)
					require.Equal(t, reported, result.UpstreamResponseModel)
					require.False(t, result.UpstreamResponseModelConflict)
					require.Equal(t, "alias", result.Model)
					require.True(t, result.UsageEstimated)
					require.Contains(t, recorder.Body.String(), "actual answer")
					status := "unknown"
					if reported == "gpt54" {
						status = "matched"
					} else if reported != "" {
						status = "substituted"
						require.Contains(t, recorder.Body.String(), "Perplexity 上游报告使用了")
						require.Contains(t, recorder.Body.String(), reported)
					}
					if !stream {
						require.Equal(t, status, recorder.Header().Get("X-Perplexity-Model-Status"))
						require.Equal(t, reported, recorder.Header().Get("X-Perplexity-Actual-Model"))
					}
					if protocol == "chat" && !stream {
						require.Equal(t, status, gjson.Get(recorder.Body.String(), "perplexity.model_identity.status").String())
					}
				})
			}
		}
	}
}

func TestPerplexityActualModelMixedAndPartialStreams(t *testing.T) {
	for _, complete := range []bool{false, true} {
		svc, _, account := perplexityIngressService(t, func(int, *http.Request) *http.Response {
			body := perplexityModelAnswer("first", "gpt54") + perplexityModelAnswer("first second", "claude46sonnet")
			if complete {
				body += "event: end_of_stream\n\n"
			}
			return perplexityAttachmentResponse(200, body)
		})
		account.Credentials["model_mapping"] = map[string]any{"alias": "gpt54"}
		body := []byte(`{"model":"alias","stream":true,"messages":[{"role":"user","content":"q"}]}`)
		c, recorder := perplexityIngressContext(t, body)
		result, err := svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
		if complete {
			require.NoError(t, err)
			require.Contains(t, recorder.Body.String(), "多个不同模型")
		} else {
			require.Error(t, err)
			require.NotContains(t, recorder.Body.String(), "[DONE]")
			require.NotContains(t, recorder.Body.String(), "多个不同模型")
		}
		require.NotNil(t, result)
		require.Empty(t, result.UpstreamResponseModel)
		require.True(t, result.UpstreamResponseModelConflict)
	}
}

func TestPerplexityBufferedIdentityFollowsAuthoritativeRewrittenAnswer(t *testing.T) {
	identity := &perplexityResponseIdentity{}
	out, err := translatePerplexityFixture(t,
		perplexityModelAnswer("discarded draft", "gpt54")+perplexityModelAnswer("final answer", "claude46sonnet")+"event: end_of_stream\n\n",
		perplexityTranslateParams{upstreamModel: "gpt54", identity: identity})
	require.NoError(t, err)
	require.Equal(t, "claude46sonnet", identity.snapshot().ActualModel)
	require.False(t, identity.snapshot().Conflict)
	require.Equal(t, "substituted", gjson.Get(out, "perplexity.model_identity.status").String())
	require.NotContains(t, out, "discarded draft")
}

func TestPerplexityModelNoticeIsNotBilledAndContinuationUsesVisibleText(t *testing.T) {
	var complete perplexityCompletion
	out, err := translatePerplexityFixture(t, perplexityModelAnswer("answer", "claude46sonnet")+"event: end_of_stream\n\n", perplexityTranslateParams{
		upstreamModel: "gpt54", onComplete: func(value perplexityCompletion) { complete = value },
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), gjson.Get(out, "usage.completion_tokens").Int())
	require.Equal(t, complete.Content, gjson.Get(out, "choices.0.message.content").String())
	require.True(t, strings.HasPrefix(complete.Content, "answer\n\n>"))
	require.False(t, *upstreamModelMismatch("pplx_pro", "turbo"))
	require.False(t, *upstreamModelMismatch("claude46sonnetthinking", "claude46sonnet"))
}
