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
		pinpoint.WithAgentName("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	return agent
}

// usePluginConfig starts an agent with the given options and initializes the
// plugin's derived config from it.
func usePluginConfig(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	agent := startAgent(t, opts...)
	httpCfg()
	return agent
}
