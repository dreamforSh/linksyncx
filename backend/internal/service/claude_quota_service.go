package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// 与 Claude Code 的两种读取模式一致：平时读免费重置券，5 小时触顶时读 at_wall（同时下发两种重置）。
	claudeUsageQueryCedarEmber = "cedar_ember=1&skip_spend=1"
	claudeUsageQueryAtWall     = "at_wall=1&skip_spend=1"

	// 后台补全订阅档位：同一账号每个实例 30 分钟内最多尝试一次，全局最多 2 个并发。
	claudeSubscriptionRefreshThrottle    = 30 * time.Minute
	claudeSubscriptionRefreshTimeout     = 20 * time.Second
	claudeSubscriptionRefreshConcurrency = 2

	// 5 小时限额的解除时间不会超过一个会话窗口；更远的解除时间是周限。
	claudeSessionWallMaxWait = 5*time.Hour + 5*time.Minute
)

var (
	ErrClaudeQuotaUnsupported    = infraerrors.BadRequest("CLAUDE_QUOTA_UNSUPPORTED", "only Claude OAuth accounts support subscription and limit-reset queries")
	ErrClaudeQuotaNoToken        = infraerrors.BadRequest("CLAUDE_QUOTA_NO_TOKEN", "the account has no usable access token")
	ErrClaudeQuotaNoOrganization = infraerrors.BadRequest("CLAUDE_QUOTA_NO_ORGANIZATION", "the account has no OAuth organization; re-authorize the account")
	ErrClaudeResetInvalidProgram = infraerrors.BadRequest("CLAUDE_RESET_INVALID_PROGRAM", "unknown limit-reset program")
	ErrClaudeResetUnavailable    = infraerrors.Conflict("CLAUDE_RESET_UNAVAILABLE", "no limit reset can be used for this account right now")
	ErrClaudeResetRequiresLimit  = infraerrors.Conflict("CLAUDE_RESET_REQUIRES_LIMIT", "this reset can only be used after the account reaches a usage limit it covers")
)

// ClaudeOAuthAPIClient 调用 Claude OAuth 账号侧的 profile 与重置领取接口，返回 2xx 响应体。
type ClaudeOAuthAPIClient interface {
	FetchProfile(ctx context.Context, opts *ClaudeUsageFetchOptions) ([]byte, error)
	ClaimRateLimitReset(ctx context.Context, opts *ClaudeUsageFetchOptions, orgUUID string, claim *ClaudeRateLimitResetRequest) ([]byte, error)
}

// ClaudeRateLimitResetRequest 是 reset_rate_limits 的请求体。cedar_ember 需要 grant_id 与
// request_id；juniper_tide 只需要 program。
type ClaudeRateLimitResetRequest struct {
	Program   string `json:"program"`
	GrantID   string `json:"grant_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type claudeAccessTokenProvider interface {
	GetAccessToken(ctx context.Context, account *Account) (string, error)
}

// ClaudeQuotaRefreshResult 是一次重置状态刷新的结果；Usage 来自同一次上游读取。
type ClaudeQuotaRefreshResult struct {
	Snapshot       *ClaudeResetSnapshot    `json:"snapshot"`
	Usage          *UsageInfo              `json:"usage,omitempty"`
	Subscription   *ClaudeSubscriptionInfo `json:"subscription,omitempty"`
	CachePersisted bool                    `json:"cache_persisted"`
}

// ClaudeQuotaService 查询 Claude OAuth 账号的订阅档位与重置状态，并领取重置。
type ClaudeQuotaService struct {
	accountRepo   AccountRepository
	usage         *AccountUsageService
	api           ClaudeOAuthAPIClient
	tokenProvider claudeAccessTokenProvider

	subscriptionAttempts sync.Map // accountID -> time.Time（本实例最近一次后台补全）
	subscriptionSlots    chan struct{}
}

// NewClaudeQuotaService 创建服务并挂到用量服务上，读取用量时即可后台补全订阅档位。
func NewClaudeQuotaService(accountRepo AccountRepository, usage *AccountUsageService, api ClaudeOAuthAPIClient, tokenProvider *ClaudeTokenProvider) *ClaudeQuotaService {
	s := &ClaudeQuotaService{
		accountRepo:       accountRepo,
		usage:             usage,
		api:               api,
		subscriptionSlots: make(chan struct{}, claudeSubscriptionRefreshConcurrency),
	}
	// 避免把 typed nil 装进接口
	if tokenProvider != nil {
		s.tokenProvider = tokenProvider
	}
	if usage != nil {
		usage.SetClaudeSubscriptionRefresher(s)
	}
	return s
}

// isClaudeQuotaAccount 只有 Claude OAuth 账号带 profile 权限，能查询订阅档位与重置；
// setup-token 只有推理权限。压测用的合成账号不能拿假凭据访问上游。
func isClaudeQuotaAccount(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformAnthropic &&
		account.Type == AccountTypeOAuth &&
		!account.IsSyntheticUITest()
}

// claudeAccountAtSessionWall 判断账号是否正处于 5 小时限额：此时每周会话重置才可查询与领取。
func claudeAccountAtSessionWall(account *Account, now time.Time) bool {
	if account == nil {
		return false
	}
	sessionOpen := account.SessionWindowEnd != nil && now.Before(*account.SessionWindowEnd)
	if sessionOpen && strings.EqualFold(strings.TrimSpace(account.SessionWindowStatus), "rejected") {
		return true
	}
	if sessionOpen && parseExtraFloat64(account.Extra["session_window_utilization"]) >= 1 {
		return true
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		return account.RateLimitResetAt.Sub(now) <= claudeSessionWallMaxWait
	}
	return false
}

// claudeAccountOrgUUID 领取重置需要 OAuth 组织 UUID：授权时写入 extra.org_uuid 与 credentials。
func claudeAccountOrgUUID(account *Account) string {
	if account == nil {
		return ""
	}
	if orgUUID := strings.TrimSpace(account.GetExtraString("org_uuid")); orgUUID != "" {
		return orgUUID
	}
	return strings.TrimSpace(account.GetCredential("org_uuid"))
}

func (s *ClaudeQuotaService) loadAccount(ctx context.Context, accountID int64) (*Account, error) {
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !isClaudeQuotaAccount(account) {
		return nil, ErrClaudeQuotaUnsupported
	}
	return account, nil
}

// accessToken 优先经 token provider 取（临近过期会先刷新），失败时回退到凭据里的 token。
func (s *ClaudeQuotaService) accessToken(ctx context.Context, account *Account) (string, error) {
	if s.tokenProvider != nil {
		token, err := s.tokenProvider.GetAccessToken(ctx, account)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		if err != nil {
			slog.Warn("claude_quota_access_token_failed", "account_id", account.ID, "error_code", infraerrors.Reason(err))
		}
	}
	if token := strings.TrimSpace(account.GetCredential("access_token")); token != "" {
		return token, nil
	}
	return "", ErrClaudeQuotaNoToken
}

func (s *ClaudeQuotaService) fetchOptions(ctx context.Context, account *Account) (*ClaudeUsageFetchOptions, error) {
	opts, err := s.usage.claudeFetchOptions(ctx, account, "")
	if err != nil {
		return nil, err
	}
	token, err := s.accessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	opts.AccessToken = token
	return opts, nil
}

// fetchResetStatus 读一次带重置状态的用量：触顶时用 at_wall 模式（两种重置都会下发），
// 否则只读免费重置券。
func (s *ClaudeQuotaService) fetchResetStatus(ctx context.Context, account *Account, opts *ClaudeUsageFetchOptions, now time.Time) (*ClaudeResetSnapshot, *ClaudeUsageResponse, error) {
	atWall := claudeAccountAtSessionWall(account, now)
	readOpts := *opts
	readOpts.Query = claudeUsageQueryCedarEmber
	if atWall {
		readOpts.Query = claudeUsageQueryAtWall
	}
	resp, err := s.usage.usageFetcher.FetchUsageWithOptions(ctx, &readOpts)
	if err != nil {
		return nil, nil, err
	}
	return buildClaudeResetSnapshot(resp, atWall, now), resp, nil
}

func (s *ClaudeQuotaService) cacheSnapshot(ctx context.Context, accountID int64, snapshot *ClaudeResetSnapshot) error {
	if snapshot == nil {
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, accountID, map[string]any{claudeResetSnapshotExtraKey: snapshot})
}

// refreshSubscription 同步读取 /api/oauth/profile 并写入 extra.claude_subscription；
// 账号缺组织 UUID 时顺带补齐（领取重置要用）。
func (s *ClaudeQuotaService) refreshSubscription(ctx context.Context, account *Account, opts *ClaudeUsageFetchOptions) (*ClaudeSubscriptionInfo, error) {
	if s.api == nil {
		return nil, ErrClaudeQuotaUnsupported
	}
	body, err := s.api.FetchProfile(ctx, opts)
	if err != nil {
		return nil, err
	}
	info, orgUUID, err := parseClaudeOAuthProfile(body, time.Now())
	if err != nil {
		return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR", err.Error())
	}
	updates := map[string]any{claudeSubscriptionExtraKey: info}
	if orgUUID != "" && claudeAccountOrgUUID(account) == "" {
		updates["org_uuid"] = orgUUID
	}
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, updates); err != nil {
		return info, err
	}
	mergeAccountExtra(account, updates)
	return info, nil
}

// RefreshQuota 向上游查询一次重置状态并写入快照，同一次读取顺带刷新用量进度条；
// 订阅档位同步刷新（失败不影响重置状态）。
func (s *ClaudeQuotaService) RefreshQuota(ctx context.Context, accountID int64) (*ClaudeQuotaRefreshResult, error) {
	account, err := s.loadAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	opts, err := s.fetchOptions(ctx, account)
	if err != nil {
		return nil, err
	}

	result := &ClaudeQuotaRefreshResult{}
	if info, err := s.refreshSubscription(ctx, account, opts); err != nil {
		slog.Warn("claude_subscription_refresh_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
	} else {
		result.Subscription = info
	}
	if result.Subscription == nil {
		result.Subscription = readClaudeSubscription(account.Extra)
	}

	snapshot, resp, err := s.fetchResetStatus(ctx, account, opts, time.Now())
	if err != nil {
		return nil, err
	}
	result.Snapshot = snapshot
	s.usage.cacheClaudeUsageResponse(account.ID, resp)
	result.Usage = s.usage.applyClaudeUsageResponse(ctx, account, resp)

	// 快照写入失败时保留旧快照：上游读取已成功，按部分成功返回，不丢掉本次结果。
	if err := s.cacheSnapshot(ctx, account.ID, snapshot); err != nil {
		slog.Warn("claude_reset_snapshot_persist_failed", "account_id", accountID, "error", err)
	} else {
		result.CachePersisted = true
	}
	return result, nil
}

// ResetCredit 领取一次重置。program 为空时自动选择（优先每周会话重置）。领取前总是重新读取
// 状态：只领取上游此刻指定的那张券，grant id 不经过前端。
func (s *ClaudeQuotaService) ResetCredit(ctx context.Context, accountID int64, program string) (*ClaudeQuotaResetResult, error) {
	program = strings.TrimSpace(program)
	if program != "" && program != ClaudeResetProgramCedarEmber && program != ClaudeResetProgramJuniperTide {
		return nil, ErrClaudeResetInvalidProgram
	}
	if s.api == nil {
		return nil, ErrClaudeQuotaUnsupported
	}
	account, err := s.loadAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	opts, err := s.fetchOptions(ctx, account)
	if err != nil {
		return nil, err
	}
	orgUUID := claudeAccountOrgUUID(account)
	if orgUUID == "" {
		// 早期账号可能没记组织 UUID：从 profile 补齐。
		if _, err := s.refreshSubscription(ctx, account, opts); err != nil {
			slog.Warn("claude_reset_org_lookup_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
		}
		if orgUUID = claudeAccountOrgUUID(account); orgUUID == "" {
			return nil, ErrClaudeQuotaNoOrganization
		}
	}

	now := time.Now()
	snapshot, resp, err := s.fetchResetStatus(ctx, account, opts, now)
	if err != nil {
		return nil, err
	}
	// 最新状态先落盘：即使这次领取不成，卡片也能看到当前的可用情况。
	if err := s.cacheSnapshot(ctx, account.ID, snapshot); err != nil {
		slog.Warn("claude_reset_snapshot_persist_failed", "account_id", accountID, "error", err)
	}
	s.usage.cacheClaudeUsageResponse(account.ID, resp)

	if program == "" {
		if program, err = snapshot.preferredProgram(now); err != nil {
			return nil, err
		}
	}
	claim := &ClaudeRateLimitResetRequest{Program: program}
	switch program {
	case ClaudeResetProgramJuniperTide:
		if !snapshot.JuniperTide.claimable() {
			return nil, ErrClaudeResetUnavailable
		}
	case ClaudeResetProgramCedarEmber:
		grant, err := snapshot.CedarEmber.nextClaimableGrant(now)
		if err != nil {
			return nil, err
		}
		claim.GrantID = grant.id
		claim.RequestID = claudeResetRequestID(account.ID, grant.id, grant.ResetsLeft)
		if !claudeResetGrantIDPattern.MatchString(claim.GrantID) || !claudeResetRequestIDPattern.MatchString(claim.RequestID) {
			return nil, ErrClaudeResetUnavailable
		}
	}

	body, err := s.api.ClaimRateLimitReset(ctx, opts, orgUUID, claim)
	if err != nil {
		return nil, err
	}
	result := parseClaudeResetClaim(body, program)
	if result.Result == "" {
		return nil, infraerrors.New(http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR", "unreadable limit-reset response")
	}
	slog.Info("claude_limit_reset_claimed",
		"account_id", accountID,
		"program", program,
		"result", result.Result,
		"reason", result.Reason,
		"cleared", result.Cleared)
	if result.Consumed() {
		// 旧用量缓存已不可信：下一次查询直接读上游。
		s.usage.invalidateClaudeUsageCache(account.ID)
	}
	return result, nil
}

// RefreshSubscriptionIfStale 实现 claudeSubscriptionRefresher：档位缺失或超过一天时后台拉取
// profile，不阻塞读取用量的请求；按账号节流并限制并发，拉取失败等下次读取再试。
func (s *ClaudeQuotaService) RefreshSubscriptionIfStale(account *Account) {
	if s == nil || s.api == nil || s.usage == nil || !isClaudeQuotaAccount(account) || account.Status == StatusError {
		return
	}
	now := time.Now()
	if !claudeSubscriptionIsStale(account.Extra, now) {
		return
	}
	if last, ok := s.subscriptionAttempts.Load(account.ID); ok {
		if lastAt, ok := last.(time.Time); ok && now.Sub(lastAt) < claudeSubscriptionRefreshThrottle {
			return
		}
	}
	select {
	case s.subscriptionSlots <- struct{}{}:
	default:
		// 并发已满：本次跳过，下次读取用量时再补。
		return
	}
	s.subscriptionAttempts.Store(account.ID, now)

	accountID := account.ID
	go func() {
		defer func() { <-s.subscriptionSlots }()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("claude_subscription_background_refresh_panic", "account_id", accountID, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), claudeSubscriptionRefreshTimeout)
		defer cancel()

		// 重新加载：拿到最新凭据，也避免与调用方共享同一个 Account 对象。
		fresh, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil || !isClaudeQuotaAccount(fresh) || !claudeSubscriptionIsStale(fresh.Extra, time.Now()) {
			return
		}
		opts, err := s.fetchOptions(ctx, fresh)
		if err != nil {
			return
		}
		if _, err := s.refreshSubscription(ctx, fresh, opts); err != nil {
			slog.Info("claude_subscription_background_refresh_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
		}
	}()
}

type claudeQuotaResetWorkflowQuota interface {
	RefreshQuota(ctx context.Context, accountID int64) (*ClaudeQuotaRefreshResult, error)
}

// ClaudeQuotaResetPostProcessResult 汇总领取后的恢复结果，警告码与 OpenAI 重置卡共用。
type ClaudeQuotaResetPostProcessResult struct {
	Refresh               *ClaudeQuotaRefreshResult
	Account               *Account
	CacheRefreshed        bool
	AccountStateRecovered bool
	WarningCode           string
}

// RunClaudeQuotaResetPostProcess 按“解除本地限流、刷新重置快照与用量、刷新账号行”的固定顺序
// 执行领取后的恢复，与 OpenAI 重置卡的后处理语义一致。
func RunClaudeQuotaResetPostProcess(
	ctx context.Context,
	accountID int64,
	quota claudeQuotaResetWorkflowQuota,
	recoverer openAIQuotaResetWorkflowRecoverer,
	loadAccount func(context.Context, int64) (*Account, error),
) ClaudeQuotaResetPostProcessResult {
	result := ClaudeQuotaResetPostProcessResult{}
	if recoverer == nil {
		result.WarningCode = OpenAIQuotaResetWarningAccountRecoveryFailed
		return result
	}
	if _, err := recoverer.RecoverAccountState(ctx, accountID, AccountRecoveryOptions{InvalidateToken: true}); err != nil {
		slog.Warn("claude_quota_reset_account_recovery_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
		result.WarningCode = OpenAIQuotaResetWarningAccountRecoveryFailed
		return result
	}
	result.AccountStateRecovered = true

	if quota != nil {
		refresh, err := quota.RefreshQuota(ctx, accountID)
		switch {
		case err != nil || refresh == nil || !refresh.CachePersisted:
			slog.Warn("claude_quota_reset_cache_refresh_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
			result.WarningCode = OpenAIQuotaResetWarningCacheRefreshFailed
			result.Refresh = refresh
		default:
			result.Refresh = refresh
			result.CacheRefreshed = true
		}
	}

	if loadAccount == nil {
		return result
	}
	account, err := loadAccount(ctx, accountID)
	if err != nil {
		slog.Warn("claude_quota_reset_account_refresh_failed", "account_id", accountID, "error_code", infraerrors.Reason(err))
		if result.WarningCode == "" {
			result.WarningCode = OpenAIQuotaResetWarningAccountRefreshFailed
		}
		return result
	}
	result.Account = account
	return result
}

// ClaudeResetNotAppliedError 表示上游返回了未扣卡的领取结果；未达限额与冷却中单独给出错误码，
// 便于界面给出可操作的提示。
func ClaudeResetNotAppliedError(result *ClaudeQuotaResetResult) error {
	detail := result.Result
	if result.Reason != "" {
		detail += ": " + result.Reason
	}
	code := "CLAUDE_RESET_NOT_APPLIED"
	switch result.Result {
	case ClaudeResetResultNotLimited:
		code = "CLAUDE_RESET_NOT_LIMITED"
	case "cooldown":
		code = "CLAUDE_RESET_COOLDOWN"
	}
	return infraerrors.Conflict(code, "the limit reset was not applied ("+detail+")").
		WithMetadata(map[string]string{"result": result.Result, "reason": result.Reason})
}
