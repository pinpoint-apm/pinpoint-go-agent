package ppfiberv3

import (
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Middleware and WrapHandler on the same route make one span, not two: the
// inner layer finds the tracer the outer one put in the user context and
// records on it, as pphttp does for a middleware inside a wrapped handler.
func TestWrapHandler_InsideMiddlewareIsNested(t *testing.T) {
	startAgent(t)

	var outer, inner pinpoint.Tracer
	app := fiber.New()
	app.Use(Middleware())
	app.Use(func(c fiber.Ctx) error {
		outer = pinpoint.FromContext(c.Context())
		return c.Next()
	})
	app.Get("/hello", WrapHandler(func(c fiber.Ctx) error {
		inner = pinpoint.FromContext(c.Context())
		return nil
	}))

	get(t, app, "/hello")

	require.NotNil(t, outer)
	require.NotNil(t, inner)
	assert.False(t, pinpoint.IsNestedTracer(outer), "the middleware owns the span")
	assert.True(t, pinpoint.IsNestedTracer(inner), "the wrapped handler records on the middleware's span")
	assert.Equal(t, outer.SpanId(), inner.SpanId(), "one span per request")
	assert.Equal(t, outer.TransactionId(), inner.TransactionId(), "one transaction per request")
}
