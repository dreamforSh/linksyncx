package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"hash"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	perplexityThreadTTL        = time.Hour
	perplexityThreadCacheLimit = 1024
	perplexityThreadBodyLimit  = 512 * 1024
	perplexityThreadTurnLimit  = 64
)

type perplexityThreadRecord struct {
	backendUUID string
	contextUUID string
	expiresAt   time.Time
}

type perplexityThreadCache struct {
	mu      sync.Mutex
	entries map[[32]byte]perplexityThreadRecord
}

func (s *OpenAIGatewayService) perplexityThreadStore() *perplexityThreadCache {
	s.perplexityThreadsOnce.Do(func() {
		if s.perplexityThreads == nil {
			s.perplexityThreads = &perplexityThreadCache{entries: make(map[[32]byte]perplexityThreadRecord)}
		}
	})
	return s.perplexityThreads
}

// A parent handle is consumed once. Concurrent or later branches replay the full
// visible history instead of mutating the same upstream conversation twice.
func (c *perplexityThreadCache) take(key [32]byte, now time.Time) (perplexityThreadRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	record, ok := c.entries[key]
	if ok {
		delete(c.entries, key)
	}
	return record, ok && now.Before(record.expiresAt)
}

func (c *perplexityThreadCache) put(key [32]byte, record perplexityThreadRecord, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]perplexityThreadRecord)
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	_, replacing := c.entries[key]
	if !replacing && len(c.entries) >= perplexityThreadCacheLimit {
		var oldestKey [32]byte
		var oldest time.Time
		for key, entry := range c.entries {
			if oldest.IsZero() || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = key, entry.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}
	record.expiresAt = now.Add(perplexityThreadTTL)
	c.entries[key] = record
}

type perplexityContinuation struct {
	store       *perplexityThreadCache
	scope       [32]byte
	history     hash.Hash
	contextUUID string
	backendUUID string
	query       string
	once        sync.Once
}

func (p *perplexityContinuation) complete(completion perplexityCompletion) {
	if p == nil || strings.TrimSpace(completion.Content) == "" || uuid.Validate(completion.BackendUUID) != nil {
		return
	}
	p.once.Do(func() {
		writePerplexityTurn(p.history, "assistant", completion.Content)
		p.store.put(perplexityHistoryKey(p.scope, p.history), perplexityThreadRecord{
			backendUUID: completion.BackendUUID, contextUUID: p.contextUUID,
		}, time.Now())
	})
}

func (s *OpenAIGatewayService) preparePerplexityContinuation(account *Account, apiKeyID int64, sessionHint, modelPref, mode string, body []byte, opts perplexity.RequestOptions) *perplexityContinuation {
	// 无痕会话（is_incognito）不读不写续链缓存：隐私会话的内容绝不落本地缓存。
	if !opts.Local.NativeFollowups || opts.Incognito || mode == perplexity.ModeComputer || apiKeyID <= 0 || account == nil || account.ID <= 0 || len(body) > perplexityThreadBodyLimit || len(opts.Internal.Attachments) != 0 {
		return nil
	}
	if len(gjson.GetBytes(body, "tools").Array()) > 0 || len(gjson.GetBytes(body, "functions").Array()) > 0 {
		return nil
	}
	proxyURL, proxyErr := account.ProxyURLForOutbound()
	if proxyErr != nil {
		return nil
	}
	proxyHash := sha256.Sum256([]byte(proxyURL))
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return nil
	}
	turns := messages.Array()
	if len(turns) == 0 || len(turns) > perplexityThreadTurnLimit || turns[len(turns)-1].Get("role").String() != "user" {
		return nil
	}
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return nil
	}
	credentialHash := sha256.Sum256(credentials)
	opts.Internal = perplexity.InternalOptions{}
	opts.IgnoredParameters = nil
	scopeData, err := json.Marshal(struct {
		AccountID      int64
		APIKeyID       int64
		ProxyID        *int64
		Proxy          [32]byte
		Credentials    [32]byte
		Session        string
		Model          string
		Mode           string
		RequestOptions perplexity.RequestOptions
	}{account.ID, apiKeyID, account.ProxyID, proxyHash, credentialHash, sessionHint, modelPref, mode, opts})
	if err != nil {
		return nil
	}
	scope := sha256.Sum256(scopeData)
	history := sha256.New()
	var parentKey [32]byte
	lastQuery := ""
	for index, turn := range turns {
		role := turn.Get("role").String()
		if role != "user" && role != "assistant" || turn.Get("name").Exists() ||
			len(turn.Get("tool_calls").Array()) > 0 || turn.Get("function_call").Exists() {
			return nil
		}
		content := turn.Get("content")
		text := ""
		if content.Type == gjson.String {
			text = content.String()
		} else if content.IsArray() {
			var builder strings.Builder
			for _, part := range content.Array() {
				if part.Get("type").String() != "text" || part.Get("text").Type != gjson.String {
					return nil
				}
				_, _ = builder.WriteString(part.Get("text").String())
			}
			text = builder.String()
		} else {
			return nil
		}
		if index == len(turns)-1 {
			parentKey = perplexityHistoryKey(scope, history)
			lastQuery = strings.TrimSpace(text)
		}
		writePerplexityTurn(history, role, text)
	}
	if lastQuery == "" {
		return nil
	}
	plan := &perplexityContinuation{
		store: s.perplexityThreadStore(), scope: scope, history: history,
		contextUUID: uuid.NewString(), query: lastQuery,
	}
	if len(turns) >= 3 && turns[len(turns)-2].Get("role").String() == "assistant" {
		if record, ok := plan.store.take(parentKey, time.Now()); ok {
			plan.backendUUID, plan.contextUUID = record.backendUUID, record.contextUUID
		}
	}
	return plan
}

func writePerplexityTurn(h hash.Hash, role, text string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(role)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(role))
	binary.BigEndian.PutUint64(size[:], uint64(len(text)))
	_, _ = h.Write(size[:])
	_, _ = h.Write([]byte(text))
}

func perplexityHistoryKey(scope [32]byte, history hash.Hash) [32]byte {
	var data [64]byte
	copy(data[:32], scope[:])
	copy(data[32:], history.Sum(nil))
	return sha256.Sum256(data[:])
}
