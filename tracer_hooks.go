package pinpoint

import "sync/atomic"

// TracerHooks are the callbacks a goroutine-local tracer registers with
// SetTracerHooks: code that keeps the current goroutine's tracer in the Go
// runtime needs to know when a tracer is created, handed to a goroutine,
// ended, and what FromContext found. Every field may be nil. The
// hooks run on the caller's goroutine, on the agent's hot path, and must
// not block; they receive the agent's Tracer interface and use nothing else.
type TracerHooks struct {
	// FromContext is called by FromContext with the tracer it found in the
	// context, NoopTracer() when there is none, and returns the tracer
	// FromContext returns instead; nil keeps the tracer it was given.
	FromContext func(Tracer) Tracer
	// SpanStart is called with the tracer NewSpanTracer and
	// NewSpanTracerWithReader return, NoopTracer() included.
	SpanStart func(Tracer)
	// SpanEvent is called first in NewSpanEvent of a sampled and of an
	// unsampled span.
	SpanEvent func(Tracer)
	// SpanEnd is called first in EndSpan of a sampled and of an unsampled
	// span.
	SpanEnd func(Tracer)
}

var tracerHooks atomic.Pointer[TracerHooks]

// SetTracerHooks registers h, replacing the hooks registered before; a zero
// TracerHooks unregisters them. Meant to be called once, from an init
// function, before any tracer exists. Unregistered, the core pays one atomic
// load per call site.
func SetTracerHooks(h TracerHooks) { tracerHooks.Store(&h) }

func hookFromContext(tracer Tracer) Tracer {
	if h := tracerHooks.Load(); h != nil && h.FromContext != nil {
		// FromContext never returns nil, and its callers use the result
		// unchecked: NewDatabaseTracer dereferenced a nil from the hook at
		// once, and the plugins wrapped it in a NestedTracer whose every method
		// panicked.
		if t := h.FromContext(tracer); t != nil {
			return t
		}
	}
	return tracer
}

func hookSpanStart(tracer Tracer) Tracer {
	if h := tracerHooks.Load(); h != nil && h.SpanStart != nil {
		h.SpanStart(tracer)
	}
	return tracer
}

func hookSpanEvent(tracer Tracer) {
	if h := tracerHooks.Load(); h != nil && h.SpanEvent != nil {
		h.SpanEvent(tracer)
	}
}

func hookSpanEnd(tracer Tracer) {
	if h := tracerHooks.Load(); h != nil && h.SpanEnd != nil {
		h.SpanEnd(tracer)
	}
}
