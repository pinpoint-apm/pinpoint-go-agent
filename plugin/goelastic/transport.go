package ppgoelastic

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
)

const (
	serviceTypeGoElastic = 9203
	annotationEsUrl      = 172
	annotationEsAction   = 174
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

	tracer.SpanEvent().SetServiceType(serviceTypeGoElastic)
	tracer.SpanEvent().SetDestination("ElasticSearch")
	tracer.SpanEvent().SetEndPoint(req.Host)
	tracer.SpanEvent().Annotations().AppendString(annotationEsUrl, req.URL.String())
	tracer.SpanEvent().Annotations().AppendString(annotationEsAction, req.Method)
	dsl := dslString(req)
	if dsl != "" {
		tracer.SpanEvent().Annotations().AppendString(173, dsl)
	}

	//log.Print(req)

	//req = req.WithContext(ctx)
	resp, err := t.rt.RoundTrip(req)
	tracer.SpanEvent().SetError(err)

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
