package pinpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unsampledReader carries the unsampled marker: NewSpanTracerWithReader then
// returns an unsampled span.
type unsampledReader struct{}

func (unsampledReader) Get(key string) (string, bool) {
	if key == HeaderSampled {
		return "s0", true
	}
	return "", false
}

// The registered hooks see every root tracer, what FromContext finds (the
// noop tracer included) and may replace it, and the first line of
// NewSpanEvent and EndSpan on sampled and unsampled spans; a zero
// TracerHooks unregisters them.
func TestTracerHooks(t *testing.T) {
	config, err := NewConfig(WithAppName("testApp"), WithAgentName("testAgent"))
	require.NoError(t, err)
	agent, err := NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	var starts, seen, events, ends []Tracer
	SetTracerHooks(TracerHooks{
		FromContext: func(tr Tracer) Tracer { seen = append(seen, tr); return tr },
		SpanStart:   func(tr Tracer) { starts = append(starts, tr) },
		SpanEvent:   func(tr Tracer) { events = append(events, tr) },
		SpanEnd:     func(tr Tracer) { ends = append(ends, tr) },
	})
	t.Cleanup(func() { SetTracerHooks(TracerHooks{}) })

	tracer := agent.NewSpanTracer("test", "/hooks")
	require.True(t, tracer.IsSampled())
	require.Equal(t, []Tracer{tracer}, starts, "SpanStart must see the tracer NewSpanTracer returns")

	assert.Same(t, tracer, FromContext(NewContext(context.Background(), tracer)))
	assert.Equal(t, NoopTracer(), FromContext(context.Background()))
	assert.Equal(t, []Tracer{tracer, NoopTracer()}, seen, "FromContext must pass what it found through the hook")

	tracer.NewSpanEvent("event").EndSpanEvent()
	tracer.EndSpan()
	assert.Equal(t, []Tracer{tracer}, events)
	assert.Equal(t, []Tracer{tracer}, ends)

	unsampled := agent.NewSpanTracerWithReader("test", "/unsampled", unsampledReader{})
	require.False(t, unsampled.IsSampled())
	assert.Equal(t, []Tracer{tracer, unsampled}, starts, "SpanStart must see an unsampled span too")
	unsampled.NewSpanEvent("event").EndSpanEvent()
	unsampled.EndSpan()
	assert.Equal(t, []Tracer{tracer, unsampled}, events)
	assert.Equal(t, []Tracer{tracer, unsampled}, ends)

	// The FromContext hook replaces the result.
	other := agent.NewSpanTracer("test", "/other")
	SetTracerHooks(TracerHooks{FromContext: func(Tracer) Tracer { return other }})
	assert.Same(t, other, FromContext(context.Background()))
	other.EndSpan()

	// Unregistered: everything passes through, nothing is called.
	SetTracerHooks(TracerHooks{})
	assert.Equal(t, NoopTracer(), FromContext(context.Background()))
	last := agent.NewSpanTracer("test", "/last")
	last.NewSpanEvent("event").EndSpanEvent()
	last.EndSpan()
	assert.Len(t, starts, 3, "the last tracer, made with the hooks unregistered, must not be seen") // tracer, unsampled, other
}
