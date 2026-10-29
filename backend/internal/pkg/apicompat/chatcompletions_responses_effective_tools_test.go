package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResponsesToChatReusesCallerEffectiveTools checks that handing the
// caller's EffectiveResponsesTools result to the conversion yields exactly the
// request the conversion builds when it resolves the tools itself.
func TestResponsesToChatReusesCallerEffectiveTools(t *testing.T) {
	bodies := []string{
		`{"model":"m","input":"hi"}`,
		`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"tool_choice":"auto"}`,
		`{"model":"m","input":[
			{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","description":"run"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"go"}]}
		],"tools":[{"type":"web_search"}]}`,
		`{"model":"m","tools":[
			{"type":"tool_search"},
			{"type":"function","name":"inspect","parameters":{"type":"object"}}
		],"input":[
			{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"workspace"}},
			{"type":"tool_search_output","call_id":"search_1","status":"completed","execution":"client","tools":[
				{"type":"function","name":"inspect","parameters":{"type":"object"}},
				{"type":"custom","name":"exec"},
				{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","parameters":{"type":"object"}}]}
			]}
		],"tool_choice":{"type":"function","name":"inspect"}}`,
	}
	for _, body := range bodies {
		var req ResponsesRequest
		require.NoError(t, json.Unmarshal([]byte(body), &req))
		want, err := ResponsesToChatCompletionsRequestWithOptions(&req, nil)
		require.NoError(t, err)

		effective, err := EffectiveResponsesTools(&req)
		require.NoError(t, err)
		got, err := ResponsesToChatCompletionsRequestWithOptions(&req, &ResponsesToChatOptions{EffectiveTools: &effective})
		require.NoError(t, err)

		wantJSON, err := json.Marshal(want)
		require.NoError(t, err)
		gotJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.Equal(t, string(wantJSON), string(gotJSON), body)
	}
}
