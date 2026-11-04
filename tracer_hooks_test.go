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
	agent := newTestAgent(defaultConfig())

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

	FromContext(NewContext(context.Background(), tracer))
	FromContext(context.Background())
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

	// A hook returning nil keeps what the context held: FromContext never
	// returns nil, and NewDatabaseTracer uses the result unchecked.
	SetTracerHooks(TracerHooks{FromContext: func(Tracer) Tracer { return nil }})
	assert.Equal(t, NoopTracer(), FromContext(context.Background()))
	assert.NotPanics(t, func() {
		NewDatabaseTracer(context.Background(), "query", &DBInfo{}).EndSpanEvent()
	})

	// Unregistered: everything passes through, nothing is called.
	SetTracerHooks(TracerHooks{})
	assert.Equal(t, NoopTracer(), FromContext(context.Background()))
	last := agent.NewSpanTracer("test", "/last")
	last.NewSpanEvent("event").EndSpanEvent()
	last.EndSpan()
	assert.Len(t, starts, 3, "the last tracer, made with the hooks unregistered, must not be seen") // tracer, unsampled, other
}

// SpanStart sees NoopTracer() too: the noop agent's tracers, and the one
// NewKafkaConsumerTracer returns for a disabled agent, go through the hook as
// an enabled agent's do.
func TestTracerHooks_SpanStartSeesNoopTracer(t *testing.T) {
	prevHooks, prevAgent := tracerHooks.Load(), GetAgent()
	t.Cleanup(func() {
		tracerHooks.Store(prevHooks)
		setGlobalAgent(prevAgent)
	})
	setGlobalAgent(NoopAgent())

	var starts []Tracer
	SetTracerHooks(TracerHooks{SpanStart: func(tr Tracer) { starts = append(starts, tr) }})

	NoopAgent().NewSpanTracer("op", "/rpc")
	NoopAgent().NewSpanTracerWithReader("op", "/rpc", &noopDistributedTracingContextReader{})
	NewKafkaConsumerTracer("Kafka Consumer Invocation", KafkaConsumerMessage{Topic: "widgets"}, nil)
	assert.Equal(t, []Tracer{NoopTracer(), NoopTracer(), NoopTracer()}, starts)
}
