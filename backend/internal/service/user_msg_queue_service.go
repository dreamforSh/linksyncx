package service

import (
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
)

// UserMsgQueueCache 用户消息串行队列 Redis 缓存接口
type UserMsgQueueCache interface {
	// AcquireLock 尝试获取 scope 的串行锁
	AcquireLock(ctx context.Context, scope UserMsgQueueScope, requestID string, lockTtlMs int) (acquired bool, err error)
	// ReleaseLock 释放锁并记录 scope 的完成时间
	ReleaseLock(ctx context.Context, scope UserMsgQueueScope, requestID string) (released bool, err error)
	// GetLastCompletedMs 获取 scope 上次完成时间（毫秒时间戳，Redis TIME 源）
	GetLastCompletedMs(ctx context.Context, scope UserMsgQueueScope) (int64, error)
	// GetCurrentTimeMs 获取 Redis 服务器当前时间（毫秒），与 ReleaseLock 记录的时间源一致
	GetCurrentTimeMs(ctx context.Context) (int64, error)
	// ReconcileExpiredLockCandidates 处理锁索引中的到期候选，按真实 PTTL 清理或刷新索引
	ReconcileExpiredLockCandidates(ctx context.Context, maxCount int) (cleaned int, err error)
}

// UserMsgQueueScope 串行锁的作用域（D8）。Session 为空时是账号级：单会话模式、按账号
// 串行的配置、请求不带会话 ID 时使用；否则是账号内的一个会话，同一会话的用户消息串行，
// 不同会话并行。上次完成时间与最小间隔按同一作用域记录。
type UserMsgQueueScope struct {
	AccountID int64
	// Session 是客户端会话 ID 的摘要（小写 hex），见 ScopeFor
	Session string
}

// String 返回日志用的作用域标识：账号级为 "123"，会话级为 "123/abcd…"
func (s UserMsgQueueScope) String() string {
	if s.Session == "" {
		return strconv.FormatInt(s.AccountID, 10)
	}
	return strconv.FormatInt(s.AccountID, 10) + "/" + s.Session
}

// umqSessionDigestBytes 会话摘要取 SHA256 的前 12 字节（24 位 hex）。摘要只在单个账号内
// 区分会话，撞车的后果只是两个会话多串行一次。
const umqSessionDigestBytes = 12

// ScopeFor 返回本次请求的串行作用域。会话取自客户端 metadata.user_id 的 session_id，
// 没有时取 X-Claude-Code-Session-Id；两者在同一账号、同一身份代次下与上游会话一一对应。
// 单会话模式（账号只有一个上游会话）、Scope = account、请求不带会话时退回账号级。
func (s *UserMessageQueueService) ScopeFor(account *Account, parsed *ParsedRequest, clientHeaders http.Header) UserMsgQueueScope {
	if account == nil {
		return UserMsgQueueScope{}
	}
	scope := UserMsgQueueScope{AccountID: account.ID}
	if s != nil && !s.cfg.SessionScoped() {
		return scope
	}
	if account.IsSessionIDMaskingEnabled() {
		return scope
	}
	session := umqClientSessionID(parsed, clientHeaders)
	if session == "" {
		return scope
	}
	sum := sha256.Sum256([]byte(session))
	scope.Session = hex.EncodeToString(sum[:umqSessionDigestBytes])
	return scope
}

// umqClientSessionID 取客户端会话 ID：metadata.user_id 的 session_id 优先，其次是会话头。
func umqClientSessionID(parsed *ParsedRequest, clientHeaders http.Header) string {
	if parsed != nil && parsed.MetadataUserID != "" {
		if uid := ParseMetadataUserID(parsed.MetadataUserID); uid != nil && uid.SessionID != "" {
			return uid.SessionID
		}
	}
	if clientHeaders == nil {
		return ""
	}
	session := strings.TrimSpace(getHeaderRaw(clientHeaders, "X-Claude-Code-Session-Id"))
	if len(session) > 128 {
		return ""
	}
	return session
}

// QueueLockResult 锁获取结果
type QueueLockResult struct {
	Acquired  bool
	RequestID string
}

// UserMessageQueueService 用户消息串行队列服务
// 对真实用户消息按会话（或账号）串行化 + RPM 自适应延迟
type UserMessageQueueService struct {
	cache    UserMsgQueueCache
	rpmCache RPMCache
	cfg      *config.UserMessageQueueConfig
	stopCh   chan struct{} // graceful shutdown
	stopOnce sync.Once     // 确保 Stop() 并发安全
}

// NewUserMessageQueueService 创建用户消息串行队列服务
func NewUserMessageQueueService(cache UserMsgQueueCache, rpmCache RPMCache, cfg *config.UserMessageQueueConfig) *UserMessageQueueService {
	return &UserMessageQueueService{
		cache:    cache,
		rpmCache: rpmCache,
		cfg:      cfg,
		stopCh:   make(chan struct{}),
	}
}

// IsRealUserMessage 检测是否为真实用户消息（非 tool_result）
// 与 claude-relay-service 的检测逻辑一致：
// 1. messages 非空
// 2. 最后一条消息 role == "user"
// 3. 最后一条消息 content（如果是数组）中不含 type:"tool_result" / "tool_use_result"
func IsRealUserMessage(parsed *ParsedRequest) bool {
	if parsed == nil {
		return false
	}
	messagesRaw := parsed.MessagesRaw()
	if len(messagesRaw) == 0 {
		return false
	}

	messages := gjson.ParseBytes(messagesRaw)
	if !messages.IsArray() {
		return false
	}
	lastMsg := gjson.Result{}
	messages.ForEach(func(_, msg gjson.Result) bool {
		lastMsg = msg
		return true
	})
	if !lastMsg.Exists() || !lastMsg.IsObject() {
		return false
	}
	if lastMsg.Get("role").String() != "user" {
		return false
	}

	content := lastMsg.Get("content")
	if !content.Exists() {
		return true
	}
	if !content.IsArray() {
		return true
	}

	isReal := true
	content.ForEach(func(_, item gjson.Result) bool {
		itemType := item.Get("type").String()
		if itemType == "tool_result" || itemType == "tool_use_result" {
			isReal = false
			return false
		}
		return true
	})
	return isReal
}

// TryAcquire 尝试立即获取串行锁
func (s *UserMessageQueueService) TryAcquire(ctx context.Context, scope UserMsgQueueScope) (*QueueLockResult, error) {
	if s.cache == nil {
		return &QueueLockResult{Acquired: true}, nil // fail-open
	}

	requestID := generateUMQRequestID()
	lockTTL := s.cfg.LockTTLMs
	if lockTTL <= 0 {
		lockTTL = 120000
	}

	acquired, err := s.cache.AcquireLock(ctx, scope, requestID, lockTTL)
	if err != nil {
		logger.LegacyPrintf("service.umq", "AcquireLock failed for scope %s: %v", scope, err)
		return &QueueLockResult{Acquired: true}, nil // fail-open
	}

	return &QueueLockResult{
		Acquired:  acquired,
		RequestID: requestID,
	}, nil
}

// Release 释放串行锁
func (s *UserMessageQueueService) Release(ctx context.Context, scope UserMsgQueueScope, requestID string) error {
	if s.cache == nil || requestID == "" {
		return nil
	}
	released, err := s.cache.ReleaseLock(ctx, scope, requestID)
	if err != nil {
		logger.LegacyPrintf("service.umq", "ReleaseLock failed for scope %s: %v", scope, err)
		return err
	}
	if !released {
		logger.LegacyPrintf("service.umq", "ReleaseLock no-op for scope %s (requestID mismatch or expired)", scope)
	}
	return nil
}

// EnforceDelay 根据 RPM 负载执行自适应延迟
// 间隔从 scope 上次完成算起，延迟长度按账号整体 RPM 计算。
// 使用 Redis TIME 确保与 releaseLockScript 记录的时间源一致
func (s *UserMessageQueueService) EnforceDelay(ctx context.Context, scope UserMsgQueueScope, baseRPM int) error {
	if s.cache == nil {
		return nil
	}

	// 先检查历史记录：没有历史则无需延迟，避免不必要的 RPM 查询
	lastMs, err := s.cache.GetLastCompletedMs(ctx, scope)
	if err != nil {
		logger.LegacyPrintf("service.umq", "GetLastCompletedMs failed for scope %s: %v", scope, err)
		return nil // fail-open
	}
	if lastMs == 0 {
		return nil // 没有历史记录，无需延迟
	}

	delay := s.CalculateRPMAwareDelay(ctx, scope.AccountID, baseRPM)
	if delay <= 0 {
		return nil
	}

	// 获取 Redis 当前时间（与 lastMs 同源，避免时钟偏差）
	nowMs, err := s.cache.GetCurrentTimeMs(ctx)
	if err != nil {
		logger.LegacyPrintf("service.umq", "GetCurrentTimeMs failed: %v", err)
		return nil // fail-open
	}

	elapsed := time.Duration(nowMs-lastMs) * time.Millisecond
	if elapsed < 0 {
		// 时钟异常（Redis 故障转移等），fail-open
		return nil
	}
	remaining := delay - elapsed
	if remaining <= 0 {
		return nil
	}

	// 执行延迟
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CalculateRPMAwareDelay 根据当前 RPM 负载计算自适应延迟
// ratio = currentRPM / baseRPM
// ratio < 0.5  → MinDelay
// 0.5 ≤ ratio < 0.8 → 线性插值 MinDelay..MaxDelay
// ratio ≥ 0.8 → MaxDelay
// 返回值包含 ±15% 随机抖动（anti-detection + 避免惊群效应）
func (s *UserMessageQueueService) CalculateRPMAwareDelay(ctx context.Context, accountID int64, baseRPM int) time.Duration {
	minDelay := time.Duration(s.cfg.MinDelayMs) * time.Millisecond
	maxDelay := time.Duration(s.cfg.MaxDelayMs) * time.Millisecond

	if minDelay <= 0 {
		minDelay = 200 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 2000 * time.Millisecond
	}
	// 防止配置错误：minDelay > maxDelay 时交换
	if minDelay > maxDelay {
		minDelay, maxDelay = maxDelay, minDelay
	}

	var baseDelay time.Duration

	if baseRPM <= 0 || s.rpmCache == nil {
		baseDelay = minDelay
	} else {
		currentRPM, err := s.rpmCache.GetRPM(ctx, accountID)
		if err != nil {
			logger.LegacyPrintf("service.umq", "GetRPM failed for account %d: %v", accountID, err)
			baseDelay = minDelay // fail-open
		} else {
			ratio := float64(currentRPM) / float64(baseRPM)
			if ratio < 0.5 {
				baseDelay = minDelay
			} else if ratio >= 0.8 {
				baseDelay = maxDelay
			} else {
				// 线性插值: 0.5 → minDelay, 0.8 → maxDelay
				t := (ratio - 0.5) / 0.3
				interpolated := float64(minDelay) + t*(float64(maxDelay)-float64(minDelay))
				baseDelay = time.Duration(math.Round(interpolated))
			}
		}
	}

	// ±15% 随机抖动
	return applyJitter(baseDelay, 0.15)
}

// StartCleanupWorker 启动孤儿锁清理 worker。
// worker 只处理锁索引中的到期候选，真正删除前由 cache 层再次校验锁 PTTL。
func (s *UserMessageQueueService) StartCleanupWorker(interval time.Duration) {
	if s == nil || s.cache == nil || interval <= 0 {
		return
	}

	runCleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// 每轮限制处理数量，避免清理任务在大量过期候选时长时间占用 Redis。
		cleaned, err := s.cache.ReconcileExpiredLockCandidates(ctx, 1000)
		if err != nil {
			logger.LegacyPrintf("service.umq", "Cleanup reconcile failed: %v", err)
			return
		}

		if cleaned > 0 {
			logger.LegacyPrintf("service.umq", "Cleanup completed: released %d orphaned locks", cleaned)
		}
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				runCleanup()
			}
		}
	}()
}

// Stop 停止后台 cleanup worker
func (s *UserMessageQueueService) Stop() {
	if s != nil && s.stopCh != nil {
		s.stopOnce.Do(func() {
			close(s.stopCh)
		})
	}
}

// applyJitter 对延迟值施加 ±jitterPct 的随机抖动
// 使用 math/rand/v2（Go 1.22+ 自动使用 crypto/rand 种子），与 nextBackoff 一致
// 例如 applyJitter(200ms, 0.15) 返回 170ms ~ 230ms
func applyJitter(d time.Duration, jitterPct float64) time.Duration {
	if d <= 0 || jitterPct <= 0 {
		return d
	}
	// [-jitterPct, +jitterPct]
	jitter := (rand.Float64()*2 - 1) * jitterPct
	return time.Duration(float64(d) * (1 + jitter))
}

// generateUMQRequestID 生成唯一请求 ID（与 generateRequestID 一致的 fallback 模式）
func generateUMQRequestID() string {
	b := make([]byte, 16)
	if _, err := cryptorand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
