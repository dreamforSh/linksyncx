//go:build unit

package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
)

func TestResolvePerplexityRequestOptions(t *testing.T) {
	account := &Account{Credentials: map[string]any{"perplexity_options": map[string]any{
		"sources": []string{"scholar"}, "language": "de-DE", "timezone": "Europe/Berlin",
		"include_sources": false, "is_incognito": true,
	}}}
	opts, err := resolvePerplexityRequestOptions(account, []byte(`{"sources":["social"],"language":"fr-FR","is_incognito":false,"perplexity":{"sources":["web","scholar","web"],"language":"zh-CN","native_followups":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.Sources, []string{"web", "scholar"}) || opts.Language != "zh-CN" || opts.Timezone != "Europe/Berlin" || opts.Incognito {
		t.Fatalf("bad merged options: %+v", opts)
	}
	if opts.Local.IncludeSources || opts.Local.NativeFollowups || !opts.Local.IncludeSearchProgress || opts.Local.StrictParameters {
		t.Fatalf("bad local options: %+v", opts.Local)
	}
	if opts.Internal.LastBackendUUID != "" || opts.Internal.FrontendContextUUID != "" || len(opts.Internal.Attachments) != 0 {
		t.Fatal("public resolver populated internal options")
	}
}

func TestResolvePerplexityRequestOptionsRejectsInvalidPublicInput(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `{`, `{"perplexity":null}`, `{"perplexity":[]}`,
		`{"perplexity":{"mode":"computer"}}`, `{"mode":"computer"}`,
		`{"perplexity":{"last_backend_uuid":"user-handle"}}`, `{"perplexity":{"frontend_context_uuid":"user-handle"}}`,
		`{"perplexity":{"attachments":["file"]}}`, `{"perplexity":{"model_preference":"expensive"}}`,
		`{"perplexity":{"local":{"strict_parameters":true}}}`, `{"perplexity":{"typo":true}}`,
		`{"sources":[]}`, `{"sources":"web"}`, `{"sources":["news"]}`, `{"sources":[true]}`,
		`{"language":null}`, `{"language":"en_US"}`, `{"language":"not a language"}`, `{"language":17}`,
		`{"timezone":"Not/AZone"}`, `{"timezone":false}`, `{"is_incognito":"true"}`,
		`{"perplexity":{"include_sources":null}}`, `{"perplexity":{"strict_parameters":1}}`,
		`{"perplexity":{"language":"invalid locale","language":"en-US"}}`,
		// Shadowed invalid values must not evade validation.
		`{"sources":["news"],"perplexity":{"sources":["web"]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, err := resolvePerplexityRequestOptions(nil, []byte(body))
			var badRequest *perplexityBadRequestError
			if !errors.As(err, &badRequest) {
				t.Fatalf("error = %v, want client 400 error", err)
			}
		})
	}
}

func TestValidatePerplexityRequestGenericCompatibility(t *testing.T) {
	for _, test := range []struct {
		protocol string
		body     string
		want     []string
	}{
		{"messages", `{"max_tokens":1024,"temperature":0.3}`, []string{"max_tokens", "temperature"}},
		{"chat_completions", `{"max_completion_tokens":256,"top_p":0.9,"presence_penalty":0}`, []string{"max_completion_tokens", "presence_penalty", "top_p"}},
		{"responses", `{"max_output_tokens":256,"reasoning":{"effort":"high"}}`, []string{"max_output_tokens", "reasoning"}},
		{"messages", `{"output_config":{"effort":"high"}}`, []string{"output_config.effort"}},
	} {
		t.Run(test.protocol+test.body, func(t *testing.T) {
			opts := perplexity.DefaultRequestOptions()
			if err := validatePerplexityRequest([]byte(test.body), test.protocol, &opts); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(opts.IgnoredParameters, test.want) {
				t.Fatalf("ignored = %v, want %v", opts.IgnoredParameters, test.want)
			}
			opts.Local.StrictParameters = true
			if err := validatePerplexityRequest([]byte(test.body), test.protocol, &opts); err == nil {
				t.Fatal("strict mode must reject unsupported general parameters")
			}
		})
	}
}

func TestValidatePerplexityRequestRejectsHardSemantics(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"type":"function","function":{"name":"f"}}]}`,
		`{"tools":[{"name":"f","input_schema":{"type":"object"}}]}`,
		`{"tool_choice":"required"}`, `{"tool_choice":{"type":"any"}}`, `{"tool_choice":{"type":"function","name":"f"}}`,
		`{"tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"]}}]}`,
		`{"tools":[{"type":"web_search_preview","search_context_size":"high"}]}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`,
		`{"response_format":{"type":"json_object"}}`, `{"response_format":{"type":"json_schema","json_schema":{}}}`,
		`{"text":{"format":{"type":"json_schema","schema":{}}}}`, `{"output_config":{"format":{"type":"json_schema","schema":{}}}}`,
		`{"stop":["END"]}`, `{"stop_sequences":["END"]}`, `{"n":2}`, `{"best_of":2}`,
		`{"previous_response_id":"resp_unsupported"}`, `{"conversation":"unsupported"}`, `{"background":true}`, `{"echo":true}`,
		`{"web_search_options":{"user_location":{"type":"approximate","country":"US"}}}`,
		`{"input":[{"type":"input_file","file_id":"unsupported"}]}`,
		`{"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","data":"AA=="}}]}]}`,
		`{"thinking":{"type":"enabled","budget_tokens":4096}}`, `{"reasoning":{"budget_tokens":4096}}`,
		`{"modalities":["audio"]}`, `{"audio":{"voice":"alloy"}}`,
		`{"messages":[{"role":"tool","content":"output"}]}`,
		`{"messages":[{"role":"assistant","content":"text","tool_calls":[{"id":"t"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"output"}]}]}`,
		`{"input":[{"type":"function_call_output","call_id":"t","output":"result"}]}`,
		`{"input":[{"type":"custom_tool_call_output","call_id":"t","output":"result"}]}`,
		`{"input":[{"type":"mcp_list_tools","tools":[]}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			opts := perplexity.DefaultRequestOptions()
			err := validatePerplexityRequest([]byte(body), "responses", &opts)
			var badRequest *perplexityBadRequestError
			if !errors.As(err, &badRequest) {
				t.Fatalf("error = %v, want client 400 error", err)
			}
		})
	}
}

func TestValidatePerplexityNativeWebSearch(t *testing.T) {
	for _, body := range []string{
		`{"tools":[{"type":"web_search"}],"tool_choice":"auto"}`,
		`{"tools":[{"type":"web_search_20250305","name":"web_search"}]}`,
		`{"tools":[{"type":"web_search_preview"}]}`,
		`{"web_search_options":{}}`,
	} {
		opts := perplexity.DefaultRequestOptions()
		opts.Sources = []string{"scholar"}
		if err := validatePerplexityRequest([]byte(body), "messages", &opts); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(opts.Sources, []string{"web"}) {
			t.Fatalf("native web search did not map to web source: %v", opts.Sources)
		}
	}
	opts := perplexity.DefaultRequestOptions()
	err := validatePerplexityRequest([]byte(`{"tools":[{"type":"web_search"}],"tool_choice":"none"}`), "responses", &opts)
	if err == nil || !strings.Contains(err.Error(), "cannot disable") {
		t.Fatalf("contradictory tool choice accepted: %v", err)
	}
	if err := validatePerplexityRequest([]byte(`{"tools":[],"tool_choice":"none","response_format":{"type":"text"},"n":1,"stop":[]}`), "chat_completions", &opts); err != nil {
		t.Fatalf("harmless defaults rejected: %v", err)
	}
}

func TestValidatePerplexityRawHistoryContentShapes(t *testing.T) {
	for _, protocol := range []struct {
		name       string
		collection string
	}{
		{"chat_completions", "messages"},
		{"messages", "messages"},
		{"responses", "input"},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			for _, content := range []string{`"plain assistant text"`, `""`, `null`, `[{"type":"text","text":"array text"}]`} {
				opts := perplexity.DefaultRequestOptions()
				body := `{"` + protocol.collection + `":[{"role":"assistant","content":` + content + `},{"role":"user","content":"question"}]}`
				if err := validatePerplexityRequest([]byte(body), protocol.name, &opts); err != nil {
					t.Errorf("valid content %s rejected: %v", content, err)
				}
			}
			for _, content := range []string{`{}`, `{"type":"text","text":"not an array"}`, `42`, `true`} {
				opts := perplexity.DefaultRequestOptions()
				body := `{"` + protocol.collection + `":[{"role":"user","content":` + content + `}]}`
				err := validatePerplexityRequest([]byte(body), protocol.name, &opts)
				var badRequest *perplexityBadRequestError
				if !errors.As(err, &badRequest) {
					t.Errorf("invalid content %s error = %v, want client 400 error", content, err)
				}
			}
		})
	}
}

func TestValidatePerplexityRejectsUnmappedSearchControls(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{"search_recency_filter", `"month"`},
		{"search_domain_filter", `["example.com"]`},
		{"search_language_filter", `["en"]`},
		{"search_after_date_filter", `"10/01/2026"`},
		{"search_before_date_filter", `"10/16/2026"`},
		{"last_updated_after_filter", `"10/01/2026"`},
		{"last_updated_before_filter", `"10/16/2026"`},
		{"search_mode", `"academic"`},
		{"search_context_size", `"high"`},
		{"return_images", `true`},
		{"return_related_questions", `true`},
		{"return_images", `"false"`},
		{"return_related_questions", `1`},
	} {
		t.Run(test.key+test.value, func(t *testing.T) {
			for _, protocol := range []string{"chat_completions", "responses", "messages"} {
				opts := perplexity.DefaultRequestOptions()
				body := `{"` + test.key + `":` + test.value + `}`
				err := validatePerplexityRequest([]byte(body), protocol, &opts)
				var badRequest *perplexityBadRequestError
				if !errors.As(err, &badRequest) || !strings.Contains(err.Error(), test.key) {
					t.Errorf("%s error = %v, want explicit client 400 error for %s", protocol, err, test.key)
				}
			}
		})
	}
}

func TestValidatePerplexityAcceptsEmptySearchControlDefaults(t *testing.T) {
	for _, key := range []string{
		"search_recency_filter", "search_domain_filter", "search_language_filter",
		"search_after_date_filter", "search_before_date_filter",
		"last_updated_after_filter", "last_updated_before_filter",
		"search_mode", "search_context_size", "return_images", "return_related_questions",
	} {
		t.Run(key, func(t *testing.T) {
			values := []string{`null`, `""`, `[]`}
			if key == "return_images" || key == "return_related_questions" {
				values = []string{`null`, `false`}
			}
			for _, value := range values {
				opts := perplexity.DefaultRequestOptions()
				body := `{"` + key + `":` + value + `,"max_tokens":16,"temperature":0.1}`
				if err := validatePerplexityRequest([]byte(body), "messages", &opts); err != nil {
					t.Errorf("empty %s=%s rejected: %v", key, value, err)
				}
				if !reflect.DeepEqual(opts.IgnoredParameters, []string{"max_tokens", "temperature"}) {
					t.Errorf("ignored = %v, want only the existing generic compatibility parameters", opts.IgnoredParameters)
				}
			}
		})
	}
}

func TestResolvePerplexityRequestOptionsPromptAndCap(t *testing.T) {
	// 默认：剥离 system（KeepSystemPrompt=false）+ 90k 折叠上限。
	opts, err := resolvePerplexityRequestOptions(nil, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if opts.Local.KeepSystemPrompt {
		t.Fatal("default must strip system prompt (search pollution)")
	}
	if opts.Local.MaxQueryChars != perplexity.DefaultMaxQueryChars {
		t.Fatalf("default MaxQueryChars = %d", opts.Local.MaxQueryChars)
	}

	// 请求级覆盖。
	opts, err = resolvePerplexityRequestOptions(nil, []byte(`{"perplexity":{"keep_system_prompt":true,"max_query_chars":5000}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Local.KeepSystemPrompt || opts.Local.MaxQueryChars != 5000 {
		t.Fatalf("bad overrides: %+v", opts.Local)
	}

	// 账号级覆盖（优先级低于请求级）。
	account := &Account{Credentials: map[string]any{"perplexity_options": map[string]any{"keep_system_prompt": true, "max_query_chars": 8000}}}
	opts, err = resolvePerplexityRequestOptions(account, []byte(`{"perplexity":{"max_query_chars":60000}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Local.KeepSystemPrompt || opts.Local.MaxQueryChars != 60000 {
		t.Fatalf("bad precedence: %+v", opts.Local)
	}

	// 非法值：低于下限 / 超过硬上限 / 类型错误。
	for _, body := range []string{
		`{"perplexity":{"max_query_chars":500}}`,
		`{"perplexity":{"max_query_chars":96001}}`,
		`{"perplexity":{"max_query_chars":"90000"}}`,
		`{"perplexity":{"keep_system_prompt":"yes"}}`,
	} {
		if _, err := resolvePerplexityRequestOptions(nil, []byte(body)); err == nil {
			t.Fatalf("body %s should be rejected", body)
		}
	}
}
