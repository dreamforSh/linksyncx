package admin

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/codebuddy"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// CodeBuddyOAuthHandler serves the CodeBuddy / WorkBuddy account lifecycle:
// scan login (domestic / international), .info and refresh-token import,
// re-authorization, token refresh, catalog sync and credits.
type CodeBuddyOAuthHandler struct {
	oauthService *service.CodeBuddyOAuthService
	adminService service.AdminService
}

func NewCodeBuddyOAuthHandler(oauthService *service.CodeBuddyOAuthService, adminService service.AdminService) *CodeBuddyOAuthHandler {
	return &CodeBuddyOAuthHandler{oauthService: oauthService, adminService: adminService}
}

// Start POST /admin/codebuddy/oauth/start {site: cn|intl|intl-codebuddy, proxy_id}
func (h *CodeBuddyOAuthHandler) Start(c *gin.Context) {
	var req struct {
		Site    string `json:"site"`
		ProxyID *int64 `json:"proxy_id"`
	}
	_ = c.ShouldBindJSON(&req)
	if strings.TrimSpace(req.Site) == "" {
		req.Site = codebuddy.LoginSiteCN
	}
	result, err := h.oauthService.Start(c.Request.Context(), req.Site, req.ProxyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Poll POST|GET /admin/codebuddy/oauth/poll {login_id}
func (h *CodeBuddyOAuthHandler) Poll(c *gin.Context) {
	loginID := strings.TrimSpace(c.Query("login_id"))
	if loginID == "" {
		var req struct {
			LoginID string `json:"login_id"`
		}
		_ = c.ShouldBindJSON(&req)
		loginID = strings.TrimSpace(req.LoginID)
	}
	if loginID == "" {
		response.BadRequest(c, "login_id is required")
		return
	}
	response.Success(c, h.oauthService.Poll(c.Request.Context(), loginID))
}

type codeBuddyCredentialSource struct {
	LoginID      string         `json:"login_id"`
	Credentials  map[string]any `json:"credentials"`
	RefreshToken string         `json:"refresh_token"`
	Profile      string         `json:"profile"`
}

// resolveCredentials turns a completed scan login, a pasted .info document or
// a pasted refresh token (with an explicit profile) into stored credentials.
// It writes the error response itself and reports false when the caller
// should stop.
func (h *CodeBuddyOAuthHandler) resolveCredentials(c *gin.Context, src codeBuddyCredentialSource, proxyID *int64) (map[string]any, bool) {
	var (
		creds *codebuddy.Credentials
		err   error
	)
	switch {
	case strings.TrimSpace(src.LoginID) != "":
		creds, err = h.oauthService.Take(strings.TrimSpace(src.LoginID))
	case len(src.Credentials) > 0:
		creds, err = h.oauthService.ImportCredentials(src.Credentials)
	case strings.TrimSpace(src.RefreshToken) != "":
		creds, err = h.oauthService.ImportRefreshToken(c.Request.Context(), strings.TrimSpace(src.Profile), src.RefreshToken, proxyID)
	default:
		response.BadRequest(c, "login_id, credentials or refresh_token is required")
		return nil, false
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	return h.oauthService.BuildAccountCredentials(creds), true
}

// CreateAccountFromOAuth POST /admin/codebuddy/oauth/create-from-oauth
func (h *CodeBuddyOAuthHandler) CreateAccountFromOAuth(c *gin.Context) {
	var req struct {
		codeBuddyCredentialSource
		CreditPolicy   string         `json:"credit_policy"`
		ModelMapping   map[string]any `json:"model_mapping"`
		ProxyID        *int64         `json:"proxy_id"`
		Name           string         `json:"name"`
		Notes          *string        `json:"notes"`
		Concurrency    int            `json:"concurrency"`
		Priority       int            `json:"priority"`
		RateMultiplier *float64       `json:"rate_multiplier"`
		LoadFactor     *int           `json:"load_factor"`
		GroupIDs       []int64        `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	credentials, ok := h.resolveCredentials(c, req.codeBuddyCredentialSource, req.ProxyID)
	if !ok {
		return
	}
	if policy := strings.TrimSpace(req.CreditPolicy); policy != "" {
		credentials["credit_policy"] = codebuddy.NormalizeCreditPolicy(policy)
	}
	if len(req.ModelMapping) > 0 {
		credentials["model_mapping"] = req.ModelMapping
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = codeBuddyDefaultAccountName(credentials)
	}
	account, err := h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{
		Name:           name,
		Notes:          req.Notes,
		Platform:       service.PlatformCodeBuddy,
		Type:           service.AccountTypeOAuth,
		Credentials:    credentials,
		ProxyID:        req.ProxyID,
		Concurrency:    req.Concurrency,
		Priority:       req.Priority,
		RateMultiplier: req.RateMultiplier,
		LoadFactor:     req.LoadFactor,
		GroupIDs:       req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.scheduleInitialProbe(account.ID)
	response.Success(c, dto.AccountFromService(account))
}

// scheduleInitialProbe syncs the catalog and balance in the background so
// account creation does not wait on two upstream round trips.
func (h *CodeBuddyOAuthHandler) scheduleInitialProbe(accountID int64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		account, err := h.adminService.GetAccount(ctx, accountID)
		if err != nil || account == nil {
			return
		}
		if _, err := h.oauthService.SyncCatalog(ctx, account); err != nil {
			logger.L().Debug("codebuddy initial catalog sync failed", zap.Int64("account_id", accountID), zap.Error(err))
		}
		if _, err := h.oauthService.QueryCredits(ctx, account); err != nil {
			logger.L().Debug("codebuddy initial credits probe failed", zap.Int64("account_id", accountID), zap.Error(err))
		}
	}()
}

func codeBuddyDefaultAccountName(credentials map[string]any) string {
	region := "CN"
	if site, _ := credentials[codebuddy.KeySite].(string); site == codebuddy.SiteIntl {
		region = "Intl"
	}
	product := "CodeBuddy"
	if p, _ := credentials[codebuddy.KeyProduct].(string); p == codebuddy.ProductWorkBuddy {
		product = "WorkBuddy"
	}
	if nickname, _ := credentials[codebuddy.KeyNickname].(string); strings.TrimSpace(nickname) != "" {
		return product + " " + region + " " + strings.TrimSpace(nickname)
	}
	if uid, _ := credentials[codebuddy.KeyUID].(string); uid != "" {
		return product + " " + region + " " + uid
	}
	return product + " " + region
}

func (h *CodeBuddyOAuthHandler) loadAccount(c *gin.Context, requireOAuth bool) (*service.Account, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return nil, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	if account == nil || !account.IsCodeBuddy() || (requireOAuth && !account.IsCodeBuddyOAuth()) {
		response.BadRequest(c, "not a codebuddy oauth account")
		return nil, false
	}
	return account, true
}

// ReAuthAccount POST /admin/codebuddy/accounts/:id/reauth swaps in fresh
// credentials while keeping groups, priority and usage history. A different
// CodeBuddy identity is rejected: that is a new account.
func (h *CodeBuddyOAuthHandler) ReAuthAccount(c *gin.Context) {
	account, ok := h.loadAccount(c, false)
	if !ok {
		return
	}
	var req codeBuddyCredentialSource
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	credentials, ok := h.resolveCredentials(c, req, account.ProxyID)
	if !ok {
		return
	}
	previousUID, _ := account.Credentials[codebuddy.KeyUID].(string)
	newUID, _ := credentials[codebuddy.KeyUID].(string)
	if strings.TrimSpace(previousUID) != "" && strings.TrimSpace(newUID) != strings.TrimSpace(previousUID) {
		response.BadRequest(c, "the authorized CodeBuddy account does not match this account's uid")
		return
	}
	for _, key := range []string{"credit_policy", "model_mapping"} {
		if value, exists := account.Credentials[key]; exists {
			credentials[key] = value
		}
	}
	// Keep the subscription tier if the fresh source did not report one, so a
	// re-auth never blanks an account's previously known account_type.
	if _, has := credentials[codebuddy.KeyAccountType]; !has {
		if value, exists := account.Credentials[codebuddy.KeyAccountType]; exists {
			credentials[codebuddy.KeyAccountType] = value
		}
	}
	updated, err := h.adminService.UpdateAccount(c.Request.Context(), account.ID, &service.UpdateAccountInput{Credentials: credentials})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.scheduleInitialProbe(updated.ID)
	response.Success(c, dto.AccountFromService(updated))
}

// RefreshAccountToken POST /admin/codebuddy/accounts/:id/refresh
func (h *CodeBuddyOAuthHandler) RefreshAccountToken(c *gin.Context) {
	account, ok := h.loadAccount(c, true)
	if !ok {
		return
	}
	result, err := h.oauthService.RefreshAccountToken(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updated, err := h.adminService.UpdateAccount(c.Request.Context(), account.ID, &service.UpdateAccountInput{
		Credentials: h.oauthService.ApplyRefresh(account, result),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, dto.AccountFromService(updated))
}

// SyncModels POST /admin/codebuddy/accounts/:id/sync-models
func (h *CodeBuddyOAuthHandler) SyncModels(c *gin.Context) {
	account, ok := h.loadAccount(c, false)
	if !ok {
		return
	}
	ids, err := h.oauthService.SyncCatalog(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"models": ids, "catalog": account.GetCodeBuddyCatalog()})
}

// QueryCredits GET /admin/codebuddy/accounts/:id/credits?refresh=true
func (h *CodeBuddyOAuthHandler) QueryCredits(c *gin.Context) {
	account, ok := h.loadAccount(c, false)
	if !ok {
		return
	}
	refresh, _ := strconv.ParseBool(c.DefaultQuery("refresh", "true"))
	report, err := h.oauthService.CreditsReport(c.Request.Context(), account, refresh)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, report)
}

// QueryRequestUsage GET /admin/codebuddy/accounts/:id/request-usage?days=30
func (h *CodeBuddyOAuthHandler) QueryRequestUsage(c *gin.Context) {
	account, ok := h.loadAccount(c, false)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(c.DefaultQuery("days", strconv.Itoa(codebuddy.RequestUsageMaxDays)))
	usage, err := h.oauthService.QueryRequestUsage(c.Request.Context(), account, days)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, usage)
}
