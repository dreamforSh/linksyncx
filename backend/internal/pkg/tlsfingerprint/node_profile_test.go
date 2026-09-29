package tlsfingerprint

import (
	"crypto/tls"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/stretchr/testify/require"
)

// node22171TestProfile is shared by local, external and capture-server tests.
func node22171TestProfile() *Profile {
	return &Profile{
		Name:                "linux_x64_node_v22171",
		EnableGREASE:        false,
		CipherSuites:        []uint16{4866, 4867, 4865, 49199, 49195, 49200, 49196, 158, 49191, 103, 49192, 107, 163, 159, 52393, 52392, 52394, 49327, 49325, 49315, 49311, 49245, 49249, 49239, 49235, 162, 49326, 49324, 49314, 49310, 49244, 49248, 49238, 49234, 49188, 106, 49187, 64, 49162, 49172, 57, 56, 49161, 49171, 51, 50, 157, 49313, 49309, 49233, 156, 49312, 49308, 49232, 61, 60, 53, 47, 255},
		Curves:              []uint16{29, 23, 30, 25, 24, 256, 257, 258, 259, 260},
		PointFormats:        []uint16{0, 1, 2},
		SignatureAlgorithms: []uint16{0x0403, 0x0503, 0x0603, 0x0807, 0x0808, 0x0809, 0x080a, 0x080b, 0x0804, 0x0805, 0x0806, 0x0401, 0x0501, 0x0601, 0x0303, 0x0301, 0x0302, 0x0402, 0x0502, 0x0602},
		ALPNProtocols:       []string{"http/1.1"},
		SupportedVersions:   []uint16{0x0304, 0x0303},
		KeyShareGroups:      []uint16{29},
		PSKModes:            []uint16{1},
		Extensions:          []uint16{0, 11, 10, 35, 16, 22, 23, 13, 43, 45, 51},
	}
}

func TestNode22171ProfileKeyShares(t *testing.T) {
	profile := node22171TestProfile()
	require.NoError(t, ValidateProfile(profile))
	spec := buildClientHelloSpecFromProfile(profile)
	var groups, shares []uint16
	for _, extension := range spec.Extensions {
		switch extension := extension.(type) {
		case *utls.SupportedCurvesExtension:
			for _, group := range extension.Curves {
				groups = append(groups, uint16(group))
			}
		case *utls.KeyShareExtension:
			for _, share := range extension.KeyShares {
				shares = append(shares, uint16(share.Group))
			}
		}
	}
	require.Equal(t, profile.Curves, groups)
	require.Equal(t, []uint16{29}, shares)

	// Empty fields independently inherit the Bun defaults, not a subset of Curves.
	for _, empty := range [][]uint16{nil, {}} {
		profile.KeyShareGroups = empty
		require.ErrorContains(t, ValidateProfile(profile), "key share 4588 is absent from supported groups")
	}
	require.NoError(t, ValidateProfile(nil))
}

func TestNode22171ProfileLoopbackTLS13(t *testing.T) {
	pool, cert := newTestPKI(t)
	target := startTLSGreeter(t, cert, &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
	})
	dialer, err := NewProxyDialer(node22171TestProfile(), nil, DialOptions{RootCAs: pool})
	require.NoError(t, err)
	conn, err := dialer.DialTLSContext(t.Context(), "tcp", target)
	require.NoError(t, err)
	readGreeting(t, conn)
}
