package pinpoint

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A process exec'd with no argv at all has an empty os.Args; slicing it from
// 1 panicked NewConfig and the registration goroutine.
func TestEmptyArgsDoNotPanic(t *testing.T) {
	saved := os.Args
	defer func() { os.Args = saved }()
	os.Args = nil

	config := defaultConfig()
	assert.NotPanics(t, func() { assert.Empty(t, config.parseCmdArgs()) })
	assert.NotPanics(t, func() { assert.Empty(t, makeServerMetaData(config).VmArg) })
}

// A panic while connecting is recovered like a worker's: the agent is released
// as a failed connect, not the host process ended.
func TestNewAgent_PanicWhileConnectingIsRecovered(t *testing.T) {
	saved := getHostName
	defer func() { getHostName = saved }()
	getHostName = func() string { panic("boom") }

	config, err := NewConfig(WithAppName("connect-panic"))
	require.NoError(t, err)
	a, err := NewAgent(config)
	require.NoError(t, err)
	t.Cleanup(a.Shutdown)

	a.(*agent).connectWg.Wait()
	assert.Equal(t, phaseFailed, a.(*agent).enable.current())
	assert.Equal(t, NoopAgent(), GetAgent(), "the failed agent is released")
}
