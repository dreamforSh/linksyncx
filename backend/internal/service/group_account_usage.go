package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 订阅组的账号限额与重置卡：组管理员与组用户都能看到账号的 5h / 7d 使用率和重置卡数量
// （组管理员看分组全部账号，组用户只看分配给自己的账号）；重置卡只有组管理员和超管可以使用。
// 这里只输出使用率、重置时间与卡数，不暴露上游错误详情和账号成本统计。

const (
	groupAccountResetPostProcessTimeout = 8 * time.Second
	groupAccountUsageMaxAccounts        = 200
)

var (
	ErrGroupNotSubscription            = infraerrors.BadRequest("GROUP_NOT_SUBSCRIPTION", "account limits and reset credits are only available for subscription groups")
	ErrGroupAccountResetUnsupported    = infraerrors.BadRequest("GROUP_ACCOUNT_RESET_UNSUPPORTED", "this account does not support reset credits")
	ErrGroupAccountUsageUnavailable    = infraerrors.ServiceUnavailable("GROUP_ACCOUNT_USAGE_UNAVAILABLE", "account usage is not available")
	errGroupAccountDependenciesMissing = errors.New("group account usage is not configured")
)

type groupAccountLoader interface {
	GetByID(ctx context.Context, id int64) (*Account, error)
	GetByIDs(ctx context.Context, ids []int64) ([]*Account, error)
}

type groupAccountUsageReader interface {
	GetUsageBatch(ctx context.Context, accountIDs []int64, force bool) (map[int64]*UsageInfo, map[int64]string, error)
}

type groupAccountQuotaService interface {
	openAIQuotaResetWorkflowQuota
	ResetCredit(ctx context.Context, accountID int64) (*OpenAIQuotaResetResult, error)
	CacheCreditsSnapshot(ctx context.Context, accountID int64, usage *OpenAIQuotaUsage) error
	CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *OpenAIRateLimitResetCredits) error
}

// groupAccountClaudeQuotaService 是 Claude OAuth 账号的重置状态刷新与领取。
type groupAccountClaudeQuotaService interface {
	claudeQuotaResetWorkflowQuota
	ResetCredit(ctx context.Context, accountID int64, program string) (*ClaudeQuotaResetResult, error)
}

// GroupAccountDependencies 订阅组账号限额与重置卡需要的依赖；缺省时这些接口返回服务不可用。
// Quota 服务 OpenAI OAuth 账号，ClaudeQuota 服务 Claude OAuth 账号。
type GroupAccountDependencies struct {
	Accounts    groupAccountLoader
	Usage       groupAccountUsageReader
	Quota       groupAccountQuotaService
	ClaudeQuota groupAccountClaudeQuotaService
	Recoverer   openAIQuotaResetWorkflowRecoverer
}

// WithGroupAccountDependencies 挂载账号限额与重置卡依赖，返回同一个服务便于链式构造。
func (s *GroupManagementService) WithGroupAccountDependencies(deps GroupAccountDependencies) *GroupManagementService {
	s.accountDeps = deps
	return s
}

// GroupAccountWindow 账号某个订阅窗口的使用率（0-100+）与重置时间。
type GroupAccountWindow struct {
	Utilization      float64    `json:"utilization"`
	ResetsAt         *time.Time `json:"resets_at,omitempty"`
	RemainingSeconds int        `json:"remaining_seconds"`
}

// GroupAccountResetCredits 账号可用的重置卡（已过期的不计入）。
type GroupAccountResetCredits struct {
	AvailableCount int      `json:"available_count"`
	ExpiresAt      []string `json:"expires_at"`
}

// GroupAccountUsage 订阅组里一个账号的限额视图。
type GroupAccountUsage struct {
	AccountID        int64                     `json:"account_id"`
	Name             string                    `json:"name"`
	Platform         string                    `json:"platform"`
	Status           string                    `json:"status"`
	RateLimited      bool                      `json:"rate_limited"`
	RateLimitResetAt *time.Time                `json:"rate_limit_reset_at,omitempty"`
	FiveHour         *GroupAccountWindow       `json:"five_hour,omitempty"`
	SevenDay         *GroupAccountWindow       `json:"seven_day,omitempty"`
	UsageUpdatedAt   *time.Time                `json:"usage_updated_at,omitempty"`
	UsageUnavailable bool                      `json:"usage_unavailable"`
	SupportsReset    bool                      `json:"supports_reset_credit"`
	ResetCredits     *GroupAccountResetCredits `json:"reset_credits,omitempty"`
	CanReset         bool                      `json:"can_reset"`
	// AssignedUserCount 组管理员视角：分配了该账号的组用户数。
	AssignedUserCount int `json:"assigned_user_count"`
}

// GroupAccountResetResult 使用重置卡的结果：上游消费成功后本地恢复可能部分失败（WarningCode）。
type GroupAccountResetResult struct {
	Code         string             `json:"code"`
	WindowsReset int                `json:"windows_reset"`
	WarningCode  string             `json:"warning_code,omitempty"`
	Account      *GroupAccountUsage `json:"account,omitempty"`
}

func (s *GroupManagementService) requireAccountDeps(needQuota bool) error {
	deps := s.accountDeps
	if deps.Accounts == nil || deps.Usage == nil {
		return errGroupAccountDependenciesMissing
	}
	if needQuota && deps.Quota == nil && deps.ClaudeQuota == nil {
		return errGroupAccountDependenciesMissing
	}
	return nil
}

// requireAccountQuotaDeps 按账号平台检查重置卡依赖是否已挂载。
func (s *GroupManagementService) requireAccountQuotaDeps(account *Account) error {
	if isClaudeQuotaAccount(account) {
		if s.accountDeps.ClaudeQuota == nil {
			return errGroupAccountDependenciesMissing
		}
		return nil
	}
	if s.accountDeps.Quota == nil {
		return errGroupAccountDependenciesMissing
	}
	return nil
}

// requireSubscriptionGroup 账号限额与重置卡只对订阅组开放。
func (s *GroupManagementService) requireSubscriptionGroup(ctx context.Context, groupID int64) error {
	settings, err := s.repo.GetSettings(ctx, groupID)
	if err != nil {
		return err
	}
	if settings == nil || !settings.Managed || settings.ManagedType != ManagedGroupTypeSubscription {
		return ErrGroupNotSubscription
	}
	return nil
}

// supportsGroupResetCredit 重置卡存在于 OpenAI OAuth 母账号（影子账号要在母账号上重置）
// 和 Claude OAuth 账号（setup-token 没有 profile 权限，查不到重置）。
func supportsGroupResetCredit(account *Account) bool {
	if account == nil {
		return false
	}
	return (account.IsOpenAIOAuth() && !account.IsShadow()) || isClaudeQuotaAccount(account)
}

// AccountUsage 订阅组账号的限额视图：组管理员 / 超管看分组全部账号，组用户只看分配给自己的账号。
func (s *GroupManagementService) AccountUsage(ctx context.Context, actorID int64, role string, groupID int64) ([]GroupAccountUsage, error) {
	if err := s.requireAccountDeps(false); err != nil {
		return nil, err
	}
	selfOnly, err := s.authorize(ctx, actorID, role, groupID, false)
	if err != nil {
		return nil, err
	}
	if err := s.requireSubscriptionGroup(ctx, groupID); err != nil {
		return nil, err
	}
	var listed []AssignedAccount
	if selfOnly {
		listed, err = s.repo.ListAccounts(ctx, groupID, &actorID)
	} else {
		listed, err = s.repo.ListAccountPool(ctx, groupID)
	}
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(listed))
	assigned := make(map[int64]int, len(listed))
	seen := make(map[int64]struct{}, len(listed))
	for _, item := range listed {
		assigned[item.ID] += len(item.AssignedUserIDs)
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return []GroupAccountUsage{}, nil
	}
	if len(ids) > groupAccountUsageMaxAccounts {
		ids = ids[:groupAccountUsageMaxAccounts]
	}
	return s.buildGroupAccountUsage(ctx, ids, assigned, !selfOnly)
}

func (s *GroupManagementService) buildGroupAccountUsage(ctx context.Context, ids []int64, assigned map[int64]int, manager bool) ([]GroupAccountUsage, error) {
	accounts, err := s.accountDeps.Accounts.GetByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Account, len(accounts))
	for _, account := range accounts {
		if account != nil {
			byID[account.ID] = account
		}
	}
	usage, _, err := s.accountDeps.Usage.GetUsageBatch(ctx, ids, false)
	if err != nil {
		// 限额只是展示信息：读取失败时仍返回账号列表，标记为暂不可用
		slog.Warn("group_account_usage_batch_failed", "account_count", len(ids), "error", err)
		usage = nil
	}
	now := time.Now()
	out := make([]GroupAccountUsage, 0, len(ids))
	for _, id := range ids {
		account := byID[id]
		if account == nil {
			continue
		}
		out = append(out, groupAccountUsageView(account, usage[id], assigned[id], manager, now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

func groupAccountUsageView(account *Account, usage *UsageInfo, assignedUsers int, manager bool, now time.Time) GroupAccountUsage {
	item := GroupAccountUsage{
		AccountID:     account.ID,
		Name:          account.Name,
		Platform:      account.Platform,
		Status:        account.Status,
		SupportsReset: supportsGroupResetCredit(account),
	}
	if account.RateLimitResetAt != nil && now.Before(*account.RateLimitResetAt) {
		item.RateLimited = true
		resetAt := *account.RateLimitResetAt
		item.RateLimitResetAt = &resetAt
	}
	if usage == nil || usage.Error != "" {
		item.UsageUnavailable = true
	} else {
		item.FiveHour = groupAccountWindowFrom(usage.FiveHour, now)
		item.SevenDay = groupAccountWindowFrom(usage.SevenDay, now)
		item.UsageUpdatedAt = usage.UpdatedAt
		if item.FiveHour == nil && item.SevenDay == nil {
			item.UsageUnavailable = true
		}
	}
	if item.SupportsReset {
		if isClaudeQuotaAccount(account) {
			item.ResetCredits = readClaudeGroupResetCredits(account.Extra, now)
		} else {
			item.ResetCredits = readGroupAccountResetCredits(account.Extra, now)
		}
		item.CanReset = manager
	}
	if manager {
		item.AssignedUserCount = assignedUsers
	}
	return item
}

func groupAccountWindowFrom(progress *UsageProgress, now time.Time) *GroupAccountWindow {
	if progress == nil {
		return nil
	}
	window := &GroupAccountWindow{Utilization: progress.Utilization, RemainingSeconds: progress.RemainingSeconds}
	if progress.ResetsAt != nil {
		resetAt := *progress.ResetsAt
		window.ResetsAt = &resetAt
		// 缓存里的剩余秒数可能是旧值，按当前时间重新计算
		window.RemainingSeconds = int(resetAt.Sub(now).Seconds())
		if window.RemainingSeconds < 0 {
			window.RemainingSeconds = 0
		}
	}
	return window
}

// readGroupAccountResetCredits 从账号的重置卡快照里读出仍有效的卡：过期的剔除，数量按剩余的卡截断。
// 快照声称有卡但已全部过期时视为未知（返回 nil），需要组管理员刷新。与前端 OpenAIQuotaResetCell 的规则一致。
func readGroupAccountResetCredits(extra map[string]any, now time.Time) *GroupAccountResetCredits {
	raw, ok := extra[openaiQuotaResetCreditsKey]
	if !ok || raw == nil {
		return nil
	}
	// 快照可能是从数据库读出的 map，也可能是进程内合并的结构体，统一经 JSON 解析
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot struct {
		AvailableCount *float64 `json:"available_count"`
		Credits        []struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"credits"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil || snapshot.AvailableCount == nil {
		return nil
	}
	expires := make([]string, 0, len(snapshot.Credits))
	for _, credit := range snapshot.Credits {
		expiresAt := strings.TrimSpace(credit.ExpiresAt)
		if expiresAt == "" {
			continue
		}
		// 无法解析的时间保留（原样展示），已过期的剔除
		if parsed, err := time.Parse(time.RFC3339, expiresAt); err == nil && !parsed.After(now) {
			continue
		}
		expires = append(expires, expiresAt)
	}
	count := *snapshot.AvailableCount
	available := int(count)
	if available < 0 {
		available = 0
	}
	if available > len(expires) {
		available = len(expires)
	}
	if count > 0 && available == 0 {
		return nil
	}
	return &GroupAccountResetCredits{AvailableCount: available, ExpiresAt: expires}
}

// readClaudeGroupResetCredits 从 Claude 重置快照读出仍持有的重置：未过期券的剩余次数与到期时间，
// 加上快照时刻可用的每周会话重置（它没有到期时间）。没有快照时视为未知（返回 nil）。
func readClaudeGroupResetCredits(extra map[string]any, now time.Time) *GroupAccountResetCredits {
	snapshot := readClaudeResetSnapshot(extra)
	if snapshot == nil {
		return nil
	}
	credits := &GroupAccountResetCredits{ExpiresAt: []string{}}
	if cedar := snapshot.CedarEmber; cedar != nil && cedar.Eligible {
		for _, grant := range cedar.Grants {
			if grant.ResetsLeft <= 0 || claudeResetExpired(grant.EndsAt, now) {
				continue
			}
			credits.AvailableCount += grant.ResetsLeft
			if endsAt := strings.TrimSpace(grant.EndsAt); endsAt != "" {
				credits.ExpiresAt = append(credits.ExpiresAt, endsAt)
			}
		}
	}
	if snapshot.JuniperTide.claimable() {
		credits.AvailableCount++
	}
	sort.Strings(credits.ExpiresAt)
	return credits
}

// requireResettableAccount 校验写操作：组管理员 / 超管、订阅组、账号属于该分组且支持重置卡。
func (s *GroupManagementService) requireResettableAccount(ctx context.Context, actorID int64, role string, groupID, accountID int64) (*Account, error) {
	if err := s.requireAccountDeps(true); err != nil {
		return nil, err
	}
	if err := s.requireAccess(ctx, actorID, role, groupID); err != nil {
		return nil, err
	}
	if accountID <= 0 {
		return nil, ErrGroupManagementBadInput
	}
	if err := s.requireSubscriptionGroup(ctx, groupID); err != nil {
		return nil, err
	}
	pool, err := s.repo.ListAccountPool(ctx, groupID)
	if err != nil {
		return nil, err
	}
	inGroup := false
	for _, item := range pool {
		if item.ID == accountID {
			inGroup = true
			break
		}
	}
	if !inGroup {
		return nil, ErrAccountNotInGroup
	}
	account, err := s.accountDeps.Accounts.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !supportsGroupResetCredit(account) {
		return nil, ErrGroupAccountResetUnsupported
	}
	if err := s.requireAccountQuotaDeps(account); err != nil {
		return nil, err
	}
	return account, nil
}

// singleAccountUsage 重新读取单个账号的限额视图（组管理员视角）。
func (s *GroupManagementService) singleAccountUsage(ctx context.Context, groupID, accountID int64) *GroupAccountUsage {
	assigned := 0
	if pool, err := s.repo.ListAccountPool(ctx, groupID); err == nil {
		for _, item := range pool {
			if item.ID == accountID {
				assigned = len(item.AssignedUserIDs)
				break
			}
		}
	}
	items, err := s.buildGroupAccountUsage(ctx, []int64{accountID}, map[int64]int{accountID: assigned}, true)
	if err != nil || len(items) == 0 {
		return nil
	}
	return &items[0]
}

// RefreshAccountQuota 组管理员刷新账号的额度与重置卡（向上游查询一次并写入快照）。
func (s *GroupManagementService) RefreshAccountQuota(ctx context.Context, actorID int64, role string, groupID, accountID int64) (*GroupAccountUsage, error) {
	account, err := s.requireResettableAccount(ctx, actorID, role, groupID, accountID)
	if err != nil {
		return nil, err
	}
	if isClaudeQuotaAccount(account) {
		if _, err := s.accountDeps.ClaudeQuota.RefreshQuota(ctx, accountID); err != nil {
			return nil, err
		}
		view := s.singleAccountUsage(ctx, groupID, accountID)
		if view == nil {
			return nil, ErrGroupAccountUsageUnavailable
		}
		return view, nil
	}
	usage, err := s.accountDeps.Quota.QueryUsage(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if usage == nil {
		return nil, ErrGroupAccountUsageUnavailable
	}
	NotifyOpenAIAutoResetCredit(accountID)
	if err := s.accountDeps.Quota.CacheCreditsSnapshot(ctx, accountID, usage); err != nil {
		slog.Warn("group_account_credits_cache_persist_failed", "account_id", accountID, "error", err)
	}
	// 快照写入失败时保留旧快照，按部分成功处理
	if err := s.accountDeps.Quota.CacheResetCreditsSnapshot(ctx, accountID, usage.RateLimitResetCredits); err != nil {
		slog.Warn("group_account_reset_credit_cache_persist_failed", "account_id", accountID, "error", err)
	}
	view := s.singleAccountUsage(ctx, groupID, accountID)
	if view == nil {
		return nil, ErrGroupAccountUsageUnavailable
	}
	return view, nil
}

// ResetAccountCredit 组管理员 / 超管对订阅组的账号使用一张重置卡，随后解除限流并刷新额度快照。
func (s *GroupManagementService) ResetAccountCredit(ctx context.Context, actorID int64, role string, groupID, accountID int64) (*GroupAccountResetResult, error) {
	account, err := s.requireResettableAccount(ctx, actorID, role, groupID, accountID)
	if err != nil {
		return nil, err
	}
	if isClaudeQuotaAccount(account) {
		return s.resetClaudeAccountCredit(ctx, actorID, role, groupID, accountID)
	}
	result, err := s.accountDeps.Quota.ResetCredit(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrGroupAccountUsageUnavailable
	}
	logger.LegacyPrintf("service.group_management", "audit: group account reset credit used actor_id=%d role=%s group_id=%d account_id=%d code=%s windows_reset=%d",
		actorID, role, groupID, accountID, result.Code, result.WindowsReset)

	// 上游已经扣卡：后处理与读取不受请求取消影响
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), groupAccountResetPostProcessTimeout)
	defer cancel()
	post := RunOpenAIQuotaResetPostProcess(postCtx, accountID, s.accountDeps.Quota, s.accountDeps.Recoverer, s.accountDeps.Accounts.GetByID)
	return &GroupAccountResetResult{
		Code:         result.Code,
		WindowsReset: result.WindowsReset,
		WarningCode:  post.WarningCode,
		Account:      s.singleAccountUsage(postCtx, groupID, accountID),
	}, nil
}

// resetClaudeAccountCredit 组管理员为 Claude OAuth 账号领取一次重置（自动选择：优先每周会话重置）。
// 组管理员界面只区分成功与失败：上游确认未扣卡的结果（未达限额、冷却中等）按失败返回并附上原因。
func (s *GroupManagementService) resetClaudeAccountCredit(ctx context.Context, actorID int64, role string, groupID, accountID int64) (*GroupAccountResetResult, error) {
	result, err := s.accountDeps.ClaudeQuota.ResetCredit(ctx, accountID, "")
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrGroupAccountUsageUnavailable
	}
	logger.LegacyPrintf("service.group_management", "audit: group account claude limit reset actor_id=%d role=%s group_id=%d account_id=%d program=%s result=%s reason=%s",
		actorID, role, groupID, accountID, result.Program, result.Result, result.Reason)
	if !result.Consumed() {
		return nil, ClaudeResetNotAppliedError(result)
	}

	// 上游已经扣卡：后处理与读取不受请求取消影响
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), groupAccountResetPostProcessTimeout)
	defer cancel()
	post := RunClaudeQuotaResetPostProcess(postCtx, accountID, s.accountDeps.ClaudeQuota, s.accountDeps.Recoverer, s.accountDeps.Accounts.GetByID)
	return &GroupAccountResetResult{
		Code:         result.Result,
		WindowsReset: len(result.Cleared),
		WarningCode:  post.WarningCode,
		Account:      s.singleAccountUsage(postCtx, groupID, accountID),
	}, nil
}
