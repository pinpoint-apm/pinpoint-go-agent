package ppgorilla

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

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

// The middleware sits in front of every route, so it must leave mux's own
// behaviour intact: route variables still resolve and the handler's status and
// body reach the client unchanged.
func TestMiddleware_PreservesRoutingAndVars(t *testing.T) {
	startAgent(t)

	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/hello/{name}", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello " + mux.Vars(req)["name"]))
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTeapot)
	}
	if want := "hello pinpoint"; rec.Body.String() != want {
		t.Errorf("body = %q, want %q", rec.Body.String(), want)
	}
}

// The handler reads its tracer out of the request context, so the wrapper has
// to hand the handler the tracer-carrying request.
func TestMiddleware_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(req)
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	if tracer == nil {
		t.Fatal("no tracer in the handler's request context")
	}
	if !tracer.IsSampled() {
		t.Error("handler received an unsampled tracer")
	}
}

// mux.CurrentRoute returns nil whenever a wrapped handler runs outside a mux
// router, so the URL-stat path lookup - which runs in a defer, after the
// handler - must tolerate its absence instead of dereferencing nil.
func TestWrapHandlerFunc_OutsideAMuxRouter(t *testing.T) {
	startAgent(t)

	called := false
	h := WrapHandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	srv := http.NewServeMux()
	srv.HandleFunc("/plain", h)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))

	if !called {
		t.Fatal("the handler did not run")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// WrapHandler instruments one route instead of the whole router; inside a mux
// router the route template is available to the deferred URL-stat collection.
func TestWrapHandler_InsideAMuxRouter(t *testing.T) {
	startAgent(t)

	var pathTemplate string
	r := mux.NewRouter()
	r.Handle("/hello/{name}", WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		pathTemplate, _ = mux.CurrentRoute(req).GetPathTemplate()
	})))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if want := "/hello/{name}"; pathTemplate != want {
		t.Errorf("route template = %q, want %q", pathTemplate, want)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash the server reports into a silent 200.
func TestMiddleware_PanicPropagates(t *testing.T) {
	startAgent(t)

	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	defer func() {
		if recover() == nil {
			t.Error("the wrapper swallowed the handler panic")
		}
	}()
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
}

// With no agent running the middleware must be a straight pass-through.
func TestMiddleware_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	r := mux.NewRouter()
	r.Use(Middleware())
	r.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		called = true
		if pinpoint.TracerFromRequestContext(req).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
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
