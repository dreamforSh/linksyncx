package service

import (
	"encoding/json"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

const AntigravityModelCatalogExtraKey = "antigravity_model_catalog"

// AntigravityCatalogModel stores upstream-discovered metadata for a single Antigravity model.
type AntigravityCatalogModel struct {
	ID               string `json:"id"`
	DisplayName      string `json:"display_name,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	SupportsThinking *bool  `json:"supports_thinking,omitempty"`
	SupportsImages   *bool  `json:"supports_images,omitempty"`
	MaxTokens        int    `json:"max_tokens,omitempty"`
	MaxOutputTokens  int    `json:"max_output_tokens,omitempty"`
	DeprecatedTo     string `json:"deprecated_to,omitempty"`
}

// AntigravityModelCatalogSnapshot persists the authoritative upstream catalog synced for an Antigravity account.
type AntigravityModelCatalogSnapshot struct {
	Source             string                             `json:"source"`
	SyncedAt           string                             `json:"synced_at"`
	Models             map[string]AntigravityCatalogModel `json:"models"`
	DeprecatedModelIDs map[string]string                  `json:"deprecated_model_ids,omitempty"`
}

type antigravityObservedEntry struct {
	models []string
	sig    uint64
}

type antigravityObservedStore struct {
	mu    sync.RWMutex
	items map[int64]antigravityObservedEntry
}

var antigravityObservedModels = &antigravityObservedStore{
	items: make(map[int64]antigravityObservedEntry),
}

func computeStringSliceSig(items []string) uint64 {
	h := fnv.New64a()
	for _, item := range items {
		_, _ = h.Write([]byte(item))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

func recordAntigravityObservedModelIDs(accountID int64, rawIDs []string) {
	if accountID <= 0 {
		return
	}
	cleaned := make([]string, 0, len(rawIDs))
	seen := make(map[string]struct{}, len(rawIDs))
	for _, id := range rawIDs {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		cleaned = append(cleaned, id)
	}
	sort.Strings(cleaned)
	sig := computeStringSliceSig(cleaned)

	antigravityObservedModels.mu.Lock()
	antigravityObservedModels.items[accountID] = antigravityObservedEntry{
		models: cleaned,
		sig:    sig,
	}
	antigravityObservedModels.mu.Unlock()
}

func recordAntigravityObservedClaudeModels(accountID int64, models []antigravity.ClaudeModel) {
	if accountID <= 0 {
		return
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if id := strings.TrimSpace(m.ID); id != "" {
			ids = append(ids, id)
		}
	}
	recordAntigravityObservedModelIDs(accountID, ids)
}

func recordAntigravityAvailableModelsFromQuota(account *Account, resp *antigravity.FetchAvailableModelsResponse) {
	if account == nil || resp == nil || len(resp.Models) == 0 {
		return
	}
	claudeModels := claudeModelsFromAvailable(resp)
	if account.ID > 0 {
		recordAntigravityObservedClaudeModels(account.ID, claudeModels)
		antigravityRawModelsCache.set(antigravityRawModelsCacheKey(account), account.ID, claudeModels, time.Now())
	}
}

func getAntigravityObservedLiveModels(accountID int64) ([]string, uint64) {
	if accountID <= 0 {
		return nil, 0
	}
	antigravityObservedModels.mu.RLock()
	entry, ok := antigravityObservedModels.items[accountID]
	antigravityObservedModels.mu.RUnlock()
	if !ok || len(entry.models) == 0 {
		return nil, 0
	}
	out := make([]string, len(entry.models))
	copy(out, entry.models)
	return out, entry.sig
}

// SetAntigravityModelCatalogSnapshot stores the synced Antigravity catalog in account.Extra.
func (a *Account) SetAntigravityModelCatalogSnapshot(snapshot AntigravityModelCatalogSnapshot) {
	if a == nil {
		return
	}
	if a.Extra == nil {
		a.Extra = make(map[string]any)
	}
	a.Extra[AntigravityModelCatalogExtraKey] = snapshot
}

// GetAntigravityModelCatalogSnapshot reads the persisted Antigravity model catalog from account.Extra.
func (a *Account) GetAntigravityModelCatalogSnapshot() *AntigravityModelCatalogSnapshot {
	if a == nil || a.Extra == nil {
		return nil
	}
	raw, ok := a.Extra[AntigravityModelCatalogExtraKey]
	if !ok || raw == nil {
		return nil
	}
	if typed, ok := raw.(AntigravityModelCatalogSnapshot); ok {
		if len(typed.Models) == 0 {
			return nil
		}
		return &typed
	}
	if typedPtr, ok := raw.(*AntigravityModelCatalogSnapshot); ok && typedPtr != nil {
		if len(typedPtr.Models) == 0 {
			return nil
		}
		return typedPtr
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot AntigravityModelCatalogSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil || len(snapshot.Models) == 0 {
		return nil
	}
	return &snapshot
}

// AntigravitySnapshotClaudeModels converts a persisted Antigravity catalog snapshot to ClaudeModel entries.
func (a *Account) AntigravitySnapshotClaudeModels() ([]antigravity.ClaudeModel, bool) {
	snapshot := a.GetAntigravityModelCatalogSnapshot()
	if snapshot == nil || len(snapshot.Models) == 0 {
		return nil, false
	}
	ids := make([]string, 0, len(snapshot.Models))
	for id := range snapshot.Models {
		id = strings.TrimSpace(id)
		if id != "" && !strings.Contains(id, "*") {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	sort.Strings(ids)
	out := make([]antigravity.ClaudeModel, 0, len(ids))
	for _, id := range ids {
		entry := snapshot.Models[id]
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
	return out, true
}

// AntigravityObservedModelIDs returns all upstream model IDs observed for this Antigravity account
// across live discovery cache, persisted catalog snapshots, and generic upstream metadata snapshots.
func (a *Account) AntigravityObservedModelIDs() ([]string, map[string]string) {
	if a == nil || a.Platform != PlatformAntigravity {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var models []string
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, "*") {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}

	liveIDs, _ := getAntigravityObservedLiveModels(a.ID)
	for _, id := range liveIDs {
		add(id)
	}

	var deprecated map[string]string
	if snapshot := a.GetAntigravityModelCatalogSnapshot(); snapshot != nil {
		for id := range snapshot.Models {
			add(id)
		}
		if len(snapshot.DeprecatedModelIDs) > 0 {
			deprecated = make(map[string]string, len(snapshot.DeprecatedModelIDs))
			for oldID, newID := range snapshot.DeprecatedModelIDs {
				oldID = strings.TrimSpace(oldID)
				newID = strings.TrimSpace(newID)
				if oldID != "" && newID != "" {
					deprecated[oldID] = newID
				}
			}
		}
	}

	if metaSnapshot := a.GetUpstreamModelMetadataSnapshot(); metaSnapshot != nil {
		for id := range metaSnapshot.Models {
			add(id)
		}
	}

	if len(models) > 1 {
		sort.Strings(models)
	}
	return models, deprecated
}

func antigravityRuntimeMappingSignature(a *Account) uint64 {
	if a == nil || a.Platform != PlatformAntigravity {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(a.GetCredential(AntigravityModelRestrictionModeKey)))
	_, liveSig := getAntigravityObservedLiveModels(a.ID)
	var buf [8]byte
	for i := 0; i < 8; i++ {
		buf[i] = byte(liveSig >> (8 * i))
	}
	_, _ = h.Write(buf[:])

	if snapshot := a.GetAntigravityModelCatalogSnapshot(); snapshot != nil {
		_, _ = h.Write([]byte(snapshot.SyncedAt))
		ids := make([]string, 0, len(snapshot.Models))
		for id := range snapshot.Models {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			_, _ = h.Write([]byte{0})
			_, _ = h.Write([]byte(id))
		}
	}
	if meta := a.GetUpstreamModelMetadataSnapshot(); meta != nil {
		_, _ = h.Write([]byte(meta.SyncedAt))
	}
	return h.Sum64()
}

func bareGeminiModelFromThinkingVariant(modelID string) (string, bool) {
	variant, ok := antigravity.SplitGeminiThinkingVariant(modelID)
	if !ok {
		return "", false
	}
	return variant.BaseModel, true
}

// mergeAntigravityObservedModelsIntoMapping overlays upstream-observed models onto
// DefaultAntigravityModelMapping when an Antigravity account has no custom mapping.
// Curated legacy aliases remain intact while newly released upstream models become
// immediately schedulable and routable without manual mapping edits.
func mergeAntigravityObservedModelsIntoMapping(base map[string]string, a *Account) map[string]string {
	if a == nil || a.Platform != PlatformAntigravity || len(base) == 0 {
		return base
	}
	observed, deprecated := a.AntigravityObservedModelIDs()
	if len(observed) == 0 && len(deprecated) == 0 {
		return base
	}

	needsCopy := false
	for _, id := range observed {
		if _, exists := base[id]; !exists {
			needsCopy = true
			break
		}
		if bare, ok := bareGeminiModelFromThinkingVariant(id); ok {
			if _, exists := base[bare]; !exists {
				needsCopy = true
				break
			}
		}
	}
	if !needsCopy {
		for oldID, newID := range deprecated {
			if _, exists := base[oldID]; !exists {
				if _, targetOK := base[newID]; targetOK {
					needsCopy = true
					break
				}
				for _, id := range observed {
					if id == newID {
						needsCopy = true
						break
					}
				}
			}
		}
	}
	if !needsCopy {
		return base
	}

	merged := make(map[string]string, len(base)+len(observed)+len(deprecated))
	for k, v := range base {
		merged[k] = v
	}
	observedSet := make(map[string]struct{}, len(observed))
	for _, id := range observed {
		observedSet[id] = struct{}{}
		if _, exists := merged[id]; !exists && !mappingHasWildcardForModel(merged, id) {
			merged[id] = id
		}
	}
	// Register bare Gemini names (e.g. gemini-4-argon) for newly observed thinking
	// variants (e.g. gemini-4-argon-high) so resolveGeminiThinkingVariantForLevel
	// routes bare Gemini requests dynamically by thinking level.
	for _, id := range observed {
		if bare, ok := bareGeminiModelFromThinkingVariant(id); ok {
			if _, exists := merged[bare]; !exists && !mappingHasWildcardForModel(merged, bare) {
				merged[bare] = bare
			}
		}
	}
	for oldID, newID := range deprecated {
		if _, exists := merged[oldID]; !exists && !mappingHasWildcardForModel(merged, oldID) {
			if _, ok := merged[newID]; ok {
				merged[oldID] = newID
			} else if _, ok := observedSet[newID]; ok {
				merged[oldID] = newID
			}
		}
	}
	return merged
}

func buildAntigravityCatalogSnapshotFromRelayModels(claudeModels []antigravity.ClaudeModel, metadata map[string]UpstreamModelMetadata) AntigravityModelCatalogSnapshot {
	models := make(map[string]AntigravityCatalogModel, len(claudeModels))
	for _, cm := range claudeModels {
		id := strings.TrimSpace(cm.ID)
		if id == "" || strings.Contains(id, "*") {
			continue
		}
		display := strings.TrimSpace(cm.DisplayName)
		if display == "" {
			display = id
		}
		entry := AntigravityCatalogModel{
			ID:          id,
			DisplayName: display,
			CreatedAt:   cm.CreatedAt,
		}
		if meta, ok := metadata[id]; ok {
			if strings.TrimSpace(meta.DisplayName) != "" {
				entry.DisplayName = strings.TrimSpace(meta.DisplayName)
			}
			if meta.Reasoning != nil {
				v := *meta.Reasoning
				entry.SupportsThinking = &v
			}
			for _, mod := range meta.InputModalities {
				if strings.EqualFold(mod, "image") {
					img := true
					entry.SupportsImages = &img
					break
				}
			}
			if meta.ContextWindow > 0 {
				entry.MaxTokens = int(meta.ContextWindow)
			}
			if meta.MaxOutputTokens > 0 {
				entry.MaxOutputTokens = int(meta.MaxOutputTokens)
			}
		}
		models[id] = entry
	}
	return AntigravityModelCatalogSnapshot{
		Source:   "antigravity_relay",
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
		Models:   models,
	}
}
