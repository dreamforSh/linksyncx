//go:build unit

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// legacyCodeBuddySessionKey is codeBuddySessionKey before the early-stop walk
// (git 78ed9aa40): it lists every message before reading the first ones.
func legacyCodeBuddySessionKey(c *gin.Context, account *Account, body []byte) string {
	if explicit := explicitOpenAIHeaderSessionID(c); explicit != "" {
		return fmt.Sprintf("%d:h:%s", account.ID, explicit)
	}
	var system strings.Builder
	firstUser := ""
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		role := message.Get("role").String()
		if role == "system" && firstUser == "" {
			_, _ = system.WriteString(codeBuddyMessageText(message.Get("content")))
			continue
		}
		if role == "user" {
			firstUser = codeBuddyMessageText(message.Get("content"))
			break
		}
	}
	if system.Len() == 0 && firstUser == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(system.String() + "\x00" + firstUser))
	return fmt.Sprintf("%d:c:%s", account.ID, hex.EncodeToString(sum[:16]))
}

func TestCodeBuddySessionKeyMatchesFullWalk(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := codeBuddyTestAccount(codebuddy.ProfileCNCLI)
	chatBodies := []string{
		`{"model":"glm-5.2","messages":[{"role":"system","content":"sys"},{"role":"user","content":"q1"},{"role":"assistant","content":"a"},{"role":"user","content":"q2"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"developer","content":"dev"},{"role":"system","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]},{"role":"user","content":[{"type":"text","text":"q"},{"type":"text","text":"r"}]}]}`,
		`{"model":"glm-5.2","messages":[{"role":"assistant","content":"only"},{"role":"tool","content":"t"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":""},{"role":"user","content":"second"}]}`,
		`{"model":"glm-5.2","messages":[{"role":"user","content":{"type":"text","text":{"nested": 1}}}]}`,
		`{"model":"glm-5.2","messages":[{"role":7,"content":"x"},{"role":"user","content":[{"type":"text","text":"q"}]}]}`,
		`{"model":"glm-5.2","max_tokens":10,"frequency_penalty":0.5,"messages":[{"role":"user","content":"a ] } \" ["}]}`,
		string(buildCodeBuddyBenchChatBody(64 << 10)),
	}
	var bodies [][]byte
	for _, chat := range chatBodies {
		for _, profile := range []string{codebuddy.ProfileCNCLI, codebuddy.ProfileIntlWork} {
			prepared, err := codebuddy.PrepareChatBody([]byte(chat), profile)
			require.NoError(t, err, chat)
			start, ok := jsonObjectMemberValueStart(bytesView(prepared), "messages")
			require.True(t, ok, "prepared bodies take the early-stop walk")
			require.Equal(t, byte('['), prepared[start])
			bodies = append(bodies, prepared)
		}
	}
	// Shapes PrepareChat never writes still answer like the full walk.
	for _, raw := range []string{
		`{}`,
		`{"messages":null}`,
		`{"messages":{"role":"user","content":"object"}}`,
		`{"messages":"text"}`,
		` { "model" : "m" , "messages" : [ { "role" : "user" , "content" : "spaced" } ] } `,
		`{"messages":[{"role":"system","content":"first"}],"messages":[{"role":"user","content":"second"}]}`,
		`{"model":{"messages":[{"role":"user","content":"nested"}]},"messages":[{"role":"user","content":"top"}]}`,
	} {
		bodies = append(bodies, []byte(raw))
	}

	for _, body := range bodies {
		for _, header := range []string{"", "sess-1"} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if header != "" {
				c.Request.Header.Set("session_id", header)
			}
			require.Equal(t, legacyCodeBuddySessionKey(c, account, body), codeBuddySessionKey(c, account, body), "%s", body)
		}
	}
}

func TestCodeBuddyChatStreamUsageErrorMatchesSJSON(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[]}`,
		`{"model":"m","stream_options":null}`,
		`{"model":"m","stream_options":"x"}`,
		`{"model":"m","stream_options":{"include_usage":false}}`,
		`{"model":"m","stream_options":[1,2]}`,
		`{"model":"m","stream_options": [ ] }`,
		`{"stream_options":[1],"stream_options":{}}`,
		`{"stream_options":{},"stream_options":[1]}`,
		`{"messages":[{"stream_options":[1]}]}`,
	} {
		_, want := ensureOpenAIChatStreamUsage([]byte(body))
		got := codeBuddyChatStreamUsageError([]byte(body))
		if want == nil {
			require.NoError(t, got, body)
			continue
		}
		require.EqualError(t, got, want.Error(), body)
	}
}
