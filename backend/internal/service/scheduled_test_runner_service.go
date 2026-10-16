package service

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

const scheduledTestDefaultMaxWorkers = 10

// ScheduledTestRunnerService periodically scans due test plans and executes them.
type ScheduledTestRunnerService struct {
	planRepo       ScheduledTestPlanRepository
	scheduledSvc   *ScheduledTestService
	accountTestSvc *AccountTestService
	rateLimitSvc   *RateLimitService
	cfg            *config.Config
	locks          LeaderLockCache
	owner          string
	rootCtx        context.Context
	cancel         context.CancelFunc

	mu        sync.Mutex
	cron      *cron.Cron
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewScheduledTestRunnerService(planRepo ScheduledTestPlanRepository, scheduledSvc *ScheduledTestService, accountTestSvc *AccountTestService, rateLimitSvc *RateLimitService, cfg *config.Config) *ScheduledTestRunnerService {
	ctx, cancel := context.WithCancel(context.Background())
	return &ScheduledTestRunnerService{
		planRepo: planRepo, scheduledSvc: scheduledSvc, accountTestSvc: accountTestSvc,
		rateLimitSvc: rateLimitSvc, cfg: cfg, owner: uuid.NewString(), rootCtx: ctx, cancel: cancel,
	}
}

func (s *ScheduledTestRunnerService) SetLeaderLock(locks LeaderLockCache) { s.locks = locks }

func (s *ScheduledTestRunnerService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.rootCtx.Err() != nil {
			return
		}
		loc := time.Local
		if s.cfg != nil {
			if parsed, err := time.LoadLocation(s.cfg.Timezone); err == nil && parsed != nil {
				loc = parsed
			}
		}
		c := cron.New(cron.WithParser(scheduledTestCronParser), cron.WithLocation(loc))
		if _, err := c.AddFunc("* * * * *", s.runScheduled); err != nil {
			logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] invalid schedule")
			return
		}
		s.cron = c
		c.Start()
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] started (tick=every minute)")
	})
}

func (s *ScheduledTestRunnerService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		s.mu.Lock()
		if s.cancel != nil {
			s.cancel()
		}
		c := s.cron
		s.mu.Unlock()
		if c != nil {
			ctx := c.Stop()
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] cron stop timed out")
			}
		}
	})
}

func (s *ScheduledTestRunnerService) runScheduled() {
	root := s.rootCtx
	if root == nil {
		root = context.Background()
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-root.Done():
		return
	case <-timer.C:
	}
	ctx, cancel := context.WithTimeout(root, 5*time.Minute)
	defer cancel()
	plans, err := s.planRepo.ListDue(ctx, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] ListDue failed")
		return
	}
	sem := make(chan struct{}, scheduledTestDefaultMaxWorkers)
	var workers sync.WaitGroup
	defer workers.Wait()
	for _, plan := range plans {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		workers.Add(1)
		go func(plan *ScheduledTestPlan) {
			defer workers.Done()
			defer func() { <-sem }()
			s.runOnePlan(ctx, plan)
		}(plan)
	}
}

func (s *ScheduledTestRunnerService) runOnePlan(ctx context.Context, plan *ScheduledTestPlan) {
	if plan == nil || ctx.Err() != nil {
		return
	}
	if s.locks != nil {
		key := "scheduled-test-plan:" + strconv.FormatInt(plan.ID, 10)
		acquired, err := s.locks.TryAcquireLeaderLock(ctx, key, s.owner, 6*time.Minute)
		if err != nil || !acquired {
			return
		}
		defer func() {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.locks.ReleaseLeaderLock(releaseCtx, key, s.owner)
		}()
	} else if plan.TestMode == AccountTestModePerplexityModel {
		// Explicitly billable model probes require instance coordination.
		return
	}
	fresh, err := s.planRepo.GetByID(ctx, plan.ID)
	if err != nil || fresh == nil || !fresh.Enabled || fresh.NextRunAt == nil || fresh.NextRunAt.After(time.Now()) {
		return
	}
	if fresh.ModelID != plan.ModelID || fresh.TestMode != plan.TestMode || !fresh.UpdatedAt.Equal(plan.UpdatedAt) {
		return
	}
	plan = fresh
	result, err := s.accountTestSvc.RunTestBackground(ctx, plan.AccountID, plan.ModelID, plan.TestMode)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d test failed to execute", plan.ID)
		return
	}
	if err := s.scheduledSvc.SaveResult(ctx, plan.ID, plan.MaxResults, result); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d SaveResult failed", plan.ID)
	}
	// Perplexity owns narrowly scoped session/model recovery. A successful
	// request must not clear an administrator pause or unrelated runtime state.
	if result.Status == "success" && plan.AutoRecover && result.Metadata["platform"] != PlatformPerplexity {
		s.tryRecoverAccount(ctx, plan.AccountID, plan.ID)
	}
	nextRun, err := computeNextRun(plan.CronExpression, time.Now())
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d invalid schedule", plan.ID)
		return
	}
	if _, err := s.planRepo.UpdateAfterRunIfUnchanged(ctx, plan.ID, plan.UpdatedAt, time.Now(), nextRun); err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d UpdateAfterRun failed", plan.ID)
	}
}

func (s *ScheduledTestRunnerService) tryRecoverAccount(ctx context.Context, accountID int64, planID int64) {
	if s.rateLimitSvc == nil {
		return
	}
	recovery, err := s.rateLimitSvc.RecoverAccountAfterSuccessfulTest(ctx, accountID)
	if err != nil {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover failed: %v", planID, err)
		return
	}
	if recovery == nil {
		return
	}
	if recovery.ClearedError {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d recovered from error status", planID, accountID)
	}
	if recovery.ClearedRateLimit {
		logger.LegacyPrintf("service.scheduled_test_runner", "[ScheduledTestRunner] plan=%d auto-recover: account=%d cleared rate-limit/runtime state", planID, accountID)
	}
}
