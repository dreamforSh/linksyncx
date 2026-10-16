package perplexity

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	sessionCookieName = "__Secure-next-auth.session-token"
	csrfCookieName    = "next-auth.csrf-token"
	maxSessionBytes   = 32 * 1024
	sessionChunkBytes = 3800
)

// SessionCookieHeader also supports NextAuth's chunked session cookies. The
// canonical token is kept as one credential so rotations cannot mix generations.
func SessionCookieHeader(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= sessionChunkBytes {
		return sessionCookieName + "=" + token
	}
	var chunks []string
	for index, start := 0, 0; start < len(token); index++ {
		end := start + sessionChunkBytes
		if end > len(token) {
			end = len(token)
		}
		chunks = append(chunks, sessionCookieName+"."+strconv.Itoa(index)+"="+token[start:end])
		start = end
	}
	return strings.Join(chunks, "; ")
}

func validRotatedCookieValue(value string) bool {
	for _, r := range value {
		if r < 33 || r > 126 {
			return false
		}
	}
	return true
}

// RotatedSessionCookies extracts only full-origin authentication cookies. It
// ignores deletions and rejects incomplete/ambiguous chunks rather than replacing
// a working token with a partial one. No cookie value appears in an error.
func RotatedSessionCookies(header http.Header, requestURL *url.URL, now time.Time) (map[string]string, error) {
	if requestURL == nil || requestURL.Scheme != "https" {
		return nil, nil
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	var accepted []*http.Cookie
	seen := make(map[string]bool)
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		isSession := cookie.Name == sessionCookieName || strings.HasPrefix(cookie.Name, sessionCookieName+".")
		if !isSession && cookie.Name != csrfCookieName {
			continue
		}
		if cookie.MaxAge < 0 || !cookie.Expires.IsZero() && !cookie.Expires.After(now) || cookie.Value == "" {
			continue
		}
		// Broadening a scoped cookie to every Perplexity endpoint is unsafe.
		if cookie.Path != "/" || isSession && !cookie.Secure || cookie.Valid() != nil || !validRotatedCookieValue(cookie.Value) || len(cookie.Value) > maxSessionBytes || seen[cookie.Name] {
			return nil, fmt.Errorf("perplexity returned invalid or ambiguous session cookies")
		}
		seen[cookie.Name] = true
		accepted = append(accepted, cookie)
	}
	jar.SetCookies(requestURL, accepted)
	patch := make(map[string]string)
	chunks := make(map[int]string)
	for _, cookie := range jar.Cookies(requestURL) {
		switch {
		case cookie.Name == sessionCookieName:
			patch["api_key"] = cookie.Value
		case cookie.Name == csrfCookieName:
			patch["csrf_token"] = cookie.Value
		case strings.HasPrefix(cookie.Name, sessionCookieName+"."):
			suffix := strings.TrimPrefix(cookie.Name, sessionCookieName+".")
			index, err := strconv.Atoi(suffix)
			if err != nil || index < 0 || index >= 16 || strconv.Itoa(index) != suffix {
				return nil, fmt.Errorf("perplexity returned invalid session cookie chunks")
			}
			chunks[index] = cookie.Value
		}
	}
	if len(chunks) != 0 {
		if patch["api_key"] != "" {
			return nil, fmt.Errorf("perplexity returned mixed session cookie formats")
		}
		var token strings.Builder
		for index := 0; index < len(chunks); index++ {
			chunk, exists := chunks[index]
			if !exists {
				return nil, fmt.Errorf("perplexity returned incomplete session cookie chunks")
			}
			_, _ = token.WriteString(chunk)
		}
		if token.Len() > maxSessionBytes {
			return nil, fmt.Errorf("perplexity session cookie exceeds its size limit")
		}
		patch["api_key"] = token.String()
	}
	return patch, nil
}
