package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	codeBuddyTokenCacheSkew          = 2 * time.Minute
	codeBuddyRequestRefreshTimeout   = 10 * time.Second
	codeBuddyRefreshLockWaitTimeout  = 3 * time.Second
	codeBuddyRefreshLockPollInterval = 50 * time.Millisecond
)

var (
	errCodeBuddyRefreshNotConfigured = errors.New("codebuddy oauth refresh is not configured")
	errCodeBuddyAccessTokenMissing   = errors.New("codebuddy access token is missing")
	errCodeBuddyRefreshTokenMissing  = errors.New("codebuddy refresh token is missing")
	errCodeBuddyAccessTokenExpired   = errors.New("codebuddy access token is expired")
)

// CodeBuddyTokenProvider hands out a valid access token, refreshing through
// the shared OAuthRefreshAPI (distributed lock + DB re-read) when the token is
// within codeBuddyTokenRefreshSkew of expiry. CodeBuddy rotates refresh
// tokens, so every refresh must go through that single locked path.
type CodeBuddyTokenProvider struct {
	accountRepo   AccountRepository
	tokenCache    GeminiTokenCache
	refreshAPI    *OAuthRefreshAPI
	executor      OAuthRefreshExecutor
	refreshPolicy ProviderRefreshPolicy
}

func NewCodeBuddyTokenProvider(accountRepo AccountRepository, tokenCache GeminiTokenCache) *CodeBuddyTokenProvider {
	return &CodeBuddyTokenProvider{
		accountRepo:   accountRepo,
		tokenCache:    tokenCache,
		refreshPolicy: CodeBuddyProviderRefreshPolicy(),
	}
}

func (p *CodeBuddyTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

func (p *CodeBuddyTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if !account.IsCodeBuddyOAuth() {
		return "", errors.New("not a codebuddy oauth account")
	}
	accessToken := account.GetCodeBuddyAccessToken()
	if accessToken == "" {
		return "", errCodeBuddyAccessTokenMissing
	}
	if account.GetCodeBuddyRefreshToken() == "" {
		return "", errCodeBuddyRefreshTokenMissing
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	cacheKey := CodeBuddyTokenCacheKey(account)
	if p.tokenCache != nil && expiresAt != nil && time.Until(*expiresAt) > codeBuddyTokenRefreshSkew {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) == accessToken {
			return accessToken, nil
		}
	}

	if expiresAt == nil || time.Until(*expiresAt) <= codeBuddyTokenRefreshSkew {
		if p.refreshAPI == nil || p.executor == nil {
			if expiresAt != nil && time.Now().Before(*expiresAt) {
				return accessToken, nil
			}
			return "", errCodeBuddyRefreshNotConfigured
		}
		refreshCtx, cancel := context.WithTimeout(ctx, codeBuddyRequestRefreshTimeout)
		defer cancel()
		result, err := p.refreshAPI.RefreshIfNeeded(withOAuthRefreshRequestPath(refreshCtx), account, p.executor, codeBuddyTokenRefreshSkew)
		switch {
		case err != nil:
			// A still-valid token keeps serving while refresh is failing.
			if expiresAt != nil && time.Now().Before(*expiresAt) {
				return accessToken, nil
			}
			return "", err
		case result != nil && result.LockHeld:
			if token, waitErr := p.waitForRefreshedToken(refreshCtx, account, cacheKey); waitErr == nil {
				return token, nil
			}
			if expiresAt == nil || !time.Now().Before(*expiresAt) {
				return "", errCodeBuddyAccessTokenExpired
			}
		case result != nil && result.Account != nil:
			account = result.Account
			accessToken = account.GetCodeBuddyAccessToken()
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	}
	if accessToken == "" {
		return "", errCodeBuddyAccessTokenMissing
	}
	if expiresAt != nil && !time.Now().Before(*expiresAt) {
		return "", errCodeBuddyAccessTokenExpired
	}
	if p.tokenCache != nil && expiresAt != nil {
		if latest, stale := CheckTokenVersion(ctx, account, p.accountRepo); stale && latest != nil {
			if token := latest.GetCodeBuddyAccessToken(); token != "" {
				return token, nil
			}
		} else if ttl := time.Until(*expiresAt) - codeBuddyTokenCacheSkew; ttl > 0 {
			_ = p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
		}
	}
	return accessToken, nil
}

func (p *CodeBuddyTokenProvider) waitForRefreshedToken(ctx context.Context, account *Account, cacheKey string) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, codeBuddyRefreshLockWaitTimeout)
	defer cancel()
	initial := account.GetCodeBuddyAccessToken()
	ticker := time.NewTicker(codeBuddyRefreshLockPollInterval)
	defer ticker.Stop()
	for {
		if p.accountRepo != nil {
			if latest, err := p.accountRepo.GetByID(waitCtx, account.ID); err == nil && latest != nil {
				token := latest.GetCodeBuddyAccessToken()
				expiresAt := latest.GetCredentialAsTime("expires_at")
				if token != "" && token != initial && expiresAt != nil && time.Now().Before(*expiresAt) {
					return token, nil
				}
			}
		} else if p.tokenCache != nil {
			if token, err := p.tokenCache.GetAccessToken(waitCtx, cacheKey); err == nil && strings.TrimSpace(token) != "" && strings.TrimSpace(token) != initial {
				return strings.TrimSpace(token), nil
			}
		}
		select {
		case <-waitCtx.Done():
			return "", waitCtx.Err()
		case <-ticker.C:
		}
	}
}
