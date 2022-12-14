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
	"bytes"
	"compress/gzip"
	"io"
	"net/http"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type transport struct {
	rt   http.RoundTripper
	addr []string
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

// NewTransportWithAddr returns a new http.RoundTripper to instrument elasticsearch calls.
// If a http.RoundTripper parameter is not provided, http.DefaultTransport will be instrumented.
func NewTransportWithAddr(r http.RoundTripper, addr []string) http.RoundTripper {
	if r == nil {
		r = http.DefaultTransport
	}
	t := &transport{rt: r, addr: addr}
	return t
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	tracer := pinpoint.FromContext(ctx)
	if !tracer.IsSampled() {
		return t.rt.RoundTrip(req)
	}

	tracer.NewSpanEvent("elasticsearch")
	defer tracer.EndSpanEvent()

	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeGoHttpClient)
	se.SetDestination("ElasticSearch")
	se.SetEndPoint(t.endpoint(req))

	a := se.Annotations()
	if dsl := dslString(req); dsl != "" {
		a.AppendString(pinpoint.AnnotationEsDsl, dsl)
	}

	resp, err := t.rt.RoundTrip(req)
	se.SetError(err)

	return resp, err
}

func (t *transport) endpoint(req *http.Request) string {
	var endpoint string

	if t.addr != nil {
		endpoint = t.addr[0]
	} else {
		endpoint = req.Host
	}
	if endpoint == "" {
		endpoint = "unknown"
	}
	return endpoint
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
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(dsl))

	if req.Header.Get("Content-Encoding") == "gzip" {
		r, err := gzip.NewReader(bytes.NewReader(dsl))
		if err != nil {
			return string(dsl)
		}
		dsl, _ = io.ReadAll(r)
	}
	return string(dsl)
}
