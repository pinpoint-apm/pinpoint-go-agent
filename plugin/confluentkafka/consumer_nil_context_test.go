package ppconfluentkafka

import (
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/require"
)

// A nil context is a message with nothing attached; reading a value off it
// panicked in the consumer once the agent was enabled.
func Test_newConsumerTracer_NilContext(t *testing.T) {
	startAgent(t)
	var tracer pinpoint.Tracer
	require.NotPanics(t, func() { tracer = newConsumerTracer(nil, consumed("w", 0, 0)) })
	require.NotNil(t, tracer)
	tracer.EndSpan()
}
