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
	"bytes"
	"context"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/pinpoint-apm/pinpoint-go-agent/plugin/http"
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
		requestHeader := RequestHeader{&ctx.Request.Header}
		status := http.StatusOK
		tracer := pphttp.NewHttpServerTracerWithReader(method, string(ctx.Path()), serverName, requestHeader)
		// Record straight from the fasthttp request: converting it to a
		// net/http request (fasthttpadaptor.ConvertRequest) materialized the
		// full header map, parsed the URL and buffered the body per sampled
		// request, only for values the default noop recorders never read.
		// The sampling check keeps the host copy and remote-addr formatting
		// off the unsampled path; the callee would discard them.
		if tracer.IsSampled() {
			pphttp.RecordHttpServerRequestWithReader(tracer, string(ctx.Host()), ctx.RemoteAddr().String(),
				requestHeader, Cookie{&ctx.Request.Header})
		}

		defer tracer.EndSpan()
		defer func() {
			if urlPattern != "" {
				pphttp.CollectUrlStat(tracer, urlPattern, method, status)
			}
			recordResponse(tracer, ctx, status)
		}()
		defer func() {
			if e := recover(); e != nil {
				status = http.StatusInternalServerError
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		// Derive from the RequestCtx itself - it is a context.Context - so
		// deadlines and values fasthttp carries stay visible downstream.
		ctx.SetUserValue(CtxKey, pinpoint.NewContext(ctx, tracer))
		handler(ctx)
		pphttp.RecordHttpHandlerError(tracer, ctx.Err())

		status = ctx.Response.StatusCode()
	}
}

func recordResponse(tracer pinpoint.Tracer, c *fasthttp.RequestCtx, status int) {
	pphttp.RecordHttpServerResponseWithReader(tracer, status, ResponseHeader{&c.Response.Header})
}

type distributedTracingContextWriterMD struct {
	Header *fasthttp.RequestHeader
}

func (w *distributedTracingContextWriterMD) Set(key string, value string) {
	w.Header.Set(key, value)
}

func before(tracer pinpoint.Tracer, operationName string, req *fasthttp.Request) {
	tracer.NewSpanEvent(operationName)
	se := tracer.SpanEvent()
	se.SetEndPoint(string(req.Host()))
	se.SetDestination(string(req.Host()))
	se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)

	if tracer.IsSampled() {
		var b bytes.Buffer
		b.WriteString(string(req.Header.Method()))
		b.WriteString(" ")
		b.WriteString(req.URI().String())

		a := se.Annotations()
		a.AppendString(pinpoint.AnnotationHttpUrl, b.String())
		pphttp.RecordClientHttpRequestHeader(a, RequestHeader{&req.Header})
		pphttp.RecordClientHttpCookie(a, Cookie{&req.Header})
	}

	wr := &distributedTracingContextWriterMD{&req.Header}
	tracer.Inject(wr)
}

func after(tracer pinpoint.Tracer, resp *fasthttp.Response, err error) {
	se := tracer.SpanEvent()
	se.SetError(err)
	if resp != nil && tracer.IsSampled() {
		a := se.Annotations()
		a.AppendInt(pinpoint.AnnotationHttpStatusCode, int32(resp.StatusCode()))
		pphttp.RecordClientHttpResponseHeader(a, ResponseHeader{&resp.Header})
	}
	tracer.EndSpanEvent()
}

// RequestHeader adapts a *fasthttp.RequestHeader to the pphttp.Header
// interface. Exported for the fasthttp-family plugins (fiber, fiberv3) so
// they don't each carry their own copy of the same adapter.
type RequestHeader struct {
	Hdr *fasthttp.RequestHeader
}

func (h RequestHeader) Get(key string) string {
	return string(h.Hdr.Peek(key))
}

func (h RequestHeader) Values(key string) []string {
	return []string{string(h.Hdr.Peek(key))}
}

func (h RequestHeader) VisitAll(f func(name string, values []string)) {
	h.Hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

// ResponseHeader adapts a *fasthttp.ResponseHeader to pphttp.Header.
type ResponseHeader struct {
	Hdr *fasthttp.ResponseHeader
}

func (h ResponseHeader) Values(key string) []string {
	return []string{string(h.Hdr.Peek(key))}
}

func (h ResponseHeader) VisitAll(f func(name string, values []string)) {
	h.Hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

// Cookie adapts the cookies of a *fasthttp.RequestHeader to pphttp.Cookie.
type Cookie struct {
	Hdr *fasthttp.RequestHeader
}

func (c Cookie) VisitAll(f func(name string, value string)) {
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
