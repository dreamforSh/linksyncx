package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// OpenAITicketProxyPoolSettingKey 是存放打票代理池（代理 ID 列表）的 settings 键。
// 值是一个 JSON 数组，例如 [3,4,5]。
const OpenAITicketProxyPoolSettingKey = "openai_ticket_proxy_pool"

const (
	openAITicketCheckCycleInterval = 5 * time.Minute
	openAITicketCheckFarFuture     = 10 * 365 * 24 * time.Hour
)

// TicketCheckService 负责 OpenAI（ChatGPT OAuth）账号的打票检测：
//   - 账号开启检测后每 6 小时跑一次 turn-state 探测；
//   - 判定降智且账号开启了「降智时临时停止调用」时，把账号置为临时不可调度
//     （有到期时间就停到账号到期，没有就长期停用），并复用打票代理池重新探测；
//   - 后续探测恢复健康时自动解除临时停止调用。
type TicketCheckService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	settingRepo SettingRepository
	gateway     *OpenAIGatewayService

	mu        sync.Mutex
	started   bool
	stopped   bool
	parentCtx context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewTicketCheckService 构造打票检测服务。
func NewTicketCheckService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	settingRepo SettingRepository,
	gateway *OpenAIGatewayService,
) *TicketCheckService {
	ctx, cancel := context.WithCancel(context.Background())
	return &TicketCheckService{
		accountRepo: accountRepo,
		proxyRepo:   proxyRepo,
		settingRepo: settingRepo,
		gateway:     gateway,
		parentCtx:   ctx,
		cancel:      cancel,
	}
}

// ProvideTicketCheckService 构造并启动打票检测服务（wire provider）。
func ProvideTicketCheckService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	settingRepo SettingRepository,
	openAIGatewayService *OpenAIGatewayService,
) *TicketCheckService {
	svc := NewTicketCheckService(accountRepo, proxyRepo, settingRepo, openAIGatewayService)
	svc.Start()
	return svc
}

// Start 启动后台检测循环。
func (s *TicketCheckService) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.stopped {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	go s.runLoop()
}

// Stop 停止后台检测循环。
func (s *TicketCheckService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *TicketCheckService) runLoop() {
	defer s.wg.Done()
	_ = s.RunDue(s.parentCtx)
	ticker := time.NewTicker(openAITicketCheckCycleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
			if err := s.RunDue(s.parentCtx); err != nil {
				logger.LegacyPrintf("service.ticket_check", "run_due_failed: err=%v", err)
			}
		}
	}
}

// RunDue 扫描所有开启了打票检测且到期的 OpenAI 账号并执行检测。
func (s *TicketCheckService) RunDue(ctx context.Context) error {
	if s == nil || s.accountRepo == nil || s.gateway == nil {
		return nil
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return fmt.Errorf("list openai accounts: %w", err)
	}
	now := time.Now()
	for i := range accounts {
		account := &accounts[i]
		if !IsOpenAITicketCheckAccount(account) {
			continue
		}
		settings := TicketCheckSettingsFromAccount(account)
		if !settings.Enabled {
			continue
		}
		if !ticketCheckDue(account, now) {
			continue
		}
		if _, err := s.CheckAccount(ctx, account); err != nil {
			logger.LegacyPrintf("service.ticket_check", "account %d probe failed: err=%v", account.ID, err)
		}
	}
	return nil
}

func ticketCheckDue(account *Account, now time.Time) bool {
	if account == nil || account.Extra == nil {
		return true
	}
	raw, ok := account.Extra[OpenAITicketCheckLastAtExtraKey].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return true
	}
	last, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return true
	}
	interval := OpenAITicketCheckInterval
	// 已因降智停用的账号更频繁地探测（30 分钟），一旦恢复健康立刻放回调度。
	if stopped := ticketCheckStopped(account); stopped {
		interval = 30 * time.Minute
	}
	return now.Sub(last) >= interval
}

// CheckAccount 对单个账号执行一次打票检测并落库结果。manual 为 true 时
// 表示管理员手动触发，不改变 6 小时周期（结果照常记录）。
func (s *TicketCheckService) CheckAccount(ctx context.Context, account *Account) (TicketProbeResult, error) {
	if s == nil || s.gateway == nil {
		return TicketProbeResult{}, fmt.Errorf("ticket check service is not configured")
	}
	settings := TicketCheckSettingsFromAccount(account)
	model := EffectiveTicketCheckModel(account, settings)
	// 跟随出口时区：每次检测前刷新一次出口 IP → 时区，保证请求体改写用的是
	// 当前代理所在地，而不是客户端本地时区。
	if account.OpenAIRequestTimezoneFollowsProxy() {
		if _, err := s.gateway.DetectOpenAIRequestTimezone(ctx, account); err != nil {
			logger.LegacyPrintf("service.ticket_check", "account %d timezone detect failed: err=%v", account.ID, err)
		}
	}
	proxyURL, err := s.resolveProbeProxy(ctx, account)
	if err != nil {
		return TicketProbeResult{}, err
	}

	result, probeErr := s.gateway.ProbeOpenAITicket(ctx, account, model, proxyURL)
	if probeErr != nil {
		result.Model = model
		result.CheckedAt = time.Now()
		result.Error = probeErr.Error()
		// 网络/凭据类错误不改变账号的调度状态，只记录最近一次失败原因。
		_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
			OpenAITicketCheckLastAtExtraKey:    result.CheckedAt.Format(time.RFC3339),
			OpenAITicketCheckLastErrorExtraKey: result.Error,
		})
		mergeAccountExtra(account, map[string]any{
			OpenAITicketCheckLastAtExtraKey:    result.CheckedAt.Format(time.RFC3339),
			OpenAITicketCheckLastErrorExtraKey: result.Error,
		})
		return result, probeErr
	}

	updates := map[string]any{
		OpenAITicketCheckLastAtExtraKey:      result.CheckedAt.Format(time.RFC3339),
		OpenAITicketCheckLastLenExtraKey:     result.StateLength,
		OpenAITicketCheckLastHealthyExtraKey: result.Healthy,
		OpenAITicketCheckLastErrorExtraKey:   result.Error,
		OpenAITicketCheckModelExtraKey:       model,
	}

	if result.Healthy {
		// 健康票据落库，供后续转发注入。
		if result.State != "" {
			s.gateway.recordCodexTurnTicket(account, result.State, model)
		}
		if ticketCheckStopped(account) {
			if err := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); err != nil {
				logger.LegacyPrintf("service.ticket_check", "account %d clear temp unschedulable failed: err=%v", account.ID, err)
			}
			updates[OpenAITicketCheckStoppedExtraKey] = false
			logger.LegacyPrintf("service.ticket_check", "account %d recovered (state_len=%d), resumed scheduling", account.ID, result.StateLength)
		}
	} else if settings.StopOnDegraded && !ticketCheckStopped(account) && result.StateLength > 0 {
		until := time.Now().Add(openAITicketCheckFarFuture)
		if account.ExpiresAt != nil && account.ExpiresAt.After(time.Now()) {
			until = *account.ExpiresAt
		}
		if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, ticketCheckStopReasonPrefix+"：账号降智，已暂停调用"); err != nil {
			logger.LegacyPrintf("service.ticket_check", "account %d set temp unschedulable failed: err=%v", account.ID, err)
		} else {
			updates[OpenAITicketCheckStoppedExtraKey] = true
			// 降智票据不能再用，清空缓存，避免继续注入。
			updates[codexTurnTicketExtraKey] = ""
			updates[codexTurnTicketAtExtraKey] = ""
			logger.LegacyPrintf("service.ticket_check", "account %d degraded (state_len=%d), stopped until %s", account.ID, result.StateLength, until.Format(time.RFC3339))
		}
	}

	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return result, fmt.Errorf("persist ticket check result: %w", err)
	}
	mergeAccountExtra(account, updates)
	return result, nil
}

// RunNow 立即检测指定账号（管理员手动触发）。
func (s *TicketCheckService) RunNow(ctx context.Context, accountID int64) (TicketProbeResult, error) {
	if s == nil || s.accountRepo == nil {
		return TicketProbeResult{}, fmt.Errorf("ticket check service is not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return TicketProbeResult{}, err
	}
	if account == nil {
		return TicketProbeResult{}, fmt.Errorf("account not found")
	}
	if !IsOpenAITicketCheckAccount(account) {
		return TicketProbeResult{}, fmt.Errorf("only OpenAI OAuth (ChatGPT) accounts support ticket check")
	}
	return s.CheckAccount(ctx, account)
}

// UpdateSettings 保存账号级打票检测配置。
func (s *TicketCheckService) UpdateSettings(ctx context.Context, accountID int64, settings TicketCheckSettings) error {
	if s == nil || s.accountRepo == nil {
		return fmt.Errorf("ticket check service is not configured")
	}
	updates := map[string]any{
		OpenAITicketCheckEnabledExtraKey: settings.Enabled,
		OpenAITicketCheckStopExtraKey:    settings.StopOnDegraded,
	}
	if model := strings.TrimSpace(settings.Model); model != "" {
		updates[OpenAITicketCheckModelExtraKey] = model
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, updates)
}

// ticketCheckStopReasonPrefix 标记由打票检测写入的临时停用原因，用来区分
// 管理员手工停用 / 401 冷却等其他来源。
const ticketCheckStopReasonPrefix = "打票检测"

// ticketCheckStopped 判断账号当前是否因打票检测而被停用。
//
// 以账号自身的临时停用字段为准而不是只看 extra 里的标记：管理员在界面上
// "重置临时停用" 只会清 temp_unschedulable_*，extra 标记会残留；如果继续
// 信任残留标记，账号明明可调度却会被判定为"已停用"，于是既不会重新停用、
// 探测也永远走打票代理池。
func ticketCheckStopped(account *Account) bool {
	if account == nil {
		return false
	}
	if account.TempUnschedulableUntil != nil && time.Now().Before(*account.TempUnschedulableUntil) {
		return strings.HasPrefix(account.TempUnschedulableReason, ticketCheckStopReasonPrefix)
	}
	if account.Extra == nil {
		return false
	}
	// 账号已经被恢复调度，残留标记一律忽略。
	return false
}

// TicketCheckStatus 是给管理端展示的打票检测状态。
type TicketCheckStatus struct {
	AccountID       int64               `json:"account_id"`
	Settings        TicketCheckSettings `json:"settings"`
	Stopped         bool                `json:"stopped"`
	LastAt          string              `json:"last_at,omitempty"`
	LastStateLength int                 `json:"last_state_length,omitempty"`
	LastHealthy     bool                `json:"last_healthy"`
	LastError       string              `json:"last_error,omitempty"`
	Timezone        string              `json:"timezone,omitempty"`
	TimezoneIP      string              `json:"timezone_ip,omitempty"`
	TimezoneMode    string              `json:"timezone_mode,omitempty"`
}

// Status 读取账号当前的打票检测配置与最近一次检测结果。
func (s *TicketCheckService) Status(ctx context.Context, accountID int64) (TicketCheckStatus, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return TicketCheckStatus{}, err
	}
	if account == nil {
		return TicketCheckStatus{}, fmt.Errorf("account not found")
	}
	status := TicketCheckStatus{
		AccountID: accountID,
		Settings:  TicketCheckSettingsFromAccount(account),
		Stopped:   ticketCheckStopped(account),
	}
	status.TimezoneMode = account.OpenAIRequestTimezoneMode()
	status.Timezone = account.OpenAIRequestTimezone()
	switch detected := account.Extra[openAIRequestTimezoneDetectedExtraKey].(type) {
	case OpenAIDetectedTimezone:
		status.TimezoneIP = detected.IP
	case map[string]any:
		status.TimezoneIP, _ = detected["ip"].(string)
	}
	if account.Extra != nil {
		status.LastAt, _ = account.Extra[OpenAITicketCheckLastAtExtraKey].(string)
		status.LastHealthy, _ = account.Extra[OpenAITicketCheckLastHealthyExtraKey].(bool)
		status.LastError, _ = account.Extra[OpenAITicketCheckLastErrorExtraKey].(string)
		switch value := account.Extra[OpenAITicketCheckLastLenExtraKey].(type) {
		case float64:
			status.LastStateLength = int(value)
		case int:
			status.LastStateLength = value
		case int64:
			status.LastStateLength = int(value)
		}
	}
	return status, nil
}

// resolveProbeProxy 选择探测使用的代理：账号被降智停用后走打票代理池轮换，
// 其余情况沿用账号自己的代理（fail-closed，分配了代理就绝不直连）。
func (s *TicketCheckService) resolveProbeProxy(ctx context.Context, account *Account) (string, error) {
	if ticketCheckStopped(account) {
		if poolURL, ok := s.pickPoolProxyURL(ctx, account.ID); ok {
			return poolURL, nil
		}
	}
	return account.ProxyURLForOutbound()
}

func (s *TicketCheckService) pickPoolProxyURL(ctx context.Context, accountID int64) (string, bool) {
	ids, err := s.TicketProxyPool(ctx)
	if err != nil || len(ids) == 0 || s.proxyRepo == nil {
		return "", false
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, ids)
	if err != nil || len(proxies) == 0 {
		return "", false
	}
	index := int((time.Now().Unix()/60 + accountID) % int64(len(proxies)))
	proxyURL := proxies[index].URL()
	if strings.TrimSpace(proxyURL) == "" {
		return "", false
	}
	return proxyURL, true
}

// TicketProxyPool 读取打票代理池中的代理 ID 列表。
func (s *TicketCheckService) TicketProxyPool(ctx context.Context) ([]int64, error) {
	if s == nil || s.settingRepo == nil {
		return nil, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, OpenAITicketProxyPoolSettingKey)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parse ticket proxy pool: %w", err)
	}
	return ids, nil
}

// SetTicketProxyPool 保存打票代理池。
func (s *TicketCheckService) SetTicketProxyPool(ctx context.Context, ids []int64) error {
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("ticket check service is not configured")
	}
	if ids == nil {
		ids = []int64{}
	}
	data, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, OpenAITicketProxyPoolSettingKey, string(data))
}
