package service

import (
	"context"
	"time"
)

// ClientIdentityRecord 是账号客户端身份的持久化形态（account_client_identities 表）。
// 数据库是身份的真相源，Redis 指纹键只是它的缓存。
type ClientIdentityRecord struct {
	AccountID     int64
	Platform      string
	IdentityEpoch int64
	DeviceID      string
	// OwnerRef 是身份所属的上游账号主人（Claude 为 extra.account_uuid）；空表示未知。
	OwnerRef  string
	UserAgent string
	Headers   ClientIdentityHeaders
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ClientIdentityHeaders 是学习到的 X-Stainless-* 头，以 JSON 对象存入 headers 列。
type ClientIdentityHeaders struct {
	Lang           string `json:"lang,omitempty"`
	PackageVersion string `json:"package_version,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
}

// ClientIdentityStore 持久化账号客户端身份。三个写方法都是条件写，条件不满足时不写，
// 返回库中的当前记录，由调用方以库为准；记录不存在时返回 (nil, nil)。
type ClientIdentityStore interface {
	// Get 读取账号身份；不存在返回 (nil, nil)。
	Get(ctx context.Context, accountID int64) (*ClientIdentityRecord, error)
	// Insert 首建身份，先写者胜：已有记录时不覆盖，返回库中的记录。
	Insert(ctx context.Context, rec *ClientIdentityRecord) (*ClientIdentityRecord, error)
	// UpdateHeaders 只在 identity_epoch 与 device_id 都未变时更新 UA 与头，owner_ref 为空时顺带补上
	// （不会改写已有主人：换主人走 Rotate）。
	UpdateHeaders(ctx context.Context, accountID, identityEpoch int64, deviceID, ownerRef, userAgent string, headers ClientIdentityHeaders) (*ClientIdentityRecord, error)
	// Rotate 只在 identity_epoch 仍等于 expectedEpoch 时把身份换成 next 描述的新设备：epoch + 1，
	// 写入 next 的 device_id、owner_ref、UA 与头（忽略 next.IdentityEpoch）；记录不存在时直接以
	// expectedEpoch + 1 建立。
	Rotate(ctx context.Context, expectedEpoch int64, next *ClientIdentityRecord) (*ClientIdentityRecord, error)
}

// fingerprintFromRecord 把库中身份转成缓存 / 请求使用的指纹。UpdatedAt 是缓存语义
// （上次写缓存的时间，决定何时续期），取当前时间。
func fingerprintFromRecord(rec *ClientIdentityRecord) *Fingerprint {
	return &Fingerprint{
		ClientID:                rec.DeviceID,
		UserAgent:               rec.UserAgent,
		StainlessLang:           rec.Headers.Lang,
		StainlessPackageVersion: rec.Headers.PackageVersion,
		StainlessOS:             rec.Headers.OS,
		StainlessArch:           rec.Headers.Arch,
		StainlessRuntime:        rec.Headers.Runtime,
		StainlessRuntimeVersion: rec.Headers.RuntimeVersion,
		UpdatedAt:               time.Now().Unix(),
		IdentityEpoch:           rec.IdentityEpoch,
		OwnerRef:                rec.OwnerRef,
		Persisted:               true,
	}
}

func clientIdentityHeadersOf(fp *Fingerprint) ClientIdentityHeaders {
	return ClientIdentityHeaders{
		Lang:           fp.StainlessLang,
		PackageVersion: fp.StainlessPackageVersion,
		OS:             fp.StainlessOS,
		Arch:           fp.StainlessArch,
		Runtime:        fp.StainlessRuntime,
		RuntimeVersion: fp.StainlessRuntimeVersion,
	}
}

// clientIdentityRecordFrom 把指纹转成待入库的身份记录（IdentityService 目前只服务 Claude 账号）。
func clientIdentityRecordFrom(accountID int64, fp *Fingerprint) *ClientIdentityRecord {
	return &ClientIdentityRecord{
		AccountID:     accountID,
		Platform:      PlatformAnthropic,
		IdentityEpoch: fp.IdentityEpoch,
		DeviceID:      fp.ClientID,
		OwnerRef:      fp.OwnerRef,
		UserAgent:     fp.UserAgent,
		Headers:       clientIdentityHeadersOf(fp),
	}
}
