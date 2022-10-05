package ppgoelastic

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
)

type transport struct {
	rt http.RoundTripper
}

func NewTransport(r http.RoundTripper) http.RoundTripper {
	if r == nil {
		r = http.DefaultTransport
	}
	t := &transport{rt: r}
	return t
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tracer := pinpoint.FromContext(ctx)
	if tracer == nil {
		return t.rt.RoundTrip(req)
	}

	tracer.NewSpanEvent("elasticsearch")
	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeGoElastic)
	se.SetDestination("ElasticSearch")
	se.SetEndPoint(req.Host)

	a := se.Annotations()
	a.AppendString(pinpoint.AnnotationEsUrl, req.URL.String())
	a.AppendString(pinpoint.AnnotationEsAction, req.Method)
	dsl := dslString(req)
	if dsl != "" {
		a.AppendString(pinpoint.AnnotationEsDsl, dsl)
	}

	resp, err := t.rt.RoundTrip(req)
	se.SetError(err)

	return resp, err
}

func dslString(req *http.Request) string {
	if req.URL.RawQuery != "" {
		if dsl := req.URL.Query().Get("q"); dsl != "" {
			return dsl
		}
	}

	if req.Body == nil || req.Body == http.NoBody {
		return ""
	}

	dsl := make([]byte, req.ContentLength)
	req.Body.Read(dsl)
	return string(dsl)
}

func RequestWithContext(req *http.Request) *http.Request {
	url := req.URL
	req.URL = nil
	reqCopy := req.WithContext(req.Context())
	reqCopy.URL = url
	req.URL = url
	return reqCopy
}
