package service

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpwire"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// wireRecorder 是记录写入字节的内存连接。
type wireRecorder struct{ bytes.Buffer }

func (*wireRecorder) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (*wireRecorder) Close() error                     { return nil }
func (*wireRecorder) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*wireRecorder) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*wireRecorder) SetDeadline(time.Time) error      { return nil }
func (*wireRecorder) SetReadDeadline(time.Time) error  { return nil }
func (*wireRecorder) SetWriteDeadline(time.Time) error { return nil }

// 网关 OAuth mimic 路径构造的真实请求，经 net/http 写出、httpwire 重排后，头名
// 顺序与真实 Claude Code 2.1.290（Bun fetch）/v1/messages 抓包一致。
func TestClaudeCodeMimicRequestWireOrderMatchesBunCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	svc := &GatewayService{cfg: &config.Config{}}
	account := &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	req, _, err := svc.buildUpstreamRequest(context.Background(), c, account,
		[]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		"test-token", "oauth", "claude-sonnet-4-6", true, true)
	require.NoError(t, err)

	rec := &wireRecorder{}
	require.NoError(t, req.Write(httpwire.NewConn(rec)))
	head, _, ok := strings.Cut(rec.String(), "\r\n\r\n")
	require.True(t, ok)
	lines := strings.Split(head, "\r\n")
	require.Equal(t, "POST /v1/messages?beta=true HTTP/1.1", lines[0])
	names := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		name, _, found := strings.Cut(line, ": ")
		require.True(t, found, line)
		names = append(names, name)
	}

	require.Equal(t, []string{
		"Accept",
		"Authorization",
		"Content-Type",
		"User-Agent",
		"X-Claude-Code-Session-Id",
		"X-Stainless-Arch",
		"X-Stainless-Lang",
		"X-Stainless-OS",
		"X-Stainless-Package-Version",
		"X-Stainless-Retry-Count",
		"X-Stainless-Runtime",
		"X-Stainless-Runtime-Version",
		"X-Stainless-Timeout",
		"anthropic-beta",
		"anthropic-dangerous-direct-browser-access",
		"anthropic-version",
		"x-app",
		"x-claude-code-request-class",
		"x-client-request-id",
		// Bun 补全的尾块
		"Connection",
		"Host",
		"Accept-Encoding",
		"Content-Length",
	}, names)
	require.Contains(t, lines, "Connection: keep-alive")
	require.Contains(t, lines, "Host: api.anthropic.com")
	require.Contains(t, lines, "Accept-Encoding: gzip, deflate, br, zstd")
}
