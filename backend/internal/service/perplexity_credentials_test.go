//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
)

type perplexityStateRepoStub struct {
	AccountRepository
	mu              sync.Mutex
	accounts        map[int64]*Account
	cookieAttempts  int
	cookieWrites    int
	profileAttempts int
	profileWrites   int
}

func newPerplexityStateRepo(accounts ...*Account) *perplexityStateRepoStub {
	repo := &perplexityStateRepoStub{accounts: make(map[int64]*Account)}
	for _, account := range accounts {
		repo.accounts[account.ID] = copyPerplexityAccount(account)
	}
	return repo
}

func (r *perplexityStateRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	if account == nil {
		return nil, ErrAccountNotFound
	}
	return copyPerplexityAccount(account), nil
}

func (r *perplexityStateRepoStub) ListByPlatform(ctx context.Context, platform string) ([]Account, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []Account
	for _, account := range r.accounts {
		if account.Platform == platform {
			result = append(result, *copyPerplexityAccount(account))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (r *perplexityStateRepoStub) UpdatePerplexityCookiesIfUnchanged(ctx context.Context, id int64, expected map[string]any, proxy *int64, patch map[string]string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cookieAttempts++
	account := r.accounts[id]
	if account == nil || !reflect.DeepEqual(account.Credentials, expected) || !reflect.DeepEqual(account.ProxyID, proxy) {
		return false, nil
	}
	account.Credentials = shallowCopyMap(account.Credentials)
	for key, value := range patch {
		account.Credentials[key] = value
	}
	r.cookieWrites++
	return true, nil
}

func (r *perplexityStateRepoStub) ApplyPerplexityProfileIfUnchanged(ctx context.Context, id int64, expected map[string]any, proxy *int64, update PerplexityProfileUpdate) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profileAttempts++
	account := r.accounts[id]
	if account == nil || !reflect.DeepEqual(account.Credentials, expected) || !reflect.DeepEqual(account.ProxyID, proxy) {
		return false, nil
	}
	previous := perplexityProfileFromSnapshot(account.Extra[perplexityQuotaSnapshotExtraKey])
	if previous.ObservedAtMs >= update.ObservedAtMs {
		return false, nil
	}
	account.Extra = shallowCopyMap(account.Extra)
	account.Extra[perplexityQuotaSnapshotExtraKey] = update.Snapshot
	if update.SessionValid != nil {
		if !*update.SessionValid && (account.Status == StatusActive || account.ErrorMessage == PerplexityInvalidSessionMessage) {
			account.Status, account.ErrorMessage = StatusError, PerplexityInvalidSessionMessage
		} else if *update.SessionValid && account.Status == StatusError && account.ErrorMessage == PerplexityInvalidSessionMessage {
			account.Status, account.ErrorMessage = StatusActive, ""
		}
	}
	r.profileWrites++
	return true, nil
}

func (r *perplexityStateRepoStub) changeCredentials(id int64, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	account := r.accounts[id]
	account.Credentials = shallowCopyMap(account.Credentials)
	account.Credentials["api_key"] = token
}

func TestPerplexityCookieRotationAcrossProfileRequestsIsConditionalAndIsolated(t *testing.T) {
	svc, upstream, original := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == perplexity.SessionPath {
			w.Header().Add("Set-Cookie", "__Secure-next-auth.session-token=replacement; Path=/; Secure; HttpOnly")
			w.Header().Add("Set-Cookie", "next-auth.csrf-token=replacement-csrf; Path=/; Secure")
		}
		servePerplexityGoodProfile(w, req)
	})
	original.Status = StatusActive
	original.Credentials["operator_option"] = "preserved"
	repo := newPerplexityStateRepo(original)
	svc.accountRepo = repo
	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), original))
	require.True(t, *profile.SessionValid)
	require.Equal(t, 1, repo.cookieWrites)
	require.Equal(t, 1, repo.profileWrites)
	require.Contains(t, upstream.requests[0].Header.Get("Cookie"), "fake-session")
	for _, req := range upstream.requests[1:] {
		require.Contains(t, req.Header.Get("Cookie"), "session-token=replacement")
		require.Contains(t, req.Header.Get("Cookie"), "csrf-token=replacement-csrf")
	}
	current, err := repo.GetByID(context.Background(), original.ID)
	require.NoError(t, err)
	require.Equal(t, "replacement", current.GetPerplexitySessionToken())
	require.Equal(t, "preserved", current.GetCredential("operator_option"))
	require.Equal(t, perplexityAccountFingerprint(current), profile.IdentityFingerprint)
	require.Equal(t, "fake-session", original.GetPerplexitySessionToken(), "shared scheduler snapshot must remain immutable")
	require.Empty(t, original.Extra)
}

func TestPerplexityDelayedRotationAndAuthFailureCannotOverwriteAdminCredentials(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		var repo *perplexityStateRepoStub
		svc, _, original := newPerplexityInfoTestService(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path == perplexity.SessionPath {
				repo.changeCredentials(72, "administrator-replacement")
				w.Header().Add("Set-Cookie", "__Secure-next-auth.session-token=delayed-rotation; Path=/; Secure")
				if status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
			}
			servePerplexityGoodProfile(w, req)
		})
		original.Status = StatusActive
		repo = newPerplexityStateRepo(original)
		svc.accountRepo = repo
		profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), original))
		current, err := repo.GetByID(context.Background(), original.ID)
		require.NoError(t, err)
		require.Equal(t, "administrator-replacement", current.GetPerplexitySessionToken())
		require.Equal(t, StatusActive, current.Status)
		require.Zero(t, repo.cookieWrites)
		require.Zero(t, repo.profileWrites)
		require.Nil(t, profile.SessionValid)
		require.Empty(t, profile.Snapshot)
	}
}

func TestPerplexityAuthenticatedSenderNeverPublishesOrStoresUntrustedCookies(t *testing.T) {
	for _, status := range []int{200, 302, 401, 403} {
		svc, _, original := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Add("Set-Cookie", "__Secure-next-auth.session-token.1=secret-incomplete-chunk; Path=/; Secure")
			w.WriteHeader(status)
		})
		repo := newPerplexityStateRepo(original)
		svc.accountRepo = repo
		account := copyPerplexityAccount(original)
		resp, err := svc.doPerplexityRequest(context.Background(), account, http.MethodGet, perplexity.SessionPath)
		require.NoError(t, err)
		require.Empty(t, resp.Header.Values("Set-Cookie"))
		require.NoError(t, resp.Body.Close())
		require.Zero(t, repo.cookieAttempts)
		require.Equal(t, "fake-session", account.GetPerplexitySessionToken())
	}
}

func TestPerplexityAskLoadsCurrentCookieAndPersistsRotation(t *testing.T) {
	svc, upstream, original := perplexityIngressService(t, func(int, *http.Request) *http.Response {
		resp := perplexityAttachmentResponse(200, perplexityModelAnswer("answer", "pplx_pro")+"event: end_of_stream\n\n")
		resp.Header.Add("Set-Cookie", "__Secure-next-auth.session-token=ask-rotation; Path=/; Secure")
		return resp
	})
	original.Status, original.Schedulable = StatusActive, true
	repo := newPerplexityStateRepo(original)
	repo.changeCredentials(original.ID, "current-token")
	svc.accountRepo = repo
	body := []byte(`{"model":"sonar-pro","messages":[{"role":"user","content":"question"}]}`)
	c, recorder := perplexityIngressContext(t, body)
	result, err := svc.forwardPerplexityChatCompletions(context.Background(), c, original, body, "", "")
	require.NoError(t, err)
	require.Equal(t, "pplx_pro", result.UpstreamResponseModel)
	require.Contains(t, upstream.requests[0].Header.Get("Cookie"), "session-token=current-token")
	require.Empty(t, recorder.Header().Values("Set-Cookie"))
	current, err := repo.GetByID(context.Background(), original.ID)
	require.NoError(t, err)
	require.Equal(t, "ask-rotation", current.GetPerplexitySessionToken())
	require.Equal(t, "fake-session", original.GetPerplexitySessionToken())
}

func TestPerplexityModelsAndUploadAuthorizationCaptureRotationButStorageCannot(t *testing.T) {
	t.Run("model discovery", func(t *testing.T) {
		svc, _, original := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Add("Set-Cookie", "__Secure-next-auth.session-token=model-rotation; Path=/; Secure")
			_, _ = w.Write([]byte(`{"models":[{"model":"gpt54"}]}`))
		})
		repo := newPerplexityStateRepo(original)
		svc.accountRepo = repo
		models, err := svc.FetchPerplexityAccountModels(context.Background(), original)
		require.NoError(t, err)
		require.Len(t, models, 1)
		current, err := repo.GetByID(context.Background(), original.ID)
		require.NoError(t, err)
		require.Equal(t, "model-rotation", current.GetPerplexitySessionToken())
	})
	t.Run("image upload boundary", func(t *testing.T) {
		const objectURL = "https://fixture-bucket.s3.us-east-1.amazonaws.com/images/photo.png"
		svc, upstream, _ := perplexityIngressService(t, func(index int, req *http.Request) *http.Response {
			switch index {
			case 0:
				resp := perplexityAttachmentResponse(200, `{"s3_bucket_url":"https://fixture-bucket.s3.us-east-1.amazonaws.com/","s3_object_url":"`+objectURL+`","fields":{"key":"images/photo.png","policy":"fake"}}`)
				resp.Header.Add("Set-Cookie", "__Secure-next-auth.session-token=upload-rotation; Path=/; Secure")
				return resp
			case 1:
				require.Empty(t, req.Header.Get("Cookie"))
				resp := perplexityAttachmentResponse(204, "")
				resp.Header.Add("Set-Cookie", "__Secure-next-auth.session-token=storage-must-not-win; Path=/; Secure")
				return resp
			default:
				require.Contains(t, req.Header.Get("Cookie"), "session-token=upload-rotation")
				return perplexityAttachmentResponse(200, perplexityIngressAnswer("answer", "", true, false))
			}
		})
		original := perplexityAttachmentAccount()
		original.Status, original.Schedulable = StatusActive, true
		repo := newPerplexityStateRepo(original)
		svc.accountRepo = repo
		body, err := json.Marshal(map[string]any{"model": "sonar-pro", "messages": []any{map[string]any{"role": "user", "content": []any{perplexityAttachmentPart(perplexityAttachmentURL("image/png", perplexityAttachmentPNG(t)))}}}})
		require.NoError(t, err)
		c, _ := perplexityIngressContext(t, body)
		_, err = svc.forwardPerplexityChatCompletions(context.Background(), c, original, body, "", "")
		require.NoError(t, err)
		require.Len(t, upstream.requests, 3)
		require.Equal(t, 1, repo.cookieWrites)
		current, err := repo.GetByID(context.Background(), original.ID)
		require.NoError(t, err)
		require.Equal(t, "upload-rotation", current.GetPerplexitySessionToken())
	})
}

func TestPerplexityForwardRechecksAdministrativePauseAfterSelection(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		svc, upstream, original := perplexityIngressService(t, func(int, *http.Request) *http.Response {
			t.Fatal("a paused account must not reach upstream")
			return nil
		})
		original.Status, original.Schedulable = StatusActive, true
		paused := copyPerplexityAccount(original)
		if disabled {
			paused.Status = StatusDisabled
		} else {
			paused.Schedulable = false
		}
		svc.accountRepo = newPerplexityStateRepo(paused)
		body := []byte(`{"model":"sonar-pro","messages":[{"role":"user","content":"question"}]}`)
		c, _ := perplexityIngressContext(t, body)
		result, err := svc.forwardPerplexityChatCompletions(context.Background(), c, original, body, "", "")
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Nil(t, result)
		require.Empty(t, upstream.requests)
	}
}

func TestPerplexityConcurrentRotationsHaveOneGenerationWinner(t *testing.T) {
	var sequence atomic.Int32
	svc, _, original := newPerplexityInfoTestService(func(w http.ResponseWriter, _ *http.Request) {
		value := "rotation-" + strconv.Itoa(int(sequence.Add(1)))
		w.Header().Add("Set-Cookie", "__Secure-next-auth.session-token="+value+"; Path=/; Secure")
		_, _ = w.Write([]byte(`{"user":{"id":"fixture"}}`))
	})
	repo := newPerplexityStateRepo(original)
	svc.accountRepo = repo
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			account := copyPerplexityAccount(original)
			resp, err := svc.doPerplexityRequest(context.Background(), account, http.MethodGet, perplexity.SessionPath)
			if err != nil {
				t.Errorf("rotation request failed: %v", err)
				return
			}
			require.Empty(t, resp.Header.Values("Set-Cookie"))
			require.NoError(t, resp.Body.Close())
		}()
	}
	workers.Wait()
	require.Equal(t, 32, repo.cookieAttempts)
	require.Equal(t, 1, repo.cookieWrites)
	current, err := repo.GetByID(context.Background(), original.ID)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(current.GetPerplexitySessionToken(), "rotation-"))
	require.Equal(t, "fake-session", original.GetPerplexitySessionToken())
}

func TestPerplexityProfileLateSuccessDoesNotClearNewerAuthFailure(t *testing.T) {
	svc, upstream, original := newPerplexityInfoTestService(servePerplexityGoodProfile)
	original.Status = StatusError
	original.ErrorMessage = "administrator disabled account"
	repo := newPerplexityStateRepo(original)
	svc.accountRepo = repo
	profile := perplexityProfileFromSnapshot(svc.FetchPerplexityAccountInfo(context.Background(), original))
	require.True(t, *profile.SessionValid)
	current, err := repo.GetByID(context.Background(), original.ID)
	require.NoError(t, err)
	require.Equal(t, StatusError, current.Status)
	require.Equal(t, original.ErrorMessage, current.ErrorMessage)

	// A canceled request must not begin another upstream fetch.
	before := upstream.count()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = svc.fetchPerplexityJSON(ctx, original, perplexity.SessionPath)
	require.Error(t, err)
	require.Equal(t, before, upstream.count())
	require.NotContains(t, err.Error(), "fake-session")
	require.NotContains(t, strings.ToLower(err.Error()), "cookie=")
}
