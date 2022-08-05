package pinpoint

import (
	"context"
	"net/http"
)

const ContextKey = "pinpoint.spanTracer"

func NewContext(ctx context.Context, tracer Tracer) context.Context {
	return context.WithValue(ctx, ContextKey, tracer)
}

func FromContext(ctx context.Context) Tracer {
	if v := ctx.Value(ContextKey); v != nil {
		tracer, ok := v.(Tracer)
		if !ok {
			return NoopTracer()
		}
		return tracer
	} else {
		return NoopTracer()
	}
}

func RequestWithTracerContext(req *http.Request, tracer Tracer) *http.Request {
	ctx := NewContext(req.Context(), tracer)
	return req.WithContext(ctx)
}

func TracerFromRequestContext(req *http.Request) Tracer {
	if req != nil {
		return FromContext(req.Context())
	}
	return NoopTracer()
}
