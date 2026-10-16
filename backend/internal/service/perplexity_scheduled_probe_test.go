//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type scheduledProbePlanRepo struct {
	ScheduledTestPlanRepository
	mu       sync.Mutex
	plan     *ScheduledTestPlan
	creates  int
	advances int
}

func (r *scheduledProbePlanRepo) GetByID(ctx context.Context, _ int64) (*ScheduledTestPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *r.plan
	return &copy, nil
}
func (r *scheduledProbePlanRepo) Create(_ context.Context, plan *ScheduledTestPlan) (*ScheduledTestPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *plan
	r.plan = &copy
	r.creates++
	return &copy, nil
}
func (r *scheduledProbePlanRepo) UpdateAfterRunIfUnchanged(ctx context.Context, _ int64, expected, last, next time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.plan.Enabled || !r.plan.UpdatedAt.Equal(expected) {
		return false, nil
	}
	r.plan.LastRunAt = &last
	r.plan.NextRunAt = &next
	r.plan.UpdatedAt = time.Now().UTC()
	r.advances++
	return true, nil
}

type scheduledProbeResultRepo struct {
	ScheduledTestResultRepository
	mu      sync.Mutex
	results []*ScheduledTestResult
}

func (r *scheduledProbeResultRepo) Create(ctx context.Context, result *ScheduledTestResult) (*ScheduledTestResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *result
	r.results = append(r.results, &copy)
	return &copy, nil
}
func (r *scheduledProbeResultRepo) PruneOldResults(context.Context, int64, int) error { return nil }

func scheduledProbePlan(accountID int64) *ScheduledTestPlan {
	past := time.Now().Add(-time.Minute)
	return &ScheduledTestPlan{ID: 9, AccountID: accountID, ModelID: "public", TestMode: AccountTestModePerplexityModel, CronExpression: "7 * * * *", Enabled: true, MaxResults: 50, NextRunAt: &past, UpdatedAt: time.Now().Add(-time.Hour)}
}

func TestPerplexityScheduledProbeIsExplicitAndStoresIdentity(t *testing.T) {
	for _, reported := range []string{"gpt54", "claude46sonnet", ""} {
		t.Run(reported, func(t *testing.T) {
			tests, upstream, account, _ := perplexityModelProbeFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, perplexityModelAnswer("OK", reported)+"event: end_of_stream\n\n")
			})
			plan := scheduledProbePlan(account.ID)
			plans := &scheduledProbePlanRepo{plan: plan}
			results := &scheduledProbeResultRepo{}
			svc := NewScheduledTestService(plans, results)
			runner := NewScheduledTestRunnerService(plans, svc, tests, nil, nil)
			runner.SetLeaderLock(&fakeLeaderLockCache{})
			defer runner.Stop()
			snapshot := *plan
			runner.runOnePlan(context.Background(), &snapshot)
			require.Equal(t, 1, upstream.count())
			require.Len(t, results.results, 1)
			result := results.results[0]
			if reported == "gpt54" {
				require.Equal(t, "success", result.Status)
			} else {
				require.Equal(t, "warning", result.Status)
			}
			probe := result.Metadata["perplexity_probe"].(*PerplexityModelProbeResult)
			require.Equal(t, "scheduled", probe.Source)
			require.Equal(t, reported, probe.Identity.ActualModel)
			require.Equal(t, 1, plans.advances)
			// A peer with the original due-list snapshot cannot execute it again.
			runner.runOnePlan(context.Background(), &snapshot)
			require.Equal(t, 1, upstream.count())
		})
	}
}

func TestPerplexityScheduledProbeDoesNotIgnorePauseOrCoordination(t *testing.T) {
	for _, change := range []string{"no_lock", "lock_busy", "disabled", "model_changed", "mode_changed", "future"} {
		t.Run(change, func(t *testing.T) {
			tests, upstream, account, _ := perplexityModelProbeFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected billable request") })
			plan := scheduledProbePlan(account.ID)
			snapshot := *plan
			plans := &scheduledProbePlanRepo{plan: plan}
			runner := NewScheduledTestRunnerService(plans, NewScheduledTestService(plans, &scheduledProbeResultRepo{}), tests, nil, nil)
			defer runner.Stop()
			locks := &fakeLeaderLockCache{}
			runner.SetLeaderLock(locks)
			switch change {
			case "no_lock":
				runner.SetLeaderLock(nil)
			case "lock_busy":
				locks.owners = map[string]string{"scheduled-test-plan:9": "peer"}
			case "disabled":
				plan.Enabled = false
			case "model_changed":
				plan.ModelID = "alternate"
			case "mode_changed":
				plan.TestMode = AccountTestModeDefault
			case "future":
				future := time.Now().Add(time.Hour)
				plan.NextRunAt = &future
			}
			runner.runOnePlan(context.Background(), &snapshot)
			require.Zero(t, upstream.count())
		})
	}
}

func TestPerplexityLegacyScheduledPlanRemainsSessionOnly(t *testing.T) {
	tests, upstream, account, repo := perplexityModelProbeFixture(t, servePerplexityGoodProfile)
	repo.accounts[account.ID].Status = StatusError
	repo.accounts[account.ID].ErrorMessage = "administrator disabled this account"
	plan := scheduledProbePlan(account.ID)
	plan.TestMode = AccountTestModeDefault
	plan.AutoRecover = true
	plans := &scheduledProbePlanRepo{plan: plan}
	results := &scheduledProbeResultRepo{}
	runner := NewScheduledTestRunnerService(plans, NewScheduledTestService(plans, results), tests, &RateLimitService{accountRepo: repo}, nil)
	runner.SetLeaderLock(&fakeLeaderLockCache{})
	defer runner.Stop()
	snapshot := *plan
	runner.runOnePlan(context.Background(), &snapshot)
	require.Equal(t, 3, upstream.count())
	for _, req := range upstream.requests {
		require.NotEqual(t, "/rest/sse/perplexity_ask", req.URL.Path)
	}
	current, err := repo.GetByID(context.Background(), account.ID)
	require.NoError(t, err)
	require.Equal(t, StatusError, current.Status)
	require.Equal(t, "administrator disabled this account", current.ErrorMessage)
}

func TestPerplexityScheduledProbeStopCancelsActiveRequest(t *testing.T) {
	entered := make(chan struct{})
	tests, _, account, _ := perplexityModelProbeFixture(t, func(w http.ResponseWriter, req *http.Request) {
		close(entered)
		<-req.Context().Done()
		w.WriteHeader(503)
	})
	plan := scheduledProbePlan(account.ID)
	plans := &scheduledProbePlanRepo{plan: plan}
	runner := NewScheduledTestRunnerService(plans, NewScheduledTestService(plans, &scheduledProbeResultRepo{}), tests, nil, nil)
	runner.SetLeaderLock(&fakeLeaderLockCache{})
	done := make(chan struct{})
	go func() { runner.runOnePlan(runner.rootCtx, plan); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	runner.Stop()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not cancel active model probe")
	}
}

func TestPerplexityScheduledModeValidationDoesNotUpgradeOldPlans(t *testing.T) {
	plans := &scheduledProbePlanRepo{}
	svc := NewScheduledTestService(plans, &scheduledProbeResultRepo{})
	legacy, err := svc.CreatePlan(context.Background(), &ScheduledTestPlan{AccountID: 1, CronExpression: "7 * * * *"})
	require.NoError(t, err)
	require.Equal(t, AccountTestModeDefault, legacy.TestMode)
	_, err = svc.CreatePlan(context.Background(), &ScheduledTestPlan{AccountID: 1, ModelID: "public", TestMode: AccountTestModePerplexityModel, CronExpression: "7 * * * *"})
	require.Error(t, err)
	_, _, account, repo := perplexityModelProbeFixture(t, servePerplexityGoodProfile)
	svc.SetAccountRepository(repo)
	plan := scheduledProbePlan(account.ID)
	plan.AutoRecover = true
	_, err = svc.CreatePlan(context.Background(), plan)
	require.Error(t, err)
	plan.AutoRecover = false
	_, err = svc.CreatePlan(context.Background(), plan)
	require.NoError(t, err)
}
