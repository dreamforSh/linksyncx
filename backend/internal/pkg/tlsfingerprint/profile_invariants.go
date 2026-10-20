package tlsfingerprint

import (
	"fmt"
	"strconv"

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

// ProfileCacheKey 返回 profile 线级配置（决定 ClientHello 的全部字段）的摘要，拼进上游连接池的缓存键。
//
// 连接池按缓存键复用 transport（连同 dialer 与 TLS 会话票据缓存）：模板被编辑、账号改绑模板后
// ClientHello 变了，键也必须跟着变，否则持续有流量的账号永远命中旧 transport，新模板要等重启
// 或空闲回收才生效。Name 只是展示名，不进入摘要，改名不重建连接池。
//
// 每个 DoWithTLS 请求都会算一次，所以直接把字段喂进 FNV-1a 64，不经 JSON 编码、不做临时分配；
// 键只在进程内比较、不受外部输入左右，抗碰撞性无关紧要。每个字段先写字段标记和长度：字段顺序
// 固定，长度前缀已足以让编码无歧义，不同字段里放相同取值（如 curves 与 key_share_groups 都是 [29]）
// 摘要也不同；字段标记只是冗余的第二道分隔，防止日后有字段漏写长度时与相邻字段混淆。
// nil 与全默认 profile 的 ClientHello 相同，摘要也相同。
//
// 给 Profile 新增线级字段时必须同步加到这里，TestProfileCacheKeyCoversEveryWireField 会用反射兜底。
func ProfileCacheKey(profile *Profile) string {
	var p Profile
	if profile != nil {
		p = *profile
	}
	d := profileDigest(fnvOffset64)
	if p.EnableGREASE {
		d.writeByte(1)
	} else {
		d.writeByte(0)
	}
	d.writeUint16s(1, p.CipherSuites)
	d.writeUint16s(2, p.Curves)
	d.writeUint16s(3, p.PointFormats)
	d.writeUint16s(4, p.SignatureAlgorithms)
	d.writeUint16s(5, p.SupportedVersions)
	d.writeUint16s(6, p.KeyShareGroups)
	d.writeUint16s(7, p.PSKModes)
	d.writeUint16s(8, p.Extensions)
	d.writeStrings(9, p.ALPNProtocols)
	return strconv.FormatUint(uint64(d), 16)
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// profileDigest 是逐字节累加的 FNV-1a 64 状态，避免 hash.Hash 接口带来的分配。
type profileDigest uint64

func (d *profileDigest) writeByte(b byte) {
	*d = (*d ^ profileDigest(b)) * fnvPrime64
}

func (d *profileDigest) writeLen(n int) {
	v := uint64(n)
	for i := 0; i < 8; i++ {
		d.writeByte(byte(v >> (8 * i)))
	}
}

func (d *profileDigest) writeUint16s(field byte, values []uint16) {
	d.writeByte(field)
	d.writeLen(len(values))
	for _, v := range values {
		d.writeByte(byte(v >> 8))
		d.writeByte(byte(v))
	}
}

func (d *profileDigest) writeStrings(field byte, values []string) {
	d.writeByte(field)
	d.writeLen(len(values))
	for _, s := range values {
		d.writeLen(len(s))
		for i := 0; i < len(s); i++ {
			d.writeByte(s[i])
		}
	}
}
