package perplexity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var weatherSpec = ToolSpec{Name: "get_weather", Description: "Get weather", Parameters: `{"type":"object"}`}

func TestParseToolSpecs(t *testing.T) {
	specs, err := ParseToolSpecs(gjson.Parse(`[
		{"type":"web_search"},
		{"type":"function","function":{"name":"get_weather","description":"Get\n current   weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}},
		{"type":"function","function":{"name":"no_params"}}
	]`))
	require.NoError(t, err)
	require.Len(t, specs, 2)
	require.Equal(t, "get_weather", specs[0].Name)
	require.Equal(t, "Get current weather", specs[0].Description)
	require.Equal(t, `{"type":"object","properties":{"city":{"type":"string"}}}`, specs[0].Parameters)
	require.Equal(t, defaultToolParameters, specs[1].Parameters)

	for _, raw := range []string{
		`{"type":"function"}`,
		`[{"type":"function","function":{"name":"pplx_search"}}]`,
		`[{"type":"function","function":{"name":"bad name"}}]`,
		`[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"a"}}]`,
		`[{"type":"code_interpreter"}]`,
		`[{"type":"function","function":{"name":"a","parameters":"x"}}]`,
	} {
		_, err := ParseToolSpecs(gjson.Parse(raw))
		require.Error(t, err, raw)
	}
}

func TestParseToolChoice(t *testing.T) {
	specs := []ToolSpec{weatherSpec}
	cases := []struct {
		raw  string
		want ToolChoice
	}{
		{"", ToolChoice{Mode: ToolChoiceAuto}},
		{`"auto"`, ToolChoice{Mode: ToolChoiceAuto}},
		{`"none"`, ToolChoice{Mode: ToolChoiceNone}},
		{`"required"`, ToolChoice{Mode: ToolChoiceRequired}},
		{`{"type":"any"}`, ToolChoice{Mode: ToolChoiceRequired}},
		{`{"type":"function","function":{"name":"get_weather"}}`, ToolChoice{Mode: ToolChoiceFunction, Name: "get_weather"}},
		{`{"type":"tool","name":"get_weather"}`, ToolChoice{Mode: ToolChoiceFunction, Name: "get_weather"}},
	}
	for _, tc := range cases {
		got, err := ParseToolChoice(gjson.Parse(tc.raw), specs)
		require.NoError(t, err, tc.raw)
		require.Equal(t, tc.want, got, tc.raw)
	}
	_, err := ParseToolChoice(gjson.Parse(`{"type":"function","function":{"name":"other"}}`), specs)
	require.Error(t, err)
	_, err = ParseToolChoice(gjson.Parse(`"required"`), nil)
	require.Error(t, err)
}

func TestToolChoiceSatisfied(t *testing.T) {
	call := ToolCall{Name: "get_weather"}
	require.True(t, ToolChoice{Mode: ToolChoiceAuto}.Satisfied(nil))
	require.False(t, ToolChoice{Mode: ToolChoiceRequired}.Satisfied(nil))
	require.True(t, ToolChoice{Mode: ToolChoiceRequired}.Satisfied([]ToolCall{call}))
	require.False(t, ToolChoice{Mode: ToolChoiceFunction, Name: "x"}.Satisfied([]ToolCall{call}))
	require.Empty(t, ToolChoice{Mode: ToolChoiceNone}.AllowedSpecs([]ToolSpec{weatherSpec}))
}

func TestFoldMessagesWithTools(t *testing.T) {
	messages := gjson.Parse(`[
		{"role":"system","content":"You are helpful"},
		{"role":"user","content":"Weather in Tokyo?"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"{\"temp\":20}"}
	]`)
	query, err := FoldMessagesWithTools(messages, []ToolSpec{weatherSpec}, ToolChoice{Mode: ToolChoiceAuto}, FoldOptions{StripSystem: true})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(query, "[Tool protocol]"))
	require.Contains(t, query, "- get_weather: Get weather")
	require.Contains(t, query, `Assistant: <tool_call>{"name":"get_weather","arguments":{"city":"Tokyo"}}</tool_call>`)
	require.Contains(t, query, "<tool_result name=\"get_weather\" id=\"call_1\">\n{\"temp\":20}\n</tool_result>")
	require.NotContains(t, query, "You are helpful")

	// The plain folder must still reject raw tool history.
	_, err = FoldMessagesToQuery(messages)
	require.ErrorIs(t, err, ErrUnsupportedToolHistory)
}

func TestFoldMessagesWithToolsBudget(t *testing.T) {
	long := ToolSpec{Name: "big", Description: strings.Repeat("a", 1000), Parameters: `{}`}
	_, err := FoldMessagesWithTools(gjson.Parse(`[{"role":"user","content":"hi"}]`), []ToolSpec{long}, ToolChoice{Mode: ToolChoiceAuto}, FoldOptions{MaxChars: 1000})
	require.Error(t, err)

	query, err := FoldMessagesWithTools(gjson.Parse(`[{"role":"user","content":"hi"}]`), []ToolSpec{weatherSpec}, ToolChoice{Mode: ToolChoiceNone}, FoldOptions{MaxChars: 1000})
	require.NoError(t, err)
	require.Equal(t, "hi", query)
}

func TestToolCallParserStreamingSplitTag(t *testing.T) {
	p := NewToolCallParser([]ToolSpec{weatherSpec})
	var text strings.Builder
	for _, chunk := range []string{"  \n<tool", `_call>{"name":"get_weather",`, `"arguments":{"city":"Tokyo"}}</tool_call>`} {
		_, _ = text.WriteString(p.Feed(chunk))
	}
	require.True(t, p.Buffering())
	rest, calls := p.Finish()
	require.Empty(t, text.String()+rest)
	require.Len(t, calls, 1)
	require.Equal(t, "get_weather", calls[0].Name)
	require.Equal(t, `{"city":"Tokyo"}`, calls[0].Arguments)
	require.True(t, strings.HasPrefix(calls[0].ID, "call_"))
	require.Len(t, calls[0].ID, len("call_")+24)
}

func TestToolCallParserPlainText(t *testing.T) {
	p := NewToolCallParser([]ToolSpec{weatherSpec})
	require.Equal(t, "", p.Feed("<"))
	require.Equal(t, "<p>Hello", p.Feed("p>Hello"))
	require.Equal(t, " world", p.Feed(" world"))
	rest, calls := p.Finish()
	require.Empty(t, rest)
	require.Nil(t, calls)
}

func TestToolCallParserMultipleCallsAndStringArgs(t *testing.T) {
	p := NewToolCallParser([]ToolSpec{weatherSpec})
	input := "<tool_call>{\"name\":\"get_weather\",\"arguments\":\"{\\\"city\\\": \\\"Paris\\\"}\"}</tool_call>\n" +
		"<tool_call>\n```json\n{\"name\":\"get_weather\",\"arguments\":{\"ids\":[1, 2]}}\n```\n</tool_call>"
	require.Empty(t, p.Feed(input))
	rest, calls := p.Finish()
	require.Empty(t, rest)
	require.Len(t, calls, 2)
	require.Equal(t, `{"city":"Paris"}`, calls[0].Arguments)
	require.Equal(t, `{"ids":[1,2]}`, calls[1].Arguments)
	require.NotEqual(t, calls[0].ID, calls[1].ID)

	delta := ToolCallsChatDelta(calls)
	require.Len(t, delta, 2)
	second, ok := delta[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 1, second["index"])
}

func TestToolCallParserFallbackToText(t *testing.T) {
	for _, input := range []string{
		`<tool_call>{bad json}</tool_call>`,
		`<tool_call>{"name":"unknown","arguments":{}}</tool_call>`,
		`<tool_call>{"name":"get_weather","arguments":{}}`,
		`<tool_call>{"name":"get_weather","arguments":{}}</tool_call> Done.`,
		`<tool_call>{"name":"get_weather","arguments":[1]}</tool_call>`,
	} {
		p := NewToolCallParser([]ToolSpec{weatherSpec})
		emitted := p.Feed(input)
		rest, calls := p.Finish()
		require.Nil(t, calls, input)
		require.Equal(t, input, emitted+rest, input)
	}
}

func TestToolCallParserDisabledPassthrough(t *testing.T) {
	p := NewToolCallParser(nil)
	require.Equal(t, "<tool_call>", p.Feed("<tool_call>"))
	rest, calls := p.Finish()
	require.Empty(t, rest)
	require.Nil(t, calls)
}

func TestToolCallParserOverflowReleasesText(t *testing.T) {
	p := NewToolCallParser([]ToolSpec{weatherSpec})
	require.Empty(t, p.Feed(ToolCallOpenTag))
	big := strings.Repeat("x", maxToolCallBufferBytes)
	out := p.Feed(big)
	require.Equal(t, ToolCallOpenTag+big, out)
	require.Equal(t, "tail", p.Feed("tail"))
}
