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
		u, ok := v.(Tracer)
		if !ok {
			return nil
		}
		return u
	} else {
		return nil
	}
}

func RequestWithTracerContext(req *http.Request, tracer Tracer) *http.Request {
	ctx := NewContext(req.Context(), tracer)
	return req.WithContext(ctx)
}

func TracerFromRequestContext(req *http.Request) Tracer {
	var tracer Tracer
	if req != nil {
		tracer = FromContext(req.Context())
	}
	return tracer
}
