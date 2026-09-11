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
	if pinpoint.TracerFromRequestContext(req).IsSampled() {
		pinpoint.Log("http").Debugf("request context already carries a sampled tracer (%s): is the pinpoint middleware installed twice?", req.URL.Path)
	}
	tracer = NewHttpServerTracerWithReader(req.Method, req.URL.Path, operation,
		pinpoint.HttpHeaderReader(req.Header))
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

const (
	proxyTypeApp    int32 = 1
	proxyTypeNginx  int32 = 2
	proxyTypeApache int32 = 3
	proxyTypeUser   int32 = 4
)

// IdValidateUtils.validateId for the app= token.
const proxyAppMaxLength = 30

// ProxyRequestHeader.isValid(): DefaultProxyRequestRecorder records a header
// only when its parser marked it valid.
type proxyRequest struct {
	valid        bool
	receivedTime int64
	durationTime int32
	idlePercent  int32
	busyPercent  int32
	app          string
}

// setProxyHeader records one proxy annotation per proxy header the request
// Nginx, App and the configured user headers - and records each valid result,
// so a request that passed through more than one proxy gets one annotation per
// hop rather than only the first match.
func setProxyHeader(a pinpoint.Annotation, h Header) {
	if v := headerFirst(h, proxyHeaderApache); v != "" {
		appendProxyHeader(a, proxyTypeApache, parseProxyApache(v))
	}
	if v := headerFirst(h, proxyHeaderNginx); v != "" {
		appendProxyHeader(a, proxyTypeNginx, parseProxyNginx(v))
	}
	if v := headerFirst(h, proxyHeaderApp); v != "" {
		appendProxyHeader(a, proxyTypeApp, parseProxyApp(v))
	}
	for _, name := range proxyUserHeaderNames() {
		if v := headerFirst(h, name); v != "" {
			appendProxyHeader(a, proxyTypeUser, parseProxyUser(name, v))
		}
	}
}

func appendProxyHeader(a pinpoint.Annotation, code int32, p proxyRequest) {
	// A header whose receive time is missing or not positive is discarded
	// of 0 would draw the proxy hop at the epoch in the timeline.
	if !p.valid || p.receivedTime <= 0 {
		return
	}
	a.AppendLongIntIntByteByteString(pinpoint.AnnotationHttpProxyHeader, p.receivedTime, code, p.durationTime,
		p.idlePercent, p.busyPercent, p.app)
}

// proxyTokens calls fn with the key and value of every "k=v" token of value;
// tokens without '=' are skipped.
func proxyTokens(value string, fn func(k, v string)) {
	for _, tok := range strings.Split(value, " ") {
		if k, v, ok := strings.Cut(tok, "="); ok {
			fn(k, v)
		}
	}
}

// parseProxyApache reads "t=<epoch micros> D=<micros> i=<idle%> b=<busy%>",
// the way ApacheRequestParser does: a duration that is not positive is left
// unset, and a percent outside [0, 100] is left unset.
func parseProxyApache(value string) proxyRequest {
	p := proxyRequest{valid: true}
	proxyTokens(value, func(k, v string) {
		switch k {
		case "t":
			p.receivedTime = proxyDigits(v) / 1000
		case "D":
			p.durationTime = proxyMicros(v)
		case "i":
			p.idlePercent = proxyPercent(v)
		case "b":
			p.busyPercent = proxyPercent(v)
		}
	})
	return p
}

// parseProxyNginx reads "t=<sec.mmm> D=<sec.mmm>": nginx's $msec and
// NginxRequestParser (toReceivedTimeMillis / toDurationTimeMicros) checks
// that shape and treats anything else, including a value with no decimal
// point, as 0. Reading the digits around the point as an integer keeps the
// millisecond exact where a float multiply could round it.
func parseProxyNginx(value string) proxyRequest {
	p := proxyRequest{valid: true}
	proxyTokens(value, func(k, v string) {
		switch k {
		case "t":
			p.receivedTime = nginxMillis(v)
		case "D":
			p.durationTime = nginxDurationMicros(v)
		}
	})
	return p
}

// nginxMillis converts a "sec.mmm" value to an integer count of milliseconds;
// a value without a decimal point, or with other than three digits after it,
// is malformed and yields 0.
func nginxMillis(v string) int64 {
	dot := strings.LastIndexByte(v, '.')
	if dot == -1 || len(v)-dot != 4 {
		return 0
	}
	n, err := strconv.ParseInt(v[:dot]+v[dot+1:], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// nginxDurationMicros converts a "sec.mmm" duration to microseconds. Not
// positive is unset, as NginxRequestParser's `> 0` guard leaves it; the wire
// field is an int32, so a product out of its range is reported as no
// duration rather than a wrapped one (the C++ agent's
// parseProxyNginxDurationMicros does the same).
func nginxDurationMicros(v string) int32 {
	ms := nginxMillis(v)
	if ms <= 0 || ms > math.MaxInt32/1000 {
		return 0
	}
	return int32(ms * 1000)
}

// proxyDigits parses a decimal integer, 0 when it does not parse or does not
// fit int64 - NumberUtils.parseLong(value, 0).
func proxyDigits(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// proxyMicros reads a plain microsecond count; not positive or beyond int32
// is unset.
func proxyMicros(v string) int32 {
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n <= 0 {
		return 0
	}
	return int32(n)
}

// proxyPercent reads an apache idle/busy percent; outside [0, 100] is unset.
func proxyPercent(v string) int32 {
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < 0 || n > 100 {
		return 0
	}
	return int32(n)
}

// does. An app= token that is not a valid id - the [a-zA-Z0-9._-] character
// class, at most proxyAppMaxLength bytes - discards the header.
func parseProxyApp(value string) proxyRequest {
	p := proxyRequest{valid: true}
	proxyTokens(value, func(k, v string) {
		switch k {
		case "t":
			p.receivedTime = proxyDigits(v)
		case "app":
			if !pinpoint.IsValidId(v, proxyAppMaxLength) {
				p.valid = false
				return
			}
			p.app = v
		}
	})
	return p
}

// parseProxyUser reads "t=... D=..." from a header named by
// Http.Server.ProxyUserHeaderNames; the header name is recorded as the app.
// A user header may have been written by any of the three proxies, so
// UserRequestParser infers the format from the value's shape, and so does
// this (userReceivedTimeMillis / userDurationMicros).
func parseProxyUser(name, value string) proxyRequest {
	p := proxyRequest{valid: true, app: name}
	proxyTokens(value, func(k, v string) {
		switch k {
		case "t":
			p.receivedTime = userReceivedTimeMillis(v)
		case "D":
			p.durationTime = userDurationMicros(v)
		}
	})
	return p
}

// userReceivedTimeMillis is UserRequestParser.toReceivedTimeMillis: shorter
// than a millisecond epoch (13 digits) is rejected; 16 or more digits is
// apache's microseconds, converted by dropping the last three digits before
// parsing so the value cannot overflow first; a '.' at index 10 or later is
// nginx's sec.mmm; anything else is an app's milliseconds.
func userReceivedTimeMillis(v string) int64 {
	n := len(v)
	if n < 13 {
		return 0
	}
	if n >= 16 {
		return proxyDigits(v[:n-3])
	}
	if dot := strings.LastIndexByte(v, '.'); dot != -1 {
		if dot < 10 {
			return 0
		}
		return nginxMillis(v)
	}
	return proxyDigits(v)
}

// userDurationMicros is UserRequestParser.toDurationTimeMicros: a value with
// a '.' is nginx's fractional seconds, anything else a microsecond count.
func userDurationMicros(v string) int32 {
	if strings.IndexByte(v, '.') != -1 {
		return nginxDurationMicros(v)
	}
	return proxyMicros(v)
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

	var urlPattern func(*http.Request) string
	if pattern != "" {
		urlPattern = func(*http.Request) string { return pattern }
	}
	return TraceHandler(handler, srvName, HandlerFuncName(handler), urlPattern)
}

// TraceHandler wraps handler in the standard pinpoint HTTP server trace:
// span per request, response status and header recording, URL stat
// collection, and 500-on-panic. It is the single source of the trace
// sequence for the net/http-shaped framework adapters (chi, gorilla, ...).
// urlPattern returns the route pattern for URL stats and is called after the
// handler ran, when the framework has resolved the route; nil disables URL
// stat collection.
func TraceHandler(handler http.Handler, serverName, funcName string, urlPattern func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !pinpoint.GetAgent().Enable() {
			handler.ServeHTTP(w, r)
			return
		}

		status := http.StatusOK
		tracer := NewHttpServerTracer(r, serverName)

		defer tracer.EndSpan()
		defer func() {
			// Route-pattern lookups can be costly per call, so don't pay for
			// them when the stat would be dropped anyway.
			if urlPattern != nil && IsUrlStatEnabled() {
				CollectUrlStat(tracer, urlPattern(r), r.Method, status)
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
	status      *int
	wroteHeader bool
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
	rw := &responseWriter{ResponseWriter: w, status: status}
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
	if w.wroteHeader {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.ResponseWriter.WriteHeader(status)
	if status >= 100 && status <= 199 && status != http.StatusSwitchingProtocols {
		return
	}
	w.wroteHeader = true
	*w.status = status
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func (w *responseWriter) flush(f http.Flusher) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	f.Flush()
}

func (w responseWriterF) Flush()   { w.responseWriter.flush(w.Flusher) }
func (w responseWriterFH) Flush()  { w.responseWriter.flush(w.Flusher) }
func (w responseWriterFP) Flush()  { w.responseWriter.flush(w.Flusher) }
func (w responseWriterFHP) Flush() { w.responseWriter.flush(w.Flusher) }

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
