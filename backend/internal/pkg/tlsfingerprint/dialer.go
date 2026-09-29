// Package tlsfingerprint provides TLS fingerprint simulation for HTTP clients.
// It uses the utls library to create TLS connections that mimic Claude Code
// (Bun fetch) clients, directly or through http/https/socks5 proxies.
package tlsfingerprint

import (
	"context"
	"net"
	"net/url"

	utls "github.com/refraction-networking/utls"
)

// Profile contains TLS fingerprint configuration.
// All slice fields use built-in defaults when empty.
type Profile struct {
	Name                string // Profile name for identification
	CipherSuites        []uint16
	Curves              []uint16
	PointFormats        []uint16
	EnableGREASE        bool
	SignatureAlgorithms []uint16 // Empty uses defaultSignatureAlgorithms
	ALPNProtocols       []string // Empty uses ["http/1.1"]
	SupportedVersions   []uint16 // Empty uses [TLS1.3, TLS1.2]
	KeyShareGroups      []uint16 // Empty uses [X25519]
	PSKModes            []uint16 // Empty uses [psk_dhe_ke]
	Extensions          []uint16 // Extension type IDs in order; empty uses default Claude Code 2.1.280 (Bun) order
}

// Dialer creates TLS connections with custom fingerprints.
// It is a direct-connection ProxyDialer with default timeouts.
type Dialer struct {
	profile *Profile
	dialer  *ProxyDialer
}

// HTTPProxyDialer creates TLS connections through HTTP/HTTPS proxies with custom fingerprints.
// It handles the CONNECT tunnel establishment before performing TLS handshake.
type HTTPProxyDialer struct {
	profile  *Profile
	proxyURL *url.URL
	dialer   *ProxyDialer
	err      error
}

// SOCKS5ProxyDialer creates TLS connections through SOCKS5 proxies with custom fingerprints.
// It uses golang.org/x/net/proxy to establish the SOCKS5 tunnel.
type SOCKS5ProxyDialer struct {
	profile  *Profile
	proxyURL *url.URL
	dialer   *ProxyDialer
	err      error
}

// Default TLS fingerprint values captured from real Claude Code 2.1.280 (Bun/1.4.3)
// via claude-gw tlscap (本机 claude.exe 实测抓包).
// JA3 (域名 SNI):  1523504b38f0fae0d881d4b6554aac1b   JA4: t13d1713h1_5b57614c22b0_6a3d802a7139
// JA3 (IP 直连):   5260242a2eb12c71995767c24569bff5
// 与早前 Node.js 24.x 采集的差异：supported_groups/key_share 首位为 X25519MLKEM768
// （后量子混合密钥交换），无 GREASE ECH 扩展。
var (
	// defaultCipherSuites contains the 17 cipher suites from Node.js 24.x
	// Order is critical for JA3 fingerprint matching
	defaultCipherSuites = []uint16{
		// TLS 1.3 cipher suites
		0x1301, // TLS_AES_128_GCM_SHA256
		0x1302, // TLS_AES_256_GCM_SHA384
		0x1303, // TLS_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-GCM
		0xc02b, // TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256
		0xc02f, // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
		0xc02c, // TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
		0xc030, // TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384

		// ECDHE + ChaCha20-Poly1305
		0xcca9, // TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256
		0xcca8, // TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256

		// ECDHE + AES-CBC-SHA (legacy fallback)
		0xc009, // TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA
		0xc013, // TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA
		0xc00a, // TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA
		0xc014, // TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA

		// RSA + AES-GCM (non-PFS)
		0x009c, // TLS_RSA_WITH_AES_128_GCM_SHA256
		0x009d, // TLS_RSA_WITH_AES_256_GCM_SHA384

		// RSA + AES-CBC-SHA (non-PFS, legacy)
		0x002f, // TLS_RSA_WITH_AES_128_CBC_SHA
		0x0035, // TLS_RSA_WITH_AES_256_CBC_SHA
	}

	// defaultCurves contains the 4 supported groups from Claude Code 2.1.280 (Bun)
	// —— 首位为后量子混合组 X25519MLKEM768
	defaultCurves = []utls.CurveID{
		utls.X25519MLKEM768, // 0x11ec
		utls.X25519,         // 0x001d
		utls.CurveP256,      // 0x0017 (secp256r1)
		utls.CurveP384,      // 0x0018 (secp384r1)
	}

	// defaultPointFormats contains point formats from Node.js 24.x
	defaultPointFormats = []uint16{
		0, // uncompressed
	}

	// defaultSignatureAlgorithms contains the 9 signature algorithms from Node.js 24.x
	defaultSignatureAlgorithms = []utls.SignatureScheme{
		0x0403, // ecdsa_secp256r1_sha256
		0x0804, // rsa_pss_rsae_sha256
		0x0401, // rsa_pkcs1_sha256
		0x0503, // ecdsa_secp384r1_sha384
		0x0805, // rsa_pss_rsae_sha384
		0x0501, // rsa_pkcs1_sha384
		0x0806, // rsa_pss_rsae_sha512
		0x0601, // rsa_pkcs1_sha512
		0x0201, // rsa_pkcs1_sha1
	}
)

// NewDialer creates a new TLS fingerprint dialer.
// baseDialer is used for TCP connection establishment (supports proxy scenarios).
// If baseDialer is nil, direct TCP dial with the default dial timeout is used.
// The TLS handshake is bounded by the default handshake timeout.
func NewDialer(profile *Profile, baseDialer func(ctx context.Context, network, addr string) (net.Conn, error)) *Dialer {
	// 直连不会返回错误（错误只来自代理配置）。
	dialer, _ := newProxyDialer(profile, nil, DialOptions{}, baseDialer)
	return &Dialer{profile: profile, dialer: dialer}
}

// NewHTTPProxyDialer creates a new TLS fingerprint dialer that works through HTTP/HTTPS proxies.
// It establishes a CONNECT tunnel (after a TLS handshake with https proxies) before
// performing the TLS handshake with custom fingerprint.
func NewHTTPProxyDialer(profile *Profile, proxyURL *url.URL) *HTTPProxyDialer {
	dialer, err := newProxyDialer(profile, proxyURL, DialOptions{}, nil)
	return &HTTPProxyDialer{profile: profile, proxyURL: proxyURL, dialer: dialer, err: err}
}

// NewSOCKS5ProxyDialer creates a new TLS fingerprint dialer that works through SOCKS5 proxies.
// It establishes a SOCKS5 tunnel before performing TLS handshake with custom fingerprint.
func NewSOCKS5ProxyDialer(profile *Profile, proxyURL *url.URL) *SOCKS5ProxyDialer {
	dialer, err := newProxyDialer(profile, proxyURL, DialOptions{}, nil)
	return &SOCKS5ProxyDialer{profile: profile, proxyURL: proxyURL, dialer: dialer, err: err}
}

// DialTLSContext establishes a TLS connection through SOCKS5 proxy with the configured fingerprint.
// Flow: SOCKS5 CONNECT to target -> TLS handshake with utls on the tunnel
func (d *SOCKS5ProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.dialer.DialTLSContext(ctx, network, addr)
}

// DialTLSContext establishes a TLS connection through HTTP proxy with the configured fingerprint.
// Flow: TCP connect to proxy -> (TLS with https proxy) -> CONNECT tunnel -> TLS handshake with utls
func (d *HTTPProxyDialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.dialer.DialTLSContext(ctx, network, addr)
}

// DialTLSContext establishes a TLS connection with the configured fingerprint.
// This method is designed to be used as http.Transport.DialTLSContext.
func (d *Dialer) DialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.DialTLSContext(ctx, network, addr)
}

// toUTLSCurves converts uint16 slice to utls.CurveID slice.
func toUTLSCurves(curves []uint16) []utls.CurveID {
	result := make([]utls.CurveID, len(curves))
	for i, c := range curves {
		result[i] = utls.CurveID(c)
	}
	return result
}

// defaultExtensionOrder is the Claude Code 2.1.280 (Bun) extension order.
// Used when Profile.Extensions is empty.
var defaultExtensionOrder = []uint16{
	0,     // server_name
	23,    // extended_master_secret
	65281, // renegotiation_info
	10,    // supported_groups
	11,    // ec_point_formats
	35,    // session_ticket
	16,    // alpn
	5,     // status_request
	13,    // signature_algorithms
	18,    // signed_certificate_timestamp
	51,    // key_share
	45,    // psk_key_exchange_modes
	43,    // supported_versions
}

// isGREASEValue checks if a uint16 value matches the TLS GREASE pattern (0x?a?a).
func isGREASEValue(v uint16) bool {
	return v&0x0f0f == 0x0a0a && v>>8 == v&0xff
}

// buildClientHelloSpecFromProfile constructs ClientHelloSpec from a Profile.
// This is a standalone function that can be used by both Dialer and HTTPProxyDialer.
func buildClientHelloSpecFromProfile(profile *Profile) *utls.ClientHelloSpec {
	// Resolve effective values (profile overrides or built-in defaults)
	cipherSuites := defaultCipherSuites
	if profile != nil && len(profile.CipherSuites) > 0 {
		cipherSuites = profile.CipherSuites
	}

	curves := defaultCurves
	if profile != nil && len(profile.Curves) > 0 {
		curves = toUTLSCurves(profile.Curves)
	}

	pointFormats := defaultPointFormats
	if profile != nil && len(profile.PointFormats) > 0 {
		pointFormats = profile.PointFormats
	}

	signatureAlgorithms := defaultSignatureAlgorithms
	if profile != nil && len(profile.SignatureAlgorithms) > 0 {
		signatureAlgorithms = make([]utls.SignatureScheme, len(profile.SignatureAlgorithms))
		for i, s := range profile.SignatureAlgorithms {
			signatureAlgorithms[i] = utls.SignatureScheme(s)
		}
	}

	alpnProtocols := []string{"http/1.1"}
	if profile != nil && len(profile.ALPNProtocols) > 0 {
		alpnProtocols = profile.ALPNProtocols
	}

	supportedVersions := []uint16{utls.VersionTLS13, utls.VersionTLS12}
	if profile != nil && len(profile.SupportedVersions) > 0 {
		supportedVersions = profile.SupportedVersions
	}

	keyShareGroups := []utls.CurveID{utls.X25519MLKEM768, utls.X25519} // 2.1.280 实测：MLKEM + X25519 双份额
	if profile != nil && len(profile.KeyShareGroups) > 0 {
		keyShareGroups = toUTLSCurves(profile.KeyShareGroups)
	}

	pskModes := []uint16{uint16(utls.PskModeDHE)}
	if profile != nil && len(profile.PSKModes) > 0 {
		pskModes = profile.PSKModes
	}

	enableGREASE := profile != nil && profile.EnableGREASE

	// Build key shares
	keyShares := make([]utls.KeyShare, len(keyShareGroups))
	for i, g := range keyShareGroups {
		keyShares[i] = utls.KeyShare{Group: g}
	}

	// Determine extension order
	extOrder := defaultExtensionOrder
	if profile != nil && len(profile.Extensions) > 0 {
		extOrder = profile.Extensions
	}

	// Build extensions list from the ordered IDs.
	// Parametric extensions (curves, sigalgs, etc.) are populated with resolved profile values.
	// Unknown IDs use GenericExtension (sends type ID with empty data).
	extensions := make([]utls.TLSExtension, 0, len(extOrder)+2)
	for _, id := range extOrder {
		if isGREASEValue(id) {
			extensions = append(extensions, &utls.UtlsGREASEExtension{})
			continue
		}
		switch id {
		case 0: // server_name
			extensions = append(extensions, &utls.SNIExtension{})
		case 5: // status_request (OCSP)
			extensions = append(extensions, &utls.StatusRequestExtension{})
		case 10: // supported_groups
			extensions = append(extensions, &utls.SupportedCurvesExtension{Curves: curves})
		case 11: // ec_point_formats
			extensions = append(extensions, &utls.SupportedPointsExtension{SupportedPoints: toUint8s(pointFormats)})
		case 13: // signature_algorithms
			extensions = append(extensions, &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatureAlgorithms})
		case 16: // alpn
			extensions = append(extensions, &utls.ALPNExtension{AlpnProtocols: alpnProtocols})
		case 18: // signed_certificate_timestamp
			extensions = append(extensions, &utls.SCTExtension{})
		case 23: // extended_master_secret
			extensions = append(extensions, &utls.ExtendedMasterSecretExtension{})
		case 35: // session_ticket
			extensions = append(extensions, &utls.SessionTicketExtension{})
		case 43: // supported_versions
			extensions = append(extensions, &utls.SupportedVersionsExtension{Versions: supportedVersions})
		case 45: // psk_key_exchange_modes
			extensions = append(extensions, &utls.PSKKeyExchangeModesExtension{Modes: toUint8s(pskModes)})
		case 50: // signature_algorithms_cert
			extensions = append(extensions, &utls.SignatureAlgorithmsCertExtension{SupportedSignatureAlgorithms: signatureAlgorithms})
		case 51: // key_share
			extensions = append(extensions, &utls.KeyShareExtension{KeyShares: keyShares})
		case 0xfe0d: // encrypted_client_hello (ECH, 65037)
			// Send GREASE ECH with random payload — mimics Node.js behavior when no real ECHConfig is available.
			// An empty GenericExtension causes "error decoding message" from servers that validate ECH format.
			extensions = append(extensions, &utls.GREASEEncryptedClientHelloExtension{})
		case 0xff01: // renegotiation_info
			extensions = append(extensions, &utls.RenegotiationInfoExtension{})
		default:
			// Unknown extension — send as GenericExtension (type ID + empty data).
			// This covers encrypt_then_mac(22) and any future extensions.
			extensions = append(extensions, &utls.GenericExtension{Id: id})
		}
	}

	// For default extension order with EnableGREASE, wrap with GREASE bookends
	if enableGREASE && (profile == nil || len(profile.Extensions) == 0) {
		extensions = append([]utls.TLSExtension{&utls.UtlsGREASEExtension{}}, extensions...)
		extensions = append(extensions, &utls.UtlsGREASEExtension{})
	}

	return &utls.ClientHelloSpec{
		CipherSuites:       cipherSuites,
		CompressionMethods: []uint8{0}, // null compression only (standard)
		Extensions:         extensions,
		TLSVersMax:         utls.VersionTLS13,
		TLSVersMin:         utls.VersionTLS10,
	}
}

// toUint8s converts []uint16 to []uint8 (for utls fields that require []uint8).
func toUint8s(vals []uint16) []uint8 {
	out := make([]uint8, len(vals))
	for i, v := range vals {
		out[i] = uint8(v)
	}
	return out
}
