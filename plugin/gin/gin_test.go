package ppgin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	m.Run()
}

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Shutdown)
}

// The middleware sits in front of every route, so it must leave gin's own
// behaviour intact: the matched route still runs with its path parameters, and
// the handler's status and body reach the client unchanged.
func TestMiddleware_PreservesRouting(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(Middleware())
	r.GET("/hello/:name", func(c *gin.Context) {
		c.String(http.StatusTeapot, "hello %s (%s)", c.Param("name"), c.FullPath())
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if want := "hello pinpoint (/hello/:name)"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

// The handler reads its tracer out of the request context, so the middleware
// has to replace c.Request with the tracer-carrying one before calling Next.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := gin.New()
	r.Use(Middleware())
	r.GET("/", func(c *gin.Context) {
		tracer = pinpoint.FromContext(c.Request.Context())
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if tracer == nil {
		t.Fatal("no tracer in the handler's request context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
	if tracer.TransactionId().String() == "" {
		t.Error("handler received a tracer without a transaction id")
	}
}

// WrapHandler instruments one route instead of the whole router, and has to
// give that handler the same tracer-carrying request the middleware does.
func TestWrapHandler_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var sampled bool
	r := gin.New()
	r.GET("/wrapped", WrapHandler(func(c *gin.Context) {
		sampled = pinpoint.FromContext(c.Request.Context()).IsSampled()
		c.Status(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/wrapped", nil))

	if !sampled {
		t.Error("wrapped handler received an unsampled tracer")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash gin's Recovery middleware reports into a silent 200.
func TestMiddleware_RepanicsAndLetsRecoveryRespond(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(gin.Recovery(), Middleware())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// Without a recovery middleware the panic must still reach the caller.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(Middleware())
	r.GET("/boom", func(c *gin.Context) { panic("boom") })

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
}

// c.Error is gin's way of reporting a failure without aborting; the wrapper
// records the last one, and must not disturb the response gin sends.
func TestMiddleware_HandlerErrorsDoNotChangeTheResponse(t *testing.T) {
	startAgent(t)

	r := gin.New()
	r.Use(Middleware())
	r.GET("/err", func(c *gin.Context) {
		_ = c.Error(errors.New("first"))
		_ = c.Error(errors.New("last"))
		c.String(http.StatusBadRequest, "bad")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/err", nil))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rec.Body.String() != "bad" {
		t.Errorf("body = %q, want %q", rec.Body.String(), "bad")
	}
}

// With no agent running the middleware must be a straight pass-through: no
// tracer in the context and no panic from tracing a disabled agent.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := gin.New()
	r.Use(Middleware())
	r.GET("/", func(c *gin.Context) {
		called = true
		if pinpoint.FromContext(c.Request.Context()).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
		c.Status(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("the handler did not run")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
