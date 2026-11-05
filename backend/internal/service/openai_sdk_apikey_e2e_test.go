//go:build unit

package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const apiKeyE2EPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// openAIPlatformStub stands in for an OpenAI-compatible upstream behind an
// API-key account: it stores Responses (previous_response_id, retrieve,
// input_items, delete) and answers the Images API, recording every request by
// the "case=" marker it carries.
type openAIPlatformStub struct {
	mu        sync.Mutex
	seq       int
	responses map[string][]byte
	inputs    map[string][]json.RawMessage
	requests  map[string]*http.Request
	bodies    map[string][]byte
	lookups   []string
}

func (u *openAIPlatformStub) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.Header.Get("Authorization") != "Bearer sk-relay" {
		return stubJSON(http.StatusUnauthorized, `{"error":{"message":"bad key","type":"invalid_request_error"}}`), nil
	}
	if req.URL.Host != "relay.example.test" {
		return stubJSON(http.StatusBadGateway, `{"error":{"message":"unexpected host"}}`), nil
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	path := strings.TrimPrefix(req.URL.Path, "/v1")
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case req.Method == http.MethodPost && path == "/images/generations":
		u.record(req, body, gjson.GetBytes(body, "prompt").String())
		return stubJSON(http.StatusOK, u.imagesResponse(gjson.GetBytes(body, "size").String())), nil
	case req.Method == http.MethodPost && path == "/images/edits":
		fields, files := parseStubMultipart(req.Header.Get("Content-Type"), body)
		fields["_files"] = fmt.Sprint(files)
		encoded, _ := json.Marshal(fields)
		u.record(req, encoded, fields["prompt"])
		return stubJSON(http.StatusOK, u.imagesResponse(fields["size"])), nil
	case req.Method == http.MethodPost && path == "/responses":
		return u.createResponse(req, body), nil
	case strings.HasPrefix(path, "/responses/"):
		rest := strings.TrimPrefix(path, "/responses/")
		u.lookups = append(u.lookups, req.Method+" "+rest+"?"+req.URL.RawQuery)
		id, suffix, _ := strings.Cut(rest, "/")
		stored, ok := u.responses[id]
		if !ok {
			return stubJSON(http.StatusNotFound, fmt.Sprintf(`{"error":{"message":"Response with id '%s' not found.","type":"invalid_request_error","param":null,"code":null}}`, id)), nil
		}
		switch {
		case req.Method == http.MethodGet && suffix == "":
			return stubJSON(http.StatusOK, string(stored)), nil
		case req.Method == http.MethodGet && suffix == "input_items":
			items, _ := json.Marshal(u.inputs[id])
			return stubJSON(http.StatusOK, `{"object":"list","data":`+string(items)+`,"has_more":false}`), nil
		case req.Method == http.MethodDelete && suffix == "":
			delete(u.responses, id)
			return stubJSON(http.StatusOK, fmt.Sprintf(`{"id":%q,"object":"response.deleted","deleted":true}`, id)), nil
		}
	}
	return stubJSON(http.StatusNotFound, `{"error":{"message":"no route"}}`), nil
}

func (u *openAIPlatformStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func (u *openAIPlatformStub) record(req *http.Request, body []byte, text string) {
	if match := codexSSECaseMarker.FindStringSubmatch(text); match != nil {
		u.requests[match[1]] = req
		u.bodies[match[1]] = body
	}
}

func (u *openAIPlatformStub) imagesResponse(size string) string {
	if size == "" {
		size = "1024x1024"
	}
	return fmt.Sprintf(`{"created":1767225600,"data":[{"b64_json":%q}],"size":%q,"quality":"low","output_format":"png","usage":{"input_tokens":12,"input_tokens_details":{"text_tokens":12,"image_tokens":0},"output_tokens":272,"output_tokens_details":{"image_tokens":272},"total_tokens":284}}`, apiKeyE2EPNG, size)
}

func (u *openAIPlatformStub) createResponse(req *http.Request, body []byte) *http.Response {
	u.seq++
	id := fmt.Sprintf("resp_api_%d", u.seq)
	input := gjson.GetBytes(body, "input")
	var items []json.RawMessage
	if input.Type == gjson.String {
		item, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("msg_in_%d", u.seq), "type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": input.String()}}})
		items = append(items, item)
	} else {
		for _, item := range input.Array() {
			items = append(items, json.RawMessage(item.Raw))
		}
	}
	u.inputs[id] = items
	u.record(req, body, input.Raw)
	text := "Hello from relay"
	previous := gjson.GetBytes(body, "previous_response_id").String()
	if previous != "" {
		text = "I do not know"
		if _, ok := u.responses[previous]; ok && bytes.Contains(stubInputJSON(u.inputs[previous]), []byte("remember 7")) {
			text = "The number is 7"
		}
	}
	response := fmt.Sprintf(`{"id":%q,"object":"response","created_at":1767225600,"status":"completed","model":%q,"output":[{"id":"msg_%s","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":0},"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":16}}`,
		id, gjson.GetBytes(body, "model").String(), id, text)
	if previous != "" {
		response, _ = sjson.Set(response, "previous_response_id", previous)
	}
	u.responses[id] = []byte(response)
	if !gjson.GetBytes(body, "stream").Bool() {
		return stubJSON(http.StatusOK, response)
	}
	var events strings.Builder
	n := 0
	emit := func(eventType, payload string) {
		fmt.Fprintf(&events, "event: %s\ndata: {\"type\":%q,\"sequence_number\":%d,%s}\n\n", eventType, eventType, n, payload)
		n++
	}
	inProgress, _ := sjson.Set(response, "status", "in_progress")
	inProgress, _ = sjson.SetRaw(inProgress, "output", "[]")
	emit("response.created", `"response":`+inProgress)
	half := len(text) / 2
	emit("response.output_item.added", fmt.Sprintf(`"output_index":0,"item":{"id":"msg_%s","type":"message","status":"in_progress","role":"assistant","content":[]}`, id))
	emit("response.content_part.added", fmt.Sprintf(`"item_id":"msg_%s","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}`, id))
	emit("response.output_text.delta", fmt.Sprintf(`"item_id":"msg_%s","output_index":0,"content_index":0,"delta":%q,"logprobs":[]`, id, text[:half]))
	emit("response.output_text.delta", fmt.Sprintf(`"item_id":"msg_%s","output_index":0,"content_index":0,"delta":%q,"logprobs":[]`, id, text[half:]))
	emit("response.output_text.done", fmt.Sprintf(`"item_id":"msg_%s","output_index":0,"content_index":0,"text":%q,"logprobs":[]`, id, text))
	emit("response.output_item.done", `"output_index":0,"item":`+gjson.Get(response, "output.0").Raw)
	emit("response.completed", `"response":`+response)
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(events.String()))}
}

func stubJSON(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func stubInputJSON(items []json.RawMessage) []byte {
	out, _ := json.Marshal(items)
	return out
}

func parseStubMultipart(contentType string, body []byte) (map[string]string, int) {
	fields := map[string]string{}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return fields, 0
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	files := 0
	for {
		part, err := reader.NextPart()
		if err != nil {
			return fields, files
		}
		data, _ := io.ReadAll(part)
		if part.FileName() != "" {
			files++
			continue
		}
		fields[part.FormName()] = string(data)
	}
}

// newOpenAIAPIKeySDKE2EServer mirrors the handler steps for an API-key
// account: owner binding on create, gateway store first and the upstream
// account second for lookups, and the Images API forwarding.
func newOpenAIAPIKeySDKE2EServer(t *testing.T, svc *OpenAIGatewayService, account *Account) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	const groupID, userID, apiKeyID = int64(0), int64(1), int64(1)
	router := gin.New()
	router.POST("/v1/responses", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		if gjson.GetBytes(body, "previous_response_id").String() != "" {
			continuation, err := svc.ExpandOpenAIResponsesContinuation(c.Request.Context(), groupID, userID, apiKeyID, body)
			require.NoError(t, err)
			require.Nil(t, continuation, "API-key continuations stay upstream")
		} else if pending := svc.NewOpenAIResponsesStatePending(groupID, userID, apiKeyID, body); pending != nil {
			SetOpenAIResponsesStatePending(c, pending)
		}
		SetOpenAIHTTPResponseOwner(c, userID, apiKeyID)
		if _, err := svc.Forward(c.Request.Context(), c, account, body); err != nil && !c.Writer.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": err.Error()}})
		}
	})
	lookup := func(method, suffix string) gin.HandlerFunc {
		return func(c *gin.Context) {
			id := c.Param("id")
			if method == http.MethodGet && suffix == "" {
				if _, found, _ := svc.GetOpenAIResponsesStoredResponse(c.Request.Context(), groupID, id, userID, apiKeyID); found {
					t.Errorf("API-key response %s must not be served from the gateway store", id)
				}
			}
			handled, err := svc.ForwardOpenAIResponsesLookup(c.Request.Context(), c, groupID, id, userID, apiKeyID, method, suffix)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "upstream_error", "message": err.Error()}})
				return
			}
			if !handled {
				c.JSON(http.StatusNotFound, gin.H{"error": gin.H{"type": "invalid_request_error", "message": "not found"}})
			}
		}
	}
	router.GET("/v1/responses/:id", lookup(http.MethodGet, ""))
	router.DELETE("/v1/responses/:id", lookup(http.MethodDelete, ""))
	router.GET("/v1/responses/:id/input_items", lookup(http.MethodGet, "/input_items"))
	images := func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		parsed, err := svc.ParseOpenAIImagesRequest(c, body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "message": err.Error()}})
			return
		}
		if _, err := svc.ForwardImages(c.Request.Context(), c, account, body, parsed, ""); err != nil && !c.Writer.Written() {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": err.Error()}})
		}
	}
	router.POST("/v1/images/generations", images)
	router.POST("/v1/images/edits", images)
	return httptest.NewServer(router)
}

// TestOpenAINodeSDKAgainstAPIKeyAccount runs the official OpenAI Node SDK
// against an OpenAI API-key (relay) account: Images generate/edit with GPT
// Image 2.6 complexity, and Responses create, retrieve, input_items,
// previous_response_id, delete and streaming. Point OPENAI_NODE_SDK_DIR at an
// installed `openai` package to run it.
func TestOpenAINodeSDKAgainstAPIKeyAccount(t *testing.T) {
	sdkDir := os.Getenv("OPENAI_NODE_SDK_DIR")
	if sdkDir == "" {
		t.Skip("set OPENAI_NODE_SDK_DIR to an installed openai npm package")
	}
	upstream := &openAIPlatformStub{responses: map[string][]byte{}, inputs: map[string][]json.RawMessage{}, requests: map[string]*http.Request{}, bodies: map[string][]byte{}}
	account := &Account{
		ID: 778, Name: "sdk-e2e-relay", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 8,
		Credentials: map[string]any{"api_key": "sk-relay", "base_url": "https://relay.example.test/v1"},
		Status:      StatusActive, Schedulable: true,
	}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{OpenAIResponsesStateEnabled: true}},
		httpUpstream: upstream,
		accountRepo:  newStubCredRepo(account),
	}
	server := newOpenAIAPIKeySDKE2EServer(t, svc, account)
	defer server.Close()

	script, err := filepath.Abs(filepath.Join("testdata", "openai_sdk_apikey_e2e.cjs"))
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

	r := result("images_generate_26")
	require.Equal(t, int64(1), r.Get("count").Int())
	require.Equal(t, apiKeyE2EPNG, r.Get("b64").String())
	require.Equal(t, "1024x1024", r.Get("size").String())
	sent := upstream.bodies["img_generate"]
	require.Equal(t, "https://relay.example.test/v1/images/generations", upstream.requests["img_generate"].URL.String())
	for key, want := range map[string]string{"model": "gpt-image-2.6-flare", "size": "1024x1024", "quality": "low", "complexity": "low", "background": "transparent", "output_format": "png"} {
		require.Equal(t, want, gjson.GetBytes(sent, key).String(), key)
	}

	r = result("images_edit_26")
	require.Equal(t, int64(1), r.Get("count").Int())
	require.Equal(t, "1024x1024", r.Get("size").String())
	sent = upstream.bodies["img_edit"]
	require.Equal(t, "https://relay.example.test/v1/images/edits", upstream.requests["img_edit"].URL.String())
	for key, want := range map[string]string{"model": "gpt-image-2.6-sunburst", "size": "1024x1024", "quality": "medium", "complexity": "medium", "_files": "1"} {
		require.Equal(t, want, gjson.GetBytes(sent, key).String(), key)
	}

	r = result("responses_retrieve_delete")
	firstID := r.Get("firstID").String()
	require.Equal(t, firstID, r.Get("fetchedID").String())
	require.Equal(t, "Hello from relay", r.Get("fetchedText").String())
	require.Equal(t, int64(1), r.Get("inputItemCount").Int())
	require.Equal(t, "case=apikey_first remember 7", r.Get("inputItemText").String())
	require.Equal(t, "The number is 7", r.Get("secondText").String(), "previous_response_id is resolved by the upstream")
	require.Equal(t, firstID, r.Get("secondPrev").String())
	require.Equal(t, int64(404), r.Get("afterDeleteStatus").Int())
	require.Equal(t, int64(404), r.Get("foreignStatus").Int(), "a response the gateway never bound is not looked up upstream")
	require.Equal(t, firstID, gjson.GetBytes(upstream.bodies["apikey_second"], "previous_response_id").String())
	upstream.mu.Lock()
	lookups := append([]string(nil), upstream.lookups...)
	upstream.mu.Unlock()
	require.Equal(t, []string{"GET " + firstID + "?", "GET " + firstID + "/input_items?limit=5", "DELETE " + firstID + "?"}, lookups,
		"lookups reach the upstream with the client's query; after the delete the binding is gone")

	r = result("responses_stream_apikey")
	require.Equal(t, "Hello from relay", r.Get("deltas").String())
	require.Equal(t, "Hello from relay", r.Get("outputText").String())
}
