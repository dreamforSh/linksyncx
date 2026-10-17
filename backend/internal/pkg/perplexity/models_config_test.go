package perplexity

import (
	"reflect"
	"testing"
)

func TestParseModelsConfigIDs_V2Picker(t *testing.T) {
	body := []byte(`{
		"models": {
			"gpt_future": {"label":"GPT Future","mode":"search","context_window":64000},
			"gpt_future_thinking": {"label":"GPT Future Thinking","mode":"search"},
			"claudefuturesonnet": {"label":"Sonnet Future","mode":"search"},
			"claudefutureopus": {"label":"Opus Future","mode":"search"},
			"old_hidden_model": {"label":"Old hidden model","mode":"search"},
			"browser_sonnet": {"label":"Browser agent","mode":"browser_agent"},
			"computer_gpt": {"label":"Computer model","mode":"asi"},
			"pplx_alpha": {"label":"Deep research","mode":"research"}
		},
		"search_config": [
			{"label":"GPT Future","subscription_tier":"pro","non_reasoning_model":"gpt_future","reasoning_model":"gpt_future_thinking"},
			{"label":"Sonnet Future","subscription_tier":"pro","non_reasoning_model":"claudefuturesonnet","reasoning_model":null},
			{"label":"Opus Future","subscription_tier":"max","non_reasoning_model":"claudefutureopus"},
			{"non_reasoning_model":"gpt_future","reasoning_model":"missing_model"},
			{"non_reasoning_model":"browser_sonnet"}
		],
		"computer_config": [{"model":"computer_gpt","fast_model":null}],
		"default_models": {"search":"gpt_future_thinking","research":"pplx_alpha"}
	}`)
	want := []string{"gpt_future", "gpt_future_thinking", "claudefuturesonnet", "claudefutureopus", "pplx_alpha"}
	if got := ParseModelsConfigIDs(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseModelsConfigIDs() = %v, want %v", got, want)
	}
}

func TestParseModelsConfigIDs_V2DictionaryWithoutPicker(t *testing.T) {
	body := []byte(`{"models":{"sonnet_future":{"label":"Sonnet"},"gpt_future":{"label":"GPT"},"broken":null,"empty":false}}`)
	want := []string{"gpt_future", "sonnet_future"}
	if got := ParseModelsConfigIDs(body); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseModelsConfigIDs() = %v, want %v", got, want)
	}
}

func TestParseModelsConfigIDs_V2EmptyPickerDoesNotExposeOldCatalog(t *testing.T) {
	for _, body := range []string{
		`{"models":{"old_model":{"label":"Old"}},"search_config":[]}`,
		`{"models":{"old_model":{"label":"Old"}},"search_config":[{"non_reasoning_model":"missing"}]}`,
		`{"models":{}}`,
		`{"models":"invalid"}`,
	} {
		if got := ParseModelsConfigIDs([]byte(body)); len(got) != 0 {
			t.Errorf("ParseModelsConfigIDs(%s) = %v, want no models", body, got)
		}
	}
}
