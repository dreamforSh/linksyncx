package service

import (
	"context"
	"errors"
	"time"
)

// CodeBuddyTokenRefresher plugs CodeBuddy into the background refresh loop
// and the request-path OAuthRefreshAPI.
type CodeBuddyTokenRefresher struct {
	oauthService *CodeBuddyOAuthService
}

func NewCodeBuddyTokenRefresher(oauthService *CodeBuddyOAuthService) *CodeBuddyTokenRefresher {
	return &CodeBuddyTokenRefresher{oauthService: oauthService}
}

func (r *CodeBuddyTokenRefresher) CacheKey(account *Account) string {
	return CodeBuddyTokenCacheKey(account)
}

func (r *CodeBuddyTokenRefresher) CanRefresh(account *Account) bool {
	return account.IsCodeBuddyOAuth() && account.GetCodeBuddyRefreshToken() != ""
}

func (r *CodeBuddyTokenRefresher) NeedsRefresh(account *Account, refreshWindow time.Duration) bool {
	if !r.CanRefresh(account) {
		return false
	}
	if account.GetCodeBuddyAccessToken() == "" {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return true
	}
	if refreshWindow < codeBuddyTokenRefreshSkew {
		refreshWindow = codeBuddyTokenRefreshSkew
	}
	return time.Until(*expiresAt) < refreshWindow
}

func (r *CodeBuddyTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r == nil || r.oauthService == nil {
		return nil, errors.New("codebuddy oauth service is not configured")
	}
	result, err := r.oauthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}
	return r.oauthService.ApplyRefresh(account, result), nil
}
