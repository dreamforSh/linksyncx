package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const perplexityRequestMaxDuration = 30 * time.Minute

type perplexityUpstreamBody struct {
	io.ReadCloser
	once sync.Once
	err  error
}

func (b *perplexityUpstreamBody) Close() error {
	b.once.Do(func() { b.err = b.ReadCloser.Close() })
	return b.err
}

type perplexityResponseIdentity struct {
	mu    sync.Mutex
	value perplexity.ModelIdentity
}

func (s *perplexityResponseIdentity) store(value perplexity.ModelIdentity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *perplexityResponseIdentity) snapshot() perplexity.ModelIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// perplexityResponseMetrics 是翻译协程产出、转发收尾消费的指标细胞：
// 上游不返回 token 用量，搜索引用数等真实指标经此通道带出（线程安全）。
type perplexityResponseMetrics struct {
	mu          sync.Mutex
	searchCount int
}

func (m *perplexityResponseMetrics) storeSearchCount(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searchCount = n
}

func (m *perplexityResponseMetrics) snapshot() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.searchCount
}

type perplexityCCBody struct {
	reader   *io.PipeReader
	upstream io.Closer
	cancel   context.CancelFunc
	onRead   func()
	readOnce sync.Once
	identity *perplexityResponseIdentity
	metrics  *perplexityResponseMetrics
	once     sync.Once
	err      error
}

func (b *perplexityCCBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if n > 0 && b.onRead != nil {
		b.readOnce.Do(b.onRead)
	}
	return n, err
}

func (b *perplexityCCBody) Close() error {
	b.once.Do(func() {
		b.cancel()
		_ = b.upstream.Close()
		b.err = b.reader.Close()
	})
	return b.err
}

func isPerplexityCCResponse(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	_, ok := resp.Body.(*perplexityCCBody)
	return ok
}

func perplexityOptionsForIngress(c *gin.Context, account *Account, body []byte, protocol string) (perplexity.RequestOptions, error) {
	opts, err := resolvePerplexityRequestOptions(account, body)
	if err == nil {
		err = validatePerplexityRequest(body, protocol, &opts)
	}
	if err != nil {
		return opts, err
	}
	c.Header("X-Perplexity-Usage-Estimated", "true")
	opts.Internal.SessionScope = perplexityConversationHint(c, body)
	if len(opts.IgnoredParameters) > 0 {
		c.Header("X-Perplexity-Ignored-Parameters", strings.Join(opts.IgnoredParameters, ", "))
	} else {
		c.Writer.Header().Del("X-Perplexity-Ignored-Parameters")
	}
	return opts, nil
}

func markPerplexityResult(result *OpenAIForwardResult, err error, resp *http.Response) (*OpenAIForwardResult, error) {
	if result != nil {
		result.UpstreamEndpoint = perplexityUpstreamEndpoint
		result.UsageEstimated = true
		result.UsageIncomplete = result.UsageIncomplete || err != nil
		// The Chat Completions body is synthetic. Never audit its requested model
		// as an upstream declaration when Perplexity did not report one.
		result.UpstreamResponseModel = ""
		result.UpstreamResponseModelConflict = false
		if body, ok := resp.Body.(*perplexityCCBody); ok {
			if body.metrics != nil {
				if n := body.metrics.snapshot(); n > 0 && result.SearchCount == 0 {
					result.SearchCount = n
				}
			}
			if body.identity != nil {
				identity := body.identity.snapshot()
				result.UpstreamModel = identity.RequestedPreference
				result.UpstreamResponseModel = identity.ActualModel
				result.UpstreamResponseModelConflict = identity.Conflict
			}
		}
	}
	return result, err
}

func perplexityContinuationRejected(status int, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusNotFound && status != http.StatusConflict {
		return false
	}
	if !gjson.ValidBytes(body) {
		return false
	}
	root := gjson.ParseBytes(body)
	message := ""
	for _, path := range []string{"error.message", "message", "detail"} {
		if value := root.Get(path); value.Type == gjson.String {
			message = strings.ToLower(value.String())
			break
		}
	}
	code := strings.ToLower(root.Get("error.code").String())
	if code == "invalid_backend_uuid" || code == "backend_uuid_not_found" || code == "backend_uuid_expired" {
		return true
	}
	return strings.Contains(message, "backend_uuid") &&
		(strings.Contains(message, "invalid") || strings.Contains(message, "expired") || strings.Contains(message, "not found"))
}

func perplexityResponsesFailure(state *apicompat.ChatCompletionsToResponsesStreamState, reason string) apicompat.ResponsesStreamEvent {
	state.CompletedSent = true
	event := apicompat.ResponsesStreamEvent{
		Type: "response.failed", SequenceNumber: state.SequenceNumber,
		Response: &apicompat.ResponsesResponse{
			ID: state.ResponseID, Object: "response", CreatedAt: state.Created, Model: state.Model,
			Status: "failed", Output: []apicompat.ResponsesOutput{},
			Error: &apicompat.ResponsesError{Code: "upstream_error", Message: reason},
		},
	}
	state.SequenceNumber++
	return event
}
