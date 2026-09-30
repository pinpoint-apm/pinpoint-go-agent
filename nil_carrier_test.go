package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A nil carrier writes and reads nothing on the sampled span, as it does on
// the noop tracer, instead of panicking only once the request is sampled.
func TestSpan_NilCarrier(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	tracer := agent.NewSpanTracer("root", "/rpc")
	defer tracer.EndSpan()

	assert.NotPanics(t, func() { tracer.Inject(nil) })
	assert.NotPanics(t, func() { tracer.Extract(nil) })
	assert.NotEmpty(t, tracer.TransactionId().AgentId, "Extract(nil) starts a transaction like an empty carrier")
}
