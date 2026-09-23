package pinpoint

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A second NewAgent returns the existing agent and ErrAgentAlreadyCreated, a
// sentinel an application (or the compile-time instrumentation tool's
// bootstrap) can test for with errors.Is instead of failing on the message.
func TestNewAgent_AlreadyCreated(t *testing.T) {
	config, err := NewConfig(WithAppName("firstApp"), WithAgentName("firstAgent"))
	require.NoError(t, err)
	first, err := NewTestAgent(config, t)
	require.NoError(t, err)
	defer first.Shutdown()

	second, err := NewConfig(WithAppName("secondApp"), WithAgentName("secondAgent"))
	require.NoError(t, err)
	got, err := NewAgent(second)
	assert.True(t, errors.Is(err, ErrAgentAlreadyCreated), "err = %v", err)
	assert.Equal(t, "agent is already created", err.Error(), "the message applications matched so far is unchanged")
	assert.Same(t, first, got, "the existing agent is returned")
}
