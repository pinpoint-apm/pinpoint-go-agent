package it

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Runtime reconfiguration flows through the agent's config-file watcher, not
// through a restart: the watcher rebuilds the config in place and republishes
// it, and the running agent must pick up the new dynamic values without
// re-registering itself.
func TestReloadsConfigFileAndAppliesNewSamplingRate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinpoint-config.yaml")
	writeSamplingConfig := func(rate int) {
		t.Helper()
		body := fmt.Sprintf("Sampling:\n  Type: COUNTER\n  CounterRate: %d\n", rate)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
	writeSamplingConfig(1)

	mc := startCollector(t)
	cfg := defaultAgentConfig()
	// The config file wins over ConfigOption values, so sampling comes from the
	// file while everything else stays inline.
	options := append(cfg.options(mc), pinpoint.WithConfigFile(path))
	config, err := pinpoint.NewConfig(options...)
	require.NoError(t, err)
	agent, err := pinpoint.NewAgent(config)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	require.True(t, mc.WaitFor(func(s Snapshot) bool { return len(s.AgentInfos) > 0 }, waitTimeout))
	require.True(t, waitUntil(func() bool { return agent.Enable() }, waitTimeout))
	require.Equal(t, 1, agent.Config().Int(pinpoint.CfgSamplingCounterRate))

	before := agent.NewSpanTracer("reload.probe", "/reloaded/before")
	assert.True(t, before.IsSampled())
	before.EndSpan()
	infosBefore := len(mc.Snapshot().AgentInfos)

	writeSamplingConfig(2)
	require.True(t, waitUntil(func() bool {
		return agent.Config().Int(pinpoint.CfgSamplingCounterRate) == 2
	}, waitTimeout), "the config-file watcher never applied the new sampling rate")

	// The reloaded sampler starts a fresh count, so it admits its first new
	// trace and every second one after that, and everything else keeps tracing.
	expected := []bool{true, false, true, false}
	sampled := driveSamplingPattern(t, agent, "reload.probe", "/reloaded/after/", expected, nil)
	require.NotEmpty(t, sampled)

	require.True(t, mc.WaitFor(func(s Snapshot) bool {
		return findSpanByRpc(s, "/reloaded/before") != nil &&
			findSpanByRpc(s, "/reloaded/after/0") != nil
	}, waitTimeout))

	s := mc.Snapshot()
	assert.Equal(t, 1, countSpansByRpc(s, "/reloaded/before"))
	expectSamplingPattern(t, s, "/reloaded/after/", expected)
	// A config reload must not re-send AgentInfo.
	assert.Len(t, s.AgentInfos, infosBefore)
	assert.True(t, agent.Enable())
}
