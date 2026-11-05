//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type lookupAccountRepo struct {
	AccountRepository
	byID map[int64]*Account
}

func (r *lookupAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	return r.byID[id], nil
}

func TestForwardOpenAIResponsesLookupGuards(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKeyAccount := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Credentials: map[string]any{"api_key": "sk-relay", "base_url": "https://relay.example.test/v1"}}
	oauthAccount := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"access_token": "tok", "chatgpt_account_id": "acc"}}
	upstream := &openAIPlatformStub{responses: map[string][]byte{"resp_mine": []byte(`{"id":"resp_mine","object":"response"}`)},
		inputs: map[string][]json.RawMessage{}, requests: map[string]*http.Request{}, bodies: map[string][]byte{}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream, accountRepo: &lookupAccountRepo{byID: map[int64]*Account{apiKeyAccount.ID: apiKeyAccount, oauthAccount.ID: oauthAccount}}}
	ctx := context.Background()
	bind := func(responseID string, accountID, ownerUserID int64) {
		require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, 0, responseID, accountID, time.Hour))
		require.NoError(t, svc.BindOpenAIHTTPResponseOwner(ctx, 0, responseID, ownerUserID, ownerUserID))
	}
	lookup := func(responseID string) (bool, error, *httptest.ResponseRecorder) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses/"+url.PathEscape(responseID), nil)
		handled, err := svc.ForwardOpenAIResponsesLookup(ctx, c, 0, responseID, 1, 1, http.MethodGet, "")
		return handled, err, rec
	}

	bind("resp_mine", apiKeyAccount.ID, 1)
	handled, err, rec := lookup("resp_mine")
	require.NoError(t, err)
	require.True(t, handled)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"id":"resp_mine","object":"response"}`, rec.Body.String())

	bind("resp_theirs", apiKeyAccount.ID, 2)
	handled, err, rec = lookup("resp_theirs")
	require.NoError(t, err)
	require.False(t, handled, "another user's response is never fetched")
	require.Zero(t, rec.Body.Len())

	bind("resp_oauth", oauthAccount.ID, 1)
	handled, err, _ = lookup("resp_oauth")
	require.NoError(t, err)
	require.False(t, handled, "subscription responses live only in the gateway store")

	for _, unsafe := range []string{"..", "resp a", ""} {
		handled, err, _ = lookup(unsafe)
		require.NoError(t, err)
		require.False(t, handled, unsafe)
	}

	upstream.mu.Lock()
	calls := len(upstream.lookups)
	upstream.mu.Unlock()
	require.Equal(t, 1, calls, "only the owned API-key response reached the upstream")

	apiKeyAccount.Credentials["api_key"] = "sk-revoked"
	handled, err, rec = lookup("resp_mine")
	require.ErrorIs(t, err, ErrOpenAIResponsesLookupUpstream)
	require.False(t, handled)
	require.Zero(t, rec.Body.Len(), "the upstream's auth failure is not relayed to the caller")
}
