//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestFetchAntigravityAccountModels_ProjectsAliasesAndSkipsUnmapped(t *testing.T) {
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	account := &Account{
		ID:       9101,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"public-opus": "claude-opus-4-8",
				"claude-*":    "claude-opus-4-8",
			},
		},
	}
	svc := &AccountTestService{
		antigravityModelsLoad: func(context.Context, *Account) ([]antigravity.ClaudeModel, error) {
			return []antigravity.ClaudeModel{
				{ID: "claude-opus-4-8", Type: "model", DisplayName: "Opus 4.8"},
				{ID: "unmapped-model", Type: "model", DisplayName: "Hidden"},
				{ID: "gemini-3-flash", Type: "model", DisplayName: "Gemini 3 Flash"},
			}, nil
		},
	}

	models, err := svc.FetchAntigravityAccountModels(context.Background(), account)
	require.NoError(t, err)
	byID := map[string]antigravity.ClaudeModel{}
	for _, model := range models {
		byID[model.ID] = model
	}
	require.NotContains(t, byID, "unmapped-model")
	require.NotContains(t, byID, "claude-*")
	require.Equal(t, "public-opus", byID["public-opus"].DisplayName)
	require.Equal(t, "Gemini 3 Flash", byID["gemini-3-flash"].DisplayName)
}

func TestFetchAntigravityAccountModels_ErrorFallsBackAndIsNotCached(t *testing.T) {
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	var calls atomic.Int32
	account := &Account{ID: 9102, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	svc := &AccountTestService{}
	svc.antigravityModelsLoad = func(context.Context, *Account) ([]antigravity.ClaudeModel, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("upstream down")
		}
		return []antigravity.ClaudeModel{{ID: "claude-sonnet-4-6", Type: "model", DisplayName: "Sonnet"}}, nil
	}

	first, err := svc.FetchAntigravityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Greater(t, len(first), 1)

	second, err := svc.FetchAntigravityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, 2, int(calls.Load()))
	require.NotEmpty(t, second)
	require.Less(t, len(second), len(first))
	for _, model := range second {
		target, matched := account.ResolveMappedModel(model.ID)
		require.True(t, matched)
		require.Equal(t, "claude-sonnet-4-6", target)
	}
}

func TestFetchAntigravityAccountModels_EmptyProjectionStaysEmptyAndIsCached(t *testing.T) {
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	var calls atomic.Int32
	account := &Account{ID: 9103, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	svc := &AccountTestService{
		antigravityModelsLoad: func(context.Context, *Account) ([]antigravity.ClaudeModel, error) {
			calls.Add(1)
			return []antigravity.ClaudeModel{}, nil
		},
	}

	first, err := svc.FetchAntigravityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Empty(t, first)
	second, err := svc.FetchAntigravityAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Empty(t, second)
	require.Equal(t, 1, int(calls.Load()))
}

func TestFetchAntigravityAccountModels_SingleflightAndCacheKey(t *testing.T) {
	resetAntigravityModelsCache()
	t.Cleanup(resetAntigravityModelsCache)

	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	account := &Account{
		ID:       9104,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"base_url":     "https://models.example/antigravity",
			"access_token": "token-a",
		},
	}
	svc := &AccountTestService{
		antigravityModelsLoad: func(context.Context, *Account) ([]antigravity.ClaudeModel, error) {
			calls.Add(1)
			once.Do(func() { close(started) })
			<-release
			return []antigravity.ClaudeModel{{ID: "claude-sonnet-4-6", Type: "model", DisplayName: "Sonnet"}}, nil
		},
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			models, err := svc.FetchAntigravityAccountModels(context.Background(), account)
			if err != nil {
				errCh <- err
				return
			}
			if len(models) == 0 {
				errCh <- errors.New("unexpected models")
				return
			}
			for _, model := range models {
				target, matched := account.ResolveMappedModel(model.ID)
				if !matched || target != "claude-sonnet-4-6" {
					errCh <- errors.New("unexpected models")
					return
				}
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	require.Equal(t, 1, int(calls.Load()))

	refreshed := &Account{
		ID:       account.ID,
		Platform: account.Platform,
		Type:     account.Type,
		Credentials: map[string]any{
			"base_url":     "https://models.example/antigravity",
			"access_token": "token-b",
		},
	}
	_, err := svc.FetchAntigravityAccountModels(context.Background(), refreshed)
	require.NoError(t, err)
	require.Equal(t, 1, int(calls.Load()), "access token refresh must not miss the cache")

	moved := &Account{
		ID:       account.ID,
		Platform: account.Platform,
		Type:     account.Type,
		Credentials: map[string]any{
			"base_url": "https://other.example/antigravity",
		},
	}
	_, err = svc.FetchAntigravityAccountModels(context.Background(), moved)
	require.NoError(t, err)
	require.Equal(t, 2, int(calls.Load()), "base URL change must miss the cache")
}

func TestSynthesizeAntigravityCatalogBody_OmitsUnknownReasoning(t *testing.T) {
	thinking := true
	images := true
	maxTokens := 128000
	body, err := synthesizeAntigravityCatalogBody(&antigravity.FetchAvailableModelsResponse{
		Models: map[string]antigravity.ModelInfo{
			"claude-opus-4-8": {
				DisplayName:      "Opus",
				SupportsThinking: &thinking,
				SupportsImages:   &images,
				MaxTokens:        &maxTokens,
			},
			"gemini-3-flash": {DisplayName: "Flash"},
		},
	})
	require.NoError(t, err)

	_, metadata, err := extractUpstreamModelCatalog(body, false)
	require.NoError(t, err)
	require.False(t, upstreamModelMetadataIsComplete(metadata["claude-opus-4-8"]))
	flash, ok := metadata["gemini-3-flash"]
	require.True(t, ok)
	require.Nil(t, flash.Reasoning)
	require.Equal(t, []string{"text"}, flash.InputModalities)
	require.Equal(t, []string{"text", "image"}, metadata["claude-opus-4-8"].InputModalities)
	require.Equal(t, int64(128000), metadata["claude-opus-4-8"].ContextWindow)
}
