package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestClaudeUsageFetchAppendsReadModeQuery(t *testing.T) {
	var rawQuery string
	srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "five_hour": {"utilization": 1, "resets_at": "2026-01-01T00:00:00Z"},
  "limits": [{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},"percent":9}],
  "cedar_ember": {"eligible": true, "grants": []}
}`)
	}))
	client := &claudeUsageService{usageURL: srv.URL, allowPrivateHosts: true}

	resp, err := client.FetchUsageWithOptions(context.Background(), &service.ClaudeUsageFetchOptions{
		AccessToken: "at",
		Query:       "cedar_ember=1&skip_spend=1",
	})
	require.NoError(t, err)
	require.Equal(t, "cedar_ember=1&skip_spend=1", rawQuery)
	require.JSONEq(t, `{"eligible": true, "grants": []}`, string(resp.CedarEmber))
	require.Contains(t, string(resp.Limits), "Fable")

	_, err = client.FetchUsageWithOptions(context.Background(), &service.ClaudeUsageFetchOptions{AccessToken: "at"})
	require.NoError(t, err)
	require.Empty(t, rawQuery, "plain reads keep the bare usage URL")
}

func TestClaudeOAuthAPIClientFetchProfile(t *testing.T) {
	var authorization, beta, userAgent, method string
	srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		authorization = r.Header.Get("Authorization")
		beta = r.Header.Get("anthropic-beta")
		userAgent = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, `{"organization":{"organization_type":"claude_max"}}`)
	}))
	client := &claudeUsageService{profileURL: srv.URL, allowPrivateHosts: true}

	body, err := client.FetchProfile(context.Background(), &service.ClaudeUsageFetchOptions{
		AccessToken: "at",
		Fingerprint: &service.Fingerprint{UserAgent: "claude-cli/2.1.283 (external, cli)"},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"organization":{"organization_type":"claude_max"}}`, string(body))
	require.Equal(t, http.MethodGet, method)
	require.Equal(t, "Bearer at", authorization)
	require.Equal(t, "oauth-2025-04-20", beta)
	require.Equal(t, "claude-cli/2.1.283 (external, cli)", userAgent, "the cached fingerprint UA is reused")
}

func TestClaudeOAuthAPIClientClaimRateLimitReset(t *testing.T) {
	var path string
	var payload map[string]any
	srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		decoded := map[string]any{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&decoded))
		payload = decoded
		_, _ = io.WriteString(w, `{"result":"reset","resets_left":0}`)
	}))
	client := &claudeUsageService{organizationsURL: srv.URL + "/api/organizations", allowPrivateHosts: true}

	body, err := client.ClaimRateLimitReset(context.Background(), &service.ClaudeUsageFetchOptions{AccessToken: "at"}, "org-1", &service.ClaudeRateLimitResetRequest{
		Program:   "cedar_ember",
		GrantID:   "grant_1",
		RequestID: "req-1",
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"result":"reset","resets_left":0}`, string(body))
	require.Equal(t, "/api/organizations/org-1/reset_rate_limits", path)
	require.Equal(t, map[string]any{"program": "cedar_ember", "grant_id": "grant_1", "request_id": "req-1"}, payload)

	// juniper_tide 只带 program
	_, err = client.ClaimRateLimitReset(context.Background(), &service.ClaudeUsageFetchOptions{AccessToken: "at"}, "org-1", &service.ClaudeRateLimitResetRequest{Program: "juniper_tide"})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"program": "juniper_tide"}, payload)

	_, err = client.ClaimRateLimitReset(context.Background(), &service.ClaudeUsageFetchOptions{AccessToken: "at"}, " ", &service.ClaudeRateLimitResetRequest{Program: "juniper_tide"})
	require.Error(t, err)
}

func TestClaudeOAuthAPIClientMapsUpstreamErrors(t *testing.T) {
	cases := []struct {
		status     int
		wantStatus int
		wantReason string
	}{
		// 上游 401/403 不能原样透传：后台前端会把 401 当成管理员登录失效。
		{http.StatusUnauthorized, http.StatusBadGateway, "CLAUDE_UPSTREAM_AUTH_FAILED"},
		{http.StatusForbidden, http.StatusBadGateway, "CLAUDE_UPSTREAM_AUTH_FAILED"},
		{http.StatusTooManyRequests, http.StatusTooManyRequests, "CLAUDE_UPSTREAM_RATE_LIMITED"},
		{http.StatusInternalServerError, http.StatusBadGateway, "CLAUDE_UPSTREAM_ERROR"},
	}
	for _, tc := range cases {
		srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, `{"error":{"type":"x"}}`)
		}))
		client := &claudeUsageService{profileURL: srv.URL, allowPrivateHosts: true}
		_, err := client.FetchProfile(context.Background(), &service.ClaudeUsageFetchOptions{AccessToken: "at"})
		require.Error(t, err)
		require.Equal(t, tc.wantStatus, infraerrors.Code(err), "status %d", tc.status)
		require.Equal(t, tc.wantReason, infraerrors.Reason(err), "status %d", tc.status)
		srv.Close()
	}
}
