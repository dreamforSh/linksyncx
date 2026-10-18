package codebuddy

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Credentials is the normalized CodeBuddy credential stored on an account.
type Credentials struct {
	AccessToken      string
	RefreshToken     string
	IDToken          string
	TokenType        string
	ExpiresAt        int64 // unix seconds
	RefreshExpiresAt int64 // unix seconds
	Domain           string
	UID              string
	Nickname         string
	EnterpriseID     string
	EnterpriseName   string
	AccountType      string // personal / ultimate / exclusive / ...
	Profile          string
	Site             string
	Product          string
}

// Stored credential keys.
const (
	KeyAccessToken      = "access_token"
	KeyRefreshToken     = "refresh_token"
	KeyIDToken          = "id_token"
	KeyTokenType        = "token_type"
	KeyExpiresAt        = "expires_at"
	KeyRefreshExpiresAt = "refresh_expires_at"
	KeyDomain           = "domain"
	KeyUID              = "uid"
	KeyNickname         = "nickname"
	KeyEnterpriseID     = "enterprise_id"
	KeyEnterpriseName   = "enterprise_name"
	KeyAccountType      = "account_type"
	KeyProfile          = "profile"
	KeySite             = "site"
	KeyProduct          = "product"
)

// IsEnterprise reports whether the account bills through an enterprise
// tenant: a tenant id is a direct hit, and some backups carry only the
// ultimate/exclusive account type.
func (c Credentials) IsEnterprise() bool {
	if strings.TrimSpace(c.EnterpriseID) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(c.AccountType)) {
	case "ultimate", "exclusive":
		return true
	}
	return false
}

// Valid checks the minimum a usable credential needs.
func (c Credentials) Valid() error {
	if strings.TrimSpace(c.AccessToken) == "" {
		return fmt.Errorf("missing access_token")
	}
	if strings.TrimSpace(c.UID) == "" {
		return fmt.Errorf("missing uid")
	}
	if !OriginAllowed(c.Domain, c.AccessToken) {
		return fmt.Errorf("authentication domain is not an official CodeBuddy / WorkBuddy origin")
	}
	if _, err := ProfileForAuth(c.Domain, c.AccessToken); err != nil {
		return err
	}
	return nil
}

// ParseCredentials accepts the stored flat shape, the official desktop .info
// shape ({"account": {...}, "auth": {...}}) and loose camelCase variants.
func ParseCredentials(raw map[string]any) (Credentials, error) {
	if raw == nil {
		return Credentials{}, fmt.Errorf("credentials are empty")
	}
	if nested, ok := raw["auth"].(map[string]any); ok {
		return parseOfficialInfo(raw, nested)
	}
	creds := Credentials{
		AccessToken:      stringField(raw, KeyAccessToken, "accessToken", "token"),
		RefreshToken:     stringField(raw, KeyRefreshToken, "refreshToken"),
		IDToken:          stringField(raw, KeyIDToken, "idToken"),
		TokenType:        firstNonEmpty(stringField(raw, KeyTokenType, "tokenType"), "Bearer"),
		ExpiresAt:        unixSeconds(raw[KeyExpiresAt], raw["expiresAt"]),
		RefreshExpiresAt: unixSeconds(raw[KeyRefreshExpiresAt], raw["refreshExpiresAt"]),
		Domain:           stringField(raw, KeyDomain),
		UID:              stringField(raw, KeyUID),
		Nickname:         stringField(raw, KeyNickname),
		EnterpriseID:     stringField(raw, KeyEnterpriseID, "enterpriseId"),
		EnterpriseName:   stringField(raw, KeyEnterpriseName, "enterpriseName"),
		AccountType:      stringField(raw, KeyAccountType, "accountType"),
		Profile:          stringField(raw, KeyProfile),
	}
	return finalizeCredentials(creds)
}

func parseOfficialInfo(root, auth map[string]any) (Credentials, error) {
	account := mapField(root, "account")
	if len(account) == 0 {
		if accounts, ok := root["accounts"].([]any); ok && len(accounts) > 0 {
			account, _ = accounts[0].(map[string]any)
		}
	}
	if err := rejectEncryptedTokens(auth); err != nil {
		return Credentials{}, err
	}
	creds := Credentials{
		AccessToken:      stringField(auth, "accessToken", KeyAccessToken, "token"),
		RefreshToken:     stringField(auth, "refreshToken", KeyRefreshToken),
		IDToken:          stringField(auth, "idToken", KeyIDToken),
		TokenType:        firstNonEmpty(stringField(auth, "tokenType", KeyTokenType), "Bearer"),
		ExpiresAt:        unixSeconds(auth["expiresAt"], auth[KeyExpiresAt]),
		RefreshExpiresAt: unixSeconds(auth["refreshExpiresAt"], auth[KeyRefreshExpiresAt]),
		Domain:           stringField(auth, "domain"),
		UID:              uidField(account),
		Nickname:         stringField(account, "nickname"),
		EnterpriseID:     stringField(account, "enterpriseId", KeyEnterpriseID),
		EnterpriseName:   stringField(account, "enterpriseName", KeyEnterpriseName),
		AccountType:      stringField(account, "type"),
	}
	if creds.ExpiresAt == 0 {
		if in := unixSeconds(auth["expiresIn"]); in > 0 && in < 1e10 {
			if last := unixSeconds(auth["lastRefreshTime"]); last > 0 {
				creds.ExpiresAt = last + in
			}
		}
	}
	return finalizeCredentials(creds)
}

func finalizeCredentials(creds Credentials) (Credentials, error) {
	authProfile, err := ProfileForAuth(creds.Domain, creds.AccessToken)
	if err != nil {
		return Credentials{}, err
	}
	// A stored profile wins only when it agrees with the token on the region;
	// the domestic issuer cannot tell CLI from WorkBuddy, so the stored value
	// carries that choice.
	if !ValidProfile(creds.Profile) || ProfileRegion(creds.Profile) != ProfileRegion(authProfile) {
		creds.Profile = authProfile
	}
	creds.Site = ProfileRegion(creds.Profile)
	creds.Product = ProfileProduct(creds.Profile)
	if creds.Domain == "" {
		creds.Domain = DomainForAuth("", creds.AccessToken)
		if creds.Domain == DefaultDomain || creds.Domain == copilotIssuerHost {
			creds.Domain = ProfileDomains[creds.Profile]
		}
	}
	if err := creds.Valid(); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

// ToMap renders the credential in the stored flat shape. Expiry timestamps are
// RFC3339 strings, the format the account layer's expiry helpers read.
func (c Credentials) ToMap() map[string]any {
	out := map[string]any{
		KeyAccessToken:  c.AccessToken,
		KeyRefreshToken: c.RefreshToken,
		KeyTokenType:    firstNonEmpty(c.TokenType, "Bearer"),
		KeyDomain:       c.Domain,
		KeyUID:          c.UID,
		KeyProfile:      c.Profile,
		KeySite:         c.Site,
		KeyProduct:      c.Product,
	}
	optional := map[string]string{
		KeyIDToken:        c.IDToken,
		KeyNickname:       c.Nickname,
		KeyEnterpriseID:   c.EnterpriseID,
		KeyEnterpriseName: c.EnterpriseName,
		KeyAccountType:    c.AccountType,
	}
	for key, value := range optional {
		if value != "" {
			out[key] = value
		}
	}
	if c.ExpiresAt > 0 {
		out[KeyExpiresAt] = time.Unix(c.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	if c.RefreshExpiresAt > 0 {
		out[KeyRefreshExpiresAt] = time.Unix(c.RefreshExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	return out
}

// rejectEncryptedTokens refuses desktop exports whose tokens are encrypted
// envelopes rather than plaintext strings.
func rejectEncryptedTokens(auth map[string]any) error {
	for _, key := range []string{"accessToken", "refreshToken", KeyAccessToken, KeyRefreshToken} {
		if value, ok := auth[key]; ok && value != nil {
			if _, isString := value.(string); !isString {
				return fmt.Errorf("%s must be a plaintext token string (encrypted desktop exports are not supported)", key)
			}
		}
	}
	return nil
}

func uidField(m map[string]any) string {
	if m == nil {
		return ""
	}
	switch v := m["uid"].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		if v == math.Trunc(v) && !math.IsInf(v, 0) {
			return strconv.FormatInt(int64(v), 10)
		}
	case json.Number:
		return v.String()
	}
	return ""
}

func stringField(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	v, _ := m[key].(map[string]any)
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func unixSeconds(values ...any) int64 {
	for _, v := range values {
		if ts := toUnixSeconds(v); ts > 0 {
			return ts
		}
	}
	return 0
}

// toUnixSeconds accepts seconds, milliseconds (official .info files) and
// RFC3339 strings (stored credentials).
func toUnixSeconds(v any) int64 {
	var n int64
	switch t := v.(type) {
	case int64:
		n = t
	case int:
		n = int64(t)
	case float64:
		n = int64(t)
	case json.Number:
		parsed, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0
			}
			parsed = int64(f)
		}
		n = parsed
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			return ts.Unix()
		}
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0
		}
		n = parsed
	default:
		return 0
	}
	if n <= 0 {
		return 0
	}
	if n > 1e12 { // milliseconds
		return n / 1000
	}
	return n
}
