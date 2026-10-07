package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// clientIdentityRepository 持久化账号客户端身份（account_client_identities）。
// 写方法都是条件写：条件不满足时 RETURNING 不返回行，改读库中当前记录交给调用方裁定。
type clientIdentityRepository struct {
	db *sql.DB
}

func NewClientIdentityRepository(db *sql.DB) service.ClientIdentityStore {
	return &clientIdentityRepository{db: db}
}

const clientIdentityColumns = `account_id, platform, identity_epoch, device_id, owner_ref, user_agent, headers, created_at, updated_at`

const (
	clientIdentitySelectSQL = `SELECT ` + clientIdentityColumns + ` FROM account_client_identities WHERE account_id = $1`

	clientIdentityInsertSQL = `INSERT INTO account_client_identities
	(account_id, platform, identity_epoch, device_id, owner_ref, user_agent, headers, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, NOW(), NOW())
ON CONFLICT (account_id) DO NOTHING
RETURNING ` + clientIdentityColumns

	// 只在代次与设备都未变时更新；owner_ref 只补空值，不改写已有主人（换主人走 Rotate）。
	clientIdentityUpdateHeadersSQL = `UPDATE account_client_identities
SET user_agent = $4,
	headers = $5::jsonb,
	owner_ref = CASE WHEN owner_ref = '' THEN $6 ELSE owner_ref END,
	updated_at = NOW()
WHERE account_id = $1 AND identity_epoch = $2 AND device_id = $3
RETURNING ` + clientIdentityColumns

	// 记录不存在时以 expected + 1 建立；存在且代次仍为 expected 时 epoch + 1 并换成新设备；
	// 代次已变（其它实例已轮换）时 WHERE 不成立、不写。
	clientIdentityRotateSQL = `INSERT INTO account_client_identities
	(account_id, platform, identity_epoch, device_id, owner_ref, user_agent, headers, created_at, updated_at)
VALUES ($1, $2, $3::bigint + 1, $4, $5, $6, $7::jsonb, NOW(), NOW())
ON CONFLICT (account_id) DO UPDATE SET
	identity_epoch = account_client_identities.identity_epoch + 1,
	device_id = EXCLUDED.device_id,
	owner_ref = EXCLUDED.owner_ref,
	user_agent = EXCLUDED.user_agent,
	headers = EXCLUDED.headers,
	updated_at = NOW()
WHERE account_client_identities.identity_epoch = $3::bigint
RETURNING ` + clientIdentityColumns
)

type clientIdentityRowScanner interface {
	Scan(dest ...any) error
}

func scanClientIdentity(row clientIdentityRowScanner) (*service.ClientIdentityRecord, error) {
	var rec service.ClientIdentityRecord
	var headers []byte
	if err := row.Scan(&rec.AccountID, &rec.Platform, &rec.IdentityEpoch, &rec.DeviceID, &rec.OwnerRef,
		&rec.UserAgent, &headers, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		if err := json.Unmarshal(headers, &rec.Headers); err != nil {
			return nil, fmt.Errorf("decode client identity headers for account %d: %w", rec.AccountID, err)
		}
	}
	return &rec, nil
}

func encodeClientIdentityHeaders(headers service.ClientIdentityHeaders) (string, error) {
	encoded, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *clientIdentityRepository) Get(ctx context.Context, accountID int64) (*service.ClientIdentityRecord, error) {
	rec, err := scanClientIdentity(r.db.QueryRowContext(ctx, clientIdentitySelectSQL, accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rec, err
}

func (r *clientIdentityRepository) Insert(ctx context.Context, rec *service.ClientIdentityRecord) (*service.ClientIdentityRecord, error) {
	if rec == nil || rec.DeviceID == "" {
		return nil, errors.New("client identity requires a device id")
	}
	headers, err := encodeClientIdentityHeaders(rec.Headers)
	if err != nil {
		return nil, err
	}
	stored, err := scanClientIdentity(r.db.QueryRowContext(ctx, clientIdentityInsertSQL,
		rec.AccountID, rec.Platform, rec.IdentityEpoch, rec.DeviceID, rec.OwnerRef, rec.UserAgent, headers))
	if errors.Is(err, sql.ErrNoRows) {
		return r.Get(ctx, rec.AccountID)
	}
	return stored, err
}

func (r *clientIdentityRepository) UpdateHeaders(ctx context.Context, accountID, identityEpoch int64, deviceID, ownerRef, userAgent string, headers service.ClientIdentityHeaders) (*service.ClientIdentityRecord, error) {
	encoded, err := encodeClientIdentityHeaders(headers)
	if err != nil {
		return nil, err
	}
	stored, err := scanClientIdentity(r.db.QueryRowContext(ctx, clientIdentityUpdateHeadersSQL,
		accountID, identityEpoch, deviceID, userAgent, encoded, ownerRef))
	if errors.Is(err, sql.ErrNoRows) {
		return r.Get(ctx, accountID)
	}
	return stored, err
}

func (r *clientIdentityRepository) Rotate(ctx context.Context, expectedEpoch int64, next *service.ClientIdentityRecord) (*service.ClientIdentityRecord, error) {
	if next == nil || next.DeviceID == "" {
		return nil, errors.New("client identity requires a device id")
	}
	headers, err := encodeClientIdentityHeaders(next.Headers)
	if err != nil {
		return nil, err
	}
	stored, err := scanClientIdentity(r.db.QueryRowContext(ctx, clientIdentityRotateSQL,
		next.AccountID, next.Platform, expectedEpoch, next.DeviceID, next.OwnerRef, next.UserAgent, headers))
	if errors.Is(err, sql.ErrNoRows) {
		return r.Get(ctx, next.AccountID)
	}
	return stored, err
}
