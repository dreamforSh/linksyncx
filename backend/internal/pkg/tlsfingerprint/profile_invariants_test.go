package tlsfingerprint

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

// 自定义 curves 却没有写 key_share_groups 时，key_share 继承默认的 [4588, 29]，与 curves 不自洽：
// 直连与经代理的 DialTLSContext 都要在建连之前拒绝，而不是发出一个不像任何客户端的 ClientHello。
func TestValidateProfileRejectsKeyShareOutsideSupportedGroupsBeforeDial(t *testing.T) {
	calls := 0
	dialer := NewDialer(&Profile{Curves: []uint16{29}}, func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("network must not run")
	})
	_, err := dialer.DialTLSContext(t.Context(), "tcp", "example.invalid:443")
	require.ErrorContains(t, err, "key share")
	require.Zero(t, calls)

	require.NoError(t, ValidateProfile(&Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29}}))
	require.NoError(t, ValidateProfile(nil))
	require.NoError(t, ValidateProfile(&Profile{}))
	require.ErrorContains(t, ValidateProfile(&Profile{Curves: []uint16{29}, KeyShareGroups: []uint16{29, 23}}), "key share 23")

	for _, raw := range []string{"http://proxy.invalid:8080", "https://proxy.invalid:443", "socks5h://proxy.invalid:1080"} {
		t.Run(raw, func(t *testing.T) {
			proxyURL, err := url.Parse(raw)
			require.NoError(t, err)
			dialer, err := newProxyDialer(&Profile{Curves: []uint16{29}}, proxyURL, DialOptions{}, func(context.Context, string, string) (net.Conn, error) {
				calls++
				return nil, errors.New("network must not run")
			})
			require.NoError(t, err)
			_, err = dialer.DialTLSContext(t.Context(), "tcp", "example.invalid:443")
			require.ErrorContains(t, err, "key share")
			require.Zero(t, calls)
		})
	}
}

// 每个 dialer 独占一份会话票据缓存，不同账号/代理的 TLS 会话不会互相复用。
func TestTLSSessionTicketsAreScopedToDialer(t *testing.T) {
	proxyURL, err := url.Parse("http://127.0.0.1:1080")
	require.NoError(t, err)
	caches := []utls.ClientSessionCache{
		NewDialer(nil, nil).dialer.opts.SessionCache,
		NewDialer(nil, nil).dialer.opts.SessionCache,
		NewHTTPProxyDialer(nil, proxyURL).dialer.opts.SessionCache,
		NewSOCKS5ProxyDialer(nil, proxyURL).dialer.opts.SessionCache,
	}
	for i, cache := range caches {
		_, found := cache.Get("same.example")
		require.False(t, found, "ticket leaked from an earlier dialer")
		cache.Put("same.example", &utls.ClientSessionState{})
		_, found = cache.Get("same.example")
		require.True(t, found, "dialer %d must retain its own ticket", i)
	}
}

// ProfileCacheKey 是手写的逐字段摘要。Profile 以后新增线级字段却忘了加进摘要的话，
// 只改了该字段的模板会悄悄沿用旧连接池（旧 ClientHello）。这里用反射逐个设置每个字段：
// 非 Name 字段的每种取值都必须得到互不相同的摘要（同样的值放进不同字段也不能撞），
// Name 只是展示名，不能影响摘要。
func TestProfileCacheKeyCoversEveryWireField(t *testing.T) {
	base := ProfileCacheKey(&Profile{})
	require.Equal(t, base, ProfileCacheKey(nil), "nil and an all-default profile produce the same ClientHello")

	seen := map[string]string{base: "<zero profile>"}
	profileType := reflect.TypeOf(Profile{})
	for i := 0; i < profileType.NumField(); i++ {
		field := profileType.Field(i)
		for variant := 1; variant <= 2; variant++ {
			var p Profile
			if !setProfileFieldForDigestTest(t, field, reflect.ValueOf(&p).Elem().Field(i), variant) {
				continue
			}
			key := ProfileCacheKey(&p)
			label := fmt.Sprintf("%s variant %d", field.Name, variant)
			if field.Name == "Name" {
				require.Equal(t, base, key, "Name is a display label and must not change the digest")
				continue
			}
			prev, dup := seen[key]
			require.False(t, dup, "%s has the same digest as %s; add the field to ProfileCacheKey", label, prev)
			seen[key] = label
		}
	}
}

// setProfileFieldForDigestTest 给字段填第 variant 种非零取值；bool 只有一种非零取值，第二轮跳过。
// 遇到摘要尚未覆盖的字段类型直接失败，提醒同时扩展 ProfileCacheKey 与本测试。
func setProfileFieldForDigestTest(t *testing.T, field reflect.StructField, value reflect.Value, variant int) bool {
	t.Helper()
	switch value.Kind() {
	case reflect.Bool:
		if variant > 1 {
			return false
		}
		value.SetBool(true)
	case reflect.String:
		value.SetString(fmt.Sprintf("value-%d", variant))
	case reflect.Slice:
		elem := reflect.New(value.Type().Elem()).Elem()
		switch elem.Kind() {
		case reflect.Uint16:
			elem.SetUint(uint64(variant))
		case reflect.String:
			elem.SetString(fmt.Sprintf("value-%d", variant))
		default:
			t.Fatalf("Profile.%s has unsupported element type %s; extend ProfileCacheKey and this test", field.Name, elem.Type())
		}
		value.Set(reflect.Append(reflect.MakeSlice(value.Type(), 0, 1), elem))
	default:
		t.Fatalf("Profile.%s has unsupported type %s; extend ProfileCacheKey and this test", field.Name, value.Type())
	}
	return true
}

// 真实模板里最常见的几种改动都要换键：只改名不换键；curves 与 key_share_groups 互换同一取值、
// 扩展顺序调换（ClientHello 的扩展顺序本身就是指纹的一部分）都要换键。
func TestProfileCacheKeyTracksWireConfiguration(t *testing.T) {
	before := &Profile{Name: "before", Curves: []uint16{29}, KeyShareGroups: []uint16{29}}
	renamed := *before
	renamed.Name = "renamed"
	require.Equal(t, ProfileCacheKey(before), ProfileCacheKey(&renamed))

	renamed.KeyShareGroups = []uint16{23}
	require.NotEqual(t, ProfileCacheKey(before), ProfileCacheKey(&renamed))

	require.NotEqual(t,
		ProfileCacheKey(&Profile{Curves: []uint16{29}}),
		ProfileCacheKey(&Profile{KeyShareGroups: []uint16{29}}),
		"the same values in different fields are different ClientHellos")
	require.NotEqual(t,
		ProfileCacheKey(&Profile{Extensions: []uint16{0, 10, 11}}),
		ProfileCacheKey(&Profile{Extensions: []uint16{0, 11, 10}}),
		"extension order is part of the fingerprint")
	require.NotEqual(t,
		ProfileCacheKey(&Profile{ALPNProtocols: []string{"h2", "http/1.1"}}),
		ProfileCacheKey(&Profile{ALPNProtocols: []string{"h2h", "ttp/1.1"}}),
		"ALPN entries are length-delimited")
	require.Equal(t,
		ProfileCacheKey(&Profile{Curves: nil}),
		ProfileCacheKey(&Profile{Curves: []uint16{}}),
		"nil and empty slices both fall back to the built-in default")
}

func BenchmarkProfileCacheKey(b *testing.B) {
	profile := &Profile{
		Name:                "bench",
		EnableGREASE:        true,
		CipherSuites:        []uint16{4865, 4866, 4867, 49195, 49199, 49196, 49200, 52393, 52392, 49171, 49172, 156, 157, 47, 53},
		Curves:              []uint16{4588, 29, 23, 24},
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{1027, 2052, 1025, 1283, 2053, 1281, 2054, 1537},
		ALPNProtocols:       []string{"h2", "http/1.1"},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{4588, 29},
		PSKModes:            []uint16{1},
		Extensions:          []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 27, 17513},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ProfileCacheKey(profile)
	}
}
