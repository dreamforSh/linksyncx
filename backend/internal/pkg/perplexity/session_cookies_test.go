package perplexity

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPerplexitySessionCookieHeaderChunks(t *testing.T) {
	require.Equal(t, sessionCookieName+"=short", SessionCookieHeader("short"))
	token := strings.Repeat("a", sessionChunkBytes) + "second"
	header := SessionCookieHeader(token)
	require.Contains(t, header, sessionCookieName+".0="+strings.Repeat("a", sessionChunkBytes))
	require.Contains(t, header, sessionCookieName+".1=second")
	require.NotContains(t, header, sessionCookieName+"=")
	require.Contains(t, (Credentials{SessionToken: token, CSRFToken: "csrf|hash"}).CookieHeader(), csrfCookieName+"=csrf|hash")
}

func TestPerplexityRotatedSessionCookies(t *testing.T) {
	origin, err := url.Parse("https://www.perplexity.ai/api/auth/session")
	require.NoError(t, err)
	now := time.Now()
	for _, tc := range []struct {
		name    string
		cookies []string
		want    map[string]string
		invalid bool
	}{
		{"primary and csrf", []string{
			sessionCookieName + "=replacement; Path=/; Secure; HttpOnly",
			csrfCookieName + "=csrf|hash; Path=/; Secure",
			"cf_clearance=not-a-session-credential; Path=/; Secure",
			"Authorization=not-a-session-credential; Path=/; Secure",
		}, map[string]string{"api_key": "replacement", "csrf_token": "csrf|hash"}, false},
		{"contiguous chunks out of order", []string{
			sessionCookieName + ".1=second; Path=/; Secure",
			sessionCookieName + ".0=first; Path=/; Secure",
			sessionCookieName + "=; Path=/; Secure; Max-Age=0",
		}, map[string]string{"api_key": "firstsecond"}, false},
		{"ignore deletion", []string{sessionCookieName + "=gone; Path=/; Secure; Max-Age=0"}, map[string]string{}, false},
		{"ignore foreign domain", []string{sessionCookieName + "=foreign; Domain=evil.example; Path=/; Secure"}, map[string]string{}, false},
		{"ignore public suffix", []string{sessionCookieName + "=foreign; Domain=com; Path=/; Secure"}, map[string]string{}, false},
		{"accept parent domain", []string{sessionCookieName + "=replacement; Domain=.perplexity.ai; Path=/; Secure"}, map[string]string{"api_key": "replacement"}, false},
		{"missing chunk zero", []string{sessionCookieName + ".1=secret-second; Path=/; Secure"}, nil, true},
		{"gapped chunks", []string{sessionCookieName + ".0=secret-first; Path=/; Secure", sessionCookieName + ".2=secret-third; Path=/; Secure"}, nil, true},
		{"ambiguous full and chunks", []string{sessionCookieName + "=secret-full; Path=/; Secure", sessionCookieName + ".0=secret-part; Path=/; Secure"}, nil, true},
		{"duplicate names", []string{sessionCookieName + "=secret-one; Path=/; Secure", sessionCookieName + "=secret-two; Path=/; Secure"}, nil, true},
		{"reject scoped cookie", []string{sessionCookieName + "=secret; Path=/api/auth; Secure"}, nil, true},
		{"require secure session", []string{sessionCookieName + "=secret; Path=/"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			header := make(http.Header)
			for _, value := range tc.cookies {
				header.Add("Set-Cookie", value)
			}
			got, err := RotatedSessionCookies(header, origin, now)
			if tc.invalid {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	insecure, err := url.Parse("http://www.perplexity.ai/api/auth/session")
	require.NoError(t, err)
	patch, err := RotatedSessionCookies(http.Header{"Set-Cookie": []string{sessionCookieName + "=secret; Path=/; Secure"}}, insecure, now)
	require.NoError(t, err)
	require.Empty(t, patch)
}
