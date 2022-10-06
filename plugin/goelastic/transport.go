// Package ppgoelastic instruments the elastic/go-elasticsearch package (https://github.com/elastic/go-elasticsearch).
//
// This package instruments the elasticsearch calls.
// Use the NewTransport as the elasticsearch.Client's Transport.
//
//	elasticsearch.NewClient(elasticsearch.Config{Transport: ppgoelastic.NewTransport(nil)})
//
// It is necessary to pass the context containing the pinpoint.Tracer to elasticsearch.Client.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	es.Search(es.Search.WithContext(ctx), es.Search.WithIndex("test"))
package ppgoelastic

import (
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"net/http"
)

type transport struct {
	rt http.RoundTripper
}

// NewTransport returns a new http.RoundTripper to instrument elasticsearch calls.
// If a http.RoundTripper parameter is not provided, http.DefaultTransport will be instrumented.
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

//func RequestWithContext(req *http.Request) *http.Request {
//	url := req.URL
//	req.URL = nil
//	reqCopy := req.WithContext(req.Context())
//	reqCopy.URL = url
//	req.URL = url
//	return reqCopy
//}
