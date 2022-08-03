package http

import (
	"context"
	pinpoint "github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
)

func NewHttpClientTracer(tracer pinpoint.Tracer, operationName string, req *http.Request) pinpoint.Tracer {
	if tracer != nil {
		tracer.NewSpanEvent(operationName)

		tracer.SpanEvent().SetEndPoint(req.Host)
		tracer.SpanEvent().SetDestination(req.Host)
		tracer.SpanEvent().SetServiceType(pinpoint.ServiceTypeGoHttpClient)
		tracer.SpanEvent().Annotations().AppendString(pinpoint.AnnotationHttpUrl, req.URL.String())
		tracer.Inject(req.Header)
	}

	return tracer
}

func EndHttpClientTracer(tracer pinpoint.Tracer, resp *http.Response, err error) {
	if tracer != nil {
		tracer.SpanEvent().SetError(err)
		if resp != nil {
			tracer.SpanEvent().Annotations().AppendInt(pinpoint.AnnotationHttpStatusCode, int32(resp.StatusCode))
		}
		tracer.EndSpanEvent()
	}
}

func DoHttpClient(doFunc func(req *http.Request) (*http.Response, error), tracer pinpoint.Tracer, operation string,
	req *http.Request) (*http.Response, error) {
	tracer = NewHttpClientTracer(tracer, operation, req)
	resp, err := doFunc(req)
	EndHttpClientTracer(tracer, resp, err)

	return resp, err
}

func DoHttpClientWithContext(doFunc func(req *http.Request) (*http.Response, error), ctx context.Context, operation string,
	req *http.Request) (*http.Response, error) {
	return DoHttpClient(doFunc, pinpoint.FromContext(ctx), operation, req)
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

	return DoHttpClient(r.original.RoundTrip, tracer, "http.Client.Do", req)
}
