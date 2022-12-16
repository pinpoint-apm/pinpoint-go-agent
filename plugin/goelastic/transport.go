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

const (
	MaxDslLength           = 256
	ServiceTypeHttpClient4 = 9052
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
	if !tracer.IsSampled() {
		return t.rt.RoundTrip(req)
	}

	defer tracer.NewSpanEvent("elasticsearch").EndSpanEvent()
	se := tracer.SpanEvent()
	se.SetServiceType(pinpoint.ServiceTypeGoElastic)
	se.SetDestination("ElasticSearch")
	se.SetEndPoint(req.URL.Host)

	dsl, err := dslString(req)
	if err != nil {
		pinpoint.Log("goelastic").Errorf("dsl read error: %s", err.Error())
	}
	if len(dsl) > MaxDslLength {
		dsl = dsl[0:MaxDslLength]
	}
	se.Annotations().AppendString(pinpoint.AnnotationEsDsl, dsl)

	// Since the service type ELASTICSEARCH_HIGHLEVEL_CLIENT(9204) depends on HTTP_CLIENT_4(9052),
	// an additional span event must be added like elasticsearch-plugin of java agent.
	defer tracer.NewSpanEvent("transport.RoundTrip()").SpanEvent()
	se = tracer.SpanEvent()
	se.SetServiceType(ServiceTypeHttpClient4)
	se.SetDestination(req.URL.Host)

	res, err := t.rt.RoundTrip(req)
	se.SetError(err)

	return res, err
}

func dslString(req *http.Request) (string, error) {
	if req.URL.RawQuery != "" {
		if dsl := req.URL.Query().Get("q"); dsl != "" {
			return dsl, nil
		}
	}
	if req.Body == nil || req.Body == http.NoBody {
		return "", nil
	}

	var (
		dsl []byte
		err error
	)
	if req.GetBody != nil {
		dsl, err = getBodyFromCopy(req)
	} else {
		dsl, err = getBody(req)
	}
	if err != nil {
		return "", err
	}

	if req.Header.Get("Content-Encoding") == "gzip" {
		dsl, err = unzip(dsl)
	}
	return string(dsl), err
}

func getBodyFromCopy(req *http.Request) ([]byte, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(body)
}

func getBody(req *http.Request) ([]byte, error) {
	dsl, err := io.ReadAll(req.Body)
	req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(dsl))
	return dsl, err
}

func unzip(dsl []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(dsl))
	if err != nil {
		return dsl, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
