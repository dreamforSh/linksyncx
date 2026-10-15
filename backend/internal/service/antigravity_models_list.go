package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"golang.org/x/sync/singleflight"
)

const antigravityModelsCacheTTL = 5 * time.Minute

// antigravityModelsLoad, when set, replaces the live upstream fetch.
// Production leaves it nil. Tests use it to avoid calling Google.
type antigravityModelsLoader func(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error)

type antigravityModelsCacheEntry struct {
	models    []antigravity.ClaudeModel
	expiresAt time.Time
}

type antigravityModelsCacheStore struct {
	mu    sync.Mutex
	items map[string]antigravityModelsCacheEntry
	group singleflight.Group
}

var antigravityModelsCache = &antigravityModelsCacheStore{
	items: map[string]antigravityModelsCacheEntry{},
}

func resetAntigravityModelsCache() {
	antigravityModelsCache.mu.Lock()
	antigravityModelsCache.items = map[string]antigravityModelsCacheEntry{}
	antigravityModelsCache.mu.Unlock()
}

func (c *antigravityModelsCacheStore) get(key string, now time.Time) ([]antigravity.ClaudeModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.items[key]
	if !ok || !now.Before(entry.expiresAt) {
		return nil, false
	}
	return cloneAntigravityClaudeModels(entry.models), true
}

func (c *antigravityModelsCacheStore) set(key string, models []antigravity.ClaudeModel, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = antigravityModelsCacheEntry{
		models:    cloneAntigravityClaudeModels(models),
		expiresAt: now.Add(antigravityModelsCacheTTL),
	}
}

func cloneAntigravityClaudeModels(models []antigravity.ClaudeModel) []antigravity.ClaudeModel {
	if models == nil {
		return []antigravity.ClaudeModel{}
	}
	out := make([]antigravity.ClaudeModel, len(models))
	copy(out, models)
	return out
}

// antigravityModelsCacheKey fingerprints the account without the access token.
// A token refresh must not miss the cache; a mapping, base URL, or key change must.
func antigravityModelsCacheKey(account *Account) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d\n%s\n%s\n%s\n",
		account.ID,
		account.Type,
		strings.TrimSpace(account.GetCredential("project_id")),
		strings.TrimSpace(account.GetCredential("base_url")),
	)
	sum := sha256.Sum256([]byte(account.GetCredential("api_key")))
	_, _ = h.Write(sum[:])
	mapping := account.GetModelMapping()
	keys := make([]string, 0, len(mapping))
	for alias, target := range mapping {
		keys = append(keys, alias+"\x00"+target)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = h.Write([]byte{'\n'})
		_, _ = h.Write([]byte(key))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FetchAntigravityAccountModels returns the account's public model aliases.
// A live fetch error falls back to the static catalog and is not cached.
// A successful fetch that projects to nothing stays empty.
func (s *AccountTestService) FetchAntigravityAccountModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if s == nil || account == nil {
		return antigravity.DefaultModels(), nil
	}
	key := antigravityModelsCacheKey(account)
	if cached, ok := antigravityModelsCache.get(key, time.Now()); ok {
		return cached, nil
	}
	v, err, _ := antigravityModelsCache.group.Do(key, func() (any, error) {
		if cached, ok := antigravityModelsCache.get(key, time.Now()); ok {
			return cached, nil
		}
		load := s.antigravityModelsLoad
		if load == nil {
			load = s.loadAntigravityUpstreamModels
		}
		loaded, loadErr := load(ctx, account)
		if loadErr != nil {
			slog.Warn("antigravity model discovery failed; using static catalog", "account_id", account.ID, "error", loadErr)
			return antigravity.DefaultModels(), nil
		}
		if loaded == nil {
			loaded = []antigravity.ClaudeModel{}
		}
		projected := projectAntigravityAccountModels(loaded, account)
		antigravityModelsCache.set(key, projected, time.Now())
		return cloneAntigravityClaudeModels(projected), nil
	})
	if err != nil || v == nil {
		return antigravity.DefaultModels(), nil
	}
	models, ok := v.([]antigravity.ClaudeModel)
	if !ok || models == nil {
		return antigravity.DefaultModels(), nil
	}
	return cloneAntigravityClaudeModels(models), nil
}

func (s *AccountTestService) loadAntigravityUpstreamModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if account == nil {
		return nil, errors.New("account is required")
	}
	if account.Type == AccountTypeAPIKey {
		return s.loadAntigravityAPIKeyModels(ctx, account)
	}
	return s.loadAntigravityOAuthModels(ctx, account)
}

func (s *AccountTestService) loadAntigravityAPIKeyModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if s == nil || s.httpUpstream == nil {
		return nil, errors.New("upstream HTTP client is not configured")
	}
	req, err := s.buildAntigravityAPIKeyModelsRequest(ctx, account)
	if err != nil {
		return nil, err
	}
	proxyURL, err := upstreamModelsProxyURL(account)
	if err != nil {
		return nil, err
	}
	resp, err := s.doUpstreamModelsRequest(req, proxyURL, account)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	bodyLimit := resolveModelsListReadLimit(s.cfg)
	body, err := io.ReadAll(io.LimitReader(resp.Body, bodyLimit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > bodyLimit {
		return nil, fmt.Errorf("antigravity model list exceeds %d bytes", bodyLimit)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("antigravity model list returned HTTP %d", resp.StatusCode)
	}
	return parseAntigravityAPIKeyModels(body)
}

func (s *AccountTestService) loadAntigravityOAuthModels(ctx context.Context, account *Account) ([]antigravity.ClaudeModel, error) {
	if s == nil || s.antigravityGatewayService == nil || s.antigravityGatewayService.GetTokenProvider() == nil {
		return nil, errors.New("antigravity token provider is not configured")
	}
	accessToken, err := s.antigravityGatewayService.GetTokenProvider().GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("no antigravity access token is available")
	}
	proxyURL, err := upstreamModelsProxyURL(account)
	if err != nil {
		return nil, err
	}
	client, err := antigravity.NewClient(proxyURL)
	if err != nil {
		return nil, err
	}
	modelsResp, _, err := client.FetchAvailableModels(
		ctx,
		accessToken,
		strings.TrimSpace(account.GetCredential("project_id")),
		resolveModelsListReadLimit(s.cfg),
	)
	if err != nil {
		return nil, err
	}
	return claudeModelsFromAvailable(modelsResp), nil
}

func parseAntigravityAPIKeyModels(body []byte) ([]antigravity.ClaudeModel, error) {
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Name        string `json:"name"`
			CreatedAt   string `json:"created_at"`
		} `json:"data"`
		Models []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Name        string `json:"name"`
			CreatedAt   string `json:"created_at"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	entries := payload.Data
	if len(entries) == 0 {
		entries = payload.Models
	}
	out := make([]antigravity.ClaudeModel, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.Name)
		}
		if id == "" {
			continue
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = id
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          id,
			Type:        "model",
			DisplayName: display,
			CreatedAt:   entry.CreatedAt,
		})
	}
	return out, nil
}

func claudeModelsFromAvailable(resp *antigravity.FetchAvailableModelsResponse) []antigravity.ClaudeModel {
	if resp == nil || len(resp.Models) == 0 {
		return []antigravity.ClaudeModel{}
	}
	ids := make([]string, 0, len(resp.Models))
	for id := range resp.Models {
		if strings.TrimSpace(id) != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]antigravity.ClaudeModel, 0, len(ids))
	for _, id := range ids {
		info := resp.Models[id]
		display := strings.TrimSpace(info.DisplayName)
		if display == "" {
			display = strings.TrimSpace(id)
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          strings.TrimSpace(id),
			Type:        "model",
			DisplayName: display,
		})
	}
	return out
}

// projectAntigravityAccountModels keeps a public alias only when its mapped
// target is in this account's upstream catalog. Wildcard keys are skipped.
func projectAntigravityAccountModels(upstream []antigravity.ClaudeModel, account *Account) []antigravity.ClaudeModel {
	if account == nil {
		return []antigravity.ClaudeModel{}
	}
	byID := make(map[string]antigravity.ClaudeModel, len(upstream))
	for _, model := range upstream {
		id := strings.TrimSpace(model.ID)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := byID[id]; !ok {
			byID[id] = model
		}
	}
	mapping := account.GetModelMapping()
	aliases := make([]string, 0, len(mapping))
	for alias := range mapping {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	out := make([]antigravity.ClaudeModel, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || strings.Contains(alias, "*") {
			continue
		}
		if _, ok := seen[alias]; ok {
			continue
		}
		target, matched := account.ResolveMappedModel(alias)
		target = strings.TrimSpace(target)
		src, available := byID[target]
		if !matched || !available {
			continue
		}
		seen[alias] = struct{}{}
		display := strings.TrimSpace(src.DisplayName)
		if alias != target || display == "" {
			display = alias
		}
		modelType := src.Type
		if modelType == "" {
			modelType = "model"
		}
		out = append(out, antigravity.ClaudeModel{
			ID:          alias,
			Type:        modelType,
			DisplayName: display,
			CreatedAt:   src.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
