package tlsfingerprint

import (
	"fmt"

	utls "github.com/refraction-networking/utls"
)

// ValidateProfile 校验 profile 最终生效的 ClientHello 是否自洽（空字段按内置默认值补齐后再查）。
//
// 目前只检查 key_share 中的每个组都出现在 supported_groups 里：TLS 1.3 规定 key_share 必须是
// supported_groups 的子集，违反的 ClientHello 既不像任何真实客户端，又可能被上游直接拒绝。
// 典型的踩坑是自定义了 curves（如 Node 的 [29, 23, 30, ...]）却没写 key_share_groups，
// 于是 key_share 继承内置默认的 [X25519MLKEM768, X25519]，而 4588 并不在 curves 里。
// nil profile（不伪装）与全默认 profile 都合法。
func ValidateProfile(profile *Profile) error {
	spec := buildClientHelloSpecFromProfile(profile)
	groups := map[utls.CurveID]bool{}
	var shares []utls.KeyShare
	for _, extension := range spec.Extensions {
		switch ext := extension.(type) {
		case *utls.SupportedCurvesExtension:
			for _, group := range ext.Curves {
				groups[group] = true
			}
		case *utls.KeyShareExtension:
			shares = ext.KeyShares
		}
	}
	for _, share := range shares {
		if !isGREASEValue(uint16(share.Group)) && !groups[share.Group] {
			return fmt.Errorf("TLS key share %d is absent from supported groups", share.Group)
		}
	}
	return nil
}
