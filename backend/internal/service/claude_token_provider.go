package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

const (
	// claudeTokenRefreshSkew 对齐真实 Claude Code 2.1.287：距过期不足 5 分钟时在请求
	// 路径上刷新（pF: now+300000 >= expiresAt）。与 claudeTokenCacheSkew 相等，缓存
	// 在过期前 5 分钟失效后的第一个请求即触发刷新，不会把将过期的令牌重新放回缓存。
	claudeTokenRefreshSkew = 5 * time.Minute
	claudeTokenCacheSkew   = 5 * time.Minute
	claudeLockWaitTime     = 200 * time.Millisecond
)

// ClaudeTokenCache token cache interface.
type ClaudeTokenCache = GeminiTokenCache

// ClaudeTokenProvider manages access_token for Claude OAuth, setup-token (with a refresh token)
// and Vertex service account accounts.
type ClaudeTokenProvider struct {
	accountRepo   AccountRepository
	tokenCache    ClaudeTokenCache
	oauthService  *OAuthService
	refreshAPI    *OAuthRefreshAPI
	executor      OAuthRefreshExecutor
	refreshPolicy ProviderRefreshPolicy
}

func NewClaudeTokenProvider(
	accountRepo AccountRepository,
	tokenCache ClaudeTokenCache,
	oauthService *OAuthService,
) *ClaudeTokenProvider {
	return &ClaudeTokenProvider{
		accountRepo:   accountRepo,
		tokenCache:    tokenCache,
		oauthService:  oauthService,
		refreshPolicy: ClaudeProviderRefreshPolicy(),
	}
}

// SetRefreshAPI injects unified OAuth refresh API and executor.
func (p *ClaudeTokenProvider) SetRefreshAPI(api *OAuthRefreshAPI, executor OAuthRefreshExecutor) {
	p.refreshAPI = api
	p.executor = executor
}

// SetRefreshPolicy injects caller-side refresh policy.
func (p *ClaudeTokenProvider) SetRefreshPolicy(policy ProviderRefreshPolicy) {
	p.refreshPolicy = policy
}

// GetAccessToken returns a valid access_token.
func (p *ClaudeTokenProvider) GetAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	if account.Platform != PlatformAnthropic || (account.Type != AccountTypeOAuth && account.Type != AccountTypeServiceAccount && account.Type != AccountTypeSetupToken) {
		return "", errors.New("not an anthropic oauth or service account")
	}
	if account.Type == AccountTypeServiceAccount {
		return p.getServiceAccountAccessToken(ctx, account)
	}
	// setup-token 通常是一年期 access token、没有 refresh token，直接用凭据里的值。
	// 带 refresh token 的老 setup-token 账号与 OAuth 一样在请求路径上按需刷新：
	// 后台刷新只覆盖活跃账号（D7），不能再指望它让闲置账号的 token 保持有效。
	if account.Type == AccountTypeSetupToken && !ClaudeSetupTokenRefreshable(account) {
		accessToken := account.GetCredential("access_token")
		if strings.TrimSpace(accessToken) == "" {
			return "", errors.New("access_token not found in credentials")
		}
		return accessToken, nil
	}

	cacheKey := ClaudeTokenCacheKey(account)

	// 1) Try cache first.
	if p.tokenCache != nil {
		if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			slog.Debug("claude_token_cache_hit", "account_id", account.ID)
			return token, nil
		} else if err != nil {
			slog.Warn("claude_token_cache_get_failed", "account_id", account.ID, "error", err)
		}
	}

	slog.Debug("claude_token_cache_miss", "account_id", account.ID)

	// 2) Refresh if needed (pre-expiry skew).
	expiresAt := account.GetCredentialAsTime("expires_at")
	needsRefresh := expiresAt == nil || time.Until(*expiresAt) <= claudeTokenRefreshSkew
	refreshFailed := false

	if needsRefresh && p.refreshAPI != nil && p.executor != nil {
		result, err := p.refreshAPI.RefreshIfNeeded(ctx, account, p.executor, claudeTokenRefreshSkew)
		if err != nil {
			if p.refreshPolicy.OnRefreshError == ProviderRefreshErrorReturn {
				return "", err
			}
			slog.Warn("claude_token_refresh_failed", "account_id", account.ID, "error", err)
			refreshFailed = true
		} else if result.LockHeld {
			if p.refreshPolicy.OnLockHeld == ProviderLockHeldWaitForCache && p.tokenCache != nil {
				time.Sleep(claudeLockWaitTime)
				if token, cacheErr := p.tokenCache.GetAccessToken(ctx, cacheKey); cacheErr == nil && strings.TrimSpace(token) != "" {
					slog.Debug("claude_token_cache_hit_after_wait", "account_id", account.ID)
					return token, nil
				}
			}
		} else {
			account = result.Account
			expiresAt = account.GetCredentialAsTime("expires_at")
		}
	} else if needsRefresh && p.tokenCache != nil {
		// Backward-compatible test path when refreshAPI is not injected.
		locked, lockErr := p.tokenCache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = p.tokenCache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			slog.Warn("claude_token_lock_failed", "account_id", account.ID, "error", lockErr)
		} else {
			time.Sleep(claudeLockWaitTime)
			if token, err := p.tokenCache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				slog.Debug("claude_token_cache_hit_after_wait", "account_id", account.ID)
				return token, nil
			}
		}
	}

	accessToken := account.GetCredential("access_token")
	if strings.TrimSpace(accessToken) == "" {
		return "", errors.New("access_token not found in credentials")
	}

	// 3) Populate cache with TTL.
	if p.tokenCache != nil {
		latestAccount, isStale := CheckTokenVersion(ctx, account, p.accountRepo)
		if isStale && latestAccount != nil {
			slog.Debug("claude_token_version_stale_use_latest", "account_id", account.ID)
			accessToken = latestAccount.GetCredential("access_token")
			if strings.TrimSpace(accessToken) == "" {
				return "", errors.New("access_token not found after version check")
			}
		} else {
			ttl := 30 * time.Minute
			if refreshFailed {
				if p.refreshPolicy.FailureTTL > 0 {
					ttl = p.refreshPolicy.FailureTTL
				} else {
					ttl = time.Minute
				}
				slog.Debug("claude_token_cache_short_ttl", "account_id", account.ID, "reason", "refresh_failed")
			} else if expiresAt != nil {
				until := time.Until(*expiresAt)
				switch {
				case until > claudeTokenCacheSkew:
					ttl = until - claudeTokenCacheSkew
				case until > 0:
					ttl = until
				default:
					ttl = time.Minute
				}
			}
			if err := p.tokenCache.SetAccessToken(ctx, cacheKey, accessToken, ttl); err != nil {
				slog.Warn("claude_token_cache_set_failed", "account_id", account.ID, "error", err)
			}
		}
	}

	return accessToken, nil
}

// ClaudeSetupTokenRefreshable 报告 setup-token 账号是否带 refresh token、可以按需刷新。
func ClaudeSetupTokenRefreshable(account *Account) bool {
	return account != nil && account.Type == AccountTypeSetupToken &&
		strings.TrimSpace(account.GetCredential("refresh_token")) != ""
}

func (p *ClaudeTokenProvider) getServiceAccountAccessToken(ctx context.Context, account *Account) (string, error) {
	return getVertexServiceAccountAccessToken(ctx, p.tokenCache, account)
}
