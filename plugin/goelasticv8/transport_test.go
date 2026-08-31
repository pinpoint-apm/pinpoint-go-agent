package ppgoelasticv8

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
)

type sampledTracer struct {
	pinpoint.Tracer
}

func (sampledTracer) IsSampled() bool { return true }

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type readErrorBody struct {
	reads  int
	closed bool
}

func (b *readErrorBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.ErrUnexpectedEOF
}

func (b *readErrorBody) Close() error {
	b.closed = true
	return nil
}

func Test_dslString_LimitsCopiedBodyRead(t *testing.T) {
	huge := strings.Repeat("x", 1<<20)

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", strings.NewReader(huge))
	if err != nil {
		t.Fatal(err)
	}
	dsl, err := dslString(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(dsl) != maxBodyRead {
		t.Errorf("read %d bytes from the body copy, want %d", len(dsl), maxBodyRead)
	}

	sent, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(sent) != huge {
		t.Errorf("request body changed: got %d bytes, want %d", len(sent), len(huge))
	}
}

func TestRoundTrip_DoesNotPreconsumeStreamingBodyWithoutGetBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", reader)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(pinpoint.NewContext(req.Context(), sampledTracer{pinpoint.NoopTracer()}))

	entered := make(chan *http.Request, 1)
	bodyRead := make(chan []byte, 1)
	rt := NewTransport(roundTripperFunc(func(got *http.Request) (*http.Response, error) {
		entered <- got
		body, err := io.ReadAll(got.Body)
		bodyRead <- body
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: got}, nil
	}))

	roundTripDone := make(chan error, 1)
	go func() {
		_, err := rt.RoundTrip(req)
		roundTripDone <- err
	}()

	var sentReq *http.Request
	select {
	case sentReq = <-entered:
	case <-time.After(2 * time.Second):
		_ = writer.CloseWithError(io.ErrClosedPipe)
		<-roundTripDone
		t.Fatal("underlying transport was not called before the streaming body reached EOF")
	}
	if sentReq.Body != reader {
		t.Errorf("underlying transport received a replaced body of type %T", sentReq.Body)
	}

	payload := []byte("streamed bulk body")
	writeDone := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		if closeErr := writer.Close(); err == nil {
			err = closeErr
		}
		writeDone <- err
	}()

	got := <-bodyRead
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-roundTripDone; err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("transport read %q, want %q", got, payload)
	}
}

func TestRoundTrip_DoesNotMutateReadErrorBodyWithoutGetBody(t *testing.T) {
	body := &readErrorBody{}
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", body)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(pinpoint.NewContext(req.Context(), sampledTracer{pinpoint.NoopTracer()}))

	var (
		sentBody     io.ReadCloser
		readsBefore  int
		closedBefore bool
	)
	rt := NewTransport(roundTripperFunc(func(got *http.Request) (*http.Response, error) {
		sentBody = got.Body
		readsBefore = body.reads
		closedBefore = body.closed
		_, err := io.ReadAll(got.Body)
		return nil, err
	}))

	_, err = rt.RoundTrip(req)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("RoundTrip error = %v, want %v", err, io.ErrUnexpectedEOF)
	}
	if sentBody != body || req.Body != body {
		t.Errorf("underlying transport received a replaced body of type %T", sentBody)
	}
	if readsBefore != 0 {
		t.Errorf("body was read %d times before reaching the underlying transport", readsBefore)
	}
	if closedBefore {
		t.Error("body was closed before reaching the underlying transport")
	}
}

// capturingTracer records what the transport puts on its span events. A real
// tracer's recorders are write-only, so this stands in for one. RoundTrip
// nests a second event inside the first, so open events are kept on a stack.
type capturingTracer struct {
	pinpoint.Tracer
	events []*capturedEvent
	open   []*capturedEvent
}

func newCapturingTracer() *capturingTracer {
	return &capturingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *capturingTracer) IsSampled() bool { return true }

func (t *capturingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	e := &capturedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	}
	t.events = append(t.events, e)
	t.open = append(t.open, e)
	return t
}

func (t *capturingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.open[len(t.open)-1] }

func (t *capturingTracer) EndSpanEvent() {
	e := t.open[len(t.open)-1]
	t.open = t.open[:len(t.open)-1]
	e.ended = true
}

type capturedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	endPoint    string
	err         error
	annotations map[int32]string
	ended       bool
}

func (e *capturedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *capturedEvent) SetDestination(id string)        { e.destination = id }
func (e *capturedEvent) SetEndPoint(endPoint string)     { e.endPoint = endPoint }
func (e *capturedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *capturedEvent) Annotations() pinpoint.Annotation {
	return capturedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type capturedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a capturedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// The Pinpoint server files the Elasticsearch client under a type that depends
// on the HTTP client type, so one call has to record both events - the outer
// one carrying the query, the inner one the host it went to.
func TestRoundTrip_RecordsBothSpanEvents(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search",
		strings.NewReader(`{"query":{"match_all":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 2 {
		t.Fatalf("recorded %d span events, want 2", len(tracer.events))
	}

	outer := tracer.events[0]
	if outer.operation != "elasticsearch" {
		t.Errorf("outer operation = %q, want %q", outer.operation, "elasticsearch")
	}
	if outer.serviceType != pinpoint.ServiceTypeGoElastic {
		t.Errorf("outer service type = %d, want %d", outer.serviceType, pinpoint.ServiceTypeGoElastic)
	}
	if outer.destination != "ElasticSearch" {
		t.Errorf("outer destination = %q, want ElasticSearch", outer.destination)
	}
	if outer.endPoint != "es:9200" {
		t.Errorf("outer endpoint = %q, want %q", outer.endPoint, "es:9200")
	}
	if got, want := outer.annotations[pinpoint.AnnotationEsDsl], `{"query":{"match_all":{}}}`; got != want {
		t.Errorf("dsl annotation = %q, want %q", got, want)
	}

	inner := tracer.events[1]
	if inner.operation != "transport.RoundTrip()" {
		t.Errorf("inner operation = %q, want %q", inner.operation, "transport.RoundTrip()")
	}
	if inner.serviceType != ServiceTypeHttpClient4 {
		t.Errorf("inner service type = %d, want %d", inner.serviceType, ServiceTypeHttpClient4)
	}
	if inner.destination != "es:9200" {
		t.Errorf("inner destination = %q, want %q", inner.destination, "es:9200")
	}
	for i, e := range tracer.events {
		if !e.ended {
			t.Errorf("span event %d was left open", i)
		}
	}
}

// A transport failure is what tracing is for, so it has to reach the caller and
// the span event that made the call.
func TestRoundTrip_RecordsTheTransportError(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://es:9200/test/_search?q=name:foo", nil)
	if err != nil {
		t.Fatal(err)
	}
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	want := errors.New("connection refused")
	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, want }))

	if _, err := rt.RoundTrip(req); !errors.Is(err, want) {
		t.Errorf("RoundTrip() = %v, want %v", err, want)
	}
	if !errors.Is(tracer.events[1].err, want) {
		t.Errorf("recorded error = %v, want %v", tracer.events[1].err, want)
	}
}

// Only the first MaxDslLength characters are recorded, so a long query has to
// be cut rather than blow up the annotation.
func TestRoundTrip_TruncatesTheDsl(t *testing.T) {
	body := `{"query":"` + strings.Repeat("x", 4*MaxDslLength) + `"}`
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}

	dsl := tracer.events[0].annotations[pinpoint.AnnotationEsDsl]
	if len(dsl) != MaxDslLength {
		t.Errorf("dsl annotation = %d bytes, want %d", len(dsl), MaxDslLength)
	}
	if dsl != body[:MaxDslLength] {
		t.Error("dsl annotation is not the start of the query")
	}
}

// The transport is installed on the client, so it sees every request the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func TestRoundTrip_IgnoresUnsampledRequests(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://es:9200/test/_search?q=name:foo", nil)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("the underlying transport was not called")
	}
}

// The DSL is whatever describes the query: the q parameter for a URI search,
// the request body for a body search, and nothing at all when there is neither
// or when the body cannot be copied.
func Test_dslString(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  func(*testing.T) *http.Request
		want string
	}{
		{
			name: "uri search",
			req: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://es:9200/test/_search?q=name:foo", nil)
			},
			want: "name:foo",
		},
		{
			// The q parameter wins: it is the query, the body is not one.
			name: "uri search with a body",
			req: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodPost, "http://es:9200/test/_search?q=name:foo",
					strings.NewReader(`{"query":{"match_all":{}}}`))
			},
			want: "name:foo",
		},
		{
			name: "other query parameters only",
			req: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://es:9200/test/_search?size=10", nil)
			},
			want: "",
		},
		{
			// http.NewRequest gives a strings.Reader body a GetBody, which is
			// the copy the annotation is read from.
			name: "body search",
			req: func(t *testing.T) *http.Request {
				req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search",
					strings.NewReader(`{"query":{"match_all":{}}}`))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			want: `{"query":{"match_all":{}}}`,
		},
		{
			name: "no body",
			req: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "http://es:9200/test/_search", nil)
			},
			want: "",
		},
		{
			// A streaming body has no GetBody, so there is no copy to read.
			name: "body without GetBody",
			req: func(t *testing.T) *http.Request {
				req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", io.LimitReader(strings.NewReader("x"), 1))
				if err != nil {
					t.Fatal(err)
				}
				return req
			},
			want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dslString(tt.req(t))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("dslString() = %q, want %q", got, tt.want)
			}
		})
	}
}

// go-elasticsearch can be configured to compress request bodies, and a gzip
// blob is not a query anyone can read off a span.
func Test_dslString_GzippedBody(t *testing.T) {
	query := `{"query":{"match_all":{}}}`

	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if _, err := zw.Write([]byte(query)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Encoding", "gzip")

	got, err := dslString(req)
	if err != nil {
		t.Fatal(err)
	}
	if got != query {
		t.Errorf("dslString() = %q, want %q", got, query)
	}
}

// A body that claims to be gzipped but is not must leave the annotation as the
// raw bytes rather than take the request down.
func Test_dslString_MalformedGzipBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", strings.NewReader("not gzip"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Encoding", "gzip")

	got, err := dslString(req)
	if err == nil {
		t.Error("dslString() reported no error for a malformed gzip body")
	}
	if got != "not gzip" {
		t.Errorf("dslString() = %q, want the raw body", got)
	}
}

// Called with no transport, the wrapper has to fall back to the one net/http
// would have used rather than leave a nil round tripper behind.
func TestNewTransport_DefaultsToHttpDefaultTransport(t *testing.T) {
	if got := NewTransport(nil).(*transport).rt; got != http.DefaultTransport {
		t.Errorf("NewTransport(nil) wraps %T, want http.DefaultTransport", got)
	}
}
