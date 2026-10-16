package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/tidwall/gjson"
)

// resolvePerplexityRequestOptions must run on the original ingress body, before
// typed protocol adapters discard extensions. Precedence is defaults < account
// perplexity_options < top-level aliases < body.perplexity. Each layer is
// independently validated: an invalid shadowed value is still a client error.
func resolvePerplexityRequestOptions(account *Account, body []byte) (perplexity.RequestOptions, error) {
	opts := perplexity.DefaultRequestOptions()
	if account != nil {
		if stored, ok := account.Credentials["perplexity_options"]; ok {
			raw, err := json.Marshal(stored)
			if err != nil {
				return opts, perplexityOptionError("invalid account perplexity_options")
			}
			if err := applyPerplexityPublicOptions(&opts, gjson.ParseBytes(raw)); err != nil {
				return opts, err
			}
		}
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return opts, perplexityOptionError("request body must be a JSON object")
	}
	root := gjson.ParseBytes(body)
	aliases := make(map[string]json.RawMessage)
	for _, key := range []string{"sources", "language", "timezone", "is_incognito"} {
		value := root.Get(key)
		// Raw distinguishes explicit null (invalid) from an omitted option.
		if value.Raw != "" {
			aliases[key] = json.RawMessage(value.Raw)
		}
	}
	if len(aliases) > 0 {
		raw, _ := json.Marshal(aliases)
		if err := applyPerplexityPublicOptions(&opts, gjson.ParseBytes(raw)); err != nil {
			return opts, err
		}
	}
	for _, key := range []string{"mode", "model_preference", "last_backend_uuid", "frontend_context_uuid", "attachments"} {
		if root.Get(key).Raw != "" {
			return opts, perplexityOptionError("%s is not a public Perplexity request option", key)
		}
	}
	if extension := root.Get("perplexity"); extension.Raw != "" {
		if err := applyPerplexityPublicOptions(&opts, extension); err != nil {
			return opts, err
		}
	}
	return opts, nil
}

func perplexityOptionError(format string, args ...any) error {
	return &perplexityBadRequestError{msg: fmt.Sprintf(format, args...)}
}

func applyPerplexityPublicOptions(opts *perplexity.RequestOptions, extension gjson.Result) error {
	if !extension.IsObject() {
		return perplexityOptionError("perplexity options must be a JSON object")
	}
	var optionErr error
	seen := make(map[string]bool)
	extension.ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if seen[name] {
			optionErr = perplexityOptionError("duplicate perplexity option %q", name)
			return false
		}
		seen[name] = true
		switch name {
		case "sources":
			if !value.IsArray() {
				optionErr = perplexityOptionError("perplexity.sources must be a non-empty array of web, scholar, or social")
				break
			}
			sources := make([]string, 0, len(value.Array()))
			for _, source := range value.Array() {
				if source.Type != gjson.String {
					optionErr = perplexityOptionError("perplexity.sources must contain only strings")
					break
				}
				sources = append(sources, source.String())
			}
			opts.Sources = sources
		case "language", "timezone":
			if value.Type != gjson.String {
				optionErr = perplexityOptionError("perplexity.%s must be a string", name)
				break
			}
			if name == "language" {
				opts.Language = value.String()
			} else {
				opts.Timezone = value.String()
			}
		case "is_incognito", "include_sources", "include_search_progress", "native_followups", "strict_parameters", "keep_system_prompt":
			if value.Type != gjson.True && value.Type != gjson.False {
				optionErr = perplexityOptionError("perplexity.%s must be a boolean", name)
				break
			}
			switch name {
			case "is_incognito":
				opts.Incognito = value.Bool()
			case "include_sources":
				opts.Local.IncludeSources = value.Bool()
			case "include_search_progress":
				opts.Local.IncludeSearchProgress = value.Bool()
			case "native_followups":
				opts.Local.NativeFollowups = value.Bool()
			case "strict_parameters":
				opts.Local.StrictParameters = value.Bool()
			case "keep_system_prompt":
				opts.Local.KeepSystemPrompt = value.Bool()
			}
		case "max_query_chars":
			if value.Type != gjson.Number || value.Float() != float64(value.Int()) {
				optionErr = perplexityOptionError("perplexity.max_query_chars must be an integer")
				break
			}
			opts.Local.MaxQueryChars = int(value.Int())
		default:
			optionErr = perplexityOptionError("unsupported perplexity option %q", name)
		}
		return optionErr == nil
	})
	if optionErr != nil {
		return optionErr
	}
	if err := opts.Normalize(); err != nil {
		return perplexityOptionError("%s", err)
	}
	return nil
}

// validatePerplexityRequest rejects hard semantics that a search-only upstream
// cannot honor. Common sampling/token limits are recorded for the response's
// X-Perplexity-Ignored-Parameters header instead of breaking ordinary clients
// (notably the mandatory max_tokens field of Anthropic Messages).
func validatePerplexityRequest(body []byte, protocol string, opts *perplexity.RequestOptions) error {
	if opts == nil {
		return perplexityOptionError("missing Perplexity request options")
	}
	switch protocol {
	case "chat_completions", "responses", "messages", "anthropic":
	default:
		return perplexityOptionError("unsupported Perplexity ingress protocol %q", protocol)
	}
	if !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return perplexityOptionError("request body must be a JSON object")
	}
	root := gjson.ParseBytes(body)
	// Sonar API search constraints are not equivalent to the web Ask endpoint's
	// sources/language settings. Never silently broaden a constrained search.
	for _, path := range []string{
		"search_recency_filter", "search_domain_filter", "search_language_filter",
		"search_after_date_filter", "search_before_date_filter",
		"last_updated_after_filter", "last_updated_before_filter",
		"search_mode", "search_context_size",
	} {
		value := root.Get(path)
		if value.Raw == "" || value.Type == gjson.Null || (value.Type == gjson.String && value.String() == "") || (value.IsArray() && len(value.Array()) == 0) {
			continue
		}
		return perplexityOptionError("%s cannot be applied by the Perplexity web Ask endpoint", path)
	}
	for _, path := range []string{"return_images", "return_related_questions"} {
		value := root.Get(path)
		if value.Raw == "" || value.Type == gjson.Null || value.Type == gjson.False {
			continue
		}
		return perplexityOptionError("%s is not supported by the Perplexity web Ask endpoint; omit it or set false", path)
	}
	for _, path := range []string{"response_format", "text.format", "output_config.format"} {
		format := root.Get(path)
		if format.Raw != "" && format.Type != gjson.Null {
			if !format.IsObject() || format.Get("type").String() != "text" || len(format.Map()) != 1 {
				return perplexityOptionError("%s is not supported by Perplexity; only plain text output is available", path)
			}
		}
	}
	for _, path := range []string{"json_schema", "schema", "response_schema"} {
		if root.Get(path).Raw != "" && root.Get(path).Type != gjson.Null {
			return perplexityOptionError("%s is not supported by Perplexity", path)
		}
	}
	for _, path := range []string{"stop", "stop_sequences"} {
		stop := root.Get(path)
		if stop.Raw == "" || stop.Type == gjson.Null || (stop.Type == gjson.String && stop.String() == "") || (stop.IsArray() && len(stop.Array()) == 0) {
			continue
		}
		return perplexityOptionError("non-empty %s is not supported by Perplexity", path)
	}
	if n := root.Get("n"); n.Raw != "" && n.Type != gjson.Null && (n.Type != gjson.Number || n.Float() != 1) {
		return perplexityOptionError("Perplexity supports only n=1")
	}
	if bestOf := root.Get("best_of"); bestOf.Exists() && (bestOf.Type != gjson.Number || bestOf.Float() != 1) {
		return perplexityOptionError("Perplexity supports only best_of=1")
	}
	for _, path := range []string{"previous_response_id", "conversation", "suffix"} {
		if value := root.Get(path); value.Exists() && (value.Type != gjson.String || value.String() != "") {
			return perplexityOptionError("%s is not supported by Perplexity; send explicit text history instead", path)
		}
	}
	if root.Get("background").Bool() || root.Get("echo").Bool() {
		return perplexityOptionError("Perplexity does not support background or echo responses")
	}
	for _, path := range []string{"thinking.budget_tokens", "reasoning.budget_tokens", "reasoning.max_tokens", "reasoning.max_output_tokens"} {
		if value := root.Get(path); value.Raw != "" && value.Type != gjson.Null {
			return perplexityOptionError("%s cannot be applied to the Perplexity source model", path)
		}
	}
	for _, path := range []string{"audio", "prediction"} {
		if value := root.Get(path); value.Raw != "" && value.Type != gjson.Null {
			return perplexityOptionError("%s is not supported by Perplexity", path)
		}
	}
	if modalities := root.Get("modalities"); modalities.Exists() {
		if !modalities.IsArray() {
			return perplexityOptionError("Perplexity supports only text output modalities")
		}
		for _, modality := range modalities.Array() {
			if modality.Type != gjson.String || modality.String() != "text" {
				return perplexityOptionError("Perplexity supports only text output modalities")
			}
		}
	}
	if err := validatePerplexityTools(root, opts); err != nil {
		return err
	}
	if err := validatePerplexityRawHistory(root); err != nil {
		return err
	}

	ignored := append([]string(nil), opts.IgnoredParameters...)
	thinkingIgnored, err := parsePerplexityThinkingIntent(root, opts)
	if err != nil {
		return err
	}
	ignored = append(ignored, thinkingIgnored...)
	for _, path := range []string{
		"max_tokens", "max_completion_tokens", "max_output_tokens", "temperature", "top_p", "top_k",
		"presence_penalty", "frequency_penalty", "seed", "logprobs", "top_logprobs", "logit_bias", "service_tier",
	} {
		if value := root.Get(path); value.Raw != "" && value.Type != gjson.Null {
			ignored = append(ignored, path)
		}
	}
	sort.Strings(ignored)
	opts.IgnoredParameters = nil
	for _, name := range ignored {
		if len(opts.IgnoredParameters) == 0 || opts.IgnoredParameters[len(opts.IgnoredParameters)-1] != name {
			opts.IgnoredParameters = append(opts.IgnoredParameters, name)
		}
	}
	if opts.Local.StrictParameters && len(opts.IgnoredParameters) > 0 {
		return perplexityOptionError("Perplexity cannot apply parameters in strict mode: %s", strings.Join(opts.IgnoredParameters, ", "))
	}
	return nil
}

func validatePerplexityTools(root gjson.Result, opts *perplexity.RequestOptions) error {
	if functions := root.Get("functions"); functions.Exists() && (!functions.IsArray() || len(functions.Array()) > 0) {
		return perplexityOptionError("Perplexity does not support client function tools")
	}
	if choice := root.Get("function_call"); choice.Exists() && (choice.Type != gjson.String || (choice.String() != "none" && choice.String() != "auto")) {
		return perplexityOptionError("Perplexity does not support required or named function calls")
	}
	webSearch := false
	if search := root.Get("web_search_options"); search.Exists() {
		if !search.IsObject() || len(search.Map()) != 0 {
			return perplexityOptionError("configured web_search_options cannot be mapped to Perplexity search settings")
		}
		webSearch = true
	}
	if tools := root.Get("tools"); tools.Exists() {
		if !tools.IsArray() {
			return perplexityOptionError("tools must be an array")
		}
		for _, tool := range tools.Array() {
			if !tool.IsObject() {
				return perplexityOptionError("Perplexity supports only native web_search tools")
			}
			switch tool.Get("type").String() {
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_20250305", "web_search_20260209":
			default:
				return perplexityOptionError("Perplexity does not support client function tools; only native web_search can be mapped")
			}
			for key, value := range tool.Map() {
				if key == "type" || (key == "name" && value.Type == gjson.String && value.String() == "web_search") {
					continue
				}
				return perplexityOptionError("web_search.%s cannot be mapped to Perplexity search settings", key)
			}
			webSearch = true
		}
	}
	if choice := root.Get("tool_choice"); choice.Exists() {
		kind := choice.String()
		if choice.IsObject() {
			kind = choice.Get("type").String()
			if len(choice.Map()) != 1 {
				return perplexityOptionError("Perplexity does not support named or configured tool_choice")
			}
		} else if choice.Type != gjson.String {
			return perplexityOptionError("invalid tool_choice")
		}
		if kind != "auto" && kind != "none" {
			return perplexityOptionError("Perplexity does not support required or named tool_choice")
		}
		if kind == "none" && webSearch {
			return perplexityOptionError("tool_choice=none cannot disable Perplexity native web_search")
		}
	}
	if webSearch {
		// There is no domain/location/context-size mapping. A bare native web
		// search declaration maps exactly to the web source, not a fake function.
		opts.Sources = []string{"web"}
	}
	return nil
}

func validatePerplexityRawHistory(root gjson.Result) error {
	for _, collection := range []string{"messages", "input"} {
		items := root.Get(collection)
		if !items.IsArray() {
			continue
		}
		for _, item := range items.Array() {
			if err := validatePerplexityRawImage(item); err != nil {
				return err
			}
			kind := item.Get("type").String()
			if strings.HasSuffix(kind, "_call") || strings.HasSuffix(kind, "_call_output") || strings.HasPrefix(kind, "mcp_") {
				return perplexityOptionError("Perplexity does not support client tool call or tool result history")
			}
			switch kind {
			case "tool_use", "tool_result":
				return perplexityOptionError("Perplexity does not support client tool call or tool result history")
			case "input_file", "file", "input_audio", "audio", "video", "document":
				return perplexityOptionError("Perplexity does not support file, audio, or document input")
			}
			if role := strings.ToLower(strings.TrimSpace(item.Get("role").String())); role == "tool" || role == "function" {
				return perplexityOptionError("Perplexity does not support client tool call or tool result history")
			}
			if calls := item.Get("tool_calls"); calls.Exists() && (!calls.IsArray() || len(calls.Array()) > 0) {
				return perplexityOptionError("Perplexity does not support client tool call or tool result history")
			}
			if item.Get("function_call").Exists() || item.Get("tool_call_id").Exists() {
				return perplexityOptionError("Perplexity does not support client tool call or tool result history")
			}
			content := item.Get("content")
			if content.Type == gjson.String || content.Type == gjson.Null {
				continue
			}
			if !content.IsArray() {
				return perplexityOptionError("Perplexity message content must be a string or an array")
			}
			for _, part := range content.Array() {
				if err := validatePerplexityRawImage(part); err != nil {
					return err
				}
				switch part.Get("type").String() {
				case "tool_use", "tool_result", "function_call", "function_call_output":
					return perplexityOptionError("Perplexity does not support client tool call or tool result history")
				case "text", "input_text", "output_text", "image", "image_url", "input_image", "thinking", "redacted_thinking":
					// Image validation/upload happens after all raw capability checks.
				default:
					return perplexityOptionError("Perplexity does not support message content type %q", part.Get("type").String())
				}
			}
		}
	}
	return nil
}
