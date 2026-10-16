//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityAuditConvertedStreamContract(t *testing.T) {
	protocols := []struct {
		name string
		run  func(*AntigravityGatewayService, *gin.Context, *http.Response) (*antigravityStreamResult, error)
	}{
		{"messages", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleClaudeStreamingResponse(c, r, time.Now(), "gemini-3.1-pro-high")
		}},
		{"chat", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleChatCompletionsStreamingFromAntigravity(c, r, time.Now(), "gemini-3.1-pro-high", true)
		}},
		{"responses", func(s *AntigravityGatewayService, c *gin.Context, r *http.Response) (*antigravityStreamResult, error) {
			return s.handleResponsesStreamingFromAntigravity(c, r, time.Now(), "gemini-3.1-pro-high")
		}},
	}
	content := `{"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":1}}}`
	finish := `{"response":{"candidates":[{"finishReason":"STOP"}]}}`
	lateUsage := `{"response":{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":7,"cachedContentTokenCount":60}}}`
	errorFrame := `{"error":{"code":503,"status":"UNAVAILABLE","message":"generation failed"}}`
	for _, protocol := range protocols {
		for _, scenario := range []struct {
			name   string
			frames []string
			failed bool
		}{
			{"tail_usage", []string{content, finish, lateUsage}, false},
			{"missing_finish", []string{content}, true},
			{"error_after_content", []string{content, errorFrame}, true},
			{"error_after_finish", []string{content, finish, errorFrame}, true},
			{"invalid_after_content", []string{content, "malformed"}, true},
		} {
			t.Run(protocol.name+"/"+scenario.name, func(t *testing.T) {
				svc := newAntigravityCompatService(config.GatewayConfig{}, nil)
				c, recorder := newAntigravityCompatContext(http.MethodPost, "/v1/"+protocol.name, nil)
				var body strings.Builder
				for _, frame := range scenario.frames {
					body.WriteString("data: " + frame + "\n\n")
				}
				resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body.String()))}
				defer func() { _ = resp.Body.Close() }()
				result, err := protocol.run(svc, c, resp)
				require.NotNil(t, result)
				wire := recorder.Body.String()
				require.Contains(t, wire, "partial")
				if scenario.failed {
					require.Error(t, err)
					require.True(t, result.usageIncomplete)
					require.Equal(t, 8, result.usage.InputTokens)
					require.NotContains(t, wire, `"stop_reason":"end_turn"`)
					require.NotContains(t, wire, `"finish_reason":"stop"`)
					require.NotContains(t, wire, `"type":"response.completed"`)
					require.NotContains(t, wire, "data: [DONE]")
					if protocol.name == "responses" {
						require.Equal(t, 1, strings.Count(wire, "event: response.failed\n"))
						require.Contains(t, wire, `"sequence_number":`)
					}
					return
				}
				require.NoError(t, err)
				require.False(t, result.usageIncomplete)
				require.Equal(t, 40, result.usage.InputTokens)
				require.Equal(t, 7, result.usage.OutputTokens)
				require.Equal(t, 60, result.usage.CacheReadInputTokens)
				var usage map[string]any
				for _, line := range strings.Split(wire, "\n") {
					if !strings.HasPrefix(line, "data:") {
						continue
					}
					var event map[string]any
					if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
						continue
					}
					switch protocol.name {
					case "messages":
						if event["type"] == "message_delta" {
							usage, _ = event["usage"].(map[string]any)
						}
					case "chat":
						if value, ok := event["usage"].(map[string]any); ok {
							usage = value
						}
					case "responses":
						if event["type"] == "response.completed" {
							response, _ := event["response"].(map[string]any)
							usage, _ = response["usage"].(map[string]any)
						}
					}
				}
				require.NotNil(t, usage)
				if protocol.name == "messages" {
					require.Equal(t, float64(40), usage["input_tokens"])
					require.Equal(t, float64(7), usage["output_tokens"])
					require.Equal(t, float64(60), usage["cache_read_input_tokens"])
				} else if protocol.name == "chat" {
					require.Equal(t, float64(100), usage["prompt_tokens"])
					require.Equal(t, float64(7), usage["completion_tokens"])
				} else {
					require.Equal(t, float64(100), usage["input_tokens"])
					require.Equal(t, float64(7), usage["output_tokens"])
				}
			})
		}
	}
}

type antigravityAuditDeadlineWriter struct {
	gin.ResponseWriter
	deadline      time.Time
	deadlines     []time.Time
	writeCalls    int
	block         bool
	flushErr      error
	setErr        error
	flushAttempts int
}

func (w *antigravityAuditDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	w.deadlines = append(w.deadlines, deadline)
	return w.setErr
}

func (w *antigravityAuditDeadlineWriter) Write(body []byte) (int, error) {
	w.writeCalls++
	if w.block {
		timer := time.NewTimer(time.Until(w.deadline))
		defer timer.Stop()
		<-timer.C
		return 0, os.ErrDeadlineExceeded
	}
	return w.ResponseWriter.Write(body)
}

func (w *antigravityAuditDeadlineWriter) FlushError() error {
	w.flushAttempts++
	if w.flushErr != nil {
		return w.flushErr
	}
	w.ResponseWriter.Flush()
	return nil
}

func TestAntigravityAuditWriterDeadline(t *testing.T) {
	for _, scenario := range []string{"success", "slow_write", "flush_error", "deadline_error"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := newAntigravityCompatContext(http.MethodPost, "/", nil)
			underlying := &antigravityAuditDeadlineWriter{ResponseWriter: c.Writer}
			underlying.block = scenario == "slow_write"
			if scenario == "flush_error" {
				underlying.flushErr = io.ErrClosedPipe
			}
			if scenario == "deadline_error" {
				underlying.setErr = errors.New("deadline failed")
			}
			writer := newAntigravityClientWriter(underlying, underlying, "deadline test")
			writer.writeTimeout = 20 * time.Millisecond
			disconnects := 0
			writer.onDisconnect = func() { disconnects++ }
			start := time.Now()
			ok := writer.Write([]byte("data: hi\n\n"))
			require.Less(t, time.Since(start), time.Second)
			require.Equal(t, scenario == "success", ok)
			require.Equal(t, scenario != "success", writer.Disconnected())
			if scenario == "success" {
				require.True(t, underlying.deadlines[len(underlying.deadlines)-1].IsZero())
				return
			}
			require.Equal(t, 1, disconnects)
			calls := underlying.writeCalls
			require.False(t, writer.Fprintf("more %s", "data"))
			require.Equal(t, calls, underlying.writeCalls)
			require.Equal(t, 1, disconnects)
		})
	}
}
