//go:build unit

package service

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 快速解码的字段表必须覆盖 ResponsesStreamEvent 的全部 JSON 字段且类型一致，
// 否则新增字段时快速路径会悄悄偏离 json.Unmarshal 的判定。
func TestResponsesStreamEventFieldTableMatchesStruct(t *testing.T) {
	typ := reflect.TypeOf(apicompat.ResponsesStreamEvent{})
	tags := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		require.NotEmpty(t, tag, "field %s needs an explicit json name", field.Name)
		kind, _, known := responsesStreamEventField(tag)
		require.True(t, known, "field %q missing from the fast-path table", tag)
		switch field.Type.Kind() {
		case reflect.String:
			require.Equal(t, gjson.String, kind, tag)
		case reflect.Int:
			require.Equal(t, gjson.Number, kind, tag)
		case reflect.Pointer:
			require.Equal(t, gjson.JSON, kind, tag)
		default:
			t.Fatalf("field %q has kind %s; teach decodeResponsesStreamDeltaEvent about it", tag, field.Type.Kind())
		}
		tags = append(tags, tag)
	}
	require.ElementsMatch(t, tags, responsesStreamEventFieldNames[:])
	for i, name := range responsesStreamEventFieldNames {
		_, bit, known := responsesStreamEventField(name)
		require.True(t, known, name)
		require.Equal(t, i, bit, "bit of %q must equal its index", name)
	}
}

// jsonUnicodeEscape 在运行时拼出 JSON 的 \uXXXX 转义，避免源码字面量被工具链提前解码。
func jsonUnicodeEscape(hex ...string) string {
	var sb strings.Builder
	for _, h := range hex {
		sb.WriteString("\\" + "u" + h)
	}
	return sb.String()
}

func TestDecodeResponsesStreamDeltaEvent_FastPathAndFallback(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		wantFast bool
	}{
		{"文本 delta 带 logprobs / obfuscation", `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi \"there\"\n","logprobs":[],"obfuscation":"abc"}`, true},
		{"参数 delta", `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":2,"delta":"{\"cmd\":"}`, true},
		{"推理摘要 delta", `{"type":"response.reasoning_summary_text.delta","summary_index":0,"delta":"plan"}`, true},
		{"普通 unicode 转义", `{"type":"response.output_text.delta","delta":"` + jsonUnicodeEscape("00e9") + `"}`, true},
		{"非 ASCII 原文", `{"type":"response.output_text.delta","delta":"中文😀"}`, true},
		{"嵌套字段为 null", `{"type":"response.output_text.delta","item":null,"delta":"x"}`, true},
		{"output_item.added 走完整解码", `{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"n"}}`, false},
		{"键名大小写变体", `{"type":"response.output_text.delta","Delta":"x"}`, false},
		{"重复键", `{"type":"response.output_text.delta","delta":"a","delta":"b"}`, false},
		{"整数字段是字符串", `{"type":"response.function_call_arguments.delta","output_index":"1","delta":"x"}`, false},
		{"整数字段带小数", `{"type":"response.function_call_arguments.delta","output_index":1.5,"delta":"x"}`, false},
		{"合法代理对转义", `{"type":"response.output_text.delta","delta":"` + jsonUnicodeEscape("d83d", "de00") + `"}`, false},
		{"孤立代理转义", `{"type":"response.output_text.delta","delta":"a` + jsonUnicodeEscape("d800") + `b"}`, false},
		{"转义的反斜杠后跟 u 不是代理转义", `{"type":"response.output_text.delta","delta":"\\ud800"}`, true},
		{"非法 UTF-8", "{\"type\":\"response.output_text.delta\",\"delta\":\"\xff\"}", false},
		{"嵌套对象", `{"type":"response.output_text.delta","part":{"type":"output_text"},"delta":"x"}`, false},
		{"非法 JSON", `{"type":"response.output_text.delta","delta":`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, fast := decodeResponsesStreamDeltaEvent([]byte(tt.payload))
			require.Equal(t, tt.wantFast, fast)
			if fast {
				var want apicompat.ResponsesStreamEvent
				require.NoError(t, json.Unmarshal([]byte(tt.payload), &want))
				require.Equal(t, want.Type, got.Type)
				require.Equal(t, want.Delta, got.Delta)
				require.Equal(t, want.OutputIndex, got.OutputIndex)
			}
		})
	}
}

func TestAccumulateResponsesStreamEvent_MatchesFullDecode(t *testing.T) {
	payloads := []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"shell"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"cmd\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"ls\"}"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"think"}`,
		`{"type":"response.output_text.delta","delta":"hello "}`,
		`{"type":"response.output_text.delta","Delta":"case variant"}`,
		`{"type":"response.output_text.delta","delta":"` + jsonUnicodeEscape("d83d", "de00") + `"}`,
		`{"type":"response.output_text.delta","output_index":"bad","delta":"dropped by both"}`,
	}
	legacy := apicompat.NewBufferedResponseAccumulator()
	optimized := apicompat.NewBufferedResponseAccumulator()
	for _, payload := range payloads {
		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err == nil {
			legacy.ProcessEvent(&event)
		}
		accumulateResponsesStreamEvent(optimized, []byte(payload))
	}
	require.Equal(t, legacy.BuildOutput(), optimized.BuildOutput())
	require.True(t, optimized.HasContent())
}
