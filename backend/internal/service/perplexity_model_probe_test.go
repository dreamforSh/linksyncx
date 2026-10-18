//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func (r *perplexityInfoRepo) StorePerplexityCatalogIfUnchanged(ctx context.Context, id int64, _ map[string]any, _ *int64, snapshot UpstreamModelMetadataSnapshot) (bool, error) {
	return true, r.UpdateExtra(ctx, id, map[string]any{UpstreamModelMetadataExtraKey: snapshot})
}

func (r *perplexityInfoRepo) StorePerplexityModelProbeIfUnchanged(context.Context, int64, map[string]any, *int64, PerplexityModelProbeResult) (bool, error) {
	return false, nil
}

type perplexityModelRepoStub struct {
	*perplexityStateRepoStub
	probeWrites int
}

func (r *perplexityModelRepoStub) StorePerplexityModelProbeIfUnchanged(_ context.Context, id int64, expected map[string]any, proxy *int64, result PerplexityModelProbeResult) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil || !reflect.DeepEqual(account.Credentials, expected) || !reflect.DeepEqual(account.ProxyID, proxy) {
		return false, nil
	}
	entries, _ := account.Extra[perplexityModelHealthExtraKey].(map[string]any)
	key := perplexityModelHealthKey(result.Preference, result.Mode)
	if raw := entries[key]; raw != nil {
		encoded, _ := json.Marshal(raw)
		var previous PerplexityModelProbeResult
		_ = json.Unmarshal(encoded, &previous)
		if previous.ObservedAtMs >= result.ObservedAtMs {
			return false, nil
		}
	}
	encoded, _ := json.Marshal(result)
	var stored map[string]any
	_ = json.Unmarshal(encoded, &stored)
	next := shallowCopyMap(entries)
	next[key] = stored
	account.Extra = shallowCopyMap(account.Extra)
	account.Extra[perplexityModelHealthExtraKey] = next
	r.probeWrites++
	return true, nil
}

func (r *perplexityModelRepoStub) StorePerplexityCatalogIfUnchanged(_ context.Context, id int64, expected map[string]any, proxy *int64, snapshot UpstreamModelMetadataSnapshot) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil || !reflect.DeepEqual(account.Credentials, expected) || !reflect.DeepEqual(account.ProxyID, proxy) {
		return false, nil
	}
	account.Extra = shallowCopyMap(account.Extra)
	account.Extra[UpstreamModelMetadataExtraKey] = snapshot
	return true, nil
}

func perplexityModelProbeFixture(t *testing.T, reply http.HandlerFunc) (*AccountTestService, *perplexityInfoHTTP, *Account, *perplexityModelRepoStub) {
	t.Helper()
	svc, upstream, account := newPerplexityInfoTestService(reply)
	account.Status, account.Schedulable = StatusActive, true
	account.Credentials["model_mapping"] = map[string]any{"public": "gpt54", "alternate": "claude46sonnet"}
	repo := &perplexityModelRepoStub{perplexityStateRepoStub: newPerplexityStateRepo(account)}
	svc.accountRepo = repo
	return svc, upstream, account, repo
}

func TestPerplexityRealModelProbeIdentityAndPersistence(t *testing.T) {
	for _, reported := range []string{"gpt54", "claude46sonnet", ""} {
		t.Run(reported, func(t *testing.T) {
			svc, upstream, account, repo := perplexityModelProbeFixture(t, func(w http.ResponseWriter, req *http.Request) {
				require.Equal(t, perplexity.AskPath, req.URL.Path)
				body, err := io.ReadAll(req.Body)
				require.NoError(t, err)
				require.Equal(t, "gpt54", gjson.GetBytes(body, "params.model_preference").String())
				require.Equal(t, perplexityModelProbeDefaultPrompt, gjson.GetBytes(body, "query_str").String())
				_, _ = io.WriteString(w, perplexityModelAnswer("OK", reported)+"event: end_of_stream\n\n")
			})
			result, err := svc.probePerplexityModel(context.Background(), account, "public", "", "manual")
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, reported == "gpt54", result.Verified)
			require.Equal(t, reported, result.Identity.ActualModel)
			require.Equal(t, "stored", result.Persistence)
			require.Equal(t, "OK", result.ResponseText)
			require.Equal(t, 1, upstream.count())
			require.Equal(t, 1, repo.probeWrites)
			current, err := repo.GetByID(context.Background(), account.ID)
			require.NoError(t, err)
			stored := getPerplexityModelHealth(current, "gpt54", perplexity.ModeCopilot)
			require.NotNil(t, stored)
			require.Equal(t, result.Verified, stored.Verified)
			require.Empty(t, stored.ResponseText, "answer snippets must not be stored in account Extra")
			require.Nil(t, getPerplexityModelHealth(current, "claude46sonnet", perplexity.ModeCopilot))
			require.Empty(t, account.Extra, "shared account snapshots must not be mutated")
		})
	}
}

func TestPerplexityRealModelProbeDoesNotInventEntitlementFromErrors(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body, code  string
		unavailable bool
	}{
		{400, `{"error":{"code":"model_not_found","message":"secret-cookie"}}`, "model_unavailable", true},
		{404, `{"message":"secret-cookie"}`, "upstream_error", false},
		{401, `{"message":"secret-cookie"}`, "unauthenticated", false},
		{403, `{"message":"secret-cookie"}`, "forbidden", false},
		{429, `{"error":{"code":"model_not_found"}}`, "rate_limited", false},
		{503, `{"error":{"code":"unsupported_model"}}`, "upstream_error", false},
	} {
		t.Run(tc.code, func(t *testing.T) {
			svc, _, account, repo := perplexityModelProbeFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			result, err := svc.probePerplexityModel(context.Background(), account, "public", "", "scheduled")
			require.Error(t, err)
			require.Equal(t, tc.code, result.ErrorCode)
			require.False(t, result.Success)
			require.False(t, result.Verified)
			require.NotContains(t, result.ErrorMessage, "secret-cookie")
			current, err := repo.GetByID(context.Background(), account.ID)
			require.NoError(t, err)
			require.Equal(t, StatusActive, current.Status)
			require.Equal(t, tc.unavailable, perplexityModelHealthPauseReason(context.Background(), current, "public") != "")
			require.Empty(t, perplexityModelHealthPauseReason(context.Background(), current, "alternate"))
		})
	}
}

func TestPerplexityRealModelProbeRejectsIncompleteAndMixedAnswers(t *testing.T) {
	for _, body := range []string{
		perplexityModelAnswer("partial", "gpt54"),
		"data: {broken\n\n",
		"event: end_of_stream\n\n",
		perplexityModelAnswer("one", "gpt54") + perplexityModelAnswer("one two", "claude46sonnet") + "event: end_of_stream\n\n",
	} {
		svc, _, account, _ := perplexityModelProbeFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		result, _ := svc.probePerplexityModel(context.Background(), account, "public", "", "manual")
		require.False(t, result.Verified)
		if strings.Contains(body, "one two") {
			require.True(t, result.Success)
			require.Equal(t, "mixed", result.Status)
		} else {
			require.False(t, result.Success)
		}
	}
}

func TestPerplexityRealModelProbeCannotOverwriteNewCredentials(t *testing.T) {
	var repo *perplexityModelRepoStub
	svc, _, account, stored := perplexityModelProbeFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		repo.changeCredentials(72, "administrator-replacement")
		_, _ = io.WriteString(w, perplexityModelAnswer("OK", "gpt54")+"event: end_of_stream\n\n")
	})
	repo = stored
	result, err := svc.probePerplexityModel(context.Background(), account, "public", "", "manual")
	require.NoError(t, err)
	require.Equal(t, "stale", result.Status)
	require.Equal(t, "stale", result.Persistence)
	require.False(t, result.Verified)
	require.Zero(t, repo.probeWrites)
}

func TestPerplexityRealModelProbeProxyAndCancellation(t *testing.T) {
	t.Run("assigned proxy cannot fail open", func(t *testing.T) {
		svc, upstream, account, repo := perplexityModelProbeFixture(t, servePerplexityGoodProfile)
		id := int64(5)
		account.ProxyID = &id
		repo.accounts[account.ID].ProxyID = &id
		_, err := svc.probePerplexityModel(context.Background(), account, "public", "", "manual")
		require.ErrorIs(t, err, ErrAccountProxyUnavailable)
		require.Zero(t, upstream.count())
	})
	t.Run("close a blocked reader", func(t *testing.T) {
		svc, _, account, _ := perplexityModelProbeFixture(t, nil)
		reader, writer := io.Pipe()
		defer func() { _ = writer.Close() }()
		svc.httpUpstream = &perplexityProbeBlockingHTTP{body: reader}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		started := time.Now()
		result, err := svc.probePerplexityModel(ctx, account, "public", "", "manual")
		require.Error(t, err)
		require.Equal(t, "timeout", result.ErrorCode)
		require.Less(t, time.Since(started), time.Second)
	})
}

type perplexityProbeBlockingHTTP struct {
	HTTPUpstream
	body io.ReadCloser
}

func (u *perplexityProbeBlockingHTTP) DoWithTLS(_ *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: u.body}, nil
}

func TestPerplexityProbeHealthSelectionUsesThinkingVariant(t *testing.T) {
	account := perplexityQuotaTestAccount(t, 5, 5)
	account.Credentials["model_mapping"].(map[string]any)["public"] = "gpt54"
	until := time.Now().Add(time.Minute)
	base := PerplexityModelProbeResult{Preference: "gpt54", Mode: "copilot", Status: "unavailable", UnavailableUntil: &until, FinishedAt: time.Now(), IdentityFingerprint: perplexityAccountFingerprint(account)}
	account.Extra[perplexityModelHealthExtraKey] = map[string]any{"gpt54:copilot": base}
	require.NotEmpty(t, perplexityModelHealthPauseReason(context.Background(), account, "public"))
	ctx := WithPerplexityThinkingContext(context.Background(), []byte(`{"thinking":true}`))
	require.Empty(t, perplexityModelHealthPauseReason(ctx, account, "public"))
}
