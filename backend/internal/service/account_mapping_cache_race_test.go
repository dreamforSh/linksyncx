//go:build unit

package service

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// 调度快照 / 账号缓存会把同一个 *Account 同时交给多个请求，热路径缓存必须能被
// 并发首次填充与读取；这些用例需配合 -race 运行才能暴露未同步的写入。
const mappingCacheRaceGoroutines = 16

func runConcurrently(t *testing.T, fn func() error) {
	t.Helper()
	start := make(chan struct{})
	errs := make(chan error, mappingCacheRaceGoroutines)
	var wg sync.WaitGroup
	for range mappingCacheRaceGoroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 50 {
				if err := fn(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestAccountGetModelMapping_ConcurrentCallersOnSharedAccount(t *testing.T) {
	account := &Account{
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"claude-sonnet": "upstream-sonnet",
				"claude-opus":   "upstream-opus",
			},
		},
	}

	runConcurrently(t, func() error {
		mapping := account.GetModelMapping()
		if len(mapping) != 2 || mapping["claude-sonnet"] != "upstream-sonnet" || mapping["claude-opus"] != "upstream-opus" {
			return fmt.Errorf("unexpected mapping: %v", mapping)
		}
		return nil
	})
}

func TestAccountGetModelMapping_CacheHitReturnsSameMap(t *testing.T) {
	account := &Account{
		Credentials: map[string]any{
			"model_mapping": map[string]any{"claude-sonnet": "upstream-sonnet"},
		},
	}

	first := account.GetModelMapping()
	second := account.GetModelMapping()
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatalf("expected cache hit to return the cached map instance")
	}
}

func TestAccountGetHeaderOverrides_ConcurrentCallersOnSharedAccount(t *testing.T) {
	account := headerOverrideTestAccount(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides": map[string]any{
			"User-Agent": "custom-agent",
			"X-Extra":    "extra",
		},
	})

	runConcurrently(t, func() error {
		overrides := account.GetHeaderOverrides()
		if len(overrides) != 2 || overrides["user-agent"] != "custom-agent" || overrides["x-extra"] != "extra" {
			return fmt.Errorf("unexpected overrides: %v", overrides)
		}
		return nil
	})
}

func TestAccountGetHeaderOverrides_CacheHitReturnsSameMap(t *testing.T) {
	account := headerOverrideTestAccount(PlatformAnthropic, AccountTypeAPIKey, map[string]any{
		"header_override_enabled": true,
		"header_overrides":        map[string]any{"User-Agent": "custom-agent"},
	})

	first := account.GetHeaderOverrides()
	second := account.GetHeaderOverrides()
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Fatalf("expected cache hit to return the cached map instance")
	}
}
