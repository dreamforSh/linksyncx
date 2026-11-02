package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// 打票检测（Codex turn-state 检测）相关的账号 extra 字段。
//
// 检测方式与社区 292 打票一致：用账号自己的 OAuth token 向 ChatGPT Codex
// 上游发一条最小请求，读取响应头 x-codex-turn-state。健康票据长度约为 292
// （上游把流量路由到满血集群），被降智的账号会拿到 780 左右的异常长票据。
const (
	OpenAITicketCheckEnabledExtraKey     = "ticket_check_enabled"
	OpenAITicketCheckStopExtraKey        = "ticket_check_stop_on_degraded"
	OpenAITicketCheckModelExtraKey       = "ticket_check_model"
	OpenAITicketCheckLastAtExtraKey      = "ticket_check_last_at"
	OpenAITicketCheckLastLenExtraKey     = "ticket_check_last_state_len"
	OpenAITicketCheckLastHealthyExtraKey = "ticket_check_last_healthy"
	OpenAITicketCheckLastErrorExtraKey   = "ticket_check_last_error"
	OpenAITicketCheckStoppedExtraKey     = "ticket_check_stopped"
)

// OpenAITicketCheckInterval 是开启打票检测后的检测周期（用户要求 6 小时一次）。
const OpenAITicketCheckInterval = 6 * time.Hour

// turn-state 长度的健康区间。实测健康票据约 292，降智票据约 780；
// 区间放宽以免上游微调格式导致误判，超出上限才算降智。
const (
	OpenAITicketCheckStateMinHealthy = 120
	OpenAITicketCheckStateMaxHealthy = 480
)

// TicketCheckSettings 是账号级打票检测配置，持久化在 accounts.extra 中。
type TicketCheckSettings struct {
	Enabled        bool   `json:"enabled"`
	StopOnDegraded bool   `json:"stop_on_degraded"`
	Model          string `json:"model"`
}

// TicketProbeResult 是一次打票检测的结果。
type TicketProbeResult struct {
	Healthy     bool      `json:"healthy"`
	StateLength int       `json:"state_length"`
	HTTPStatus  int       `json:"http_status"`
	Model       string    `json:"model"`
	CheckedAt   time.Time `json:"checked_at"`
	Error       string    `json:"error,omitempty"`
	// State 仅用于内部落库票据，不对外暴露。
	State string `json:"-"`
}

// IsOpenAITicketCheckAccount 判断账号是否属于可做 Codex 打票检测的类型：
// 只有 OpenAI 平台的 OAuth（ChatGPT 订阅）账号才有 Codex 通道。
func IsOpenAITicketCheckAccount(a *Account) bool {
	if a == nil || a.Platform != PlatformOpenAI {
		return false
	}
	return a.Type == AccountTypeOAuth
}

// TicketCheckSettingsFromAccount 读取账号上的打票检测配置。
func TicketCheckSettingsFromAccount(a *Account) TicketCheckSettings {
	settings := TicketCheckSettings{StopOnDegraded: true}
	if a == nil || a.Extra == nil {
		return settings
	}
	if enabled, ok := a.Extra[OpenAITicketCheckEnabledExtraKey].(bool); ok {
		settings.Enabled = enabled
	}
	if stop, ok := a.Extra[OpenAITicketCheckStopExtraKey].(bool); ok {
		settings.StopOnDegraded = stop
	}
	if model, ok := a.Extra[OpenAITicketCheckModelExtraKey].(string); ok {
		settings.Model = strings.TrimSpace(model)
	}
	return settings
}

// EffectiveTicketCheckModel 返回检测使用的模型：账号级配置优先，否则跟随账号
// 的模型映射，最后回落到 OpenAI 默认测试模型。
func EffectiveTicketCheckModel(a *Account, settings TicketCheckSettings) string {
	model := strings.TrimSpace(settings.Model)
	if model == "" && a != nil {
		model = strings.TrimSpace(a.GetMappedModel(openai.DefaultTestModel))
	}
	if model == "" {
		model = openai.DefaultTestModel
	}
	return model
}

// ProbeOpenAITicket 用账号凭据向 Codex 上游发一条最小请求并读取
// x-codex-turn-state，返回票据长度与健康判定。proxyURL 为空表示直连。
func (s *OpenAIGatewayService) ProbeOpenAITicket(ctx context.Context, account *Account, model, proxyURL string) (TicketProbeResult, error) {
	result := TicketProbeResult{Model: model, CheckedAt: time.Now()}
	if !IsOpenAITicketCheckAccount(account) {
		return result, fmt.Errorf("account is not an OpenAI OAuth account")
	}

	token, authMode, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return result, fmt.Errorf("get access token: %w", err)
	}
	if token == "" {
		return result, fmt.Errorf("account has no usable access token (auth mode %s)", authMode)
	}

	upstreamModel := normalizeOpenAIModelForUpstream(account, model)
	payload := map[string]any{
		"model": upstreamModel,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "input_text", "text": "Reply with exactly: OK"},
				},
			},
		},
		"stream":       true,
		"store":        false,
		"instructions": openai.DefaultInstructions,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result, fmt.Errorf("build probe payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, chatgptCodexAPIURL, bytes.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("build probe request: %w", err)
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	canonical := resolveCodexOutboundIdentity("")
	req.Header.Set("Originator", canonical.originator)
	if customUA := strings.TrimSpace(account.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	} else {
		req.Header.Set("User-Agent", canonical.userAgent)
	}
	setOpenAIChatGPTAccountHeaders(req.Header, account)
	enforceCodexIdentityHeadersWithUA(req.Header, account.GetOpenAIUserAgent())
	account.ApplyHeaderOverrides(req.Header)

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return result, fmt.Errorf("probe request failed: %w", err)
	}
	if resp == nil {
		return result, fmt.Errorf("probe request returned no response")
	}
	defer func() {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
		}
	}()

	result.HTTPStatus = resp.StatusCode
	state := extractOpenAICodexTurnState(resp.Header)
	result.State = state
	result.StateLength = len(state)
	result.Healthy = resp.StatusCode == http.StatusOK && result.StateLength >= OpenAITicketCheckStateMinHealthy && result.StateLength <= OpenAITicketCheckStateMaxHealthy
	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
		return result, nil
	}
	if state == "" {
		result.Error = "missing x-codex-turn-state header"
	}
	return result, nil
}
