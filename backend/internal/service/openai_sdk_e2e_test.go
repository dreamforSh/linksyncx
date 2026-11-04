//go:build unit

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// codexSSEUpstream replays the event sequence of the ChatGPT Codex backend,
// including its response.completed with an empty output list, and records the
// body of every request by the "case=" marker in its input.
type codexSSEUpstream struct {
	mu     sync.Mutex
	seq    int
	bodies map[string][]byte
}

var codexSSECaseMarker = regexp.MustCompile(`case=([a-z_]+)`)

func (u *codexSSEUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	u.mu.Lock()
	u.seq++
	responseID := fmt.Sprintf("resp_e2e_%d", u.seq)
	if match := codexSSECaseMarker.FindAllSubmatch(body, -1); len(match) > 0 {
		// The last marker belongs to the newest turn of an expanded history.
		u.bodies[string(match[len(match)-1][1])] = body
	}
	u.mu.Unlock()

	var events []string
	n := 0
	emit := func(eventType, payload string) {
		events = append(events, fmt.Sprintf("event: %s\ndata: {\"type\":%q,\"sequence_number\":%d,%s}\n\n", eventType, eventType, n, payload))
		n++
	}
	model := gjson.GetBytes(body, "model").String()
	head := fmt.Sprintf(`"id":%q,"object":"response","created_at":1767225600,"model":%q`, responseID, model)
	emit("response.created", `"response":{`+head+`,"status":"in_progress","output":[],"usage":null}`)
	emit("response.in_progress", `"response":{`+head+`,"status":"in_progress","output":[],"usage":null}`)
	usage := `"usage":{"input_tokens":21,"input_tokens_details":{"cached_tokens":0},"output_tokens":9,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":30}`

	if bytes.Contains(body, []byte("get_weather")) {
		item := `{"id":"fc_e2e","type":"function_call","status":"%s","name":"get_weather","call_id":"call_e2e","arguments":%q}`
		emit("response.output_item.added", `"output_index":0,"item":`+fmt.Sprintf(item, "in_progress", ""))
		emit("response.function_call_arguments.delta", `"item_id":"fc_e2e","output_index":0,"delta":"{\"city\":"`)
		emit("response.function_call_arguments.delta", `"item_id":"fc_e2e","output_index":0,"delta":"\"Paris\"}"`)
		emit("response.function_call_arguments.done", `"item_id":"fc_e2e","output_index":0,"arguments":"{\"city\":\"Paris\"}"`)
		done := fmt.Sprintf(item, "completed", `{"city":"Paris"}`)
		emit("response.output_item.done", `"output_index":0,"item":`+done)
		emit("response.completed", `"response":{`+head+`,"status":"completed","output":[`+done+`],`+usage+`}`)
	} else {
		text := "Hello, world"
		if bytes.Contains(body, []byte("case=state_second")) {
			text = "I do not know"
			if bytes.Contains(body, []byte("remember 7")) {
				text = "The number is 7"
			}
		}
		emit("response.output_item.added", `"output_index":0,"item":{"id":"rs_e2e","type":"reasoning","summary":[]}`)
		emit("response.reasoning_summary_part.added", `"item_id":"rs_e2e","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}`)
		emit("response.reasoning_summary_text.delta", `"item_id":"rs_e2e","output_index":0,"summary_index":0,"delta":"Thinking"`)
		emit("response.reasoning_summary_text.done", `"item_id":"rs_e2e","output_index":0,"summary_index":0,"text":"Thinking"`)
		emit("response.reasoning_summary_part.done", `"item_id":"rs_e2e","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":"Thinking"}`)
		emit("response.output_item.done", `"output_index":0,"item":{"id":"rs_e2e","type":"reasoning","summary":[{"type":"summary_text","text":"Thinking"}],"encrypted_content":"gAAAAe2e"}`)
		emit("response.output_item.added", `"output_index":1,"item":{"id":"msg_e2e","type":"message","status":"in_progress","role":"assistant","content":[]}`)
		emit("response.content_part.added", `"item_id":"msg_e2e","output_index":1,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}`)
		half := len(text) / 2
		emit("response.output_text.delta", fmt.Sprintf(`"item_id":"msg_e2e","output_index":1,"content_index":0,"delta":%q,"logprobs":[]`, text[:half]))
		emit("response.output_text.delta", fmt.Sprintf(`"item_id":"msg_e2e","output_index":1,"content_index":0,"delta":%q,"logprobs":[]`, text[half:]))
		emit("response.output_text.done", fmt.Sprintf(`"item_id":"msg_e2e","output_index":1,"content_index":0,"text":%q,"logprobs":[]`, text))
		emit("response.content_part.done", fmt.Sprintf(`"item_id":"msg_e2e","output_index":1,"content_index":0,"part":{"type":"output_text","text":%q,"annotations":[]}`, text))
		emit("response.output_item.done", fmt.Sprintf(`"output_index":1,"item":{"id":"msg_e2e","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}`, text))
		// The Codex backend leaves output empty here; the gateway rebuilds it.
		emit("response.completed", `"response":{`+head+`,"status":"completed","output":[],`+usage+`}`)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}, "X-Request-Id": []string{"req_" + responseID}},
		Body:       io.NopCloser(strings.NewReader(strings.Join(events, ""))),
	}, nil
}

func (u *codexSSEUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *codexSSEUpstream) body(t *testing.T, name string) []byte {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	body, ok := u.bodies[name]
	require.True(t, ok, "no upstream request recorded for %s", name)
	return body
}

// newOpenAISDKE2EServer exposes the gateway service paths the OpenAI SDK uses,
// doing the same pre-forward steps as the real handlers for stored responses.
func newOpenAISDKE2EServer(t *testing.T, svc *OpenAIGatewayService, account *Account) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	const groupID, userID, apiKeyID = int64(0), int64(1), int64(1)
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		if gjson.GetBytes(body, "previous_response_id").String() != "" {
			continuation, err := svc.ExpandOpenAIResponsesContinuation(c.Request.Context(), groupID, userID, apiKeyID, body)
			if err != nil || continuation == nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": fmt.Sprint("continuation: ", err)}})
				return
			}
			body = continuation.Body
			SetOpenAIResponsesStatePending(c, continuation.Pending)
		} else if pending := svc.NewOpenAIResponsesStatePending(groupID, userID, apiKeyID, body); pending != nil {
			SetOpenAIResponsesStatePending(c, pending)
		}
		if _, err := svc.Forward(c.Request.Context(), c, account, body); err != nil && !c.Writer.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": err.Error()}})
		}
	})
	router.GET("/v1/responses/:id", func(c *gin.Context) {
		response, found, err := svc.GetOpenAIResponsesStoredResponse(c.Request.Context(), groupID, c.Param("id"), userID, apiKeyID)
		if err != nil || !found {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "not found"}})
			return
		}
		c.Data(http.StatusOK, "application/json", response)
	})
	router.GET("/v1/responses/:id/input_items", func(c *gin.Context) {
		items, found, err := svc.ListOpenAIResponsesStoredInputItems(c.Request.Context(), groupID, c.Param("id"), userID, apiKeyID)
		if err != nil || !found {
			c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "not found"}})
			return
		}
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": items, "has_more": false})
	})
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		if _, err := svc.ForwardAsChatCompletions(c.Request.Context(), c, account, body, "", ""); err != nil && !c.Writer.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": err.Error()}})
		}
	})
	return httptest.NewServer(router)
}

// TestOpenAINodeSDKAgainstCodexOAuth runs the official OpenAI Node SDK against
// the Codex OAuth paths: text and image input, xhigh/max effort, Responses and
// Chat streaming (raw iteration and the SDK's strict stream helpers), tool
// calls, retrieve and previous_response_id. Point OPENAI_NODE_SDK_DIR at an
// installed `openai` package to run it.
func TestOpenAINodeSDKAgainstCodexOAuth(t *testing.T) {
	sdkDir := os.Getenv("OPENAI_NODE_SDK_DIR")
	if sdkDir == "" {
		t.Skip("set OPENAI_NODE_SDK_DIR to an installed openai npm package")
	}
	upstream := &codexSSEUpstream{bodies: map[string][]byte{}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{OpenAIResponsesStateEnabled: true}},
		httpUpstream: upstream,
	}
	account := &Account{
		ID: 777, Name: "sdk-e2e", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 8,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc"},
		Status:      StatusActive, Schedulable: true,
	}
	server := newOpenAISDKE2EServer(t, svc, account)
	defer server.Close()

	script, err := filepath.Abs(filepath.Join("testdata", "openai_sdk_e2e.cjs"))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", script, sdkDir, server.URL+"/v1").CombinedOutput()
	require.NoError(t, err, string(out))
	var report struct {
		SDKVersion string                     `json:"sdkVersion"`
		Cases      map[string]json.RawMessage `json:"cases"`
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for scanner.Scan() {
		if line, ok := strings.CutPrefix(scanner.Text(), "SDK_REPORT "); ok {
			require.NoError(t, json.Unmarshal([]byte(line), &report))
		}
	}
	require.NotEmpty(t, report.Cases, string(out))
	t.Logf("openai node sdk %s", report.SDKVersion)
	result := func(name string) gjson.Result {
		raw, ok := report.Cases[name]
		require.True(t, ok, name)
		r := gjson.ParseBytes(raw)
		require.True(t, r.Get("ok").Bool(), "%s: %s", name, r.Get("error").String())
		return r
	}

	r := result("responses_text_xhigh")
	require.Equal(t, "Hello, world", r.Get("outputText").String())
	require.Equal(t, `["reasoning","message"]`, r.Get("outputTypes").Raw, "non-stream output is rebuilt from the stream")
	require.Equal(t, int64(4), r.Get("usage.output_tokens_details.reasoning_tokens").Int())
	sent := upstream.body(t, "responses_text_xhigh")
	require.Equal(t, "xhigh", gjson.GetBytes(sent, "reasoning.effort").String())
	require.Equal(t, "auto", gjson.GetBytes(sent, "reasoning.summary").String())
	require.True(t, gjson.GetBytes(sent, "stream").Bool(), "Codex upstream is always streamed")

	r = result("responses_stream_image_max")
	require.Equal(t, "Hello, world", r.Get("text").String())
	require.Equal(t, "Thinking", r.Get("reasoning").String())
	require.True(t, r.Get("seqMonotonic").Bool())
	require.Equal(t, "response.created", r.Get("types.0").String())
	require.Equal(t, "response.completed", r.Get("types.@reverse.0").String())
	require.Equal(t, `["reasoning","message"]`, r.Get("completedOutputTypes").Raw, "streamed response.completed carries the rebuilt output")
	require.Equal(t, "Hello, world", r.Get("completedText").String())
	sent = upstream.body(t, "responses_stream_image_max")
	require.Equal(t, "max", gjson.GetBytes(sent, "reasoning.effort").String())
	image := gjson.GetBytes(sent, `input.#.content.#(type=="input_image")|0`)
	require.True(t, strings.HasPrefix(image.Get("image_url").String(), "data:image/png;base64,"), string(sent))
	require.Equal(t, "high", image.Get("detail").String())

	r = result("responses_stream_helper")
	require.Equal(t, "Hello, world", r.Get("deltas").String())
	require.Equal(t, "Hello, world", r.Get("outputText").String())

	r = result("responses_retrieve_and_continue")
	require.Equal(t, r.Get("firstID").String(), r.Get("fetchedID").String())
	require.Equal(t, "Hello, world", r.Get("fetchedText").String())
	require.Equal(t, int64(1), r.Get("inputItemCount").Int())
	require.Equal(t, "The number is 7", r.Get("secondText").String(), "previous_response_id context reached the upstream")
	require.Equal(t, r.Get("firstID").String(), r.Get("secondPrev").String())

	r = result("responses_stream_function_call")
	require.Equal(t, `{"city":"Paris"}`, r.Get("argDeltas").String())
	require.Equal(t, `{"city":"Paris"}`, r.Get("arguments").String())
	require.Equal(t, "get_weather", r.Get("name").String())

	r = result("chat_image_xhigh")
	require.Equal(t, "chat.completion", r.Get("object").String())
	require.Equal(t, "Hello, world", r.Get("content").String())
	require.Equal(t, "stop", r.Get("finish").String())
	require.Equal(t, int64(30), r.Get("usage.total_tokens").Int())
	sent = upstream.body(t, "chat_image_xhigh")
	require.Equal(t, "xhigh", gjson.GetBytes(sent, "reasoning.effort").String())
	image = gjson.GetBytes(sent, `input.#.content.#(type=="input_image")|0`)
	require.True(t, strings.HasPrefix(image.Get("image_url").String(), "data:image/png;base64,"), string(sent))
	require.Equal(t, "low", image.Get("detail").String(), "Chat image_url.detail must survive the Responses conversion")

	r = result("chat_stream_max_usage")
	require.Equal(t, "Hello, world", r.Get("content").String())
	require.Equal(t, "stop", r.Get("finish").String())
	require.Equal(t, "assistant", r.Get("role").String())
	require.Equal(t, int64(30), r.Get("usage.total_tokens").Int())
	require.Equal(t, int64(0), r.Get("usageChunkChoices").Int(), "include_usage chunk has empty choices")
	sent = upstream.body(t, "chat_stream_max_usage")
	require.Equal(t, "max", gjson.GetBytes(sent, "reasoning.effort").String())

	r = result("chat_stream_helper_tools")
	require.Equal(t, "tool_calls", r.Get("finish").String())
	require.Equal(t, "get_weather", r.Get("name").String())
	require.Equal(t, `{"city":"Paris"}`, r.Get("arguments").String())
}
