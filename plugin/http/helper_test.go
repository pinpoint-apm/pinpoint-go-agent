package pphttp

import (
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/require"
)

// startAgent brings up an offline agent for the duration of a test. The plugin
// reads its options through pinpoint.GetConfig(), so the options given here are
// what the filters and recorders under test are built from.
func startAgent(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentId("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	return agent
}

// usePluginConfig starts an agent with the given options and republishes the
// plugin's derived config from them. httpCfg() builds that config once per
// process, so a test that changes an option has to publish the rebuild itself.
func usePluginConfig(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	agent := startAgent(t, opts...)

	httpCfg() // trip the sync.Once first, or it would overwrite the store below
	previous := curHttpConfig.Load()
	curHttpConfig.Store(newHttpConfig())
	t.Cleanup(func() { curHttpConfig.Store(previous) })

	return agent
}
