package pphttp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type proxyValues struct {
	called       bool
	key          int32
	receivedTime int64
	code         int32
	duration     int32
	idle         int32
	busy         int32
	app          string
}

// proxyAnnotation captures the single proxy header annotation setProxyHeader records.
type proxyAnnotation struct {
	got proxyValues
}

func (a *proxyAnnotation) AppendLongIntIntByteByteString(key int32, l int64, i1 int32, i2 int32, b1 int32, b2 int32, s string) {
	a.got = proxyValues{true, key, l, i1, i2, b1, b2, s}
}

func (a *proxyAnnotation) AppendInt(int32, int32)                                {}
func (a *proxyAnnotation) AppendLong(int32, int64)                               {}
func (a *proxyAnnotation) AppendString(int32, string)                            {}
func (a *proxyAnnotation) AppendStringString(int32, string, string)              {}
func (a *proxyAnnotation) AppendIntStringString(int32, int32, string, string)    {}
func (a *proxyAnnotation) AppendBytesStringString(int32, []byte, string, string) {}

func Test_setProxyHeader(t *testing.T) {
	tests := []struct {
		name   string
		header string
		value  string
		want   proxyValues
	}{
		{name: "no proxy header"},

		{name: "apache", header: "Pinpoint-ProxyApache", value: "t=1500968753503 D=125 i=51 b=48",
			want: proxyValues{called: true, code: 3, receivedTime: 1500968753, duration: 125, idle: 51, busy: 48}},
		{name: "apache bare token", header: "Pinpoint-ProxyApache", value: "t",
			want: proxyValues{called: true, code: 3}},
		{name: "apache bare token before valid ones", header: "Pinpoint-ProxyApache", value: "t D=125 junk i=51 b",
			want: proxyValues{called: true, code: 3, duration: 125, idle: 51}},
		{name: "apache empty values", header: "Pinpoint-ProxyApache", value: "t= D= i= b=",
			want: proxyValues{called: true, code: 3}},
		{name: "apache extra spaces", header: "Pinpoint-ProxyApache", value: "  t=1500968753503   D=125  ",
			want: proxyValues{called: true, code: 3, receivedTime: 1500968753, duration: 125}},
		{name: "apache unparsable numbers", header: "Pinpoint-ProxyApache", value: "t=abc D=x i=y b=z",
			want: proxyValues{called: true, code: 3}},
		{name: "apache repeated keys keep the last", header: "Pinpoint-ProxyApache", value: "D=1 D=2",
			want: proxyValues{called: true, code: 3, duration: 2}},

		{name: "nginx", header: "Pinpoint-ProxyNginx", value: "t=1504164327.484 D=0.000",
			want: proxyValues{called: true, code: 2, receivedTime: 1504164327484}},
		{name: "nginx bare token", header: "Pinpoint-ProxyNginx", value: "t",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx bare token before valid one", header: "Pinpoint-ProxyNginx", value: "t D=7",
			want: proxyValues{called: true, code: 2, duration: 7}},
		{name: "nginx empty values", header: "Pinpoint-ProxyNginx", value: "t= D=",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx NaN", header: "Pinpoint-ProxyNginx", value: "t=NaN",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx +Inf", header: "Pinpoint-ProxyNginx", value: "t=Inf",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx -Inf", header: "Pinpoint-ProxyNginx", value: "t=-Inf",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx overflow", header: "Pinpoint-ProxyNginx", value: "t=1e400",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx negative overflow", header: "Pinpoint-ProxyNginx", value: "t=-1e400",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx out of int64 range", header: "Pinpoint-ProxyNginx", value: "t=1e300",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx unparsable", header: "Pinpoint-ProxyNginx", value: "t=abc",
			want: proxyValues{called: true, code: 2}},
		{name: "nginx negative time", header: "Pinpoint-ProxyNginx", value: "t=-1.5",
			want: proxyValues{called: true, code: 2, receivedTime: -1500}},

		{name: "app", header: "Pinpoint-ProxyApp", value: "t=1500968753503 app=foo-bar",
			want: proxyValues{called: true, code: 1, receivedTime: 1500968753503, app: "foo-bar"}},
		{name: "app bare token", header: "Pinpoint-ProxyApp", value: "app",
			want: proxyValues{called: true, code: 1}},
		{name: "app bare token before valid one", header: "Pinpoint-ProxyApp", value: "app t=1500968753503",
			want: proxyValues{called: true, code: 1, receivedTime: 1500968753503}},
		{name: "app empty values", header: "Pinpoint-ProxyApp", value: "t= app=",
			want: proxyValues{called: true, code: 1}},
		{name: "app time is not divided by 1000", header: "Pinpoint-ProxyApp", value: "t=1500968753503",
			want: proxyValues{called: true, code: 1, receivedTime: 1500968753503}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				req.Header.Set(tt.header, tt.value)
			}

			want := tt.want
			if want.called {
				want.key = pinpoint.AnnotationHttpProxyHeader
			}

			a := &proxyAnnotation{}
			setProxyHeader(a, header{req.Header})

			assert.Equal(t, want, a.got, "%s: %q", tt.header, tt.value)
		})
	}
}

// The three proxy headers are checked in order, so a request carrying more than
// one must report the first that matched and only that one.
func Test_setProxyHeader_Precedence(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Pinpoint-ProxyApache", "t=1500968753503")
	req.Header.Set("Pinpoint-ProxyNginx", "t=1504164327.484")
	req.Header.Set("Pinpoint-ProxyApp", "t=1 app=foo")

	a := &proxyAnnotation{}
	setProxyHeader(a, header{req.Header})

	assert.Equal(t, int32(3), a.got.code, "Apache wins over Nginx and App")
	assert.Empty(t, a.got.app, "the App header must not be read once Apache matched")
}

func Test_headerFirst(t *testing.T) {
	h := http.Header{}
	h.Add("X-Multi", "first")
	h.Add("X-Multi", "second")
	h.Set("X-Empty", "")

	assert.Equal(t, "first", headerFirst(header{h}, "X-Multi"))
	assert.Equal(t, "", headerFirst(header{h}, "X-Empty"), "a present but empty header reads as empty")
	assert.Equal(t, "", headerFirst(header{h}, "X-Missing"))
}

func Test_resolveRemoteAddr(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{
			name:       "the transport peer address is stripped of its port",
			remoteAddr: "10.0.0.1:54321",
			want:       "10.0.0.1",
		},
		{
			name:       "an IPv6 peer address is stripped of its port",
			remoteAddr: "[2001:db8::1]:54321",
			want:       "2001:db8::1",
		},
		{
			name:       "an address without a port is used as is",
			remoteAddr: "10.0.0.1",
			want:       "10.0.0.1",
		},
		{
			name:       "an empty peer address stays empty",
			remoteAddr: "",
			want:       "",
		},
		{
			name:       "X-Forwarded-For wins over the peer address",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "the first hop of X-Forwarded-For is the client",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7, 10.0.0.2, 10.0.0.3"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "X-Forwarded-For is trimmed",
			headers:    map[string]string{"X-Forwarded-For": "  203.0.113.7  , 10.0.0.2"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "X-Real-Ip is the fallback when X-Forwarded-For is absent",
			headers:    map[string]string{"X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "X-Forwarded-For wins over X-Real-Ip",
			headers:    map[string]string{"X-Forwarded-For": "203.0.113.7", "X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "an empty X-Forwarded-For falls through",
			headers:    map[string]string{"X-Forwarded-For": "", "X-Real-Ip": "203.0.113.9"},
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "an empty X-Real-Ip falls through to the peer address",
			headers:    map[string]string{"X-Real-Ip": ""},
			remoteAddr: "10.0.0.1:54321",
			want:       "10.0.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			assert.Equal(t, tt.want, resolveRemoteAddr(header{h}, tt.remoteAddr))
		})
	}
}

type optionalResponseWriter struct {
	flushes int
	hijacks int
	pushes  int
}

func (w *optionalResponseWriter) Flush() {
	w.flushes++
}

func (w *optionalResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacks++
	return nil, nil, errOptionalResponseWriter
}

func (w *optionalResponseWriter) Push(string, *http.PushOptions) error {
	w.pushes++
	return errOptionalResponseWriter
}

var errOptionalResponseWriter = errors.New("optional response writer called")

type (
	testResponseWriterF struct {
		http.ResponseWriter
		http.Flusher
	}
	testResponseWriterH struct {
		http.ResponseWriter
		http.Hijacker
	}
	testResponseWriterP struct {
		http.ResponseWriter
		http.Pusher
	}
	testResponseWriterFH struct {
		http.ResponseWriter
		http.Flusher
		http.Hijacker
	}
	testResponseWriterFP struct {
		http.ResponseWriter
		http.Flusher
		http.Pusher
	}
	testResponseWriterHP struct {
		http.ResponseWriter
		http.Hijacker
		http.Pusher
	}
	testResponseWriterFHP struct {
		http.ResponseWriter
		http.Flusher
		http.Hijacker
		http.Pusher
	}
)

func responseWriterWithOptionalInterfaces(base http.ResponseWriter, optional *optionalResponseWriter, mask int) http.ResponseWriter {
	switch mask {
	case 7:
		return testResponseWriterFHP{base, optional, optional, optional}
	case 6:
		return testResponseWriterHP{base, optional, optional}
	case 5:
		return testResponseWriterFP{base, optional, optional}
	case 4:
		return testResponseWriterP{base, optional}
	case 3:
		return testResponseWriterFH{base, optional, optional}
	case 2:
		return testResponseWriterH{base, optional}
	case 1:
		return testResponseWriterF{base, optional}
	default:
		return struct{ http.ResponseWriter }{base}
	}
}

func Test_responseWriter_PreservesOptionalInterfaces(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		t.Run(fmt.Sprintf("mask%03b", mask), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			optional := &optionalResponseWriter{}
			original := responseWriterWithOptionalInterfaces(recorder, optional, mask)
			status := 0
			wrapped := WrapResponseWriter(original, &status)

			flusher, flushes := wrapped.(http.Flusher)
			hijacker, hijacks := wrapped.(http.Hijacker)
			pusher, pushes := wrapped.(http.Pusher)
			require.Equal(t, mask&1 != 0, flushes, "http.Flusher must be reachable exactly when the original implements it")
			require.Equal(t, mask&2 != 0, hijacks, "http.Hijacker must be reachable exactly when the original implements it")
			require.Equal(t, mask&4 != 0, pushes, "http.Pusher must be reachable exactly when the original implements it")

			if flushes {
				flusher.Flush()
			}
			if hijacks {
				_, _, err := hijacker.Hijack()
				assert.ErrorIs(t, err, errOptionalResponseWriter, "Hijack() must reach the original writer")
			}
			if pushes {
				assert.ErrorIs(t, pusher.Push("/asset", nil), errOptionalResponseWriter, "Push() must reach the original writer")
			}
			assert.Equal(t, mask&1, optional.flushes, "delegated Flush calls")
			assert.Equal(t, (mask>>1)&1, optional.hijacks, "delegated Hijack calls")
			assert.Equal(t, (mask>>2)&1, optional.pushes, "delegated Push calls")

			wrapped.WriteHeader(http.StatusCreated)
			assert.Equal(t, http.StatusCreated, status, "the wrapper must publish the status it saw")
			assert.Equal(t, http.StatusCreated, recorder.Code, "the status must still reach the original writer")

			unwrapper, ok := wrapped.(interface{ Unwrap() http.ResponseWriter })
			require.True(t, ok, "http.ResponseController needs Unwrap")
			assert.Equal(t, original, unwrapper.Unwrap(), "Unwrap must return the underlying writer")
		})
	}
}

// The status pointer follows the last WriteHeader, and a plain Write leaves the
// implicit 200 the handler never set.
func Test_responseWriter_StatusTracking(t *testing.T) {
	t.Run("write without WriteHeader", func(t *testing.T) {
		rec := httptest.NewRecorder()
		status := http.StatusOK
		wrapped := WrapResponseWriter(rec, &status)

		_, err := wrapped.Write([]byte("hello"))
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "hello", rec.Body.String())
	})

	t.Run("last WriteHeader wins", func(t *testing.T) {
		rec := httptest.NewRecorder()
		status := http.StatusOK
		wrapped := WrapResponseWriter(rec, &status)

		wrapped.WriteHeader(http.StatusTeapot)
		wrapped.WriteHeader(http.StatusBadGateway) // net/http ignores this; the pointer still follows
		assert.Equal(t, http.StatusBadGateway, status)
	})

	t.Run("headers set through the wrapper reach the original", func(t *testing.T) {
		rec := httptest.NewRecorder()
		status := http.StatusOK
		wrapped := WrapResponseWriter(rec, &status)

		wrapped.Header().Set("X-Res", "v")
		wrapped.WriteHeader(http.StatusNoContent)

		assert.Equal(t, "v", rec.Header().Get("X-Res"))
	})
}

type handlerNameTestHandler struct{}

func (handlerNameTestHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func handlerNameTestFunc(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func TestWrapHandler_ConcreteHandler(t *testing.T) {
	tests := []struct {
		name     string
		handler  http.Handler
		wantName string
	}{
		{name: "function", handler: http.HandlerFunc(handlerNameTestFunc), wantName: "github.com/pinpoint-apm/pinpoint-go-agent/plugin/http.handlerNameTestFunc()"},
		{name: "value", handler: handlerNameTestHandler{}, wantName: "pphttp.handlerNameTestHandler()"},
		{name: "pointer", handler: &handlerNameTestHandler{}, wantName: "*pphttp.handlerNameTestHandler()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantName, HandlerFuncName(tt.handler))

			rec := httptest.NewRecorder()
			WrapHandler(tt.handler).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			assert.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}

func TestHandlerFuncName_Nil(t *testing.T) {
	assert.Equal(t, "<nil>()", HandlerFuncName(nil), "a nil handler must not panic in reflect")
}

// NewServeMux instruments every handler registered on it, so both registration
// forms have to keep routing to the right handler and hand it the tracer.
func TestServeMux_TracesRegisteredHandlers(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name     string
		register func(*serveMux, string, func(http.ResponseWriter, *http.Request))
	}{
		{"Handle", func(m *serveMux, p string, h func(http.ResponseWriter, *http.Request)) {
			m.Handle(p, http.HandlerFunc(h))
		}},
		{"HandleFunc", func(m *serveMux, p string, h func(http.ResponseWriter, *http.Request)) {
			m.HandleFunc(p, h)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := NewServeMux()
			var tracer pinpoint.Tracer
			tt.register(mux, "/hello", func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(http.StatusTeapot)
				_, _ = w.Write([]byte("hello"))
			})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/hello", nil)
			req.Host = "myhost:8080"
			req.RemoteAddr = "10.0.0.1:4242"
			mux.ServeHTTP(rec, req)

			require.NotNil(t, tracer, "the handler did not run")
			assert.True(t, tracer.IsSampled(), "handler received an unsampled tracer")
			assert.Equal(t, http.StatusTeapot, rec.Code)
			assert.Equal(t, "hello", rec.Body.String())

			span := spanOf(t, tracer)
			assert.Equal(t, "/hello", span.RpcName, "the span's RPC name is the request path")
			assert.Equal(t, "myhost:8080", span.EndPoint, "the span's endpoint is the request Host")
			assert.Equal(t, "10.0.0.1", span.RemoteAddr, "the span's remote address is the peer, without its port")
		})
	}
}

// The mux routes on the registered pattern while the span names itself after
// the concrete path, so a wildcard route must not collapse every request into
// one span name.
func TestServeMux_SpanNameIsTheRequestPath(t *testing.T) {
	startAgent(t)

	mux := NewServeMux()
	var tracer pinpoint.Tracer
	mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
	})

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42/profile", nil))

	require.NotNil(t, tracer)
	assert.Equal(t, "/users/42/profile", spanOf(t, tracer).RpcName)
}

// A request the config excludes must produce a noop tracer: the handler still
// runs and answers normally, but nothing is traced.
func TestExcludedRequestsAreNotTraced(t *testing.T) {
	usePluginConfig(t,
		WithHttpServerExcludeUrl([]string{"/health", "/static/**"}),
		WithHttpServerExcludeMethod([]string{"options"}),
	)

	tests := []struct {
		name        string
		method      string
		path        string
		wantSampled bool
	}{
		{name: "an excluded exact url", method: http.MethodGet, path: "/health"},
		{name: "an excluded url pattern", method: http.MethodGet, path: "/static/js/app.js"},
		{name: "an excluded method", method: http.MethodOptions, path: "/api"},
		{name: "excluded matching is case-insensitive on the method", method: "options", path: "/api"},
		{name: "a traced request", method: http.MethodGet, path: "/api", wantSampled: true},
		{name: "the method filter does not exclude other methods", method: http.MethodPost, path: "/api", wantSampled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tracer pinpoint.Tracer
			h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(http.StatusNoContent)
			}))

			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))

			require.NotNil(t, tracer, "the handler must run whether or not the request is traced")
			assert.Equal(t, tt.wantSampled, tracer.IsSampled())
			assert.Equal(t, http.StatusNoContent, rec.Code, "excluding a request must not change the response")
		})
	}
}

// The status the span records comes from the wrapped writer, so a handler that
// never calls WriteHeader has to leave the default 200 in place and still send
// its body.
func TestWrapHandlerFunc_ImplicitStatus(t *testing.T) {
	startAgent(t)

	h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "hello", rec.Body.String())
}

// The status code annotation is what the Pinpoint UI shows, and the configured
// error classes are what turn a span red.
func TestRecordHttpServerResponse(t *testing.T) {
	usePluginConfig(t, WithHttpServerStatusCodeError([]string{"5xx", "302"}))

	tests := []struct {
		status      string
		code        int
		wantFailure bool
	}{
		{status: "200 is not an error", code: http.StatusOK},
		{status: "404 is not configured as an error", code: http.StatusNotFound},
		{status: "500 falls in the configured 5xx class", code: http.StatusInternalServerError, wantFailure: true},
		{status: "302 is configured on its own", code: http.StatusFound, wantFailure: true},
		{status: "301 is not 302", code: http.StatusMovedPermanently},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			var tracer pinpoint.Tracer
			h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				w.WriteHeader(tt.code)
			})
			h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			require.NotNil(t, tracer)
			span := spanOf(t, tracer)
			assert.Equal(t, tt.wantFailure, span.Err != 0,
				"status %d should%s fail the span", tt.code, map[bool]string{true: "", false: " not"}[tt.wantFailure])
			assert.Contains(t, span.annotationInts(pinpoint.AnnotationHttpStatusCode), tt.code,
				"the status code must be annotated on the span")
		})
	}
}

// A recorded response header is read off the writer the handler wrote to, so
// the wrapper has to hand the real header map to the recorder.
func TestWrapHandler_RecordsConfiguredHeaders(t *testing.T) {
	usePluginConfig(t,
		WithHttpServerRecordRequestHeader([]string{"X-Req"}),
		WithHttpServerRecordRespondHeader([]string{"X-Res"}),
		WithHttpServerRecordRequestCookie([]string{"session"}),
	)

	var tracer pinpoint.Tracer
	h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		w.Header().Set("X-Res", "res-value")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Req", "req-value")
	req.Header.Set("X-Ignored", "nope")
	req.AddCookie(&http.Cookie{Name: "session", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "ignored", Value: "nope"})
	h(httptest.NewRecorder(), req)

	require.NotNil(t, tracer)
	annotations := string(tracer.JsonString())
	assert.Contains(t, annotations, "req-value", "the configured request header must be recorded")
	assert.Contains(t, annotations, "res-value", "the configured response header must be recorded")
	assert.Contains(t, annotations, "s1", "the configured cookie must be recorded")
	assert.NotContains(t, annotations, "nope", "headers and cookies that are not configured must be left out")
}

// The wrapper marks the span failed and re-panics; swallowing the panic would
// turn a crash net/http reports into a silent 200.
func TestWrapHandler_PanicPropagates(t *testing.T) {
	startAgent(t)

	var tracer pinpoint.Tracer
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracer = pinpoint.TracerFromRequestContext(r)
		panic("boom")
	}))

	assert.PanicsWithValue(t, "boom", func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}, "the wrapper swallowed the handler panic")

	require.NotNil(t, tracer)
	assert.NotZero(t, spanOf(t, tracer).Err, "a panicking handler must fail the span")
}

// With no agent running the wrapper must be a straight pass-through.
func TestWrapHandler_PassesThroughWhenAgentDisabled(t *testing.T) {
	if pinpoint.GetAgent().Enable() {
		t.Skip("a global agent is still enabled")
	}

	called := false
	h := WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.False(t, pinpoint.TracerFromRequestContext(r).IsSampled(), "a disabled agent produced a sampled tracer")
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hello", nil))

	require.True(t, called, "the handler did not run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// RecordHttpHandlerError is how framework adapters report an error a handler
// returned instead of panicking; the option turns it off.
func TestRecordHttpHandlerError(t *testing.T) {
	for _, tt := range []struct {
		name    string
		record  bool
		wantErr bool
	}{
		{name: "recorded by default", record: true, wantErr: true},
		{name: "suppressed by the option", record: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			usePluginConfig(t, WithHttpServerRecordHandlerError(tt.record))

			var tracer pinpoint.Tracer
			h := WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tracer = pinpoint.TracerFromRequestContext(r)
				RecordHttpHandlerError(tracer, errors.New("handler failed"))
			})
			h(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

			require.NotNil(t, tracer)
			assert.Equal(t, tt.wantErr, spanOf(t, tracer).Err != 0)
		})
	}
}

// A noop tracer reaches RecordHttpHandlerError whenever the url is excluded; it
// must be a no-op rather than a nil dereference.
func TestRecordHttpHandlerError_NoopTracer(t *testing.T) {
	startAgent(t)
	assert.NotPanics(t, func() {
		RecordHttpHandlerError(pinpoint.NoopTracer(), errors.New("handler failed"))
	})
}

// A pattern registered on the mux is collected as a URL statistic; WrapHandler
// has no pattern to report and must not collect one.
func TestCollectUrlStat(t *testing.T) {
	startAgent(t, pinpoint.WithHttpUrlStatEnable(true))

	var tracer pinpoint.Tracer
	assert.NotPanics(t, func() {
		mux := NewServeMux()
		mux.HandleFunc("/users/", func(w http.ResponseWriter, r *http.Request) {
			tracer = pinpoint.TracerFromRequestContext(r)
		})
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users/42", nil))
	})
	require.NotNil(t, tracer)

	// AddMetric with a value of the wrong type must be ignored, not fatal.
	assert.NotPanics(t, func() { tracer.AddMetric(pinpoint.MetricURLStat, "not an entry") })
	assert.NotPanics(t, func() { CollectUrlStat(pinpoint.NoopTracer(), "/users/", http.MethodGet, 200) })
}

// The deprecated wrappers must keep returning the pattern they were given
// along with an instrumented handler.
func TestWrapHandleAndWrapHandleFunc(t *testing.T) {
	startAgent(t)

	sampled := false
	pattern, handler := WrapHandle(pinpoint.GetAgent(), "hello", "/hello",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sampled = pinpoint.TracerFromRequestContext(r).IsSampled()
		}))
	assert.Equal(t, "/hello", pattern, "WrapHandle must return the pattern it was given")
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello", nil))
	assert.True(t, sampled, "WrapHandle's handler received an unsampled tracer")

	sampled = false
	pattern, handlerFunc := WrapHandleFunc(pinpoint.GetAgent(), "hello", "/hello",
		func(w http.ResponseWriter, r *http.Request) {
			sampled = pinpoint.TracerFromRequestContext(r).IsSampled()
		})
	assert.Equal(t, "/hello", pattern, "WrapHandleFunc must return the pattern it was given")
	handlerFunc(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello", nil))
	assert.True(t, sampled, "WrapHandleFunc's handler received an unsampled tracer")
}

// A server tracer continues a transaction the caller started, so the ids it
// extracts from the pinpoint headers have to be the caller's.
func TestNewHttpServerTracer_ContinuesTheCallersTransaction(t *testing.T) {
	startAgent(t)

	client := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	defer client.EndSpan()
	client.NewSpanEvent("call")
	outgoing := httptest.NewRequest(http.MethodGet, "/callee", nil)
	client.Inject(outgoing.Header)
	client.EndSpanEvent()

	server := NewHttpServerTracer(outgoing, "HTTP Server")
	defer server.EndSpan()

	require.True(t, server.IsSampled())
	assert.Equal(t, client.TransactionId().String(), server.TransactionId().String(),
		"the server span must join the caller's transaction")
}

// NewHttpServerTracerWithReader is the entry point adapters without a
// net/http request use; the sampling decision must match the request-based one.
func TestNewHttpServerTracerWithReader(t *testing.T) {
	usePluginConfig(t, WithHttpServerExcludeUrl([]string{"/health"}))

	traced := NewHttpServerTracerWithReader(http.MethodGet, "/api", "HTTP Server", http.Header{})
	defer traced.EndSpan()
	assert.True(t, traced.IsSampled())
	assert.Equal(t, "/api", spanOf(t, traced).RpcName)

	excluded := NewHttpServerTracerWithReader(http.MethodGet, "/health", "HTTP Server", http.Header{})
	defer excluded.EndSpan()
	assert.False(t, excluded.IsSampled(), "an excluded url must produce a noop tracer")
}

// spanJson is the subset of a span JsonString asserts against.
type spanJson struct {
	RpcName     string        `json:"RpcName"`
	EndPoint    string        `json:"EndPoint"`
	RemoteAddr  string        `json:"RemoteAddr"`
	Err         int           `json:"Err"`
	Annotations []interface{} `json:"Annotations"`
}

// annotationInts returns every integer annotated under key. The annotation list
// is untyped JSON - {"key":46,"value":{"Field":{"IntValue":500}}} - so each
// entry is matched on its key and then unwrapped.
func (s spanJson) annotationInts(key int32) []int {
	var values []int
	for _, a := range s.Annotations {
		m, ok := a.(map[string]interface{})
		if !ok {
			continue
		}
		if k, ok := m["key"].(float64); !ok || int32(k) != key {
			continue
		}
		value, _ := m["value"].(map[string]interface{})
		field, _ := value["Field"].(map[string]interface{})
		if n, ok := field["IntValue"].(float64); ok {
			values = append(values, int(n))
		}
	}
	return values
}

func spanOf(t *testing.T, tracer pinpoint.Tracer) spanJson {
	t.Helper()
	var s spanJson
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &s))
	return s
}
