// Package pphttp instruments Go standard HTTP library.
//
// This package instruments inbound requests handled by a http.ServeMux.
// Use NewServeMux to trace all handlers:
//
//	mux := pphttp.NewServeMux()
//	mux.HandleFunc("/bar", outGoing)
//
// Use WrapHandler or WrapHandlerFunc to select the handlers you want to track:
//
//	http.HandleFunc("/", pphttp.WrapHandlerFunc(index))
//
// This package instruments outbound requests and add distributed tracing headers.
// Use WrapClient, WrapClientWithContext or DoClient.
//
//	client := pphttp.WrapClient(&http.Client{})
//	client.Get(external_url)
//
// or
//
//	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
//	pphttp.DoClient(http.DefaultClient.Do, req)
package pphttp

import (
	"math"
	"net"
	"net/http"
	"net/textproto"
	"reflect"
	"runtime"
	"strconv"
	"strings"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

const defaultServerName = "HTTP Server"

// NewHttpServerTracer returns a pinpoint.Tracer that instruments the request handler for http server.
// The tracer extracts the pinpoint header from the http request header,
// and then creates a span that initiates or continues the transaction.
func NewHttpServerTracer(req *http.Request, operation string) (tracer pinpoint.Tracer) {
	tracer = NewHttpServerTracerWithReader(req.Method, req.URL.Path, operation, req.Header)
	RecordHttpServerRequest(tracer, req)
	return tracer
}

// NewHttpServerTracerWithReader creates an HTTP server tracer without requiring
// a net/http request. Framework adapters can make the sampling decision from
// their native request before converting it for sampled-request annotations.
func NewHttpServerTracerWithReader(method, path, operation string, reader pinpoint.DistributedTracingContextReader) pinpoint.Tracer {
	if isExcludedUrl(path) || isExcludedMethod(method) {
		return pinpoint.NoopTracer()
	}
	return pinpoint.GetAgent().NewSpanTracerWithReader(operation, path, reader)
}

// RecordHttpServerRequest records sampled request attributes on tracer.
func RecordHttpServerRequest(tracer pinpoint.Tracer, req *http.Request) {
	RecordHttpServerRequestWithReader(tracer, req.Host, req.RemoteAddr, header{req.Header}, cookie{req})
}

// RecordHttpServerRequestWithReader records sampled request attributes from
// framework-native request data. Adapters without a net/http request (fasthttp,
// fiber) use this instead of materializing one just to have it read here.
// remoteAddr is the transport-level peer address; X-Forwarded-For and
// X-Real-Ip override it, exactly as in RecordHttpServerRequest.
func RecordHttpServerRequestWithReader(tracer pinpoint.Tracer, host string, remoteAddr string, h Header, c Cookie) {
	if !tracer.IsSampled() {
		return
	}

	span := tracer.Span()
	span.SetEndPoint(host)
	span.SetRemoteAddress(resolveRemoteAddr(h, remoteAddr))

	a := span.Annotations()
	recordServerHttpRequestHeader(a, h)
	recordServerHttpCookie(a, c)
	setProxyHeader(a, h)
}

// headerFirst returns the first value of key, or "" when absent.
func headerFirst(h Header, key string) string {
	// The fasthttp-family adapters synthesize a one-element slice per Values
	// call; take their Get when they have one.
	if g, ok := h.(interface{ Get(string) string }); ok {
		return g.Get(key)
	}
	if v := h.Values(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func resolveRemoteAddr(h Header, remoteAddr string) string {
	if xff := headerFirst(h, "X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}

	if xff := headerFirst(h, "X-Real-Ip"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}

	addr, _, err := net.SplitHostPort(remoteAddr)
	if err == nil {
		return addr
	}

	return remoteAddr
}

// The proxy header names, pre-canonicalized: none of the wire spellings is in
// textproto canonical form, so passing them raw made http.Header.Values take
// the allocating canonicalization slow path on every lookup - twice, for
// headers that are usually absent. fasthttp's Peek normalizes its argument
// itself, so the canonical spelling matches there too.
var (
	proxyHeaderApache = textproto.CanonicalMIMEHeaderKey("Pinpoint-ProxyApache")
	proxyHeaderNginx  = textproto.CanonicalMIMEHeaderKey("Pinpoint-ProxyNginx")
	proxyHeaderApp    = textproto.CanonicalMIMEHeaderKey("Pinpoint-ProxyApp")
)

func setProxyHeader(a pinpoint.Annotation, h Header) {
	var receivedTime int64
	var durationTime, idlePercent, busyPercent int
	var code int32 = 0
	var app = ""

	if xff := headerFirst(h, proxyHeaderApache); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			k, v, ok := strings.Cut(str, "=")
			if !ok {
				continue
			}
			if k == "t" {
				receivedTime, _ = strconv.ParseInt(v, 10, 64)
				receivedTime = receivedTime / 1000
			} else if k == "D" {
				durationTime, _ = strconv.Atoi(v)
			} else if k == "i" {
				idlePercent, _ = strconv.Atoi(v)
			} else if k == "b" {
				busyPercent, _ = strconv.Atoi(v)
			}
		}
		code = 3
	} else if xff := headerFirst(h, proxyHeaderNginx); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			k, v, ok := strings.Cut(str, "=")
			if !ok {
				continue
			}
			if k == "t" {
				tmp, _ := strconv.ParseFloat(v, 64)
				tmp = tmp * 1000
				// Reject non-finite or out-of-range products before the cast:
				// the header is untrusted input and converting such a float64
				// to int64 yields an implementation-defined value. NaN fails
				// both comparisons; ±Inf fails one. The upper bound uses '<'
				// because float64(math.MaxInt64) rounds up to 2^63, which is
				// not representable as int64.
				if tmp >= float64(math.MinInt64) && tmp < float64(math.MaxInt64) {
					receivedTime = int64(tmp)
				}
			} else if k == "D" {
				durationTime, _ = strconv.Atoi(v)
			}
		}
		code = 2
	} else if xff := headerFirst(h, proxyHeaderApp); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			k, v, ok := strings.Cut(str, "=")
			if !ok {
				continue
			}
			if k == "t" {
				receivedTime, _ = strconv.ParseInt(v, 10, 64)
			} else if k == "app" {
				app = v
			}
		}
		code = 1
	}

	if code > 0 {
		a.AppendLongIntIntByteByteString(pinpoint.AnnotationHttpProxyHeader, receivedTime, code, int32(durationTime),
			int32(idlePercent), int32(busyPercent), app)
	}
}

// RecordHttpServerResponse records http status and response header to span.
func RecordHttpServerResponse(tracer pinpoint.Tracer, status int, h http.Header) {
	RecordHttpServerResponseWithReader(tracer, status, header{h})
}

// RecordHttpServerResponseWithReader is RecordHttpServerResponse for adapters
// whose native response header is not an http.Header: the header is read only
// when a response-header recorder is configured, so passing a reader avoids
// copying every header into a map that the default noop recorder ignores.
func RecordHttpServerResponseWithReader(tracer pinpoint.Tracer, status int, h Header) {
	if tracer.IsSampled() {
		span := tracer.Span()
		recordServerHttpStatus(span, status)
		recordServerHttpResponseHeader(span.Annotations(), h)
	}
}

func wrapHandler(pattern string, handler http.Handler, serverName ...string) http.Handler {
	var srvName string
	if len(serverName) > 0 {
		srvName = serverName[0]
	} else {
		srvName = defaultServerName
	}
	funcName := HandlerFuncName(handler)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !pinpoint.GetAgent().Enable() {
			handler.ServeHTTP(w, r)
			return
		}

		status := http.StatusOK
		tracer := NewHttpServerTracer(r, srvName)

		defer tracer.EndSpan()
		defer func() {
			if pattern != "" {
				CollectUrlStat(tracer, pattern, r.Method, status)
			}
			RecordHttpServerResponse(tracer, status, w.Header())
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(funcName).EndSpanEvent()

		w = WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)
		handler.ServeHTTP(w, r)
	})
}

// WrapHandler wraps the given http handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler http.Handler, serverName ...string) http.Handler {
	return wrapHandler("", handler, serverName...)
}

// WrapHandlerFunc wraps the given http handler function and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandlerFunc(handler func(http.ResponseWriter, *http.Request), serverName ...string) func(http.ResponseWriter, *http.Request) {
	h := wrapHandler("", http.HandlerFunc(handler), serverName...)
	return func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}

// WrapHandle is deprecated. Use WrapHandler.
func WrapHandle(agent pinpoint.Agent, handlerName string, pattern string, handler http.Handler) (string, http.Handler) {
	return pattern, WrapHandler(handler)
}

// WrapHandleFunc is deprecated. Use WrapHandlerFunc.
func WrapHandleFunc(agent pinpoint.Agent, handlerName string, pattern string, handler func(http.ResponseWriter, *http.Request)) (string, func(http.ResponseWriter, *http.Request)) {
	p, h := WrapHandle(agent, handlerName, pattern, http.HandlerFunc(handler))
	return p, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}

type responseWriter struct {
	http.ResponseWriter
	status *int
}

// Go has no conditional interface implementation, so keeping the underlying
// writer's optional interfaces reachable takes one wrapper type per
// combination. A single type implementing all three would make type
// assertions succeed on writers that do not support them, breaking the
// feature detection handlers rely on: SSE flushing (http.Flusher) and
// WebSocket upgrades (http.Hijacker). io.ReaderFrom is deliberately left
// out - preserving it would double the combinations and it only costs the
// sendfile fast path in io.Copy(w, f); add it here if that ever matters.
type (
	responseWriterF struct {
		*responseWriter
		http.Flusher
	}
	responseWriterH struct {
		*responseWriter
		http.Hijacker
	}
	responseWriterP struct {
		*responseWriter
		http.Pusher
	}
	responseWriterFH struct {
		*responseWriter
		http.Flusher
		http.Hijacker
	}
	responseWriterFP struct {
		*responseWriter
		http.Flusher
		http.Pusher
	}
	responseWriterHP struct {
		*responseWriter
		http.Hijacker
		http.Pusher
	}
	responseWriterFHP struct {
		*responseWriter
		http.Flusher
		http.Hijacker
		http.Pusher
	}
)

// WrapResponseWriter records the response status while preserving exactly the
// optional HTTP interfaces implemented by w.
func WrapResponseWriter(w http.ResponseWriter, status *int) http.ResponseWriter {
	rw := &responseWriter{w, status}
	f, canFlush := w.(http.Flusher)
	h, canHijack := w.(http.Hijacker)
	p, canPush := w.(http.Pusher)

	switch {
	case canFlush && canHijack && canPush:
		return responseWriterFHP{rw, f, h, p}
	case canFlush && canHijack:
		return responseWriterFH{rw, f, h}
	case canFlush && canPush:
		return responseWriterFP{rw, f, p}
	case canHijack && canPush:
		return responseWriterHP{rw, h, p}
	case canFlush:
		return responseWriterF{rw, f}
	case canHijack:
		return responseWriterH{rw, h}
	case canPush:
		return responseWriterP{rw, p}
	default:
		return rw
	}
}

func (w *responseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	*w.status = status
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type serveMux struct {
	*http.ServeMux
}

// NewServeMux wraps http.NewServeMux and returns a http.ServeMux ready to instrument.
func NewServeMux() *serveMux {
	return &serveMux{
		ServeMux: http.NewServeMux(),
	}
}

// Handle registers the handler for the given pattern.
// The handler is wrapped by WrapHandler.
func (mux *serveMux) Handle(pattern string, handler http.Handler) {
	mux.ServeMux.Handle(pattern, wrapHandler(pattern, handler))
}

// HandleFunc registers the handler function for the given pattern.
// The handler is wrapped by WrapHandlerFunc.
func (mux *serveMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	h := wrapHandler(pattern, http.HandlerFunc(handler))
	mux.ServeMux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) })
}

// HandlerFuncName returns the handler's function or concrete type name.
func HandlerFuncName(f interface{}) string {
	v := reflect.ValueOf(f)
	if v.Kind() == reflect.Func {
		if fn := runtime.FuncForPC(v.Pointer()); fn != nil {
			return fn.Name() + "()"
		}
	}
	if !v.IsValid() { // a nil handler: reflect.Value.Type would panic
		return "<nil>()"
	}
	return v.Type().String() + "()"
}

// CollectUrlStat collects HTTP URL statistics.
func CollectUrlStat(tracer pinpoint.Tracer, url string, method string, status int) {
	// URL stats are off by default and the consumers drop the entry when
	// disabled, so don't allocate one per request just to have it discarded.
	if !httpCfg().urlStatEnabled {
		return
	}
	tracer.AddMetric(pinpoint.MetricURLStat, &pinpoint.UrlStatEntry{Url: url, Method: method, Status: status})
}
