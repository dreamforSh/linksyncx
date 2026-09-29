//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clashWritableSettingsRepo struct{ *settingRepoStub }

func (s *clashWritableSettingsRepo) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func TestClashAutomaticProbeSettingsCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		readErr   error
		enabled   bool
	}{
		{name: "missing setting", enabled: true},
		{name: "legacy record", raw: `{}`, enabled: true},
		{name: "explicit true", raw: `{"automatic_probes_enabled":true}`, enabled: true},
		{name: "explicit false", raw: `{"automatic_probes_enabled":false}`},
		{name: "empty stored value", raw: ` `},
		{name: "null stored value", raw: `null`},
		{name: "null flag", raw: `{"automatic_probes_enabled":null}`},
		{name: "invalid stored value", raw: `{"automatic_probes_enabled":true,"max_accounts_per_exit":0}`},
		{name: "malformed record", raw: `{`},
		{name: "storage unavailable", readErr: errors.New("settings unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newClashTestEnv(t, nil)
			if tc.raw != "" {
				env.settings.values[SettingKeyClashPoolSettings] = tc.raw
			}
			env.settings.err = tc.readErr
			settings := env.svc.poolSettings(context.Background())
			require.Equal(t, tc.enabled, settings.AutomaticProbesEnabled)
			require.False(t, settings.AllowUnprobedExitBinding)
			require.Equal(t, 1, settings.MaxAccountsPerExit)
			require.Equal(t, ClashExitChangePause, settings.ExitChangePolicy)
		})
	}
}

func TestClashAutomaticProbeSettingRoundTrip(t *testing.T) {
	env := newClashTestEnv(t, nil)
	env.svc.settings.settingRepo = &clashWritableSettingsRepo{env.settings}
	settings := defaultClashPoolSettings()
	settings.AutomaticProbesEnabled = false
	require.NoError(t, env.svc.settings.SetClashPoolSettings(context.Background(), settings))
	var stored map[string]any
	require.NoError(t, json.Unmarshal([]byte(env.settings.values[SettingKeyClashPoolSettings]), &stored))
	require.Equal(t, false, stored["automatic_probes_enabled"])
	loaded, err := env.svc.settings.GetClashPoolSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, settings, loaded)
}

func TestClashAutomaticProbesDisabledKeepsLocalMaintenance(t *testing.T) {
	env := newClashTestEnv(t, withSettings(func(s *ClashPoolSettings) {
		s.AutomaticProbesEnabled = false
	}))
	ctx := context.Background()
	profile := env.repo.addProfile("no remote refresh", true)
	live := env.repo.addNode(profile.ID, "active", "")
	disabled := env.repo.addNode(profile.ID, "disabled", "", func(n *ClashNode) {
		n.Status = ClashNodeStatusDisabled
	})
	account := env.repo.bind("paused account", disabled.ProxyID, nil)
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	var listenerChecks atomic.Int64
	manager.probeListener = func(context.Context, string, string, string) error {
		listenerChecks.Add(1)
		return nil
	}
	require.NoError(t, manager.syncOnce(ctx))
	require.Equal(t, 1, env.runtime.appliedCount())
	require.Positive(t, listenerChecks.Load())
	require.NotEmpty(t, manager.currentHash(), "a synced core must not bypass the probe gate")
	require.Empty(t, env.repo.account(account.ID).Reason)

	manager.leaderCycle(ctx)
	require.Empty(t, env.runtime.attempts, "no latency requests")
	require.Zero(t, env.prober.calls, "no proxy or direct-IP requests (platform checks are downstream of these)")
	require.NotNil(t, env.repo.account(account.ID).Until, "pause reconciliation still runs")
	require.False(t, manager.lastReclaim.IsZero(), "retention maintenance still runs")
	require.Empty(t, env.repo.refreshes, "zero interval must not refresh subscriptions")
	require.Nil(t, env.repo.node(live.ID).LastCheckedAt)
	require.Nil(t, env.repo.node(live.ID).ExitCheckedAt)

	started := time.Now().Add(-time.Minute)
	sampler := newClashTrafficSampler(started)
	env.runtime.setConnections(conn("local counters", live.ID, 100, 500, started.Add(time.Second)))
	now := time.Now()
	manager.sampleTraffic(ctx, sampler, now)
	manager.flushTraffic(ctx, sampler)
	require.Equal(t, 1, manager.localTrafficLive().Nodes[live.ID].Connections)
	require.EqualValues(t, 100, env.repo.trafficDay(live.ID, clashTrafficDate(now)).Upload)
	require.EqualValues(t, 500, env.repo.trafficDay(live.ID, clashTrafficDate(now)).Download)
}

func TestClashAutomaticProbesDefaultKeepsScheduling(t *testing.T) {
	env := newClashTestEnv(t, nil)
	profile := env.repo.addProfile("no remote refresh", true)
	node := env.repo.addNode(profile.ID, "due", "")
	manager := NewClashManager(env.svc, env.runtime, nil, nil, nil, env.cfg)
	manager.probeListener = func(context.Context, string, string, string) error { return nil }
	require.NoError(t, manager.syncOnce(context.Background()))
	// The fake egress prober fails unknown proxy ports, before any platform request.
	manager.leaderCycle(context.Background())
	require.Positive(t, env.runtime.attempts[ClashProxyName(node.ID)])
	require.Positive(t, env.prober.calls)
}
