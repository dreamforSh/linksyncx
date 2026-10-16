package service

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	perplexityMaintenanceConcurrency = 4
	perplexityMaintenanceTimeout     = 45 * time.Second
)

// PerplexityMaintenanceService refreshes independently due profile sections,
// including quota-paused accounts. It never invokes a billable model request.
type PerplexityMaintenanceService struct {
	repo     AccountRepository
	tests    *AccountTestService
	locks    LeaderLockCache
	settings config.GatewayPerplexityConfig
	owner    string
	mu       sync.Mutex
	started  bool
	stopped  bool
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewPerplexityMaintenanceService(repo AccountRepository, tests *AccountTestService, locks LeaderLockCache, cfg *config.Config) *PerplexityMaintenanceService {
	settings := config.GatewayPerplexityConfig{}
	if cfg != nil {
		settings = cfg.Gateway.Perplexity
	}
	if settings.SessionKeepaliveHours <= 0 {
		settings.SessionKeepaliveHours = 6
	}
	if settings.QuotaRefreshMinutes <= 0 {
		settings.QuotaRefreshMinutes = 5
	}
	return &PerplexityMaintenanceService{repo: repo, tests: tests, locks: locks, settings: settings, owner: uuid.NewString()}
}

func (s *PerplexityMaintenanceService) Start() {
	if s == nil || s.repo == nil || s.tests == nil || !s.settings.MaintenanceEnabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopped {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.started = cancel, true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		s.runOnce(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runOnce(ctx)
			}
		}
	}()
}

func (s *PerplexityMaintenanceService) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *PerplexityMaintenanceService) runOnce(ctx context.Context) {
	accounts, err := s.repo.ListByPlatform(ctx, PlatformPerplexity)
	if err != nil {
		logger.L().Warn("perplexity: list maintenance accounts failed")
		return
	}
	sem := make(chan struct{}, perplexityMaintenanceConcurrency)
	var workers sync.WaitGroup
	defer workers.Wait()
	for i := range accounts {
		account := &accounts[i]
		if !perplexityMaintenanceEligible(account) {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		workers.Add(1)
		go func(account *Account) {
			defer workers.Done()
			defer func() { <-sem }()
			s.maintainAccount(ctx, account)
		}(account)
	}
}

func perplexityMaintenanceEligible(account *Account) bool {
	if account == nil || !account.IsPerplexity() || account.Type != AccountTypeAPIKey || account.GetPerplexitySessionToken() == "" {
		return false
	}
	// A manual scheduling pause is not a credential revocation. Continue probing
	// it, but never change schedulable or recover another subsystem's error.
	return account.Status == StatusActive || account.Status == StatusError && account.ErrorMessage == PerplexityInvalidSessionMessage
}

func (s *PerplexityMaintenanceService) maintainAccount(ctx context.Context, account *Account) {
	ctx, cancel := context.WithTimeout(ctx, perplexityMaintenanceTimeout)
	defer cancel()
	if s.locks != nil {
		key := "perplexity:maintenance:" + strconv.FormatInt(account.ID, 10)
		acquired, err := s.locks.TryAcquireLeaderLock(ctx, key, s.owner, perplexityMaintenanceTimeout+15*time.Second)
		if err != nil || !acquired {
			return
		}
		defer func() {
			releaseCtx, release := context.WithTimeout(context.Background(), 2*time.Second)
			defer release()
			_ = s.locks.ReleaseLeaderLock(releaseCtx, key, s.owner)
		}()
	}
	fresh, err := freshPerplexityAccount(ctx, s.repo, account)
	if err != nil || !perplexityMaintenanceEligible(fresh) {
		return
	}
	sections := s.dueSections(fresh, time.Now())
	if len(sections) == 0 {
		return
	}
	snapshot := s.tests.refreshPerplexityAccountInfo(ctx, fresh, sections)
	profile := perplexityProfileFromSnapshot(snapshot)
	for _, name := range sections {
		if profile.Sections[name].Status != "success" {
			logger.L().Debug("perplexity: maintenance section unavailable", zap.Int64("account_id", account.ID), zap.String("section", name))
		}
	}
}

func (s *PerplexityMaintenanceService) dueSections(account *Account, now time.Time) []string {
	profile := perplexityProfileFromSnapshot(account.Extra[perplexityQuotaSnapshotExtraKey])
	if profile.IdentityFingerprint == "" || profile.IdentityFingerprint != perplexityAccountFingerprint(account) {
		return append([]string(nil), perplexityInfoSections[:]...)
	}
	var due []string
	for _, name := range perplexityInfoSections {
		section := profile.Sections[name]
		interval := time.Duration(s.settings.QuotaRefreshMinutes) * time.Minute
		if name == "session" && section.Status == "success" {
			interval = time.Duration(s.settings.SessionKeepaliveHours) * time.Hour
		}
		last := section.AttemptedAt
		if last == nil || now.Before(*last) || now.Sub(*last) >= interval {
			due = append(due, name)
		}
	}
	return due
}
