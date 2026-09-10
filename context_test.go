package pinpoint

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContext_RoundTrip(t *testing.T) {
	tracer := defaultTestSpan()
	assert.Same(t, tracer, FromContext(NewContext(context.Background(), tracer)), "tracer")
}

func TestContext_MissingTracerIsNoop(t *testing.T) {
	assert.Equal(t, NoopTracer(), FromContext(context.Background()), "empty context")
	assert.Equal(t, NoopTracer(), FromContext(nil), "nil context")
}

// The key used to be the untyped string constant "pinpoint.spanTracer", so any
// package storing anything under that string shadowed the tracer and FromContext
// silently returned NoopTracer - the span vanished with nothing logged. The key
// type is private now, so the collision cannot be constructed from outside.
func TestContext_ForeignStringKeyDoesNotShadowTheTracer(t *testing.T) {
	tracer := defaultTestSpan()
	ctx := NewContext(context.Background(), tracer)
	ctx = context.WithValue(ctx, "pinpoint.spanTracer", "not a tracer") //nolint:staticcheck // the point of the test

	assert.Same(t, tracer, FromContext(ctx), "tracer survives the foreign key")
}
