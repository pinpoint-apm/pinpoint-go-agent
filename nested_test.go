package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNestedTracer(t *testing.T) {
	config, err := NewConfig(WithAppName("nestedApp"), WithAgentName("nestedAgent"))
	require.NoError(t, err)
	agent, err := NewTestAgent(config)
	require.NoError(t, err)
	defer agent.Shutdown()

	owner := agent.NewSpanTracer("HTTP Server", "/nested")
	nested := NestedTracer(owner)

	assert.True(t, IsNestedTracer(nested))
	assert.False(t, IsNestedTracer(owner))
	assert.Equal(t, nested, NestedTracer(nested), "nesting a nested tracer must not wrap it again")

	// The view is the same span: ids, sampling and events go to the owner.
	assert.Equal(t, owner.TransactionId(), nested.TransactionId())
	assert.Equal(t, owner.SpanId(), nested.SpanId())
	assert.True(t, nested.IsSampled())
	inner := nested.NewSpanEvent("inner")
	assert.True(t, IsNestedTracer(inner), "the tracer returned by NewSpanEvent stays nested")
	inner.EndSpanEvent()

	// EndSpan on the view does not end the owner's span.
	nested.EndSpan()
	span := owner.(*span)
	assert.False(t, span.finished.Load(), "EndSpan on a nested tracer must not finish the span")
	owner.NewSpanEvent("after").EndSpanEvent()
	owner.EndSpan()
	assert.True(t, span.finished.Load())
}

func TestNestedTracer_Noop(t *testing.T) {
	nested := NestedTracer(NoopTracer())
	assert.True(t, IsNestedTracer(nested))
	assert.False(t, nested.IsSampled())
	nested.NewSpanEvent("x").EndSpanEvent()
	nested.EndSpan()
}
