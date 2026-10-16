//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestPerplexityImagesReachAskAcrossIngressProtocols(t *testing.T) {
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, remote := range []bool{false, true} {
			name := protocol + map[bool]string{false: "/data_url", true: "/remote_rejected"}[remote]
			t.Run(name, func(t *testing.T) {
				png := perplexityAttachmentPNG(t)
				url := perplexityAttachmentURL("image/png", png)
				if remote {
					url = "https://example.org/private-image.png"
				}
				const objectURL = "https://fixture-bucket.s3.us-east-1.amazonaws.com/images/photo.png"
				svc, upstream, account := perplexityIngressService(t, func(index int, _ *http.Request) *http.Response {
					switch index {
					case 0:
						return perplexityAttachmentResponse(200, `{"s3_bucket_url":"https://fixture-bucket.s3.us-east-1.amazonaws.com/","s3_object_url":"`+objectURL+`","fields":{"key":"images/photo.png","policy":"fake-signed-policy"}}`)
					case 1:
						return perplexityAttachmentResponse(204, "")
					default:
						return perplexityAttachmentResponse(200, perplexityIngressAnswer("image answer", "", true, false))
					}
				})
				account = perplexityAttachmentAccount()
				bodyObject := map[string]any{"model": "sonar-pro"}
				switch protocol {
				case "responses":
					bodyObject["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{
						map[string]any{"type": "input_text", "text": "describe"}, map[string]any{"type": "input_image", "image_url": url},
					}}}
				case "messages":
					bodyObject["max_tokens"] = 64
					imageSource := map[string]any{"type": "base64", "media_type": "image/png", "data": base64.StdEncoding.EncodeToString(png)}
					if remote {
						imageSource = map[string]any{"type": "url", "url": url}
					}
					bodyObject["messages"] = []any{map[string]any{"role": "user", "content": []any{
						map[string]any{"type": "text", "text": "describe"}, map[string]any{"type": "image", "source": imageSource},
					}}}
				default:
					bodyObject["messages"] = []any{map[string]any{"role": "user", "content": []any{
						map[string]any{"type": "text", "text": "describe"}, perplexityAttachmentPart(url),
					}}}
				}
				body, err := json.Marshal(bodyObject)
				require.NoError(t, err)
				c, recorder := perplexityIngressContext(t, body)
				var result *OpenAIForwardResult
				switch protocol {
				case "responses":
					result, err = svc.forwardPerplexityResponses(context.Background(), c, account, body, "sonar-pro", false, time.Now())
				case "messages":
					result, err = svc.forwardPerplexityMessages(context.Background(), c, account, body, "")
				default:
					result, err = svc.forwardPerplexityChatCompletions(context.Background(), c, account, body, "", "")
				}
				if remote {
					require.Error(t, err)
					require.Nil(t, result)
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Empty(t, upstream.requests)
					return
				}
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.UsageEstimated)
				require.Len(t, upstream.requests, 3)
				require.Contains(t, upstream.requests[0].URL.Path, "create_upload_url")
				require.Contains(t, upstream.requests[0].Header.Get("Cookie"), "session-secret")
				require.Empty(t, upstream.requests[1].Header.Get("Cookie"))
				require.Empty(t, upstream.requests[1].Header.Get("Authorization"))
				require.Empty(t, upstream.requests[1].Header.Get("x-account-secret"))
				proxy, err := account.ProxyURLForOutbound()
				require.NoError(t, err)
				for _, actual := range upstream.proxies {
					require.Equal(t, proxy, actual)
				}
				require.Equal(t, objectURL, gjson.GetBytes(upstream.bodies[2], "params.attachments.0").String())
				require.Contains(t, gjson.GetBytes(upstream.bodies[2], "query_str").String(), "Image attachment")
				require.NotContains(t, string(upstream.bodies[2]), "data:image")
				require.Contains(t, recorder.Body.String(), "image answer")
				require.Nil(t, svc.perplexityThreads, "attachment contexts must not seed native text continuation")
			})
		}
	}
}

func TestPerplexityEstimatedUsageDoesNotInventRawTelemetry(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, nil, nil, nil)
	result := &OpenAIForwardResult{RequestID: "perplexity-estimate", Model: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 100, OutputTokens: 7}}
	input := &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 3}}
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	baseline := usageRepo.lastLog
	cost, actual, fingerprint := baseline.TotalCost, baseline.ActualCost, billingRepo.lastCmd.RequestFingerprint
	require.NotNil(t, baseline.RawInputTokens)
	result.UsageEstimated = true
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	estimated := usageRepo.lastLog
	require.True(t, estimated.UsageIncomplete)
	require.Nil(t, estimated.RawInputTokens)
	require.Nil(t, estimated.RawOutputTokens)
	require.Nil(t, estimated.RawCacheCreationTokens)
	require.Nil(t, estimated.RawCacheReadTokens)
	require.Equal(t, 100, estimated.InputTokens)
	require.Equal(t, 7, estimated.OutputTokens)
	require.Equal(t, cost, estimated.TotalCost)
	require.Equal(t, actual, estimated.ActualCost)
	require.Equal(t, fingerprint, billingRepo.lastCmd.RequestFingerprint)
}
