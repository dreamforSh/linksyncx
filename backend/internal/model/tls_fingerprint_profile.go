// Package model 定义服务层使用的数据模型。
package model

import (
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// TLSFingerprintProfile TLS 指纹配置模板
// 包含完整的 ClientHello 参数，用于模拟特定客户端的 TLS 握手特征
type TLSFingerprintProfile struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	Description         *string   `json:"description"`
	EnableGREASE        bool      `json:"enable_grease"`
	CipherSuites        []uint16  `json:"cipher_suites"`
	Curves              []uint16  `json:"curves"`
	PointFormats        []uint16  `json:"point_formats"`
	SignatureAlgorithms []uint16  `json:"signature_algorithms"`
	ALPNProtocols       []string  `json:"alpn_protocols"`
	SupportedVersions   []uint16  `json:"supported_versions"`
	KeyShareGroups      []uint16  `json:"key_share_groups"`
	PSKModes            []uint16  `json:"psk_modes"`
	Extensions          []uint16  `json:"extensions"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Validate 验证模板配置的有效性
//
// 除名称外还要求最终生效的 ClientHello 自洽（tlsfingerprint.ValidateProfile）：这样的模板在
// DoWithTLS 建池前一定会被拒绝，存进去只会让直接绑定它的账号每个请求都失败；“随机”模式只能
// 在加载时把它排除在候选之外（TLSFingerprintProfileService.setLocalCache），救不了直接绑定的账号。
// 目前唯一的检查是 key_share 必须是 supported_groups 的子集，
// 典型踩坑是自定义了 curves 却没填 key_share_groups，于是继承内置默认的 [4588, 29]，
// 所以错误挂在 key_share_groups 上，并提示空字段会回落到内置默认值。
// 例外是自定义 extensions 带了 key_share（51）却漏了 supported_groups（10）：curves 根本发不出去，
// 改 curves 无济于事，错误挂在 extensions 上并说明怎么补。
func (p *TLSFingerprintProfile) Validate() error {
	if p.Name == "" {
		return &ValidationError{Field: "name", Message: "name is required"}
	}
	if err := tlsfingerprint.ValidateProfile(p.ToTLSProfile()); err != nil {
		if errors.Is(err, tlsfingerprint.ErrKeyShareWithoutSupportedGroups) {
			return &ValidationError{
				Field:   "extensions",
				Message: err.Error() + "; add supported_groups (10) to extensions, or leave extensions empty to use the built-in order",
			}
		}
		return &ValidationError{
			Field:   "key_share_groups",
			Message: err.Error() + "; every key share group must also be listed in curves (empty fields fall back to the built-in defaults)",
		}
	}
	return nil
}

// ToTLSProfile 将领域模型转换为运行时使用的 tlsfingerprint.Profile
// 空切片字段会在 dialer 中 fallback 到内置默认值
func (p *TLSFingerprintProfile) ToTLSProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:                p.Name,
		EnableGREASE:        p.EnableGREASE,
		CipherSuites:        p.CipherSuites,
		Curves:              p.Curves,
		PointFormats:        p.PointFormats,
		SignatureAlgorithms: p.SignatureAlgorithms,
		ALPNProtocols:       p.ALPNProtocols,
		SupportedVersions:   p.SupportedVersions,
		KeyShareGroups:      p.KeyShareGroups,
		PSKModes:            p.PSKModes,
		Extensions:          p.Extensions,
	}
}
