package pinpoint

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A compile-time instrumentation hook can call into this package from the
// init function of a package that does not import it, before this package
// initialized. Every accessor a hook may touch answers as it would right
// after init instead of panicking on the zero-valued globals.
func TestAccessorsBeforeInit(t *testing.T) {
	savedAgent := globalAgent.Load()
	savedNoop := defaultNoopAgent
	savedLogger := logger
	savedBase := cfgBaseMap
	t.Cleanup(func() {
		globalAgent.Store(savedAgent)
		defaultNoopAgent = savedNoop
		logger = savedLogger
		cfgBaseMap = savedBase
	})

	// The state before init: nothing set, the noop agent's initializer not run.
	globalAgent = atomic.Value{}
	defaultNoopAgent = nil
	logger = nil
	cfgBaseMap = nil

	Log("init").Debugf("a hook logs before init")
	_ = IsDebugLogLevelEnabled()

	agent := GetAgent()
	require.NotNil(t, agent)
	assert.False(t, agent.Enable(), "no agent exists before init: the noop agent answers")
	assert.Equal(t, NoopAgent(), agent)
	require.NotNil(t, GetConfig())
	assert.Equal(t, "localhost", GetConfig().String(CfgCollectorHost), "the noop config carries the defaults")
	assert.False(t, GetConfig().Bool("Auto.Nothing.Registered"), "an unregistered key reads as its zero value")
}
