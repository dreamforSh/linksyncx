//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/suite"
)

type ProxyExpirySuite struct {
	suite.Suite
	ctx  context.Context
	tx   *dbent.Tx
	repo *proxyRepository
}

func (s *ProxyExpirySuite) SetupTest() {
	s.ctx = context.Background()
	s.tx = testEntTx(s.T())
	s.repo = newProxyRepositoryWithSQL(s.tx.Client(), s.tx)
}
func TestProxyExpirySuite(t *testing.T) { suite.Run(t, new(ProxyExpirySuite)) }

func (s *ProxyExpirySuite) mkProxy(name, mode string, expiresAt *time.Time, backupID *int64) int64 {
	p := &service.Proxy{Name: name, Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, FallbackMode: mode, ExpiryWarnDays: 7,
		ExpiresAt: expiresAt, BackupProxyID: backupID}
	s.Require().NoError(s.repo.Create(s.ctx, p))
	return p.ID
}

func (s *ProxyExpirySuite) mkAccountWithProxy(proxyID int64) int64 {
	var id int64
	err := scanSingleRow(s.ctx, s.tx, `
		INSERT INTO accounts (name, platform, type, credentials, extra, status, proxy_id, created_at, updated_at)
		VALUES ($1,'claude','api','{}','{}','active',$2,NOW(),NOW()) RETURNING id`,
		[]any{"acc-" + time.Now().Format("150405.000000"), proxyID}, &id)
	s.Require().NoError(err)
	return id
}

func (s *ProxyExpirySuite) accountProxyID(id int64) *int64 {
	var pid *int64
	err := scanSingleRow(s.ctx, s.tx, `SELECT proxy_id FROM accounts WHERE id=$1`, []any{id}, &pid)
	s.Require().NoError(err)
	return pid
}

// 过期改投直连（fallback_mode=direct）已移除：数据库 CHECK 约束（迁移 251）拒绝写入该取值，
// 过期扫描也就不可能再把账号的 proxy_id 置空。
func (s *ProxyExpirySuite) TestLegacyDirectFallbackModeIsRejectedByDatabase() {
	past := time.Now().Add(-time.Hour)
	p := &service.Proxy{Name: "p-direct", Protocol: "http", Host: "127.0.0.1", Port: 8080,
		Status: service.StatusActive, FallbackMode: "direct", ExpiryWarnDays: 7, ExpiresAt: &past}
	s.Require().Error(s.repo.Create(s.ctx, p), "fallback_mode=direct must be rejected by proxies_fallback_mode_check")
}

func (s *ProxyExpirySuite) TestSweep_EnqueuesChangedAccountIDsWithoutFullRebuild() {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(24 * time.Hour)
	backupProxyID := s.mkProxy("p-bulk-backup", service.FallbackModeNone, &future, nil)
	firstProxyID := s.mkProxy("p-bulk-first", service.FallbackModeProxy, &past, &backupProxyID)
	secondProxyID := s.mkProxy("p-bulk-second", service.FallbackModeProxy, &past, &backupProxyID)
	firstAccountID := s.mkAccountWithProxy(firstProxyID)
	secondAccountID := s.mkAccountWithProxy(secondProxyID)

	changed, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Require().EqualValues(2, changed)

	var payloadRaw []byte
	err = scanSingleRow(s.ctx, s.tx, `
		SELECT payload
		FROM scheduler_outbox
		WHERE event_type=$1
		ORDER BY id DESC
		LIMIT 1`, []any{service.SchedulerOutboxEventAccountBulkChanged}, &payloadRaw)
	s.Require().NoError(err)

	var payload struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	s.Require().NoError(json.Unmarshal(payloadRaw, &payload))
	s.Require().Equal([]int64{firstAccountID, secondAccountID}, payload.AccountIDs)

	var fullRebuildCount int
	err = scanSingleRow(s.ctx, s.tx, `
		SELECT COUNT(*)
		FROM scheduler_outbox
		WHERE event_type=$1`, []any{service.SchedulerOutboxEventFullRebuild}, &fullRebuildCount)
	s.Require().NoError(err)
	s.Require().Zero(fullRebuildCount)
}

func (s *ProxyExpirySuite) TestSweep_ProxyMode_Healthy() {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	backup := s.mkProxy("p-backup", service.FallbackModeNone, &future, nil)
	pid := s.mkProxy("p-main", service.FallbackModeProxy, &past, &backup)
	aid := s.mkAccountWithProxy(pid)

	_, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	s.Require().Equal(backup, *s.accountProxyID(aid))
	var origin *int64
	err = scanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id=$1`, []any{aid}, &origin)
	s.Require().NoError(err)
	s.Require().NotNil(origin)
	s.Require().Equal(pid, *origin)
}

func (s *ProxyExpirySuite) TestSweep_NoneMode_KeepsAccount() {
	past := time.Now().Add(-time.Hour)
	pid := s.mkProxy("p-none", service.FallbackModeNone, &past, nil)
	aid := s.mkAccountWithProxy(pid)

	_, err := s.repo.SweepExpiredProxies(s.ctx, time.Now())
	s.Require().NoError(err)
	got, _ := s.repo.GetByID(s.ctx, pid)
	s.Require().Equal(service.StatusExpired, got.Status)
	s.Require().Equal(pid, *s.accountProxyID(aid))
	var origin *int64
	err = scanSingleRow(s.ctx, s.tx, `SELECT proxy_fallback_origin_id FROM accounts WHERE id=$1`, []any{aid}, &origin)
	s.Require().NoError(err)
	s.Require().Nil(origin)
}
