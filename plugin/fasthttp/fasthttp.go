// Package ppfasthttp instruments the valyala/fasthttp package (https://github.com/valyala/fasthttp).
//
// This package instruments inbound requests handled by a fasthttp instance.
// Use WrapHandler to select the handlers you want to track:
//
//	fasthttp.ListenAndServe(":9000", func(ctx *fasthttp.RequestCtx) {
//	  path := string(ctx.Path())
//	  if strings.HasPrefix(path, "/foo") {
//	    ppfasthttp.WrapHandler(fooHandler, "/foo")(ctx)
//	  } else if strings.HasPrefix(path, "/bar") {
//	    ppfasthttp.WrapHandler(barHandler, "/bar")(ctx)
//	  }
//	})
//
// WrapHandler sets the pinpoint.Tracer as a user value of fasthttp handler's context.
// By using the ppfasthttp.CtxKey, this tracer can be obtained.
//
//	func requestHandler(ctx *fasthttp.RequestCtx) {
//	    tracer := pinpoint.FromContext(ctx.UserValue(ppfasthttp.CtxKey).(context.Context))
//
// This package instruments outbound requests and add distributed tracing headers.
// Use DoClient.
//
//	err := ppfasthttp.DoClient(func() error {
//		return hc.Do(req, resp)
//	}, ctx, req, resp)
//
// It is necessary to pass the context containing the pinpoint.Tracer to DoClient.
package ppfasthttp

import (
	"context"

	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/valyala/fasthttp"
)

const serverName = "FastHttp Server"
const CtxKey = "pinpoint"

// WrapHandler wraps the given http request handler.
func WrapHandler(handler fasthttp.RequestHandler, pattern ...string) fasthttp.RequestHandler {
	handlerName := pphttp.HandlerFuncName(handler)
	urlPattern := ""
	if len(pattern) > 0 {
		urlPattern = pattern[0]
	}

	return func(ctx *fasthttp.RequestCtx) {
		if !pinpoint.GetAgent().Enable() {
			handler(ctx)
			return
		}

		method := string(ctx.Method())
		tracer := serverTracer(ctx, method)
		// Not derived from the RequestCtx, although it is a context.Context:
		// fasthttp reuses it for the next request once the handler returns,
		// and its Value reads that request's user values, so a goroutine the
		// handler started with this context read another request's values and
		// raced their writes. It has no deadline, and its Done closes only at
		// server shutdown, so nothing is lost.
		ctx.SetUserValue(CtxKey, pinpoint.NewContext(context.Background(), tracer))
		pphttp.TraceSpan(tracer, handlerName, func() int {
			handler(ctx)
			// No handler error is recorded: a fasthttp handler returns none, and
			// RequestCtx.Err() is not one either - it turns non-nil only once
			// Server.Shutdown has begun, which marked every request completing
			// during a graceful stop as failed. The status carries the failure.
			return ctx.Response.StatusCode()
		}, func(status int) {
			if urlPattern != "" {
				pphttp.CollectUrlStat(tracer, urlPattern, method, status)
			}
			RecordServerResponse(tracer, ctx, status)
		})
	}
}

// serverTracer starts the request's span, or hands back a nested view of the
// tracer the request's user value already carries (pinpoint.NestedTracer) - a
// handler wrapped twice, a router adapter's route inside a manual WrapHandler
// - so one request makes one span, as pphttp.NewHttpServerTracer does: this
// layer's event goes on the existing span and its EndSpan is ignored.
func serverTracer(ctx *fasthttp.RequestCtx, method string) pinpoint.Tracer {
	if uc, ok := ctx.UserValue(CtxKey).(context.Context); ok {
		if existing := pinpoint.FromContext(uc); existing != pinpoint.NoopTracer() {
			return pinpoint.NestedTracer(existing)
		}
	}
	return NewServerTracer(ctx, method, serverName)
}

// NewServerTracer starts the span of a request served by fasthttp and records
// the request on it. It is for the plugins of frameworks built on fasthttp
// (ppfiber, ppfiberv3), which look for a tracer to nest under in their own
// context first.
func NewServerTracer(ctx *fasthttp.RequestCtx, method string, serverName string) pinpoint.Tracer {
	tracer := pphttp.NewHttpServerTracerWithReader(method, string(ctx.Path()), serverName,
		headerReader{&ctx.Request.Header})
	// Record straight from the fasthttp request: converting it to a
	// net/http request (fasthttpadaptor.ConvertRequest) materialized the
	// full header map, parsed the URL and buffered the body per sampled
	// request, only for values the default noop recorders never read.
	// The sampling check keeps the host copy and remote-addr formatting
	// off the unsampled path; the callee would discard them.
	if tracer.IsSampled() {
		pphttp.RecordHttpServerRequestWithReader(tracer, string(ctx.Host()), ctx.RemoteAddr().String(),
			requestHeader{&ctx.Request.Header}, cookie{&ctx.Request.Header})
	}
	return tracer
}

// RecordServerResponse records the status and the configured response headers
// of a request NewServerTracer started.
func RecordServerResponse(tracer pinpoint.Tracer, ctx *fasthttp.RequestCtx, status int) {
	pphttp.RecordHttpServerResponseWithReader(tracer, status, responseHeader{&ctx.Response.Header})
}

func before(tracer pinpoint.Tracer, operationName string, req *fasthttp.Request) {
	tracer.NewSpanEvent(operationName)
	se := tracer.SpanEvent()
	se.SetEndPoint(string(req.Host()))
	se.SetDestination(string(req.Host()))
	se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)

	if tracer.IsSampled() {
		a := se.Annotations()
		a.AppendString(pinpoint.AnnotationHttpUrl, pphttp.ClientUrlString(string(req.Header.Method()), req.URI().String()))
		pphttp.RecordClientHttpRequestHeader(a, requestHeader{&req.Header})
		pphttp.RecordClientHttpCookie(a, cookie{&req.Header})
	}

	tracer.Inject(&req.Header)
}

func after(tracer pinpoint.Tracer, resp *fasthttp.Response, err error) {
	se := tracer.SpanEvent()
	se.SetError(err)
	if resp != nil && tracer.IsSampled() {
		a := se.Annotations()
		a.AppendInt(pinpoint.AnnotationHttpStatusCode, int32(resp.StatusCode()))
		pphttp.RecordClientHttpResponseHeader(a, responseHeader{&resp.Header})
	}
	tracer.EndSpanEvent()
}

// requestHeader adapts a *fasthttp.RequestHeader to the pphttp.Header
// interface.
type requestHeader struct {
	Hdr *fasthttp.RequestHeader
}

func (h requestHeader) Get(key string) string {
	return string(h.Hdr.Peek(key))
}

// headerReader adapts a *fasthttp.RequestHeader to the tracing carrier
// pinpoint.DistributedTracingContextReader, whose Get reports whether the
// header was carried at all. requestHeader above keeps its plain Get: the
// pphttp recorder reads it through an interface{ Get(string) string }
// assertion, which a two-result Get would silently stop matching.
type headerReader struct {
	Hdr *fasthttp.RequestHeader
}

// Get reports a header carried with an empty value as present, so a hop whose
// Pinpoint-SpanID a proxy blanked instead of dropping still continues the
// trace.
//
// Presence has to come from PeekAll, not from Peek: fasthttp stores a value
// set to "" as an empty slice or as a nil one depending on whether the
// header's slot was reused, so a nil check on Peek reports the same header as
// present on one request and absent on the next. PeekAll returns one entry per
// stored header either way, in a slice the RequestHeader keeps and reuses
// across calls, so the lookup allocates nothing once that slice has grown.
func (h headerReader) Get(key string) (string, bool) {
	if v := h.Hdr.PeekAll(key); len(v) > 0 {
		return string(v[0]), true
	}
	return "", false
}

// Values reports a header the request does not carry as absent, the way
// net/http's Header.Values does. Returning a one-element slice holding the
// empty string instead made every configured-but-missing header look present
// to the recorder, which annotated it with an empty value on every request.
func (h requestHeader) Values(key string) []string {
	if v := h.Hdr.Peek(key); v != nil {
		return []string{string(v)}
	}
	return nil
}

func (h requestHeader) VisitAll(f func(name string, values []string)) {
	h.Hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

// responseHeader adapts a *fasthttp.ResponseHeader to pphttp.Header.
type responseHeader struct {
	Hdr *fasthttp.ResponseHeader
}

// Values reports an absent header as absent, as requestHeader.Values does.
func (h responseHeader) Values(key string) []string {
	if v := h.Hdr.Peek(key); v != nil {
		return []string{string(v)}
	}
	return nil
}

func (h responseHeader) VisitAll(f func(name string, values []string)) {
	h.Hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

// cookie adapts the cookies of a *fasthttp.RequestHeader to pphttp.Cookie.
type cookie struct {
	Hdr *fasthttp.RequestHeader
}

func (c cookie) VisitAll(f func(name string, value string)) {
	c.Hdr.VisitAllCookie(func(key, value []byte) {
		f(string(key), string(value))
	})
}

// DoClient instruments outbound requests and add distributed tracing headers.
func DoClient(doFunc func() error, ctx context.Context, req *fasthttp.Request, res *fasthttp.Response) (err error) {
	if !pinpoint.GetAgent().Enable() {
		return doFunc()
	}

	tracer := pinpoint.FromContext(ctx)
	before(tracer, "fasthttp/Client.Do()", req)
	// Deferred so a panicking doFunc still closes the span event.
	defer func() { after(tracer, res, err) }()
	err = doFunc()
	return err
}
