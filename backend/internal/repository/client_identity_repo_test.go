package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var clientIdentityColumnNames = []string{"account_id", "platform", "identity_epoch", "device_id", "owner_ref", "user_agent", "headers", "created_at", "updated_at"}

func clientIdentityRows(epoch int64, device, owner string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(clientIdentityColumnNames).
		AddRow(int64(7), service.PlatformAnthropic, epoch, device, owner, "claude-cli/2.1.287 (external, cli)", []byte(`{"os":"MacOS","arch":"arm64"}`), now, now)
}

func newClientIdentityRepoForTest(t *testing.T) (service.ClientIdentityStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewClientIdentityRepository(db), mock
}

func TestClientIdentityRepositoryGet(t *testing.T) {
	repo, mock := newClientIdentityRepoForTest(t)
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentitySelectSQL)).WithArgs(int64(7)).WillReturnRows(clientIdentityRows(2, "dev", "owner"))
	rec, err := repo.Get(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, int64(2), rec.IdentityEpoch)
	require.Equal(t, "MacOS", rec.Headers.OS)

	mock.ExpectQuery(regexp.QuoteMeta(clientIdentitySelectSQL)).WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows(clientIdentityColumnNames))
	rec, err = repo.Get(context.Background(), 8)
	require.NoError(t, err)
	require.Nil(t, rec, "a missing identity is (nil, nil)")
	require.NoError(t, mock.ExpectationsWereMet())
}

// 先写者胜：ON CONFLICT DO NOTHING 不返回行时，读回库中已有的记录。
func TestClientIdentityRepositoryInsertReturnsWinnerOnConflict(t *testing.T) {
	repo, mock := newClientIdentityRepoForTest(t)
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentityInsertSQL)).
		WithArgs(int64(7), service.PlatformAnthropic, int64(0), "mine", "owner", "ua", `{"os":"Linux"}`).
		WillReturnRows(sqlmock.NewRows(clientIdentityColumnNames))
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentitySelectSQL)).WithArgs(int64(7)).WillReturnRows(clientIdentityRows(0, "winner", "owner"))

	rec, err := repo.Insert(context.Background(), &service.ClientIdentityRecord{AccountID: 7, Platform: service.PlatformAnthropic,
		DeviceID: "mine", OwnerRef: "owner", UserAgent: "ua", Headers: service.ClientIdentityHeaders{OS: "Linux"}})
	require.NoError(t, err)
	require.Equal(t, "winner", rec.DeviceID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// 代次或设备已变时 UPDATE 不命中，返回库中当前记录。
func TestClientIdentityRepositoryUpdateHeadersIsConditional(t *testing.T) {
	repo, mock := newClientIdentityRepoForTest(t)
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentityUpdateHeadersSQL)).
		WithArgs(int64(7), int64(0), "old-dev", "new-ua", `{"os":"MacOS"}`, "owner").
		WillReturnRows(sqlmock.NewRows(clientIdentityColumnNames))
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentitySelectSQL)).WithArgs(int64(7)).WillReturnRows(clientIdentityRows(1, "rotated-dev", "owner"))

	rec, err := repo.UpdateHeaders(context.Background(), 7, 0, "old-dev", "owner", "new-ua", service.ClientIdentityHeaders{OS: "MacOS"})
	require.NoError(t, err)
	require.Equal(t, int64(1), rec.IdentityEpoch)
	require.Equal(t, "rotated-dev", rec.DeviceID)
	require.NoError(t, mock.ExpectationsWereMet())

	sql := normalizeSQLWhitespace(clientIdentityUpdateHeadersSQL)
	require.Contains(t, sql, "WHERE account_id = $1 AND identity_epoch = $2 AND device_id = $3")
	require.Contains(t, sql, "owner_ref = CASE WHEN owner_ref = '' THEN $6 ELSE owner_ref END", "an existing owner is never overwritten")
}

func TestClientIdentityRepositoryRotate(t *testing.T) {
	repo, mock := newClientIdentityRepoForTest(t)
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentityRotateSQL)).
		WithArgs(int64(7), service.PlatformAnthropic, int64(3), "new-dev", "owner-b", "ua", `{}`).
		WillReturnRows(clientIdentityRows(4, "new-dev", "owner-b"))
	rec, err := repo.Rotate(context.Background(), 3, &service.ClientIdentityRecord{AccountID: 7, Platform: service.PlatformAnthropic,
		DeviceID: "new-dev", OwnerRef: "owner-b", UserAgent: "ua"})
	require.NoError(t, err)
	require.Equal(t, int64(4), rec.IdentityEpoch)

	// 已被其它实例轮换：WHERE 不成立，读回当前记录。
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentityRotateSQL)).WillReturnRows(sqlmock.NewRows(clientIdentityColumnNames))
	mock.ExpectQuery(regexp.QuoteMeta(clientIdentitySelectSQL)).WithArgs(int64(7)).WillReturnRows(clientIdentityRows(5, "other-dev", "owner-b"))
	rec, err = repo.Rotate(context.Background(), 4, &service.ClientIdentityRecord{AccountID: 7, Platform: service.PlatformAnthropic, DeviceID: "mine"})
	require.NoError(t, err)
	require.Equal(t, "other-dev", rec.DeviceID)
	require.NoError(t, mock.ExpectationsWereMet())

	sql := normalizeSQLWhitespace(clientIdentityRotateSQL)
	require.Contains(t, sql, "VALUES ($1, $2, $3::bigint + 1,")
	require.Contains(t, sql, "identity_epoch = account_client_identities.identity_epoch + 1")
	require.Contains(t, sql, "WHERE account_client_identities.identity_epoch = $3::bigint")
}
