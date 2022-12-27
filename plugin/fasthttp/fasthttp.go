// Package ppfasthttp instruments the valyala/fasthttp package (https://github.com/valyala/fasthttp).
//
// This package instruments inbound requests handled by a fasthttp instance.
// Use WrapHandler to select the handlers you want to track:
//
//	fasthttp.ListenAndServe(":9000", ppfasthttp.WrapHandler(requestHandler))
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
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

const serverName = "FastHttp Server"
const CtxKey = "pinpoint"

// WrapHandler wraps the given http request handler.
func WrapHandler(handler fasthttp.RequestHandler) fasthttp.RequestHandler {
	handlerName := pphttp.HandlerFuncName(handler)

	return func(ctx *fasthttp.RequestCtx) {
		if !pinpoint.GetAgent().Enable() {
			handler(ctx)
			return
		}

		req := new(http.Request)
		if err := fasthttpadaptor.ConvertRequest(ctx, req, true); err != nil {
			handler(ctx)
			return
		}

		tracer := pphttp.NewHttpServerTracer(req, serverName)
		defer tracer.EndSpan()

		if !tracer.IsSampled() {
			handler(ctx)
			return
		}
		defer func() {
			if e := recover(); e != nil {
				status := http.StatusInternalServerError
				recordResponse(tracer, ctx, status)
				panic(e)
			}
		}()

		defer tracer.NewSpanEvent(handlerName).EndSpanEvent()

		ctx.SetUserValue(CtxKey, pinpoint.NewContext(context.Background(), tracer))
		handler(ctx)
		tracer.Span().SetError(ctx.Err())

		recordResponse(tracer, ctx, ctx.Response.StatusCode())
		//ctx.Path()
	}
}

func recordResponse(tracer pinpoint.Tracer, c *fasthttp.RequestCtx, status int) {
	if tracer.IsSampled() {
		h := make(http.Header)
		c.Response.Header.VisitAll(func(k, v []byte) {
			h.Set(string(k), string(v))
		})
		pphttp.RecordHttpServerResponse(tracer, status, h)
	}
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
		pphttp.RecordClientHttpRequestHeader(a, reqHeader{&req.Header})
		pphttp.RecordClientHttpCookie(a, cookie{&req.Header})
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
		pphttp.RecordClientHttpResponseHeader(a, resHeader{&resp.Header})
	}
	tracer.EndSpanEvent()
}

type reqHeader struct {
	hdr *fasthttp.RequestHeader
}

func (h reqHeader) Values(key string) []string {
	return []string{string(h.hdr.Peek(key))}
}

func (h reqHeader) VisitAll(f func(name string, values []string)) {
	h.hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

type resHeader struct {
	hdr *fasthttp.ResponseHeader
}

func (h resHeader) Values(key string) []string {
	return []string{string(h.hdr.Peek(key))}
}

func (h resHeader) VisitAll(f func(name string, values []string)) {
	h.hdr.VisitAll(func(key, value []byte) {
		f(string(key), []string{string(value)})
	})
}

type cookie struct {
	hdr *fasthttp.RequestHeader
}

func (c cookie) VisitAll(f func(name string, value string)) {
	c.hdr.VisitAllCookie(func(key, value []byte) {
		f(string(key), string(value))
	})
}

// DoClient instruments outbound requests and add distributed tracing headers.
func DoClient(doFunc func() error, ctx context.Context, req *fasthttp.Request, res *fasthttp.Response) error {
	tracer := pinpoint.FromContext(ctx)
	before(tracer, "fasthttp/Client.Do()", req)
	err := doFunc()
	after(tracer, res, err)
	return err
}
