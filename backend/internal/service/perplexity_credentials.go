package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"go.uber.org/zap"
)

// PerplexityInvalidSessionMessage identifies only this platform's auth failure.
const PerplexityInvalidSessionMessage = "perplexity session is unauthenticated or expired; update the session cookie"

// PerplexityAccountStateRepository conditionally changes only provider-owned
// fields. Delayed requests must not replace an administrator's newer credentials,
// proxy, health decision, or a later profile observation.
type PerplexityAccountStateRepository interface {
	UpdatePerplexityCookiesIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, patch map[string]string) (bool, error)
	ApplyPerplexityProfileIfUnchanged(ctx context.Context, id int64, expectedCredentials map[string]any, expectedProxyID *int64, update PerplexityProfileUpdate) (bool, error)
}

// PerplexityProfileUpdate is one bounded collection attempt; SessionValid is nil
// when authentication was not checked or the result was indeterminate.
type PerplexityProfileUpdate struct {
	Snapshot     map[string]any
	SessionValid *bool
	ObservedAtMs int64
}

func copyPerplexityAccount(account *Account) *Account {
	if account == nil {
		return nil
	}
	copy := *account
	copy.Credentials = shallowCopyMap(account.Credentials)
	copy.Extra = shallowCopyMap(account.Extra)
	return &copy
}

func freshPerplexityAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, error) {
	if account == nil {
		return nil, fmt.Errorf("perplexity account is missing")
	}
	if _, conditional := repo.(PerplexityAccountStateRepository); conditional && account.ID > 0 {
		fresh, err := repo.GetByID(ctx, account.ID)
		if err != nil {
			return nil, fmt.Errorf("load current perplexity account: %w", err)
		}
		if fresh == nil || !fresh.IsPerplexity() || fresh.Type != AccountTypeAPIKey {
			return nil, fmt.Errorf("perplexity account is no longer available")
		}
		return copyPerplexityAccount(fresh), nil
	}
	return copyPerplexityAccount(account), nil
}

func freshPerplexityForwardAccount(ctx context.Context, repo AccountRepository, account *Account) (*Account, error) {
	fresh, err := freshPerplexityAccount(ctx, repo, account)
	if err != nil {
		return nil, err
	}
	if _, conditional := repo.(PerplexityAccountStateRepository); conditional && !fresh.IsSchedulable() {
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusServiceUnavailable,
			ResponseBody: []byte(`{"error":{"type":"api_error","message":"perplexity account is no longer schedulable"}}`),
		}
	}
	return fresh, nil
}

// sendPerplexityAuthenticated is shared by Ask, session/quota probes, model
// discovery and upload authorization. Storage uploads must not call it.
func sendPerplexityAuthenticated(upstream HTTPUpstream, repo AccountRepository, account *Account, req *http.Request, profile *tlsfingerprint.Profile) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, err
	}
	// Browser-session credentials must never be redirected to another origin.
	req = req.WithContext(WithHTTPUpstreamRedirectsDisabled(req.Context()))
	resp, err := upstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, profile)
	if resp == nil {
		return resp, err
	}
	defer resp.Header.Del("Set-Cookie") // Never publish upstream session cookies.
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	creds := perplexityCredentialsFromAccount(account)
	if req.Header.Get("Cookie") != creds.CookieHeader() {
		// An explicit header override is a different identity from credentials.
		return resp, nil
	}
	patch, cookieErr := perplexity.RotatedSessionCookies(resp.Header, req.URL, time.Now())
	if cookieErr != nil {
		logger.L().Warn("perplexity: rejected invalid session cookie rotation", zap.Int64("account_id", account.ID))
		return resp, nil
	}
	next := shallowCopyMap(account.Credentials)
	changed := false
	for key, value := range patch {
		if strings.TrimSpace(account.GetCredential(key)) != value {
			next[key] = value
			changed = true
		}
	}
	if !changed {
		return resp, nil
	}
	if account.ID <= 0 {
		account.Credentials = next
		return resp, nil
	}
	conditional, ok := repo.(PerplexityAccountStateRepository)
	if !ok {
		logger.L().Warn("perplexity: conditional cookie persistence is unavailable", zap.Int64("account_id", account.ID))
		return resp, nil
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(req.Context()), 3*time.Second)
	defer cancel()
	applied, persistErr := conditional.UpdatePerplexityCookiesIfUnchanged(persistCtx, account.ID, account.Credentials, account.ProxyID, patch)
	if persistErr != nil {
		// DB/transport errors can embed SQL arguments. Do not log cookie values.
		logger.L().Warn("perplexity: persist session cookie rotation failed", zap.Int64("account_id", account.ID))
	} else if applied {
		account.Credentials = next
	}
	return resp, nil
}
