package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/gemini"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type antigravityHandlerModelsUpstream struct {
	body string
}

func (u *antigravityHandlerModelsUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(u.body)),
		Header:     make(http.Header),
	}, nil
}

func (u *antigravityHandlerModelsUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGatewayAntigravityModels_SyncsLiveUpstreamAndSupportsSingleModelRetrieve(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(88)
	repo := &gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {
				{
					ID:          501,
					Platform:    service.PlatformAntigravity,
					Type:        service.AccountTypeUpstream,
					Status:      service.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{
						"base_url": "https://relay.antigravity.example",
						"api_key":  "secret-key",
					},
				},
			},
		},
	}
	upstream := &antigravityHandlerModelsUpstream{
		body: `{"data":[{"id":"gemini-4-carbon-high","display_name":"Gemini 4 Carbon (High)"},{"id":"claude-opus-5-6","display_name":"Claude Opus 5.6"}]}`,
	}
	agSvc := service.NewAntigravityGatewayService(repo, nil, nil, nil, nil, upstream, nil, nil)
	h := &GatewayHandler{
		antigravityGatewayService: agSvc,
		maxAccountSwitches:        3,
		maxAccountSwitchesGemini:  3,
	}

	// 1. /antigravity/v1/models returns live upstream models
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/models", nil)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformAntigravity},
	})
	h.AntigravityModels(c)
	require.Equal(t, http.StatusOK, rec.Code)

	var listResp struct {
		Object string                    `json:"object"`
		Data   []antigravity.ClaudeModel `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &listResp))
	require.Equal(t, "list", listResp.Object)
	require.Len(t, listResp.Data, 3)
	require.Equal(t, "claude-opus-5-6", listResp.Data[0].ID)
	require.Equal(t, "Claude Opus 5.6", listResp.Data[0].DisplayName)
	require.Equal(t, "gemini-4-carbon", listResp.Data[1].ID)
	require.Equal(t, "Gemini 4 Carbon", listResp.Data[1].DisplayName)
	require.Equal(t, "gemini-4-carbon-high", listResp.Data[2].ID)
	require.Equal(t, "Gemini 4 Carbon (High)", listResp.Data[2].DisplayName)

	// 2. /v1/models on an Antigravity group also uses live upstream discovery
	recV1 := httptest.NewRecorder()
	cV1, _ := gin.CreateTestContext(recV1)
	cV1.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	cV1.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformAntigravity},
	})
	h.Models(cV1)
	require.Equal(t, http.StatusOK, recV1.Code)
	require.Contains(t, recV1.Body.String(), "gemini-4-carbon-high")

	// 3. Single model retrieve (/antigravity/v1/models/:model)
	recOne := httptest.NewRecorder()
	cOne, _ := gin.CreateTestContext(recOne)
	cOne.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/models/gemini-4-carbon-high", nil)
	cOne.Params = gin.Params{{Key: "model", Value: "gemini-4-carbon-high"}}
	cOne.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformAntigravity},
	})
	h.AntigravityModels(cOne)
	require.Equal(t, http.StatusOK, recOne.Code)
	var single antigravity.ClaudeModel
	require.NoError(t, json.Unmarshal(recOne.Body.Bytes(), &single))
	require.Equal(t, "gemini-4-carbon-high", single.ID)
	require.Equal(t, "Gemini 4 Carbon (High)", single.DisplayName)

	// 4. /antigravity/v1beta/models syncs Gemini models and exports bare thinking name
	recBeta := httptest.NewRecorder()
	cBeta, _ := gin.CreateTestContext(recBeta)
	cBeta.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1beta/models", nil)
	cBeta.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
	cBeta.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformAntigravity},
	})
	h.GeminiV1BetaListModels(cBeta)
	require.Equal(t, http.StatusOK, recBeta.Code)
	var betaResp gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(recBeta.Body.Bytes(), &betaResp))
	names := make([]string, 0, len(betaResp.Models))
	for _, gm := range betaResp.Models {
		names = append(names, gm.Name)
	}
	require.Contains(t, names, "models/gemini-4-carbon-high")
	require.Contains(t, names, "models/gemini-4-carbon")
	require.NotContains(t, names, "models/claude-opus-5-6")
}

type antigravityHandlerRoutingUpstream struct {
	bodies    map[int64]string
	statuses  map[int64]int
	calledIDs []int64
}

func (u *antigravityHandlerRoutingUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.calledIDs = append(u.calledIDs, accountID)
	status := u.statuses[accountID]
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(u.bodies[accountID])),
		Header:     make(http.Header),
	}, nil
}

func (u *antigravityHandlerRoutingUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func TestGatewayAntigravityModels_PinnedAccountsAndErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(89)
	overloaded := time.Now().Add(time.Hour)
	repo := &gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {
				{
					ID:                     601,
					Platform:               service.PlatformAntigravity,
					Type:                   service.AccountTypeUpstream,
					Status:                 service.StatusActive,
					Schedulable:            true,
					OverloadUntil:          &overloaded,
					TempUnschedulableUntil: &overloaded,
					Credentials: map[string]any{
						"base_url": "https://relay-a.antigravity.example",
						"api_key":  "key-a",
					},
				},
				{
					ID:          602,
					Platform:    service.PlatformAntigravity,
					Type:        service.AccountTypeUpstream,
					Status:      service.StatusActive,
					Schedulable: true,
					Credentials: map[string]any{
						"base_url": "https://relay-b.antigravity.example",
						"api_key":  "key-b",
					},
				},
			},
		},
	}
	upstream := &antigravityHandlerRoutingUpstream{
		bodies: map[int64]string{
			601: `{"data":[{"id":"gemini-4-argon-high","display_name":"Gemini 4 Argon (High)"}]}`,
			602: `{"data":[{"id":"claude-opus-5-6","display_name":"Claude Opus 5.6"}]}`,
		},
	}
	agSvc := service.NewAntigravityGatewayService(repo, nil, nil, nil, nil, upstream, nil, nil)
	h := &GatewayHandler{
		gatewayService: service.NewGatewayService(
			repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		),
		antigravityGatewayService: agSvc,
		maxAccountSwitches:        3,
		maxAccountSwitchesGemini:  3,
	}

	pinnedGroup := &service.Group{
		ID:       groupID,
		Platform: service.PlatformAntigravity,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{
			Enabled:    true,
			AccountIDs: []int64{601, 602},
		},
	}

	// 1. /antigravity/v1/models merges pinned accounts (including transiently overloaded 601)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/models", nil)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: pinnedGroup})
	h.AntigravityModels(c)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "gemini-4-argon-high")
	require.Contains(t, rec.Body.String(), "claude-opus-5-6")

	// 2. /v1beta/models on /antigravity/v1beta/models uses pinned Gemini models
	recBeta := httptest.NewRecorder()
	cBeta, _ := gin.CreateTestContext(recBeta)
	cBeta.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1beta/models", nil)
	cBeta.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
	cBeta.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: pinnedGroup})
	h.GeminiV1BetaListModels(cBeta)
	require.Equal(t, http.StatusOK, recBeta.Code)
	var betaResp gemini.ModelsListResponse
	require.NoError(t, json.Unmarshal(recBeta.Body.Bytes(), &betaResp))
	betaNames := make([]string, 0, len(betaResp.Models))
	for _, gm := range betaResp.Models {
		betaNames = append(betaNames, gm.Name)
	}
	require.Contains(t, betaNames, "models/gemini-4-argon-high")
	require.Contains(t, betaNames, "models/gemini-4-argon")

	// 3. codexModelIDsForGroup uses live/pinned Antigravity discovery and includes split bare Gemini 4 ID
	codexIDs := h.codexModelIDsForGroup(context.Background(), pinnedGroup, "")
	require.Equal(t, []string{"claude-opus-5-6", "gemini-4-argon", "gemini-4-argon-high"}, codexIDs)

	// 4. Pinned group with no usable accounts returns 503 instead of stale static list
	noUsableGroup := &service.Group{
		ID:       groupID,
		Platform: service.PlatformAntigravity,
		CodexModelsManifestConfig: service.GroupCodexModelsManifestConfig{
			Enabled:    true,
			AccountIDs: []int64{9999},
		},
	}
	rec503 := httptest.NewRecorder()
	c503, _ := gin.CreateTestContext(rec503)
	c503.Request = httptest.NewRequest(http.MethodGet, "/antigravity/v1/models", nil)
	c503.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{GroupID: &groupID, Group: noUsableGroup})
	h.AntigravityModels(c503)
	require.Equal(t, http.StatusServiceUnavailable, rec503.Code)
}

func TestGatewayModels_Gemini4ReasoningMetadataAndCompositeLiveDiscovery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	groupID := int64(90)
	repo := &gatewayModelsAccountRepoStub{
		byGroup: map[int64][]service.Account{
			groupID: {
				{
					ID:          701,
					Platform:    service.PlatformAntigravity,
					Type:        service.AccountTypeUpstream,
					Status:      service.StatusActive,
					Schedulable: true,
					Extra:       map[string]any{"mixed_scheduling": true},
					Credentials: map[string]any{
						"base_url": "https://relay-g4.antigravity.example",
						"api_key":  "key-g4",
					},
				},
			},
		},
	}
	upstream := &antigravityHandlerModelsUpstream{
		body: `{"data":[{"id":"gemini-4-argon-high","display_name":"Gemini 4 Argon (High)"}]}`,
	}
	agSvc := service.NewAntigravityGatewayService(repo, nil, nil, nil, nil, upstream, nil, nil)
	gwSvc := service.NewGatewayService(
		repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h := &GatewayHandler{
		gatewayService:            gwSvc,
		antigravityGatewayService: agSvc,
		maxAccountSwitches:        3,
		maxAccountSwitchesGemini:  3,
	}

	// 1. Antigravity /v1/models advertises both gemini-4-argon and gemini-4-argon-high with reasoning metadata
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformAntigravity},
	})
	h.Models(c)
	require.Equal(t, http.StatusOK, rec.Code)

	var agResp struct {
		Data []struct {
			ID                      string `json:"id"`
			DisplayName             string `json:"display_name"`
			SupportsReasoningEffort bool   `json:"supportsReasoningEffort"`
			ReasoningEffort         string `json:"reasoningEffort"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &agResp))
	require.Len(t, agResp.Data, 2)
	require.Equal(t, "gemini-4-argon", agResp.Data[0].ID)
	require.Equal(t, "Gemini 4 Argon", agResp.Data[0].DisplayName)
	require.True(t, agResp.Data[0].SupportsReasoningEffort)
	require.Equal(t, "high", agResp.Data[0].ReasoningEffort)
	require.Equal(t, "gemini-4-argon-high", agResp.Data[1].ID)
	require.Equal(t, "Gemini 4 Argon (High)", agResp.Data[1].DisplayName)
	require.True(t, agResp.Data[1].SupportsReasoningEffort)

	// 2. Composite group /v1/models discovers live Antigravity Gemini 4 models
	recComp := httptest.NewRecorder()
	cComp, _ := gin.CreateTestContext(recComp)
	cComp.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	cComp.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformComposite},
	})
	h.Models(cComp)
	require.Equal(t, http.StatusOK, recComp.Code)
	require.Contains(t, recComp.Body.String(), `"gemini-4-argon"`)
	require.Contains(t, recComp.Body.String(), `"gemini-4-argon-high"`)

	// 3. Gemini group /v1/models merges live mixed-scheduling Antigravity Gemini 4 models
	recGem := httptest.NewRecorder()
	cGem, _ := gin.CreateTestContext(recGem)
	cGem.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	cGem.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformGemini},
	})
	h.Models(cGem)
	require.Equal(t, http.StatusOK, recGem.Code)
	require.Contains(t, recGem.Body.String(), `"gemini-4-argon"`)
	require.Contains(t, recGem.Body.String(), `"gemini-4-argon-high"`)
}
