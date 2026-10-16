package perplexity

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildAskBodyWithOptions(t *testing.T) {
	opts := DefaultRequestOptions()
	opts.Sources = []string{"scholar", "social", "scholar"}
	opts.Language = "zh-CN"
	opts.Timezone = "Asia/Shanghai"
	opts.Incognito = true
	opts.Internal = InternalOptions{
		LastBackendUUID:     "trusted-backend",
		FrontendContextUUID: "trusted-context",
		Attachments:         []string{"trusted-upload"},
	}
	body, err := BuildAskBodyWithOptions("question", "pplx_pro", ModeCopilot, opts)
	if err != nil {
		t.Fatal(err)
	}
	params := gjson.GetBytes(body, "params")
	for key, want := range map[string]string{
		"language": "zh-CN", "timezone": "Asia/Shanghai", "mode": ModeCopilot,
		"model_preference": "pplx_pro", "last_backend_uuid": "trusted-backend",
		"frontend_context_uuid": "trusted-context", "attachments.0": "trusted-upload",
	} {
		if got := params.Get(key).String(); got != want {
			t.Errorf("params.%s = %q, want %q", key, got, want)
		}
	}
	if !params.Get("is_incognito").Bool() || params.Get("sources.#").Int() != 2 {
		t.Fatalf("bad normalized params: %s", params.Raw)
	}
	for _, key := range []string{"include_sources", "include_search_progress", "native_followups", "strict_parameters", "IgnoredParameters", "Local", "Internal"} {
		if params.Get(key).Exists() {
			t.Errorf("local option %s leaked upstream", key)
		}
	}
	if !reflect.DeepEqual(opts.Sources, []string{"scholar", "social", "scholar"}) {
		t.Fatal("building a body mutated caller-owned source slice")
	}
}

func TestRequestOptionsDefaultsAndValidation(t *testing.T) {
	defaults := DefaultRequestOptions()
	if !defaults.Local.IncludeSources || !defaults.Local.IncludeSearchProgress || !defaults.Local.NativeFollowups || defaults.Local.StrictParameters {
		t.Fatalf("unexpected local defaults: %+v", defaults.Local)
	}
	for _, test := range []struct {
		name   string
		mutate func(*RequestOptions)
	}{
		{"empty sources", func(o *RequestOptions) { o.Sources = nil }},
		{"unknown source", func(o *RequestOptions) { o.Sources = []string{"news"} }},
		{"empty source", func(o *RequestOptions) { o.Sources = []string{""} }},
		{"bad locale", func(o *RequestOptions) { o.Language = "not a locale!" }},
		{"underscore locale", func(o *RequestOptions) { o.Language = "zh_CN" }},
		{"empty locale", func(o *RequestOptions) { o.Language = "" }},
		{"bad timezone", func(o *RequestOptions) { o.Timezone = "Invalid/Somewhere" }},
		{"local timezone", func(o *RequestOptions) { o.Timezone = "Local" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := DefaultRequestOptions()
			test.mutate(&opts)
			if _, err := BuildAskBodyWithOptions("q", "turbo", "", opts); err == nil {
				t.Fatal("expected invalid options to be rejected")
			}
		})
	}
}

func TestFoldRejectsUnprocessedInputs(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages string
		want     error
	}{
		{"history image", `[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/a.png"}}]},{"role":"user","content":"q"}]`, ErrUnsupportedImageInput},
		{"system image", `[{"role":"system","content":[{"type":"image","source":{}}]},{"role":"user","content":"q"}]`, ErrUnsupportedImageInput},
		{"empty text plus image", `[{"role":"user","content":[{"type":"text","text":""},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]}]`, ErrUnsupportedImageInput},
		{"text plus call", `[{"role":"assistant","content":"calling","tool_calls":[{"id":"t"}]},{"role":"user","content":"q"}]`, ErrUnsupportedToolHistory},
		{"tool output", `[{"role":"tool","content":"result"},{"role":"user","content":"q"}]`, ErrUnsupportedToolHistory},
		{"legacy function", `[{"role":"assistant","function_call":{"name":"f"}},{"role":"user","content":"q"}]`, ErrUnsupportedToolHistory},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := FoldMessagesToQuery(gjson.Parse(test.messages))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	query, err := FoldMessagesToQuery(gjson.Parse(`[{"role":"user","content":[{"type":"text","text":"[Image 1 uploaded] Describe it"}]}]`))
	if err != nil || query != "[Image 1 uploaded] Describe it" {
		t.Fatalf("trusted upload placeholder failed: %q, %v", query, err)
	}
}
