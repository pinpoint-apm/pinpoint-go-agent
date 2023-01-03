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
	"bytes"
	"net"
	"net/http"
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
	if isExcludedUrl(req.URL.Path) || isExcludedMethod(req.Method) {
		return pinpoint.NoopTracer()
	} else {
		if tracer = pinpoint.GetAgent().NewSpanTracerWithReader(operation, req.URL.Path, req.Header); tracer.IsSampled() {
			span := tracer.Span()
			span.SetEndPoint(req.Host)
			span.SetRemoteAddress(getRemoteAddr(req))

			a := span.Annotations()
			recordServerHttpRequestHeader(a, header{req.Header})
			recordServerHttpCookie(a, cookie{req.Cookies()})
			setProxyHeader(a, req)
		}
		return tracer
	}
}

func getRemoteAddr(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	if xff := r.Header.Get("X-Real-Ip"); xff != "" {
		if parts := strings.Split(xff, ","); len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}

	addr, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return addr
	}

	return r.RemoteAddr
}

func setProxyHeader(a pinpoint.Annotation, r *http.Request) {
	var receivedTime int64
	var durationTime, idlePercent, busyPercent int
	var code int32 = 0
	var app = ""

	if xff := r.Header.Get("Pinpoint-ProxyApache"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				receivedTime, _ = strconv.ParseInt(e[1], 10, 64)
				receivedTime = receivedTime / 1000
			} else if e[0] == "D" {
				durationTime, _ = strconv.Atoi(e[1])
			} else if e[0] == "i" {
				idlePercent, _ = strconv.Atoi(e[1])
			} else if e[0] == "b" {
				busyPercent, _ = strconv.Atoi(e[1])
			}
		}
		code = 3
	} else if xff := r.Header.Get("Pinpoint-ProxyNginx"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				tmp, _ := strconv.ParseFloat(e[1], 64)
				tmp = tmp * 1000
				receivedTime = int64(tmp)
			} else if e[0] == "D" {
				durationTime, _ = strconv.Atoi(e[1])
			}
		}
		code = 2
	} else if xff := r.Header.Get("Pinpoint-ProxyApp"); xff != "" {
		parts := strings.Split(xff, " ")
		for _, str := range parts {
			e := strings.Split(str, "=")
			if e[0] == "t" {
				receivedTime, _ = strconv.ParseInt(e[1], 10, 64)
			} else if e[0] == "app" {
				app = e[1]
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
	if tracer.IsSampled() {
		span := tracer.Span()
		recordServerHttpStatus(span, status)
		recordServerHttpResponseHeader(span.Annotations(), header{h})
	}
}

// WrapHandler wraps the given http handler and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandler(handler http.Handler, serverName ...string) http.Handler {
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
			tracer.CollectUrlStat(r.URL.Path, status)
		}()
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				RecordHttpServerResponse(tracer, status, w.Header())
				panic(e)
			}
		}()

		tracer.NewSpanEvent(funcName)
		defer tracer.EndSpanEvent()

		w = WrapResponseWriter(w, &status)
		r = pinpoint.RequestWithTracerContext(r, tracer)

		handler.ServeHTTP(w, r)
		RecordHttpServerResponse(tracer, status, w.Header())
	})
}

// WrapHandlerFunc wraps the given http handler function and adds the pinpoint.Tracer to the request's context.
// By using the pinpoint.FromContext function, this tracer can be obtained.
func WrapHandlerFunc(handler func(http.ResponseWriter, *http.Request), serverName ...string) func(http.ResponseWriter, *http.Request) {
	h := WrapHandler(http.HandlerFunc(handler), serverName...)
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

func WrapResponseWriter(w http.ResponseWriter, status *int) *responseWriter {
	return &responseWriter{w, status}
}

func (w *responseWriter) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	*w.status = status
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
	mux.ServeMux.Handle(pattern, WrapHandler(handler))
}

// HandleFunc registers the handler function for the given pattern.
// The handler is wrapped by WrapHandlerFunc.
func (mux *serveMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	mux.ServeMux.HandleFunc(pattern, WrapHandlerFunc(handler))
}

// HandlerFuncName returns the name of handler function.
func HandlerFuncName(f interface{}) string {
	var buf bytes.Buffer
	buf.WriteString(runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name())
	buf.WriteString("()")
	return buf.String()
}
