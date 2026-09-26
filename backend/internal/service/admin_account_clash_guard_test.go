//go:build unit

package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type recordingClashGuard struct {
	checks  []ClashBindingCheck
	err     error
	changed int
	unlocks int
}

func (g *recordingClashGuard) CheckAccountBinding(_ context.Context, check ClashBindingCheck) (func(), error) {
	g.checks = append(g.checks, check)
	if g.err != nil {
		return func() {}, g.err
	}
	return func() { g.unlocks++ }, nil
}

func (g *recordingClashGuard) OnBindingsChanged(context.Context) { g.changed++ }

type clashGuardAccountRepo struct {
	*upstreamBillingProbeAccountRepo
}

func (r *clashGuardAccountRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func TestUpdateAccountConsultsClashGuardOnProxyChange(t *testing.T) {
	accountID, oldProxy, newProxy := int64(501), int64(7), int64(9)
	repo := &clashGuardAccountRepo{&upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {ID: accountID, Platform: PlatformAnthropic, Type: AccountTypeAPIKey, Status: StatusActive, ProxyID: &oldProxy},
	}}}
	guard := &recordingClashGuard{}
	svc := &adminServiceImpl{accountRepo: repo, clashGuard: guard}
	ctx := context.Background()

	_, err := svc.UpdateAccount(ctx, accountID, &UpdateAccountInput{ProxyID: &newProxy})
	require.NoError(t, err)
	require.Len(t, guard.checks, 1)
	check := guard.checks[0]
	require.Equal(t, []int64{accountID}, check.AccountIDs)
	require.Equal(t, oldProxy, *check.OldProxyID)
	require.Equal(t, newProxy, *check.NewProxyID)
	require.False(t, check.Bulk)
	require.Equal(t, 1, guard.unlocks, "the guard lock is released after persisting")
	require.Equal(t, 1, guard.changed, "binding changes trigger reconciliation")

	// Unrelated edits still pass through the guard (custom relay check) but do
	// not signal a binding change.
	notes := "note"
	_, err = svc.UpdateAccount(ctx, accountID, &UpdateAccountInput{Notes: &notes})
	require.NoError(t, err)
	require.Len(t, guard.checks, 2)
	require.Equal(t, 1, guard.changed)

	guard.err = infraerrors.Conflict(ClashErrCodeExitOccupied, "occupied")
	_, err = svc.UpdateAccount(ctx, accountID, &UpdateAccountInput{ProxyID: &oldProxy})
	require.Equal(t, ClashErrCodeExitOccupied, infraerrors.Reason(err))
	stored, err := repo.GetByID(ctx, accountID)
	require.NoError(t, err)
	require.Equal(t, newProxy, *stored.ProxyID, "a rejected binding is not persisted")
}

func TestBulkUpdateRejectsClashExitsThroughGuard(t *testing.T) {
	proxyID := int64(9)
	repo := &accountRepoStubForBulkUpdate{
		getByIDsAccounts: []*Account{{ID: 1, Platform: PlatformAnthropic}, {ID: 2, Platform: PlatformAnthropic}},
	}
	guard := &recordingClashGuard{err: infraerrors.BadRequest(ClashErrCodeBulkUnsupported, "bulk")}
	svc := &adminServiceImpl{accountRepo: repo, clashGuard: guard}

	_, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, ProxyID: &proxyID})
	require.Equal(t, ClashErrCodeBulkUnsupported, infraerrors.Reason(err))
	require.Len(t, guard.checks, 1)
	require.True(t, guard.checks[0].Bulk)
	require.Equal(t, []int64{1, 2}, guard.checks[0].AccountIDs)
	require.Zero(t, repo.bulkUpdateCalls, "nothing is written when the guard refuses")

	// Clearing the proxy (0) never consults the guard.
	guard.err = nil
	clear := int64(0)
	_, err = svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{AccountIDs: []int64{1, 2}, ProxyID: &clear})
	require.NoError(t, err)
	require.Len(t, guard.checks, 1)
	require.Equal(t, 1, guard.changed)
}
