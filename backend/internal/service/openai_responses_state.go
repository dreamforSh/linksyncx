package service

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// Stateful Responses emulation for subscription (Codex OAuth) accounts.
//
// The ChatGPT Codex backend only accepts store=false and has no
// previous_response_id or GET /responses/{id}. OpenAI SDK callers (and the
// Agents SDK) rely on those, so the gateway keeps each stored turn itself:
// the turn's own input items, the final response object, and the ids of the
// earlier turns in the chain. A continuation is expanded into the full input
// before scheduling, after which it is an ordinary stateless request that any
// account can serve. One MGET loads every ancestor, and the chain is collapsed
// into a full snapshot every openAIResponsesStateMaxAncestors turns, so storage
// grows linearly with the conversation and a lookup costs two round trips.

const (
	openAIResponsesStateRecordVersion     = 1
	openAIResponsesStateMaxAncestors      = 64
	openAIResponsesStateDefaultTTL        = 24 * time.Hour
	openAIResponsesStateDefaultMaxBytes   = 16 << 20
	openAIResponsesStateRedisTimeout      = 3 * time.Second
	openAIResponsesStateMemoryBudgetBytes = 256 << 20

	openAIResponsesStatePendingContextKey = "openai_responses_state_pending"
)

var (
	// ErrOpenAIResponsesStateNotOwned means the response exists but belongs to
	// another user; callers answer exactly like an unknown id.
	ErrOpenAIResponsesStateNotOwned = errors.New("openai responses state: not owned")
	// ErrOpenAIResponsesStateBroken means the response exists but an earlier
	// turn of its chain has expired, so the context cannot be rebuilt.
	ErrOpenAIResponsesStateBroken = errors.New("openai responses state: chain incomplete")
)

// openAIResponsesStateBlobStore is implemented by the Redis gateway cache.
type openAIResponsesStateBlobStore interface {
	SetOpenAIResponsesState(ctx context.Context, key string, payload []byte, ttl time.Duration) error
	GetOpenAIResponsesStates(ctx context.Context, keys []string) ([][]byte, error)
	DeleteOpenAIResponsesState(ctx context.Context, key string) error
}

type openAIResponsesStateRecord struct {
	Version            int             `json:"v"`
	UserID             int64           `json:"u,omitempty"`
	APIKeyID           int64           `json:"k,omitempty"`
	PreviousResponseID string          `json:"p,omitempty"`
	Ancestors          []string        `json:"a,omitempty"`
	Input              json.RawMessage `json:"i"`
	Response           json.RawMessage `json:"r"`
	CreatedAt          int64           `json:"t"`
}

// OpenAIResponsesStatePending is attached to a request whose result should be
// stored when an emulating upstream serves it.
type OpenAIResponsesStatePending struct {
	GroupID            int64
	UserID             int64
	APIKeyID           int64
	PreviousResponseID string
	Ancestors          []string
	// Input is this turn's own input as a JSON array; a continuation carries
	// only the new items, the history lives in Ancestors and the parent.
	Input json.RawMessage
	// Continuation marks a request expanded from previous_response_id; its
	// result is stored on any account so the chain survives account switches.
	Continuation bool
}

// SetOpenAIResponsesStatePending arms result storage for this request.
func SetOpenAIResponsesStatePending(c *gin.Context, pending *OpenAIResponsesStatePending) {
	if c == nil || pending == nil {
		return
	}
	c.Set(openAIResponsesStatePendingContextKey, pending)
}

func openAIResponsesStatePendingFromContext(c *gin.Context) *OpenAIResponsesStatePending {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAIResponsesStatePendingContextKey)
	if !ok {
		return nil
	}
	pending, _ := value.(*OpenAIResponsesStatePending)
	return pending
}

// OpenAIResponsesStateEnabled reports whether the emulation is switched on.
func (s *OpenAIGatewayService) OpenAIResponsesStateEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIResponsesStateEnabled
}

func (s *OpenAIGatewayService) openAIResponsesStateTTL() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIResponsesStateTTLSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIResponsesStateTTLSeconds) * time.Second
	}
	return openAIResponsesStateDefaultTTL
}

func (s *OpenAIGatewayService) openAIResponsesStateMaxBytes() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIResponsesStateMaxRecordBytes > 0 {
		return s.cfg.Gateway.OpenAIResponsesStateMaxRecordBytes
	}
	return openAIResponsesStateDefaultMaxBytes
}

func (s *OpenAIGatewayService) openAIResponsesStateStore() openAIResponsesStateBlobStore {
	if s != nil && s.cache != nil {
		if store, ok := s.cache.(openAIResponsesStateBlobStore); ok {
			return store
		}
	}
	return defaultOpenAIResponsesStateMemoryStore
}

func openAIResponsesStateKey(groupID int64, responseID string) string {
	return strconv.FormatInt(groupID, 10) + ":" + responseID
}

func openAIResponsesStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIResponsesStateRedisTimeout)
}

func (r *openAIResponsesStateRecord) ownedBy(userID, apiKeyID int64) bool {
	if r == nil {
		return false
	}
	return (r.UserID > 0 && r.UserID == userID) || (r.UserID <= 0 && r.APIKeyID > 0 && r.APIKeyID == apiKeyID)
}

func (s *OpenAIGatewayService) loadOpenAIResponsesStateRecords(ctx context.Context, groupID int64, ids []string) ([]*openAIResponsesStateRecord, error) {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = openAIResponsesStateKey(groupID, id)
	}
	opCtx, cancel := openAIResponsesStateContext(ctx)
	defer cancel()
	payloads, err := s.openAIResponsesStateStore().GetOpenAIResponsesStates(opCtx, keys)
	if err != nil {
		return nil, err
	}
	records := make([]*openAIResponsesStateRecord, len(ids))
	for i := range ids {
		if i >= len(payloads) || len(payloads[i]) == 0 {
			continue
		}
		var record openAIResponsesStateRecord
		if json.Unmarshal(payloads[i], &record) != nil || record.Version != openAIResponsesStateRecordVersion {
			continue
		}
		records[i] = &record
	}
	return records, nil
}

// lookupOpenAIResponsesState returns the record for responseID, nil when the
// gateway never stored it (the caller then keeps its native behavior).
func (s *OpenAIGatewayService) lookupOpenAIResponsesState(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) (*openAIResponsesStateRecord, error) {
	records, err := s.loadOpenAIResponsesStateRecords(ctx, groupID, []string{responseID})
	if err != nil {
		return nil, err
	}
	record := records[0]
	if record == nil {
		return nil, nil
	}
	if !record.ownedBy(userID, apiKeyID) {
		return nil, ErrOpenAIResponsesStateNotOwned
	}
	return record, nil
}

// openAIResponsesStateHistory rebuilds the full context of record: every
// ancestor's input and output, then the record's own input and output.
func (s *OpenAIGatewayService) openAIResponsesStateHistory(ctx context.Context, groupID int64, record *openAIResponsesStateRecord, includeOwnOutput bool) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if len(record.Ancestors) > 0 {
		ancestors, err := s.loadOpenAIResponsesStateRecords(ctx, groupID, record.Ancestors)
		if err != nil {
			return nil, err
		}
		for _, ancestor := range ancestors {
			if ancestor == nil {
				return nil, ErrOpenAIResponsesStateBroken
			}
			items = appendOpenAIResponsesStateItems(items, ancestor.Input, false)
			items = appendOpenAIResponsesStateItems(items, json.RawMessage(gjson.GetBytes(ancestor.Response, "output").Raw), true)
		}
	}
	items = appendOpenAIResponsesStateItems(items, record.Input, false)
	if includeOwnOutput {
		items = appendOpenAIResponsesStateItems(items, json.RawMessage(gjson.GetBytes(record.Response, "output").Raw), true)
	}
	return items, nil
}

// appendOpenAIResponsesStateItems appends the members of a JSON array. Output
// reasoning items are replayable on a store=false upstream only with their
// encrypted content, so items without it are dropped.
func appendOpenAIResponsesStateItems(dst []json.RawMessage, rawArray json.RawMessage, fromOutput bool) []json.RawMessage {
	arr := gjson.ParseBytes(rawArray)
	if !arr.IsArray() {
		return dst
	}
	arr.ForEach(func(_, item gjson.Result) bool {
		if fromOutput && item.Get("type").String() == "reasoning" && strings.TrimSpace(item.Get("encrypted_content").String()) == "" {
			return true
		}
		dst = append(dst, json.RawMessage(item.Raw))
		return true
	})
	return dst
}

func joinOpenAIResponsesStateItems(items []json.RawMessage) []byte {
	size := 2
	for _, item := range items {
		size += len(item) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// openAIResponsesRequestInputArray returns the request's own input as a JSON
// array, turning the string shorthand into one user message.
func openAIResponsesRequestInputArray(body []byte) (json.RawMessage, error) {
	input := gjson.GetBytes(body, "input")
	switch {
	case !input.Exists() || input.Type == gjson.Null:
		return json.RawMessage("[]"), nil
	case input.IsArray():
		return json.RawMessage(input.Raw), nil
	case input.Type == gjson.String:
		item, err := json.Marshal(map[string]any{"type": "message", "role": "user", "content": input.String()})
		if err != nil {
			return nil, err
		}
		return json.RawMessage("[" + string(item) + "]"), nil
	default:
		return nil, fmt.Errorf("input must be a string or an array")
	}
}

// OpenAIResponsesContinuation is the outcome of expanding previous_response_id.
type OpenAIResponsesContinuation struct {
	Body    []byte
	Pending *OpenAIResponsesStatePending
}

// ExpandOpenAIResponsesContinuation rewrites a request that names a
// gateway-stored previous_response_id into a self-contained request. It
// returns nil (no error) when the gateway does not know the id, so native
// API-key continuations keep working. ErrOpenAIResponsesStateNotOwned and
// ErrOpenAIResponsesStateBroken are client errors.
func (s *OpenAIGatewayService) ExpandOpenAIResponsesContinuation(ctx context.Context, groupID, userID, apiKeyID int64, body []byte) (*OpenAIResponsesContinuation, error) {
	if !s.OpenAIResponsesStateEnabled() {
		return nil, nil
	}
	previousResponseID := strings.TrimSpace(gjson.GetBytes(body, "previous_response_id").String())
	if previousResponseID == "" {
		return nil, nil
	}
	parent, err := s.lookupOpenAIResponsesState(ctx, groupID, previousResponseID, userID, apiKeyID)
	if err != nil || parent == nil {
		return nil, err
	}
	history, err := s.openAIResponsesStateHistory(ctx, groupID, parent, true)
	if err != nil {
		return nil, err
	}
	ownInput, err := openAIResponsesRequestInputArray(body)
	if err != nil {
		return nil, err
	}
	expandedItems := appendOpenAIResponsesStateItems(history, ownInput, false)
	expanded, err := sjson.SetRawBytes(body, "input", joinOpenAIResponsesStateItems(expandedItems))
	if err != nil {
		return nil, fmt.Errorf("expand previous_response_id input: %w", err)
	}
	if expanded, err = sjson.DeleteBytes(expanded, "previous_response_id"); err != nil {
		return nil, fmt.Errorf("drop previous_response_id: %w", err)
	}

	pending := &OpenAIResponsesStatePending{
		GroupID:            groupID,
		UserID:             userID,
		APIKeyID:           apiKeyID,
		PreviousResponseID: previousResponseID,
		Input:              ownInput,
		Continuation:       true,
	}
	if len(parent.Ancestors)+1 >= openAIResponsesStateMaxAncestors {
		// Collapse the chain: this turn stores the whole history as its input.
		pending.Input = json.RawMessage(gjson.GetBytes(expanded, "input").Raw)
	} else {
		pending.Ancestors = append(append([]string(nil), parent.Ancestors...), previousResponseID)
	}
	return &OpenAIResponsesContinuation{Body: expanded, Pending: pending}, nil
}

// NewOpenAIResponsesStatePending prepares storage for a request that does not
// continue a stored turn. It returns nil when the client opted out (store=false).
func (s *OpenAIGatewayService) NewOpenAIResponsesStatePending(groupID, userID, apiKeyID int64, body []byte) *OpenAIResponsesStatePending {
	if !s.OpenAIResponsesStateEnabled() {
		return nil
	}
	if store := gjson.GetBytes(body, "store"); store.Exists() && store.Type == gjson.False {
		return nil
	}
	if gjson.GetBytes(body, "background").Bool() {
		return nil
	}
	input, err := openAIResponsesRequestInputArray(body)
	if err != nil {
		return nil
	}
	return &OpenAIResponsesStatePending{GroupID: groupID, UserID: userID, APIKeyID: apiKeyID, Input: input}
}

// shouldRecordOpenAIResponsesState reports whether account's successful result
// for this request is stored by the gateway. Upstreams with native state keep
// their own records unless the request itself was an emulated continuation.
func shouldRecordOpenAIResponsesState(c *gin.Context, account *Account) bool {
	pending := openAIResponsesStatePendingFromContext(c)
	if pending == nil || account == nil || account.Platform != PlatformOpenAI || isOpenAIResponsesCompactPath(c) {
		return false
	}
	return pending.Continuation || account.UsesOpenAICodexProtocol()
}

// recordOpenAIResponsesState stores the finished turn. Failures only log: the
// response has already been delivered and only later continuation is affected.
func (s *OpenAIGatewayService) recordOpenAIResponsesState(ctx context.Context, c *gin.Context, account *Account, responseID string, response []byte) {
	if !shouldRecordOpenAIResponsesState(c, account) {
		return
	}
	pending := openAIResponsesStatePendingFromContext(c)
	responseID = strings.TrimSpace(responseID)
	response = bytes.TrimSpace(response)
	if responseID == "" || len(response) == 0 || !gjson.ValidBytes(response) {
		return
	}
	if pending.PreviousResponseID != "" {
		if patched, err := sjson.SetBytes(response, "previous_response_id", pending.PreviousResponseID); err == nil {
			response = patched
		}
	}
	payload, err := json.Marshal(openAIResponsesStateRecord{
		Version:            openAIResponsesStateRecordVersion,
		UserID:             pending.UserID,
		APIKeyID:           pending.APIKeyID,
		PreviousResponseID: pending.PreviousResponseID,
		Ancestors:          pending.Ancestors,
		Input:              pending.Input,
		Response:           response,
		CreatedAt:          time.Now().Unix(),
	})
	if err != nil {
		return
	}
	if len(payload) > s.openAIResponsesStateMaxBytes() {
		logger.L().Warn("openai.responses_state_record_too_large",
			zap.Int64("account_id", account.ID),
			zap.Int("bytes", len(payload)),
			zap.Int("limit", s.openAIResponsesStateMaxBytes()),
		)
		return
	}
	opCtx, cancel := openAIResponsesStateContext(ctx)
	defer cancel()
	if err := s.openAIResponsesStateStore().SetOpenAIResponsesState(opCtx, openAIResponsesStateKey(pending.GroupID, responseID), payload, s.openAIResponsesStateTTL()); err != nil {
		logger.L().Warn("openai.responses_state_record_failed",
			zap.Int64("account_id", account.ID),
			zap.String("response_id", truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen)),
			zap.Error(err),
		)
	}
}

// GetOpenAIResponsesStoredResponse returns the stored response object.
func (s *OpenAIGatewayService) GetOpenAIResponsesStoredResponse(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) ([]byte, bool, error) {
	if !s.OpenAIResponsesStateEnabled() {
		return nil, false, nil
	}
	record, err := s.lookupOpenAIResponsesState(ctx, groupID, strings.TrimSpace(responseID), userID, apiKeyID)
	if err != nil || record == nil {
		if errors.Is(err, ErrOpenAIResponsesStateNotOwned) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return record.Response, true, nil
}

// DeleteOpenAIResponsesStoredResponse removes a stored response owned by the caller.
func (s *OpenAIGatewayService) DeleteOpenAIResponsesStoredResponse(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) (bool, error) {
	if !s.OpenAIResponsesStateEnabled() {
		return false, nil
	}
	responseID = strings.TrimSpace(responseID)
	record, err := s.lookupOpenAIResponsesState(ctx, groupID, responseID, userID, apiKeyID)
	if err != nil || record == nil {
		if errors.Is(err, ErrOpenAIResponsesStateNotOwned) {
			return false, nil
		}
		return false, err
	}
	opCtx, cancel := openAIResponsesStateContext(ctx)
	defer cancel()
	if err := s.openAIResponsesStateStore().DeleteOpenAIResponsesState(opCtx, openAIResponsesStateKey(groupID, responseID)); err != nil {
		return false, err
	}
	return true, nil
}

// ListOpenAIResponsesStoredInputItems returns the full input context of a
// stored response (earlier turns included), oldest first.
func (s *OpenAIGatewayService) ListOpenAIResponsesStoredInputItems(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64) ([]json.RawMessage, bool, error) {
	if !s.OpenAIResponsesStateEnabled() {
		return nil, false, nil
	}
	record, err := s.lookupOpenAIResponsesState(ctx, groupID, strings.TrimSpace(responseID), userID, apiKeyID)
	if err != nil || record == nil {
		if errors.Is(err, ErrOpenAIResponsesStateNotOwned) {
			return nil, false, nil
		}
		return nil, false, err
	}
	items, err := s.openAIResponsesStateHistory(ctx, groupID, record, false)
	if err != nil {
		return nil, false, err
	}
	return items, true, nil
}

// openAIResponsesStateMemoryStore backs the emulation when the gateway cache
// is not Redis (tests, embedded setups). It is process-local and bounded.
type openAIResponsesStateMemoryStore struct {
	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
	bytes   int
	budget  int
	now     func() time.Time
}

type openAIResponsesStateMemoryEntry struct {
	key       string
	payload   []byte
	expiresAt time.Time
}

var defaultOpenAIResponsesStateMemoryStore = newOpenAIResponsesStateMemoryStore(openAIResponsesStateMemoryBudgetBytes)

func newOpenAIResponsesStateMemoryStore(budget int) *openAIResponsesStateMemoryStore {
	return &openAIResponsesStateMemoryStore{entries: map[string]*list.Element{}, order: list.New(), budget: budget, now: time.Now}
}

func (m *openAIResponsesStateMemoryStore) SetOpenAIResponsesState(_ context.Context, key string, payload []byte, ttl time.Duration) error {
	if len(payload) > m.budget {
		return fmt.Errorf("openai responses state: record exceeds memory budget")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(key)
	entry := &openAIResponsesStateMemoryEntry{key: key, payload: append([]byte(nil), payload...), expiresAt: m.now().Add(ttl)}
	m.entries[key] = m.order.PushFront(entry)
	m.bytes += len(entry.payload)
	for m.bytes > m.budget && m.order.Len() > 0 {
		m.removeLocked(m.order.Back().Value.(*openAIResponsesStateMemoryEntry).key)
	}
	return nil
}

func (m *openAIResponsesStateMemoryStore) GetOpenAIResponsesStates(_ context.Context, keys []string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(keys))
	now := m.now()
	for i, key := range keys {
		element, ok := m.entries[key]
		if !ok {
			continue
		}
		entry := element.Value.(*openAIResponsesStateMemoryEntry)
		if !now.Before(entry.expiresAt) {
			m.removeLocked(key)
			continue
		}
		m.order.MoveToFront(element)
		out[i] = entry.payload
	}
	return out, nil
}

func (m *openAIResponsesStateMemoryStore) DeleteOpenAIResponsesState(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(key)
	return nil
}

func (m *openAIResponsesStateMemoryStore) removeLocked(key string) {
	element, ok := m.entries[key]
	if !ok {
		return
	}
	entry := element.Value.(*openAIResponsesStateMemoryEntry)
	m.bytes -= len(entry.payload)
	m.order.Remove(element)
	delete(m.entries, key)
}
