package pphttp

import (
	"bytes"
	"context"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// NewHttpClientTracer is deprecated. Use WrapClient or DoClient.
func NewHttpClientTracer(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	return before(tracer, operationName, req)
}

func before(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	if tracer == nil {
		return tracer
	}

	tracer.NewSpanEvent(operationName)
	tracer.SpanEvent().SetEndPoint(req.Host)
	tracer.SpanEvent().SetDestination(req.Host)
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoHttpClient)

	if tracer.IsSampled() {
		var b bytes.Buffer
		b.WriteString(req.Method)
		if req.URL != nil {
			b.WriteString(" ")
			b.WriteString(req.URL.String())
		}
		tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationHttpUrl, b.String())

		a := tracer.SpanEvent().Annotations()
		RecordClientHttpRequestHeader(a, header{req.Header})
		RecordClientHttpCookie(a, cookie{req})
	}

	// A hand-built request (not from http.NewRequest) may carry a nil URL or
	// a nil header map; net/http rejects such a request with an error, and
	// the wrapper must not turn that error into a panic.
	if req.Header != nil {
		tracer.Inject(req.Header)
	}
	return tracer
}

// EndHttpClientTracer is deprecated.
func EndHttpClientTracer(tracer pinpoint.Tracer, resp *http.Response, err error) {
	after(tracer, resp, err)
}

func after(tracer pinpoint.Tracer, resp *http.Response, err error) {
	if tracer == nil {
		return
	}
	defer tracer.EndSpanEvent()

	tracer.SpanEvent().SetError(err)
	if resp != nil && tracer.IsSampled() {
		a := tracer.SpanEvent().Annotations()
		a.AppendInt(pinpoint.AnnotationHttpStatusCode, int32(resp.StatusCode))
		RecordClientHttpResponseHeader(a, header{resp.Header})
	}
}

type header struct {
	header http.Header
}

func (h header) Values(key string) []string {
	return h.header.Values(key)
}

func (h header) VisitAll(f func(name string, values []string)) {
	for name, values := range h.header {
		f(name, values)
	}
}

// cookie parses the request's Cookie header lazily, inside VisitAll: the
// recorder is a noop unless cookie recording is configured, and eagerly calling
// req.Cookies() paid a full parse plus allocations per sampled request just to
// discard the result.
type cookie struct {
	req *http.Request
}

func (c cookie) VisitAll(f func(name string, value string)) {
	for _, ck := range c.req.Cookies() {
		f(ck.Name, ck.Value)
	}
}

// DoClient instruments and executes a given doFunc.
// It is necessary to pass the context containing the pinpoint.Tracer to the http.Request.
//
//	req, _ := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer), "GET", url, nil)
//	pphttp.DoClient(http.DefaultClient.Do, req)
func DoClient(doFunc func(req *http.Request) (*http.Response, error), req *http.Request) (*http.Response, error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - matching the other pinpoint agents' disabled state.
	if !pinpoint.GetAgent().Enable() {
		return doFunc(req)
	}

	tracer := before(pinpoint.TracerFromRequestContext(req), "http/Client.Do()", req)
	resp, err := doFunc(req)
	after(tracer, resp, err)

	return resp, err
}

type roundTripper struct {
	original http.RoundTripper
	ctx      context.Context
}

// WrapClient returns a new *http.Client ready to instrument.
// It is necessary to pass the context containing the pinpoint.Tracer to the http.Request.
//
//	req, _ := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer), "GET", url, nil)
//	client := pphttp.WrapClient(&http.Client{})
//	client.Do(req)
func WrapClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}

	c := *client
	c.Transport = wrapRoundTripper(nil, c.Transport)
	return &c
}

// WrapClientWithContext returns a new *http.Client ready to instrument.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
//
//	client := pphttp.WrapClientWithContext(pinpoint.NewContext(context.Background(), tracer), &http.Client{})
//	client.Get(external_url)
func WrapClientWithContext(ctx context.Context, client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}

	c := *client
	c.Transport = wrapRoundTripper(ctx, c.Transport)
	return &c
}

func wrapRoundTripper(ctx context.Context, original http.RoundTripper) http.RoundTripper {
	if original == nil {
		original = http.DefaultTransport
	}

	return &roundTripper{
		original: original,
		ctx:      ctx,
	}
}

func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so skip the request clone and header copy too.
	if !pinpoint.GetAgent().Enable() {
		return r.original.RoundTrip(req)
	}

	var tracer pinpoint.Tracer

	if r.ctx != nil {
		tracer = pinpoint.FromContext(r.ctx)
	} else {
		tracer = pinpoint.FromContext(req.Context())
	}

	// By the specification of http.RoundTripper, it requires that the given Request is not changed.
	// We make a copy of the Request because pinpoint headers need to be added.
	clone := *req
	clone.Header = make(http.Header, len(req.Header))
	for k, v := range req.Header {
		clone.Header[k] = v
	}
	req = &clone

	tracer = before(tracer, "http/Client.Do()", req)
	resp, err := r.original.RoundTrip(req)
	after(tracer, resp, err)

	return resp, err
}
