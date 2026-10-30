// Package ppgoelastic instruments the elastic/go-elasticsearch package (https://github.com/elastic/go-elasticsearch).
//
// This package instruments the elasticsearch calls of every client major (v7,
// v8 and v9): NewTransport is an http.RoundTripper, and the Transport field it
// goes into is the same in each.
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
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
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
		// Debug, like the dsn parse errors of the driver wrappers: the client
		// reports its own body problems, and this runs once per request.
		pinpoint.Log("goelastic").Debugf("dsl read error: %v", err)
	}
	se.Annotations().AppendString(pinpoint.AnnotationEsDsl, dsl)

	// Since the service type ELASTICSEARCH_HIGHLEVEL_CLIENT(9204) depends on HTTP_CLIENT_4(9052),
	defer tracer.NewSpanEvent("transport.RoundTrip()").EndSpanEvent()
	se = tracer.SpanEvent()
	se.SetServiceType(ServiceTypeHttpClient4)
	se.SetDestination(req.URL.Host)

	res, err := t.rt.RoundTrip(req)
	se.SetError(err)

	return res, err
}

func dslString(req *http.Request) (string, error) {
	// URL.Query() parses the whole query string into a map; it is only worth
	// it when a q parameter can be in there at all, which a request carrying
	// refresh, routing or filter_path alone is not.
	if rq := req.URL.RawQuery; rq != "" && (strings.HasPrefix(rq, "q=") || strings.Contains(rq, "&q=")) {
		if dsl := req.URL.Query().Get("q"); dsl != "" {
			return cutDsl(dsl), nil
		}
	}
	// Without GetBody there is no copy to read: consuming req.Body would stall a
	// streaming request until EOF and, on a read error, truncate what is actually
	// sent. The annotation is not worth that.
	if req.Body == nil || req.Body == http.NoBody || req.GetBody == nil {
		return "", nil
	}

	b := dslBufs.Get().(*dslBuf)
	defer dslBufs.Put(b)
	dsl, err := b.readBody(req)
	if err != nil {
		return "", err
	}

	if req.Header.Get("Content-Encoding") == "gzip" {
		dsl, err = b.unzip(dsl)
	}
	// Cut while still bytes: only MaxDslLength of it is recorded, and the
	// string conversion is the one copy left on this path.
	return string(cutDsl(dsl)), err
}

// cutDsl is dsl cut to MaxDslLength on a rune start: a byte-offset cut can
// split a multi-byte rune, and a proto3 string field rejects invalid UTF-8.
func cutDsl[T string | []byte](dsl T) T {
	if len(dsl) <= MaxDslLength {
		return dsl
	}
	cut := MaxDslLength
	for cut > 0 && !utf8.RuneStart(dsl[cut]) {
		cut--
	}
	return dsl[:cut]
}

// CloseIdleConnections forwards http.Client.CloseIdleConnections to the
// wrapped transport, which the embedding would otherwise hide.
func (t *transport) CloseIdleConnections() {
	if c, ok := t.rt.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// maxBodyRead bounds what is read for the annotation. Only the first
// MaxDslLength characters are ever recorded, so reading a whole multi-megabyte
// _bulk payload just to slice 256 bytes off it was pure waste; the slack keeps
// the truncation marker meaningful for gzipped bodies.
const maxBodyRead = 4 * MaxDslLength

// dslBuf is what reading one body for the annotation needs, pooled: the fixed
// buffers the body and its inflated form are read into with io.ReadFull - a
// LimitReader plus io.ReadAll regrew a fresh slice through 512, 896 and 1408
// bytes per request - and the readers over them. gzip.NewReader alone allocates
// a 32 KiB window and the huffman tables per call, for at most maxBodyRead
// bytes of output.
type dslBuf struct {
	body [maxBodyRead]byte
	out  [maxBodyRead]byte
	raw  bytes.Reader
	gz   gzip.Reader
}

var dslBufs = sync.Pool{New: func() any { return new(dslBuf) }}

// readBody reads the request's body copy into b.body, up to maxBodyRead: a
// copy the request itself does not use, so it can be read partially. The
// result is valid until b is returned to the pool.
func (b *dslBuf) readBody(req *http.Request) ([]byte, error) {
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	n, err := io.ReadFull(body, b.body[:])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil // a body shorter than the buffer is the usual case
	}
	return b.body[:n], err
}

// unzip inflates dsl into b.out, up to maxBodyRead: inflating the whole
// payload would undo the read limit, and the result feeds the annotation only.
// A body that is no gzip stream comes back as it is, with the error.
func (b *dslBuf) unzip(dsl []byte) ([]byte, error) {
	b.raw.Reset(dsl)
	if err := b.gz.Reset(&b.raw); err != nil {
		return dsl, err
	}
	defer b.gz.Close()
	n, err := io.ReadFull(&b.gz, b.out[:])
	// The compressed input was itself cut at maxBodyRead, so running into the
	// truncated tail is the normal case, not a failure: propagating it logged
	// an error - and allocated a logrus entry - on every compressed request.
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return b.out[:n], err
}
