package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/google/uuid"
)

// monitorPerplexityProber reuses the account-native Cookie/TLS/proxy sender.
// It must honor cancellation and return only bounded response text.
type monitorPerplexityProber interface {
	probePerplexityModel(context.Context, *Account, string, string, string) (*PerplexityModelProbeResult, error)
}

// SetPerplexityProbe wires the native executor and fail-closed paid-probe lock.
func (s *ChannelMonitorService) SetPerplexityProbe(prober monitorPerplexityProber, locks LeaderLockCache) {
	s.perplexityProber = prober
	s.perplexityLocks = locks
}

func defaultMonitorCreateCheckMode(provider, mode string) string {
	if provider == MonitorProviderPerplexity && strings.TrimSpace(mode) == "" {
		return MonitorCheckModeQuota
	}
	return defaultCheckMode(mode)
}

func validatePerplexityRequestConfig(endpoint, apiKey string, templateID *int64, headers map[string]string, bodyMode string, body map[string]any) error {
	if strings.TrimSpace(endpoint) != "" || strings.TrimSpace(apiKey) != "" || templateID != nil || len(headers) != 0 || defaultBodyMode(bodyMode) != MonitorBodyOverrideModeOff || len(body) != 0 {
		return ErrChannelMonitorPerplexityConfig
	}
	return nil
}

func (s *ChannelMonitorService) loadPerplexityMonitorAccount(ctx context.Context, accountID *int64) (*Account, error) {
	if accountID == nil || *accountID <= 0 || s.quotaFetcher == nil {
		return nil, ErrChannelMonitorAccountRequired
	}
	account, err := s.quotaFetcher.LoadAccount(ctx, *accountID)
	if err != nil || account == nil {
		return nil, ErrChannelMonitorAccountRequired
	}
	if account.Platform != PlatformPerplexity {
		return nil, ErrChannelMonitorProviderIncompatible
	}
	if err := monitorAccountQuotaCapability(account); err != nil {
		return nil, err
	}
	return account, nil
}

func (s *ChannelMonitorService) validatePerplexityMonitorAccount(ctx context.Context, mode string, accountID *int64, primary string, extra []string) error {
	account, err := s.loadPerplexityMonitorAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if mode == MonitorCheckModeQuota {
		return nil
	}
	if s.perplexityProber == nil || s.perplexityLocks == nil {
		return ErrChannelMonitorPerplexityUnavailable
	}
	return validatePerplexityMonitorModels(account, primary, extra)
}

func validatePerplexityMonitorModels(account *Account, primary string, extra []string) error {
	for _, model := range append([]string{primary}, normalizeModels(extra)...) {
		model = strings.TrimSpace(model)
		if model == "" || model == MonitorDefaultQuotaModel || len(model) > 200 || !account.IsModelSupported(model) {
			return ErrChannelMonitorPerplexityModel
		}
		if _, _, err := perplexity.ResolveModelPreferenceChecked(account.GetMappedModel(model)); err != nil {
			return ErrChannelMonitorPerplexityModel
		}
	}
	return nil
}

// Native probes are bounded below the lock TTL, including persistence and quota
// collection. Never fall back to an unlocked or direct HTTP request.
func (s *ChannelMonitorService) runPerplexityCheck(ctx context.Context, id int64) ([]*CheckResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.perplexityProber == nil || s.perplexityLocks == nil {
		return nil, ErrChannelMonitorPerplexityUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, monitorRequestTimeout)
	defer cancel()
	key, owner := fmt.Sprintf("perplexity:channel-monitor:%d", id), uuid.NewString()
	acquired, err := s.perplexityLocks.TryAcquireLeaderLock(ctx, key, owner, 2*time.Minute)
	if err != nil || !acquired {
		return nil, ErrChannelMonitorPerplexityUnavailable
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer releaseCancel()
		_ = s.perplexityLocks.ReleaseLeaderLock(releaseCtx, key, owner)
	}()

	// Both manual checks and the runner re-read after acquiring the same lock.
	m, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.probeRuntime(ctx).ActiveProbesAllowed() {
		return nil, ErrChannelMonitorActiveProbesRetired
	}
	if m.Provider != MonitorProviderPerplexity || defaultCheckMode(m.CheckMode) == MonitorCheckModeQuota {
		return nil, ErrChannelMonitorPerplexityUnavailable
	}
	if err := validatePerplexityRequestConfig(m.Endpoint, "", m.TemplateID, m.ExtraHeaders, m.BodyOverrideMode, m.BodyOverride); err != nil {
		return nil, err
	}
	account, err := s.loadPerplexityMonitorAccount(ctx, m.AccountID)
	if err != nil {
		return nil, err
	}
	if err := validatePerplexityMonitorModels(account, m.PrimaryModel, m.ExtraModels); err != nil {
		return nil, err
	}
	models := append([]string{m.PrimaryModel}, m.ExtraModels...)
	results := make([]*CheckResult, len(models))
	var quota *domain.MonitorQuotaSnapshot
	var wg sync.WaitGroup
	if m.CheckMode == MonitorCheckModeQuotaProbe {
		wg.Go(func() { quota = s.fetchQuotaSnapshot(ctx, m) })
	}
	for i, model := range models {
		wg.Go(func() { results[i] = s.checkPerplexityModel(ctx, account, model) })
	}
	wg.Wait()
	attachQuotaSnapshot(results, quota)
	s.persistCheckResults(ctx, m, results)
	return results, nil
}

func (s *ChannelMonitorService) checkPerplexityModel(ctx context.Context, account *Account, model string) *CheckResult {
	res := &CheckResult{Model: model, CheckedAt: time.Now(), Status: MonitorStatusError}
	challenge := generateChallenge()
	start := time.Now()
	probe, err := s.perplexityProber.probePerplexityModel(ctx, copyPerplexityAccount(account), model, challenge.Prompt, "channel_monitor")
	latency := time.Since(start)
	ms := int(latency.Milliseconds())
	res.LatencyMs = &ms
	if err != nil || probe == nil || !probe.Success {
		// Do not publish arbitrary error text or raw answer bodies in shared history.
		res.Message = "perplexity model probe could not be completed"
		if probe != nil {
			res.Message = "perplexity model probe: " + sanitizeErrorMessage(probe.ErrorCode)
			if probe.ErrorCode == "unauthenticated" {
				res.Status = MonitorStatusFailed
			}
		}
		return res
	}
	if !validateChallenge(probe.ResponseText, challenge.Expected) {
		res.Status, res.Message = MonitorStatusFailed, "perplexity challenge mismatch"
		return res
	}
	if !probe.Verified || probe.Identity.Status != "matched" || probe.Persistence != "stored" || probe.Status == "stale" {
		res.Status, res.Message = MonitorStatusDegraded, "perplexity answer received; model identity or current-account persistence was not verified"
		return res
	}
	return finalizeOperationalOrDegraded(res, latency, ms)
}
