package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const codeBuddyUpstreamTimeout = 15 * time.Second

// CodeBuddyOAuthService owns the CodeBuddy credential lifecycle: scan login,
// .info / refresh-token import, token refresh, and the admin-side catalog and
// billing probes. Every outbound call goes through the account's proxy and
// fails closed when a configured proxy cannot be resolved.
type CodeBuddyOAuthService struct {
	manager     *codebuddy.OAuthManager
	proxyRepo   ProxyRepository
	accountRepo AccountRepository
}

func NewCodeBuddyOAuthService(proxyRepo ProxyRepository, accountRepo AccountRepository) *CodeBuddyOAuthService {
	return &CodeBuddyOAuthService{
		manager:     codebuddy.NewOAuthManager(),
		proxyRepo:   proxyRepo,
		accountRepo: accountRepo,
	}
}

// Start begins a scan login on site (cn / intl / intl-codebuddy).
func (s *CodeBuddyOAuthService) Start(ctx context.Context, site string, proxyID *int64) (*codebuddy.StartResult, error) {
	client, err := s.clientForProxyID(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	result, err := s.manager.Start(ctx, site, client)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_OAUTH_START_FAILED", "%v", err)
	}
	return result, nil
}

func (s *CodeBuddyOAuthService) Poll(ctx context.Context, loginID string) *codebuddy.PollResult {
	return s.manager.Poll(ctx, loginID)
}

func (s *CodeBuddyOAuthService) Take(loginID string) (*codebuddy.Credentials, error) {
	creds, err := s.manager.Take(loginID)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_TAKE_FAILED", err.Error())
	}
	return creds, nil
}

// ImportCredentials validates a pasted official .info document or a stored
// credential map.
func (s *CodeBuddyOAuthService) ImportCredentials(raw map[string]any) (*codebuddy.Credentials, error) {
	creds, err := codebuddy.ParseCredentials(raw)
	if err != nil {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_CREDENTIALS_INVALID", err.Error())
	}
	return &creds, nil
}

// ImportRefreshToken exchanges a pasted refresh token for a full credential
// on an explicitly chosen profile.
func (s *CodeBuddyOAuthService) ImportRefreshToken(ctx context.Context, profile, refreshToken string, proxyID *int64) (*codebuddy.Credentials, error) {
	client, err := s.clientForProxyID(ctx, proxyID)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*codeBuddyUpstreamTimeout)
	defer cancel()
	creds, err := codebuddy.ImportRefreshToken(reqCtx, client, profile, refreshToken)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_REFRESH_TOKEN_IMPORT_FAILED", "%v", err)
	}
	return creds, nil
}

func (s *CodeBuddyOAuthService) BuildAccountCredentials(creds *codebuddy.Credentials) map[string]any {
	if creds == nil {
		return map[string]any{}
	}
	return creds.ToMap()
}

func (s *CodeBuddyOAuthService) RefreshAccountToken(ctx context.Context, account *Account) (*codebuddy.RefreshResult, error) {
	if !account.IsCodeBuddyOAuth() {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_INVALID_ACCOUNT", "not a codebuddy oauth account")
	}
	creds, err := account.CodeBuddyCredentials()
	if err != nil {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_CREDENTIALS_INVALID", err.Error())
	}
	if strings.TrimSpace(creds.RefreshToken) == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_REFRESH_TOKEN_MISSING", "refresh token is missing")
	}
	client, err := s.clientForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	refreshCtx, cancel := context.WithTimeout(ctx, codebuddy.RefreshTimeout)
	defer cancel()
	result, err := codebuddy.Refresh(refreshCtx, client, creds)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_OAUTH_REFRESH_FAILED", "%v", err)
	}
	s.backfillAccountType(refreshCtx, client, account, creds, result)
	return result, nil
}

// backfillAccountType fills the subscription tier when neither the stored
// credential nor the refresh response carries one (older accounts imported
// before the tier was captured). It is a single self-healing lookup: once the
// tier is persisted the guard below short-circuits future refreshes. A failed
// lookup never fails the refresh.
func (s *CodeBuddyOAuthService) backfillAccountType(ctx context.Context, client codebuddy.HTTPDoer, account *Account, creds codebuddy.Credentials, result *codebuddy.RefreshResult) {
	if result == nil || result.AccountType != "" || strings.TrimSpace(creds.AccountType) != "" {
		return
	}
	info, err := codebuddy.FetchAccountInfo(ctx, client, codebuddy.Identity{
		Profile:     creds.Profile,
		AccessToken: result.AccessToken,
		Domain:      firstNonEmpty(result.Domain, creds.Domain),
		UID:         creds.UID,
	})
	if err != nil || info == nil {
		if err != nil {
			logger.L().Debug("codebuddy account_type backfill failed",
				zap.Int64("account_id", account.ID),
				zap.Error(err),
			)
		}
		return
	}
	result.AccountType = info.AccountType
	if result.EnterpriseID == "" {
		result.EnterpriseID = info.EnterpriseID
	}
	if result.EnterpriseName == "" {
		result.EnterpriseName = info.EnterpriseName
	}
	if result.Nickname == "" {
		result.Nickname = info.Nickname
	}
}

// ApplyRefresh merges rotated tokens into the account's credentials, keeping
// every admin-configured field (model_mapping, credit_policy, ...).
func (s *CodeBuddyOAuthService) ApplyRefresh(account *Account, result *codebuddy.RefreshResult) map[string]any {
	creds := map[string]any{}
	if account != nil && account.Credentials != nil {
		creds = MergeCredentials(account.Credentials, map[string]any{})
	}
	if result == nil {
		return creds
	}
	creds[codebuddy.KeyAccessToken] = result.AccessToken
	if result.RefreshToken != "" {
		creds[codebuddy.KeyRefreshToken] = result.RefreshToken
	}
	if result.IDToken != "" {
		creds[codebuddy.KeyIDToken] = result.IDToken
	}
	if result.TokenType != "" {
		creds[codebuddy.KeyTokenType] = result.TokenType
	}
	if result.Domain != "" {
		creds[codebuddy.KeyDomain] = result.Domain
	}
	if result.ExpiresAt > 0 {
		creds[codebuddy.KeyExpiresAt] = time.Unix(result.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	if result.RefreshExpiresAt > 0 {
		creds[codebuddy.KeyRefreshExpiresAt] = time.Unix(result.RefreshExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	// Backfill the subscription tier (and identity) when the refresh carried it.
	// Non-empty only: a refresh that omits these keeps whatever is already stored.
	if result.AccountType != "" {
		creds[codebuddy.KeyAccountType] = result.AccountType
	}
	if result.EnterpriseID != "" {
		creds[codebuddy.KeyEnterpriseID] = result.EnterpriseID
	}
	if result.EnterpriseName != "" {
		creds[codebuddy.KeyEnterpriseName] = result.EnterpriseName
	}
	if result.Nickname != "" {
		creds[codebuddy.KeyNickname] = result.Nickname
	}
	return creds
}

// codeBuddyProbe carries usable credentials plus a proxy-bound client.
type codeBuddyProbe struct {
	creds  codebuddy.Credentials
	client codebuddy.HTTPDoer
}

// newProbe prepares an admin-side probe. The token is refreshed only when it
// is near expiry: CodeBuddy rotates refresh tokens, so refreshing on every
// probe both wastes a round trip and risks stranding the account if the
// rotation is not persisted.
func (s *CodeBuddyOAuthService) newProbe(ctx context.Context, account *Account) (*codeBuddyProbe, error) {
	creds, err := account.CodeBuddyCredentials()
	if err != nil {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_CREDENTIALS_INVALID", err.Error())
	}
	if account.IsCodeBuddyOAuth() && codeBuddyTokenExpiring(account) {
		if refreshed, refreshErr := s.RefreshAccountToken(ctx, account); refreshErr == nil && refreshed != nil {
			s.persistRefreshedCredentials(ctx, account, refreshed)
			creds.AccessToken = refreshed.AccessToken
			creds.RefreshToken = refreshed.RefreshToken
			if refreshed.Domain != "" {
				creds.Domain = refreshed.Domain
			}
		}
	}
	client, err := s.clientForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	return &codeBuddyProbe{creds: creds, client: client}, nil
}

func codeBuddyTokenExpiring(account *Account) bool {
	if account.GetCodeBuddyAccessToken() == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	return expiresAt == nil || time.Until(*expiresAt) <= codeBuddyTokenRefreshSkew
}

func (s *CodeBuddyOAuthService) persistRefreshedCredentials(ctx context.Context, account *Account, refreshed *codebuddy.RefreshResult) {
	updated := s.ApplyRefresh(account, refreshed)
	account.Credentials = updated
	if s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.Update(ctx, account); err != nil {
		logger.L().Warn("codebuddy probe could not persist refreshed credentials",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
	}
}

// FetchCatalog pulls the account's /v3/config catalog.
func (s *CodeBuddyOAuthService) FetchCatalog(ctx context.Context, account *Account) ([]codebuddy.CatalogModel, error) {
	probe, err := s.newProbe(ctx, account)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, codeBuddyUpstreamTimeout)
	defer cancel()
	models, _, err := codebuddy.FetchCatalog(reqCtx, probe.client, probe.creds)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_CATALOG_FAILED", "%v", err)
	}
	return models, nil
}

func (s *CodeBuddyOAuthService) PersistCatalog(ctx context.Context, account *Account, models []codebuddy.CatalogModel) error {
	snapshot := CodeBuddyCatalogSnapshot{SyncedAt: time.Now().UTC().Format(time.RFC3339), Models: models}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[codeBuddyCatalogExtraKey] = snapshot
	if s == nil || s.accountRepo == nil {
		return nil
	}
	return s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{codeBuddyCatalogExtraKey: snapshot})
}

// SyncCatalog fetches and stores the catalog, returning the public model ids
// after the account's credit policy.
func (s *CodeBuddyOAuthService) SyncCatalog(ctx context.Context, account *Account) ([]string, error) {
	models, err := s.FetchCatalog(ctx, account)
	if err != nil {
		return nil, err
	}
	if err := s.PersistCatalog(ctx, account, models); err != nil {
		return nil, err
	}
	return codebuddy.CatalogIDs(codebuddy.FilterCatalog(models, account.CodeBuddyCreditPolicy())), nil
}

func (s *CodeBuddyOAuthService) QueryCredits(ctx context.Context, account *Account) (*codebuddy.CreditsSnapshot, error) {
	probe, err := s.newProbe(ctx, account)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*codeBuddyUpstreamTimeout)
	defer cancel()
	snapshot, err := codebuddy.FetchCredits(reqCtx, probe.client, probe.creds)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_CREDITS_FAILED", "%v", err)
	}
	s.storeExtra(ctx, account, codeBuddyCreditsExtraKey, snapshot)
	return snapshot, nil
}

// QueryRequestUsage pulls CodeBuddy's own billed consumption, which covers
// every client of the account rather than just this gateway.
func (s *CodeBuddyOAuthService) QueryRequestUsage(ctx context.Context, account *Account, days int) (*codebuddy.RequestUsage, error) {
	probe, err := s.newProbe(ctx, account)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*codeBuddyUpstreamTimeout)
	defer cancel()
	usage, err := codebuddy.FetchRequestUsage(reqCtx, probe.client, probe.creds, days)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEBUDDY_USAGE_FAILED", "%v", err)
	}
	s.storeExtra(ctx, account, codeBuddyRequestUsageExtraKey, usage)
	return usage, nil
}

func (s *CodeBuddyOAuthService) storeExtra(ctx context.Context, account *Account, key string, value any) {
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[key] = value
	if s.accountRepo != nil {
		_ = s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{key: value})
	}
}

// CodeBuddyCreditsReport pairs the balance with the official billing report
// and the gateway's own tally.
type CodeBuddyCreditsReport struct {
	*codebuddy.CreditsSnapshot
	Usage    codebuddy.CreditsUsage  `json:"usage"`
	Official *codebuddy.RequestUsage `json:"official,omitempty"`
	Stale    bool                    `json:"stale"`
	Profile  string                  `json:"profile,omitempty"`
}

// CodeBuddyCachedCreditsReport assembles the report from stored Extra only.
func (a *Account) CodeBuddyCachedCreditsReport() *CodeBuddyCreditsReport {
	if !a.IsCodeBuddy() {
		return nil
	}
	return &CodeBuddyCreditsReport{
		CreditsSnapshot: a.GetCodeBuddyCredits(),
		Usage:           a.GetCodeBuddyCreditsUsage(),
		Official:        a.GetCodeBuddyRequestUsage(),
		Stale:           a.CodeBuddyCreditsStale(),
		Profile:         a.CodeBuddyProfile(),
	}
}

// CreditsReport probes billing only when forced or when the cached balance
// aged out, so admin page loads do not each cost an upstream round trip.
func (s *CodeBuddyOAuthService) CreditsReport(ctx context.Context, account *Account, refresh bool) (*CodeBuddyCreditsReport, error) {
	if !account.IsCodeBuddy() {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_INVALID_ACCOUNT", "not a codebuddy account")
	}
	snapshot := account.GetCodeBuddyCredits()
	official := account.GetCodeBuddyRequestUsage()
	if refresh || snapshot == nil || account.CodeBuddyCreditsStale() {
		probed, err := s.QueryCredits(ctx, account)
		if err != nil {
			if snapshot == nil {
				return nil, err
			}
		} else {
			snapshot = probed
		}
		if fetched, usageErr := s.QueryRequestUsage(ctx, account, codebuddy.RequestUsageMaxDays); usageErr == nil {
			official = fetched
		} else {
			logger.L().Debug("codebuddy request usage probe failed",
				zap.Int64("account_id", account.ID),
				zap.Error(usageErr),
			)
		}
	}
	return &CodeBuddyCreditsReport{
		CreditsSnapshot: snapshot,
		Usage:           account.GetCodeBuddyCreditsUsage(),
		Official:        official,
		Stale:           account.CodeBuddyCreditsStale(),
		Profile:         account.CodeBuddyProfile(),
	}, nil
}

func (s *CodeBuddyOAuthService) clientForProxyID(ctx context.Context, proxyID *int64) (codebuddy.HTTPDoer, error) {
	proxyURL := ""
	if proxyID != nil {
		if s.proxyRepo == nil {
			return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_PROXY_NOT_AVAILABLE", "proxy repository is not available")
		}
		proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
		if err != nil || proxy == nil {
			if err != nil && !errors.Is(err, ErrProxyNotFound) {
				return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEBUDDY_OAUTH_PROXY_LOOKUP_FAILED", "proxy lookup is temporarily unavailable")
			}
			return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_PROXY_NOT_FOUND", "configured proxy was not found")
		}
		if proxyURL = proxy.URL(); proxyURL == "" {
			return nil, infraerrors.New(http.StatusBadRequest, "CODEBUDDY_OAUTH_PROXY_NOT_FOUND", "configured proxy has no usable URL")
		}
	}
	return newCodeBuddyHTTPClient(proxyURL)
}

// clientForAccount builds a client bound to the account's outbound proxy.
// A configured proxy that is not loaded is looked up; it never falls back to
// a direct connection.
func (s *CodeBuddyOAuthService) clientForAccount(ctx context.Context, account *Account) (codebuddy.HTTPDoer, error) {
	if account.ProxyID != nil && (account.Proxy == nil || account.Proxy.ID != *account.ProxyID) && s.proxyRepo != nil {
		proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if err == nil && proxy != nil {
			withProxy := *account
			withProxy.Proxy = proxy
			account = &withProxy
		}
	}
	proxyURL, err := account.ProxyURLForOutbound()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusServiceUnavailable, "CODEBUDDY_PROXY_UNAVAILABLE", "%v", err)
	}
	return newCodeBuddyHTTPClient(proxyURL)
}

func newCodeBuddyHTTPClient(proxyURL string) (codebuddy.HTTPDoer, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL: proxyURL,
		Timeout:  2 * codeBuddyUpstreamTimeout,
	})
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, "CODEBUDDY_PROXY_INVALID", "%v", err)
	}
	return client, nil
}
