package httpwire

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rawServer 按原始字节读取请求（保留线级头序与大小写），把每个请求的头块交给
// check 校验，校验结果写进响应状态码。
type rawServer struct {
	addr     string
	accepted atomic.Int64
	closed   chan struct{} // 每当服务端观察到客户端关闭连接时收到一个信号
	check    func(head string, body []byte) error
	// closeAfterResponse 为 true 时每个响应后立即关连接（模拟上游 keep-alive 超时）。
	closeAfterResponse bool
}

func startRawServer(t *testing.T, s *rawServer) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s.addr = ln.Addr().String()
	s.closed = make(chan struct{}, 64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			go s.serve(conn)
		}
	}()
	return s
}

// teeReader 记录读到的原始字节；HTTP/1.1 客户端不做 pipelining，一个请求读完时
// 记录里恰好只有这个请求。
type teeReader struct {
	r   io.Reader
	buf bytes.Buffer
}

func (t *teeReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	_, _ = t.buf.Write(p[:n])
	return n, err
}

func (s *rawServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	tee := &teeReader{r: conn}
	br := bufio.NewReader(tee)
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			select {
			case s.closed <- struct{}{}:
			default:
			}
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return
		}
		raw := tee.buf.Bytes()
		head := string(raw[:bytes.Index(raw, headTerminator)+4])
		tee.buf.Reset()

		status, payload := "200 OK", "len="+strconv.Itoa(len(body))
		if s.check != nil {
			if err := s.check(head, body); err != nil {
				status, payload = "500 Internal Server Error", err.Error()
			}
		}
		fmt.Fprintf(conn, "HTTP/1.1 %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", status, len(payload), payload)
		if s.closeAfterResponse {
			return
		}
	}
}

func newTestTransport(maxConns int) *http.Transport {
	return &http.Transport{
		DialContext:         WrapDialer((&net.Dialer{Timeout: 5 * time.Second}).DialContext),
		DisableCompression:  true,
		MaxConnsPerHost:     maxConns,
		MaxIdleConnsPerHost: maxConns,
		IdleConnTimeout:     30 * time.Second,
	}
}

func expectedMessagesHead(host string, bodyLen int) string {
	return strings.Replace(messagesWireHead(strconv.Itoa(bodyLen)),
		"Host: api.anthropic.com", "Host: "+host, 1)
}

// 高并发 + 连接上限 + keep-alive 复用下，每个请求的线级头序都是 Bun 形态，请求体
// 逐字节到达；连接数不超过 MaxConnsPerHost（排队等待而不是无限建连）。
func TestTransportKeepsBunWireShapeUnderConcurrency(t *testing.T) {
	srv := &rawServer{}
	srv.check = func(head string, body []byte) error {
		if want := expectedMessagesHead(srv.addr, len(body)); head != want {
			return fmt.Errorf("head mismatch:\n%s", head)
		}
		for i, b := range body {
			if b != byte('a'+i%26) {
				return fmt.Errorf("body corrupted at %d", i)
			}
		}
		return nil
	}
	startRawServer(t, srv)

	const maxConns = 4
	client := &http.Client{Transport: newTestTransport(maxConns), Timeout: 30 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	sizes := []int{0, 1, 100, 4000, 4096, 5000, 40000, 70000}
	headers := newMessagesRequest(t, nil).Header
	var wg sync.WaitGroup
	errs := make(chan error, 16*25)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				size := sizes[(g+i)%len(sizes)]
				body := make([]byte, size)
				for j := range body {
					body[j] = byte('a' + j%26)
				}
				req, err := http.NewRequest(http.MethodPost, "http://"+srv.addr+"/v1/messages?beta=true", bytes.NewReader(body))
				if err != nil {
					errs <- err
					return
				}
				req.Header = headers.Clone()
				resp, err := client.Do(req)
				if err != nil {
					errs <- err
					return
				}
				payload, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK || string(payload) != "len="+strconv.Itoa(size) {
					errs <- fmt.Errorf("status %d: %s", resp.StatusCode, payload)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := srv.accepted.Load(); got > maxConns {
		t.Fatalf("opened %d connections, MaxConnsPerHost is %d", got, maxConns)
	}
}

// 上游在响应后关闭 keep-alive 连接：net/http 的 readLoop 发现后丢弃该连接，后续
// 请求新建连接，线级头序不受影响。
func TestTransportReplacesServerClosedIdleConn(t *testing.T) {
	srv := &rawServer{closeAfterResponse: true}
	srv.check = func(head string, _ []byte) error {
		want := wireHead(
			"GET /api/oauth/usage HTTP/1.1",
			"Authorization: Bearer at",
			"Connection: keep-alive",
			"User-Agent: Bun/1.4.3",
			"Accept: */*",
			"Host: "+srv.addr,
			"Accept-Encoding: gzip, deflate, br, zstd",
		)
		if head != want {
			return fmt.Errorf("head mismatch:\n%s", head)
		}
		return nil
	}
	startRawServer(t, srv)
	client := &http.Client{Transport: newTestTransport(1), Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+srv.addr+"/api/oauth/usage", nil)
		setRaw(req.Header, "Authorization", "Bearer at")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		payload, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %s", i, payload)
		}
	}
	if got := srv.accepted.Load(); got != 3 {
		t.Fatalf("expected a fresh connection per request after server close, got %d", got)
	}
}

// IdleConnTimeout 到期后包装连接被关闭回收，下一个请求重新建连。
func TestTransportIdleTimeoutReclaimsWrappedConn(t *testing.T) {
	srv := startRawServer(t, &rawServer{})
	transport := newTestTransport(2)
	transport.IdleConnTimeout = 50 * time.Millisecond
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	get := func() {
		t.Helper()
		resp, err := client.Get("http://" + srv.addr + "/")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	get()
	select {
	case <-srv.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("idle connection was not reclaimed")
	}
	get()
	if got := srv.accepted.Load(); got != 2 {
		t.Fatalf("expected 2 connections, got %d", got)
	}
}

// 请求取消会中断卡在响应头上的连接，并且不影响之后的请求。
func TestTransportCancellationClosesStalledConn(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listener unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, conn) }() // 只读不回：模拟上游卡住
		}
	}()
	client := &http.Client{Transport: newTestTransport(1)}
	t.Cleanup(client.CloseIdleConnections)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ln.Addr().String()+"/", strings.NewReader("{}"))
	start := time.Now()
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}
