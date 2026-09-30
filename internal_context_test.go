package pinpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every context the agent sends a collector RPC with is marked, so that gRPC
// client instrumentation applied to every connection in the process can skip
// the agent's own calls.
func TestIsInternalContext(t *testing.T) {
	config, err := NewConfig(WithAppName("internalApp"), WithAgentName("internalAgent"))
	require.NoError(t, err)
	a, err := NewTestAgent(config)
	require.NoError(t, err)
	defer a.Shutdown()
	ag := a.(*agent)

	assert.False(t, IsInternalContext(context.Background()))
	assert.False(t, IsInternalContext(nil))
	assert.False(t, IsInternalContext(NewContext(context.Background(), NoopTracer())))

	assert.True(t, IsInternalContext(grpcMetadataContext(ag, -1)), "base outgoing context")
	assert.True(t, IsInternalContext(grpcMetadataContext(ag, 7)), "ping stream context")
	assert.True(t, IsInternalContext(commandMetadataContext(ag)), "command stream context")
	ctx, cancel := context.WithCancel(grpcMetadataContext(ag, -1))
	defer cancel()
	assert.True(t, IsInternalContext(ctx), "derived contexts keep the marker")
}
