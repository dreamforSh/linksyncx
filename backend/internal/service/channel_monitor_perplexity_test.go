//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/perplexity"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func (r *quotaModeRepoStub) Create(_ context.Context, m *ChannelMonitor) error {
	m.ID = 1
	stored := *m
	r.monitor = &stored
	return nil
}

func newPerplexityMonitorFixture(t *testing.T, reply http.HandlerFunc) (*ChannelMonitorService, *quotaModeRepoStub, *perplexityInfoHTTP, *perplexityModelRepoStub) {
	t.Helper()
	prober, upstream, account, accounts := perplexityModelProbeFixture(t, reply)
	repo := &quotaModeRepoStub{monitor: &ChannelMonitor{
		ID: 1, Name: "perplexity", Provider: MonitorProviderPerplexity, CheckMode: MonitorCheckModeProbe,
		PrimaryModel: "public", AccountID: &account.ID, IntervalSeconds: 60, Enabled: true,
		APIKey: "corrupt-unused-key",
	}}
	svc := newQuotaModeService(repo)
	usage := &AccountUsageService{cache: NewUsageCache()}
	usage.SetPerplexityAccountInfoFetcher(prober)
	svc.SetQuotaFetcher(&ChannelMonitorQuotaFetcher{accounts: accounts, usage: usage})
	svc.SetPerplexityProbe(prober, &fakeLeaderLockCache{})
	return svc, repo, upstream, accounts
}

func monitorChallengeAnswer(t *testing.T, prompt string) string {
	t.Helper()
	last := strings.LastIndex(prompt, "Q: ")
	require.NotEqual(t, -1, last)
	var a, b int
	var op string
	_, err := fmt.Sscanf(prompt[last+3:], "%d %s %d", &a, &op, &b)
	require.NoError(t, err)
	if op == "+" {
		return fmt.Sprint(a + b)
	}
	return fmt.Sprint(a - b)
}

func TestPerplexityMonitorCreateUpdateAndCapabilityMatrix(t *testing.T) {
	svc, repo, upstream, accounts := newPerplexityMonitorFixture(t, nil)
	id := int64(72)
	base := ChannelMonitorCreateParams{Name: "web", Provider: MonitorProviderPerplexity, AccountID: &id, IntervalSeconds: 60}
	created, err := svc.Create(context.Background(), base)
	require.NoError(t, err)
	require.Equal(t, MonitorCheckModeQuota, created.CheckMode, "default must not invoke a paid model")
	require.Equal(t, "quota", created.PrimaryModel)
	require.Empty(t, created.APIKey)
	require.Equal(t, "NEW:", repo.monitor.APIKey)
	for _, mode := range []string{MonitorCheckModeProbe, MonitorCheckModeQuotaProbe} {
		p := base
		p.CheckMode, p.PrimaryModel = mode, "public"
		_, err := svc.Create(context.Background(), p)
		require.NoError(t, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*ChannelMonitorCreateParams)
		want   error
	}{
		{"missing account", func(p *ChannelMonitorCreateParams) { p.AccountID = nil }, ErrChannelMonitorAccountRequired},
		{"zero account", func(p *ChannelMonitorCreateParams) { p.AccountID = int64Ptr(0) }, ErrChannelMonitorAccountRequired},
		{"endpoint", func(p *ChannelMonitorCreateParams) { p.Endpoint = "https://www.perplexity.ai" }, ErrChannelMonitorPerplexityConfig},
		{"cookie as api key", func(p *ChannelMonitorCreateParams) { p.APIKey = "fake-cookie" }, ErrChannelMonitorPerplexityConfig},
		{"template", func(p *ChannelMonitorCreateParams) { p.TemplateID = int64Ptr(5) }, ErrChannelMonitorPerplexityConfig},
		{"headers", func(p *ChannelMonitorCreateParams) { p.ExtraHeaders = map[string]string{"Cookie": "fake"} }, ErrChannelMonitorPerplexityConfig},
		{"body", func(p *ChannelMonitorCreateParams) { p.BodyOverride = map[string]any{"query_str": "fake"} }, ErrChannelMonitorPerplexityConfig},
		{"responses", func(p *ChannelMonitorCreateParams) { p.APIMode = MonitorAPIModeResponses }, ErrChannelMonitorInvalidAPIMode},
		{"model", func(p *ChannelMonitorCreateParams) {
			p.CheckMode = MonitorCheckModeProbe
			p.PrimaryModel = "not-mapped"
		}, ErrChannelMonitorPerplexityModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.change(&p)
			_, err := svc.Create(context.Background(), p)
			require.ErrorIs(t, err, tc.want)
		})
	}
	account := accounts.accounts[72]
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		account.Type = accountType
		_, err := svc.Create(context.Background(), base)
		require.ErrorIs(t, err, ErrChannelMonitorAccountNotSupportable)
	}
	account.Type = AccountTypeAPIKey
	account.Credentials["api_key"] = ""
	_, err = svc.Create(context.Background(), base)
	require.ErrorIs(t, err, ErrChannelMonitorAccountNotSupportable)
	account.Credentials["api_key"] = "fake-session"
	account.Platform = PlatformGrok
	_, err = svc.Create(context.Background(), base)
	require.ErrorIs(t, err, ErrChannelMonitorProviderIncompatible)
	account.Platform = PlatformPerplexity

	repo.monitor.CheckMode, repo.monitor.PrimaryModel = MonitorCheckModeQuota, "quota"
	probe, model := MonitorCheckModeProbe, "public"
	updated, err := svc.Update(context.Background(), 1, ChannelMonitorUpdateParams{CheckMode: &probe, PrimaryModel: &model})
	require.NoError(t, err)
	require.Empty(t, updated.APIKey)
	require.Empty(t, updated.Endpoint)
	zero := int64(0)
	_, err = svc.Update(context.Background(), 1, ChannelMonitorUpdateParams{AccountID: &zero})
	require.ErrorIs(t, err, ErrChannelMonitorAccountRequired)
	cookie := "do-not-store-cookie-here"
	_, err = svc.Update(context.Background(), 1, ChannelMonitorUpdateParams{APIKey: &cookie})
	require.ErrorIs(t, err, ErrChannelMonitorPerplexityConfig)
	require.Zero(t, upstream.count(), "CRUD must never invoke or validate upstream automatically")
}

func TestPerplexityMonitorNativeProbeUsesCookiePersonaAndPersistsHistory(t *testing.T) {
	svc, repo, upstream, accounts := newPerplexityMonitorFixture(t, func(w http.ResponseWriter, req *http.Request) {
		require.Equal(t, perplexity.AskPath, req.URL.Path)
		body, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		preference := gjson.GetBytes(body, "params.model_preference").String()
		_, _ = io.WriteString(w, perplexityModelAnswer(monitorChallengeAnswer(t, gjson.GetBytes(body, "query_str").String()), preference)+"event: end_of_stream\n\n")
	})
	proxyID := int64(19)
	accounts.accounts[72].ProxyID = &proxyID
	accounts.accounts[72].Proxy = &Proxy{ID: proxyID, Protocol: "socks5", Host: "127.0.0.1", Port: 7891, Status: StatusActive}
	repo.monitor.ExtraModels = []string{"alternate"}
	results, err := svc.RunCheck(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Len(t, repo.history, 2)
	require.Equal(t, []int64{1}, repo.markedIDs)
	for i, result := range results {
		require.Equal(t, MonitorStatusOperational, result.Status)
		require.Nil(t, result.PingLatencyMs, "must not issue a direct HEAD request")
		require.Empty(t, result.Message)
		require.Equal(t, "socks5://127.0.0.1:7891", upstream.proxies[i])
		require.NotNil(t, upstream.profiles[i])
		require.Contains(t, upstream.requests[i].Header.Get("Cookie"), "fake-session")
		require.Empty(t, upstream.requests[i].Header.Get("Authorization"))
		require.True(t, HTTPUpstreamRedirectsDisabled(upstream.requests[i].Context()))
	}
	require.Equal(t, 2, upstream.count())
	require.Equal(t, 2, accounts.probeWrites)
}

func TestPerplexityMonitorUnverifiedAndWrongAnswersAreNotHealthy(t *testing.T) {
	for _, tc := range []struct {
		name, identity, want string
		wrong                bool
	}{
		{"substitution", "claude46sonnet", MonitorStatusDegraded, false},
		{"unknown", "", MonitorStatusDegraded, false},
		{"wrong answer", "gpt54", MonitorStatusFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _, _ := newPerplexityMonitorFixture(t, func(w http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				answer := monitorChallengeAnswer(t, gjson.GetBytes(body, "query_str").String())
				if tc.wrong {
					answer = "wrong answer with private-cookie"
				}
				_, _ = io.WriteString(w, perplexityModelAnswer(answer, tc.identity)+"event: end_of_stream\n\n")
			})
			results, err := svc.RunCheck(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.want, results[0].Status)
			require.NotContains(t, results[0].Message, "private-cookie")
		})
	}
}

func TestPerplexityMonitorQuotaDoesNotInvokeModelAndQuotaProbeAttachesOnce(t *testing.T) {
	for _, mode := range []string{MonitorCheckModeQuota, MonitorCheckModeQuotaProbe} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, upstream, _ := newPerplexityMonitorFixture(t, func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case perplexity.AskPath:
					body, _ := io.ReadAll(req.Body)
					answer := monitorChallengeAnswer(t, gjson.GetBytes(body, "query_str").String())
					_, _ = io.WriteString(w, perplexityModelAnswer(answer, gjson.GetBytes(body, "params.model_preference").String())+"event: end_of_stream\n\n")
				case perplexity.RateLimitAllPath:
					_, _ = io.WriteString(w, `{"remaining_pro":40,"remaining_research":5}`)
				default:
					servePerplexityGoodProfile(w, req)
				}
			})
			repo.monitor.CheckMode = mode
			if mode == MonitorCheckModeQuota {
				repo.monitor.PrimaryModel = "quota"
			} else {
				repo.monitor.ExtraModels = []string{"alternate"}
			}
			results, err := svc.RunCheck(context.Background(), 1)
			require.NoError(t, err)
			require.True(t, results[0].Quota.Success)
			require.Len(t, results[0].Quota.Remaining, 2)
			require.NotNil(t, repo.history[0].Quota)
			if mode == MonitorCheckModeQuota {
				require.Equal(t, 3, upstream.count())
				require.Nil(t, results[0].LatencyMs)
				for _, req := range upstream.requests {
					require.NotEqual(t, perplexity.AskPath, req.URL.Path)
				}
			} else {
				require.Len(t, results, 2)
				require.Nil(t, results[1].Quota)
				require.Equal(t, 5, upstream.count())
			}
		})
	}
}

func TestPerplexityMonitorLocksRuntimeProxyAndCancellationFailClosed(t *testing.T) {
	for _, state := range []string{"no lock", "busy", "lock error", "v2", "disabled", "proxy missing", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			svc, _, upstream, accounts := newPerplexityMonitorFixture(t, nil)
			ctx := context.Background()
			switch state {
			case "no lock":
				svc.perplexityLocks = nil
			case "busy":
				svc.perplexityLocks = &fakeLeaderLockCache{owners: map[string]string{"perplexity:channel-monitor:1": "peer"}}
			case "lock error":
				svc.perplexityLocks = &fakeLeaderLockCache{acquireErr: errors.New("unavailable")}
			case "v2":
				svc.SetRuntimeReader(channelMonitorRuntimeStub{rt: ChannelMonitorRuntime{Enabled: true, Mode: ChannelMonitorModeV2}})
			case "disabled":
				svc.SetRuntimeReader(channelMonitorRuntimeStub{rt: ChannelMonitorRuntime{Enabled: false, Mode: ChannelMonitorModeV1}})
			case "proxy missing":
				accounts.accounts[72].ProxyID = int64Ptr(9)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			results, err := svc.RunCheck(ctx, 1)
			if state == "proxy missing" {
				require.NoError(t, err)
				require.Equal(t, MonitorStatusError, results[0].Status)
			} else {
				require.Error(t, err)
			}
			require.Zero(t, upstream.count())
		})
	}
}

func TestPerplexityMonitorNativeTemplateRejectedBeforePersistence(t *testing.T) {
	params := ChannelMonitorRequestTemplateCreateParams{Name: "cookie template", Provider: MonitorProviderPerplexity}
	require.ErrorIs(t, validateTemplateCreateParams(params), ErrChannelMonitorPerplexityConfig)
	require.ErrorIs(t, applyTemplateUpdate(&ChannelMonitorRequestTemplate{Provider: MonitorProviderPerplexity}, ChannelMonitorRequestTemplateUpdateParams{}), ErrChannelMonitorPerplexityConfig)
}

func TestPerplexityMonitorQuotaSnapshotUnknownNotGreen(t *testing.T) {
	svc, repo, _, _ := newPerplexityMonitorFixture(t, servePerplexityGoodProfile)
	repo.monitor.CheckMode, repo.monitor.PrimaryModel = MonitorCheckModeQuota, "quota"
	results, err := svc.RunCheck(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, MonitorStatusError, results[0].Status)
	require.False(t, results[0].Quota.Success)
	require.WithinDuration(t, time.Now(), results[0].CheckedAt, time.Second)
}
