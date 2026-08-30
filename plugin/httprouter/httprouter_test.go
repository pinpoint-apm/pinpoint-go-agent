package pphttprouter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/julienschmidt/httprouter"
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

// Every method helper has to register the wrapped handler under the same path
// and method, or the route silently disappears. Each one is registered on its
// own router and driven end to end.
func TestRouter_AllMethodsStayRouted(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		method   string
		register func(*Router, string, httprouter.Handle)
	}{
		{http.MethodGet, (*Router).GET},
		{http.MethodHead, (*Router).HEAD},
		{http.MethodOptions, (*Router).OPTIONS},
		{http.MethodPost, (*Router).POST},
		{http.MethodPut, (*Router).PUT},
		{http.MethodPatch, (*Router).PATCH},
		{http.MethodDelete, (*Router).DELETE},
	} {
		t.Run(tt.method, func(t *testing.T) {
			r := New()
			var name string
			tt.register(r, "/hello/:name", func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
				name = p.ByName("name")
				w.WriteHeader(http.StatusNoContent)
			})

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(tt.method, "/hello/pinpoint", nil))

			if name != "pinpoint" {
				t.Errorf("route parameter = %q, want %q", name, "pinpoint")
			}
			if rec.Code != http.StatusNoContent {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
			}
		})
	}
}

// Handle takes the method as an argument rather than from the helper name.
func TestRouter_Handle(t *testing.T) {
	startAgent(t)

	r := New()
	called := false
	r.Handle(http.MethodGet, "/hello/:name", func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
		called = true
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if !called {
		t.Error("the handler did not run")
	}
}

// Handler and HandlerFunc adapt a net/http handler, which reads its route
// parameters from the request context rather than from an argument. The
// wrapper replaces the request to add the tracer, so the params it stored have
// to survive that replacement.
func TestRouter_HandlerKeepsParamsInRequestContext(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name     string
		register func(*Router, string, http.HandlerFunc)
	}{
		{"Handler", func(r *Router, p string, h http.HandlerFunc) { r.Handler(http.MethodGet, p, h) }},
		{"HandlerFunc", func(r *Router, p string, h http.HandlerFunc) { r.HandlerFunc(http.MethodGet, p, h) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			var name string
			var sampled bool
			tt.register(r, "/hello/:name", func(w http.ResponseWriter, req *http.Request) {
				name = httprouter.ParamsFromContext(req.Context()).ByName("name")
				sampled = pinpoint.TracerFromRequestContext(req).IsSampled()
			})

			r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

			if name != "pinpoint" {
				t.Errorf("route parameter = %q, want %q", name, "pinpoint")
			}
			if !sampled {
				t.Error("handler received an unsampled tracer")
			}
		})
	}
}

// The handler reads its tracer out of the request context, so the wrapper has
// to hand the handler the tracer-carrying request.
func TestRouter_PutsSampledTracerInRequestContext(t *testing.T) {
	startAgent(t)

	r := New()
	var tracer pinpoint.Tracer
	r.GET("/", func(w http.ResponseWriter, req *http.Request, _ httprouter.Params) {
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

// WrapHandle instruments a handler registered on a plain httprouter.Router,
// where the wrapper never learns the route pattern. It still has to trace the
// call and keep the route working.
func TestWrapHandle_OnAPlainRouter(t *testing.T) {
	startAgent(t)

	r := httprouter.New()
	var name string
	var sampled bool
	r.GET("/hello/:name", WrapHandle(func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
		name = p.ByName("name")
		sampled = pinpoint.TracerFromRequestContext(req).IsSampled()
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if name != "pinpoint" {
		t.Errorf("route parameter = %q, want %q", name, "pinpoint")
	}
	if !sampled {
		t.Error("handler received an unsampled tracer")
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash httprouter's PanicHandler reports into a silent 200.
func TestRouter_RepanicsIntoThePanicHandler(t *testing.T) {
	startAgent(t)

	r := New()
	recovered := false
	r.PanicHandler = func(w http.ResponseWriter, req *http.Request, _ interface{}) {
		recovered = true
		w.WriteHeader(http.StatusInternalServerError)
	}
	r.GET("/boom", func(http.ResponseWriter, *http.Request, httprouter.Params) { panic("boom") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if !recovered {
		t.Error("the wrapper swallowed the handler panic")
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// With no agent running the wrapper must be a straight pass-through.
func TestRouter_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	r := New()
	called := false
	r.GET("/hello/:name", func(w http.ResponseWriter, req *http.Request, p httprouter.Params) {
		called = true
		if p.ByName("name") != "pinpoint" {
			t.Errorf("route parameter = %q, want %q", p.ByName("name"), "pinpoint")
		}
		if pinpoint.TracerFromRequestContext(req).IsSampled() {
			t.Error("a disabled agent produced a sampled tracer")
		}
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello/pinpoint", nil))

	if !called {
		t.Fatal("the handler did not run")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
