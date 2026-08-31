// Package ppgoelasticv8 instruments the elastic/go-elasticsearch/v8 package (https://github.com/elastic/go-elasticsearch).
//
// This package instruments the go-elasticsearch/v8 calls.
// Use the NewTransport as the elasticsearch.Client's Transport.
//
//	elasticsearch.NewClient(elasticsearch.Config{Transport: ppgoelasticv8.NewTransport(nil)})
//
// It is necessary to pass the context containing the pinpoint.Tracer to elasticsearch.Client.
//
//	ctx := pinpoint.NewContext(context.Background(), tracer)
//	es.Search(es.Search.WithContext(ctx), es.Search.WithIndex("test"))
package ppgoelasticv8

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
		pinpoint.Log("goelasticv8").Errorf("dsl read error: %s", err.Error())
	}
	if len(dsl) > MaxDslLength {
		dsl = dsl[0:MaxDslLength]
	}
	se.Annotations().AppendString(pinpoint.AnnotationEsDsl, dsl)

	// Since the service type ELASTICSEARCH_HIGHLEVEL_CLIENT(9204) depends on HTTP_CLIENT_4(9052),
	// an additional span event must be added like elasticsearch-plugin of java agent.
	defer tracer.NewSpanEvent("transport.RoundTrip()").EndSpanEvent()
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
	// Without GetBody there is no copy to read: consuming req.Body would stall a
	// streaming request until EOF and, on a read error, truncate what is actually
	// sent. The annotation is not worth that.
	if req.Body == nil || req.Body == http.NoBody || req.GetBody == nil {
		return "", nil
	}

	dsl, err := getBodyFromCopy(req)
	if err != nil {
		return "", err
	}

	if req.Header.Get("Content-Encoding") == "gzip" {
		dsl, err = unzip(dsl)
	}
	return string(dsl), err
}

// maxBodyRead bounds what is read for the annotation. Only the first
// MaxDslLength characters are ever recorded, so reading a whole multi-megabyte
// _bulk payload just to slice 256 bytes off it was pure waste; the slack keeps
// the truncation marker meaningful for gzipped bodies.
const maxBodyRead = 4 * MaxDslLength

func getBodyFromCopy(req *http.Request) ([]byte, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	// A copy the request itself does not use, so it can be read partially.
	return io.ReadAll(io.LimitReader(body, maxBodyRead))
}

func unzip(dsl []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(dsl))
	if err != nil {
		return dsl, err
	}
	defer r.Close()
	// Inflating the whole payload would undo the read limit above; the result
	// feeds the annotation only.
	return io.ReadAll(io.LimitReader(r, maxBodyRead))
}
