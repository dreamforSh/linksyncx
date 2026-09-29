package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/clashsub"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
)

const clashFetchMaxRedirects = 5

// clashFetchPolicy controls which subscription hosts may be contacted.
type clashFetchPolicy struct {
	AllowPrivateHosts bool
	Timeout           time.Duration
	MaxBodyBytes      int64
}

type clashFetchResult struct {
	Body     []byte
	UserInfo *ClashUserInfo
}

// clashMetadataBlocked reports addresses that stay blocked even when private
// subscription hosts are allowed (cloud metadata and link-local ranges).
func clashMetadataBlocked(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func (p clashFetchPolicy) ipBlocked(ip net.IP) bool {
	if p.AllowPrivateHosts {
		return clashMetadataBlocked(ip)
	}
	return isPrivateIP(ip)
}

func (p clashFetchPolicy) hostnameBlocked(hostname string) bool {
	lower := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if p.AllowPrivateHosts {
		switch lower {
		case "localhost", "localhost.localdomain":
			return false
		}
	}
	return isBlockedHostname(lower)
}

// validateURL rejects non-HTTP schemes and literal hosts outside the policy.
func (p clashFetchPolicy) validateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "subscription URL must be an http(s) URL")
	}
	host := u.Hostname()
	if p.hostnameBlocked(host) {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid, "subscription host is not allowed")
	}
	if ip := net.ParseIP(host); ip != nil && p.ipBlocked(ip) {
		return infraerrors.BadRequest(ClashErrCodeProfileInvalid,
			"subscription host resolves to a blocked address (enable clash_pool.subscription.allow_private_hosts for self-hosted converters)")
	}
	return nil
}

var clashFetchDialer = &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

// dialContext re-checks every resolved address at connect time (DNS rebinding).
func (p clashFetchPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if p.ipBlocked(ip) {
			return nil, &net.AddrError{Err: "blocked by subscription fetch policy", Addr: address}
		}
		return clashFetchDialer.DialContext(ctx, network, address)
	}
	if p.hostnameBlocked(host) {
		return nil, &net.AddrError{Err: "blocked by subscription fetch policy", Addr: address}
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error = &net.AddrError{Err: "no usable addresses", Addr: host}
	for _, addr := range addrs {
		if p.ipBlocked(addr.IP) {
			lastErr = &net.AddrError{Err: "blocked by subscription fetch policy", Addr: addr.IP.String()}
			continue
		}
		conn, dialErr := clashFetchDialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	return nil, lastErr
}

// fetchClashSubscription downloads a subscription. When via is set the request
// goes through that (manual) proxy and only URL-level checks apply.
func fetchClashSubscription(ctx context.Context, policy clashFetchPolicy, rawURL, userAgent string, via *Proxy) (*clashFetchResult, error) {
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, infraerrors.BadRequest(ClashErrCodeProfileInvalid, "subscription URL is invalid")
	}
	if err := policy.validateURL(target); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		DialContext:           policy.dialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: policy.Timeout,
		MaxIdleConns:          1,
		DisableKeepAlives:     true,
	}
	if via != nil {
		_, parsed, err := proxyurl.Parse(via.URL())
		if err != nil {
			return nil, fmt.Errorf("fetch proxy: %w", err)
		}
		transport.DialContext = clashFetchDialer.DialContext
		if err := proxyutil.ConfigureTransportProxy(transport, parsed); err != nil {
			return nil, fmt.Errorf("fetch proxy: %w", err)
		}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   policy.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= clashFetchMaxRedirects {
				return errors.New("too many redirects")
			}
			return policy.validateURL(req.URL)
		},
	}
	defer transport.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(userAgent) == "" {
		userAgent = clashDefaultUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %s", redactClashURLError(err, target))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, policy.MaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	if int64(len(body)) > policy.MaxBodyBytes {
		return nil, fmt.Errorf("subscription exceeds %d bytes", policy.MaxBodyBytes)
	}
	result := &clashFetchResult{Body: body}
	if info, ok := clashsub.ParseUserInfo(resp.Header.Get("Subscription-Userinfo")); ok {
		result.UserInfo = &ClashUserInfo{Upload: info.Upload, Download: info.Download, Total: info.Total, Expire: info.Expire}
	}
	return result, nil
}

// redactClashURLError strips the subscription URL (which carries the access
// token) from transport errors before they are stored or logged.
var clashErrorURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func redactClashURLError(err error, target *url.URL) string {
	message := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		message = urlErr.Err.Error()
	}
	if target != nil {
		message = strings.ReplaceAll(message, target.String(), maskClashURL(target.String()))
	}
	// Redirect and nested parser errors may contain a different URL from the
	// original target, including credentials in its path or query string.
	return clashErrorURLPattern.ReplaceAllStringFunc(message, maskClashURL)
}

// maskClashURL keeps scheme and host and hides path, query and credentials.
func maskClashURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "***"
	}
	masked := u.Scheme + "://" + u.Host
	if u.Path != "" || u.RawQuery != "" {
		masked += "/***"
	}
	return masked
}

func clashURLFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}
