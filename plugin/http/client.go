package pphttp

import (
	"context"
	"maps"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
)

// NewHttpClientTracer starts the client span event for req on tracer and
// injects the distributed tracing headers into req.Header: the entry half of
// what WrapClient and DoClient do around a request. It is for instrumentation
// that cannot wrap the client itself, such as an adapter driving a
// framework's own HTTP client (plugin/beego) or a compile-time hook inside
// (*http.Transport).RoundTrip. The request is modified, so a caller bound by
// the RoundTripper contract copies it first, as WrapClient does. A request
// whose headers already carry a Pinpoint trace gets a noop tracer, so that
// two layers around one request record one event. End it with
// EndHttpClientTracer. Applications use WrapClient or DoClient.
func NewHttpClientTracer(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	return before(tracer, operationName, req)
}

func before(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	if tracer == nil {
		return tracer
	}
	// Nested in an outer instrumented layer - a client wrapped twice, or a
	// proxy forwarding its inbound headers - that already wrote the context:
	// does. The returned noop tracer keeps after() from ending the caller's
	// open event.
	// Read through HttpHeaderReader rather than Header.Get: it looks the
	// pre-canonicalized key up directly, where Get canonicalizes
	// "Pinpoint-TraceID" on every call and allocates doing so.
	if pinpoint.IsInjected(pinpoint.HttpHeaderReader(req.Header)) {
		return pinpoint.NoopTracer()
	}

	// One lookup: each SpanEvent() call takes the event stack lock.
	tracer.NewSpanEvent(operationName)
	se := tracer.SpanEvent()
	// http.NewRequest fills req.Host from the URL; a hand-built request may
	// leave it empty, and an empty host would also drop the Pinpoint-Host
	// header the callee fills acceptorHost from.
	host := req.Host
	if host == "" && req.URL != nil {
		host = req.URL.Host
	}
	se.SetEndPoint(host)
	se.SetDestination(host)
	se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)

	if tracer.IsSampled() {
		a := se.Annotations()
		a.AppendString(pinpoint.AnnotationHttpUrl, ClientUrl(req.Method, req.URL))
		RecordClientHttpRequestHeader(a, header{req.Header})
		RecordClientHttpCookie(a, cookie{req})
	}

	// A hand-built request (not from http.NewRequest) may carry a nil URL or
	// a nil header map; net/http rejects such a request with an error, and
	// the wrapper must not turn that error into a panic.
	if req.Header != nil {
		// HttpHeaderWriter, not the Header itself: Header.Set canonicalizes
		// each key and allocates twice per non-canonical Pinpoint header.
		tracer.Inject(pinpoint.HttpHeaderWriter(req.Header))
	}
	return tracer
}

// EndHttpClientTracer records the response (status code, configured headers)
// or the error on the span event NewHttpClientTracer started and ends it.
func EndHttpClientTracer(tracer pinpoint.Tracer, resp *http.Response, err error) {
	after(tracer, resp, err)
}

func after(tracer pinpoint.Tracer, resp *http.Response, err error) {
	if tracer == nil {
		return
	}
	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetError(err)
	if resp != nil && tracer.IsSampled() {
		a := se.Annotations()
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
func DoClient(doFunc func(req *http.Request) (*http.Response, error), req *http.Request) (resp *http.Response, err error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - matching the other pinpoint agents' disabled state.
	if !pinpoint.GetAgent().Enable() {
		return doFunc(req)
	}

	req = withOwnHeader(req)
	tracer := before(pinpoint.TracerFromRequestContext(req), "http/Client.Do()", req)
	// Deferred so a panicking doFunc still ends the client event; left open,
	// EndSpan would close it with the request's whole duration.
	defer func() { after(tracer, resp, err) }()
	return doFunc(req)
}

// withOwnHeader returns a shallow copy of req with a header map of its own for
// the tracing headers: the caller's request stays as it was, which the
// RoundTripper contract requires, and so does a map other requests share -
// req.WithContext keeps it - where DoClient's writes raced and left the first
// transaction's ids on every later call. The values are shared, since Set
// replaces a key's slice rather than writing into it. A nil map stays nil,
// for net/http to reject as it would have.
func withOwnHeader(req *http.Request) *http.Request {
	clone := *req
	clone.Header = maps.Clone(req.Header)
	return &clone
}

type roundTripper struct {
	original http.RoundTripper
	ctx      context.Context
}

func (r *roundTripper) CloseIdleConnections() {
	if c, ok := r.original.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
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
	//lint:ignore SA1012 nil is "no client context": the request's own is used
	c.Transport = wrapRoundTripper(nil, c.Transport)
	return &c
}

// WrapClientWithContext returns a new *http.Client ready to instrument.
// It is possible to trace only when the given context contains a pinpoint.Tracer.
// The context's tracer is the fallback for a request whose own context carries
// none; a request that does carry one is traced on its own, so the client may
// outlive the request it was built for.
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

func (r *roundTripper) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	// A disabled agent traces nothing and injects nothing - not even the
	// unsampled marker - so skip the request clone and header copy too.
	if !pinpoint.GetAgent().Enable() {
		return r.original.RoundTrip(req)
	}

	// The request's own tracer first: a client built with WrapClientWithContext
	// is a long-lived object, and a request made under another transaction
	// must record on that transaction, not on the one the client was built
	// with. The client's context stands in for a request that carries none.
	tracer := pinpoint.FromContext(req.Context())
	if tracer == pinpoint.NoopTracer() && r.ctx != nil {
		tracer = pinpoint.FromContext(r.ctx)
	}

	req = withOwnHeader(req)
	tracer = before(tracer, "http/Client.Do()", req)
	// Deferred for the same reason as in DoClient: a panicking transport must
	// not leave the client event open.
	defer func() { after(tracer, resp, err) }()
	return r.original.RoundTrip(req)
}
