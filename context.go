package pinpoint

import (
	"context"
	"net/http"
)

// contextKeyType makes the tracer's context key unforgeable outside this
// package: an untyped string constant is a key any other package can produce
// by accident, and a collision on it would send FromContext down its !ok
// branch and drop the whole span with nothing logged (staticcheck SA1029).
type contextKeyType struct{}

var contextKey = contextKeyType{}

// NewContext returns a new Context that contains the given Tracer.
func NewContext(ctx context.Context, tracer Tracer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey, tracer)
}

// FromContext returns the Tracer from the context. If not present, NoopTracer is returned.
func FromContext(ctx context.Context) Tracer {
	if ctx == nil {
		return NoopTracer()
	}

	if v := ctx.Value(contextKey); v != nil {
		tracer, ok := v.(Tracer)
		if !ok {
			return NoopTracer()
		}
		return tracer
	} else {
		return NoopTracer()
	}
}

// RequestWithTracerContext returns the request that has a Context carrying the given Tracer.
func RequestWithTracerContext(req *http.Request, tracer Tracer) *http.Request {
	if req != nil {
		ctx := NewContext(req.Context(), tracer)
		return req.WithContext(ctx)
	} else {
		return req
	}
}

// TracerFromRequestContext returns the Tracer from the request's context. If not present, NoopTracer is returned.
func TracerFromRequestContext(req *http.Request) Tracer {
	if req != nil {
		return FromContext(req.Context())
	}
	return NoopTracer()
}
