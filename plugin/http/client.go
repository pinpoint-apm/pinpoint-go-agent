package http

import (
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
)

func NewHttpClientTracer(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	tracer.NewSpanEvent(operationName)

	tracer.SpanEvent().SetEndPoint(req.Host)
	tracer.SpanEvent().SetDestination(req.Host)
	tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoHttpClient)
	tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationHttpUrl, req.URL.String())
	tracer.Inject(req.Header)

	return tracer
}

func EndHttpClientTracer(tracer pinpoint.Tracer, resp *http.Response, err error) {
	tracer.SpanEvent().SetError(err)
	if resp != nil {
		tracer.SpanEvent().Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(resp.StatusCode))
		tracer.Span().SetHttpStatusCode(resp.StatusCode)
	}
	tracer.EndSpanEvent()
}

type roundTripper struct {
	original http.RoundTripper
}

func WrapClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}

	c := *client
	c.Transport = wrapRoundTripper(c.Transport)
	return &c
}

func wrapRoundTripper(original http.RoundTripper) http.RoundTripper {
	if original == nil {
		original = http.DefaultTransport
	}

	return &roundTripper{
		original: original,
	}
}

func (r *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	tracer := pinpoint.TracerFromRequestContext(req)
	if tracer == nil {
		return r.original.RoundTrip(req)
	}

	//clone request
	clone := *req
	clone.Header = make(http.Header, len(req.Header))
	for k, v := range req.Header {
		clone.Header[k] = v
	}
	req = &clone

	tracer = NewHttpClientTracer(tracer, "http.Client", req)
	resp, err := r.original.RoundTrip(req)
	EndHttpClientTracer(tracer, resp, err)

	return resp, err
}
