package ppgoelastic

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)

	dsl, err := dslString(req)
	require.NoError(t, err)
	assert.Len(t, dsl, maxBodyRead, "the body copy must be read only up to the limit")

	sent, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	assert.Equal(t, huge, string(sent), "the request body the transport sends must be untouched")
}

func TestRoundTrip_DoesNotPreconsumeStreamingBodyWithoutGetBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", reader)
	require.NoError(t, err)
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
	assert.Equal(t, io.ReadCloser(reader), sentReq.Body,
		"the underlying transport received a replaced body of type %T", sentReq.Body)

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
	require.NoError(t, <-writeDone)
	require.NoError(t, <-roundTripDone)
	assert.Equal(t, string(payload), string(got))
}

func TestRoundTrip_DoesNotMutateReadErrorBodyWithoutGetBody(t *testing.T) {
	body := &readErrorBody{}
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/_bulk", body)
	require.NoError(t, err)
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
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
	assert.Equal(t, io.ReadCloser(body), sentBody,
		"the underlying transport received a replaced body of type %T", sentBody)
	assert.Equal(t, io.ReadCloser(body), req.Body, "the caller's request body was replaced")
	assert.Zero(t, readsBefore, "the body was read before reaching the underlying transport")
	assert.False(t, closedBefore, "the body was closed before reaching the underlying transport")
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
	require.NoError(t, err)
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	require.Len(t, tracer.events, 2, "one call must record the Elasticsearch event and the HTTP one")

	outer := tracer.events[0]
	assert.Equal(t, "elasticsearch", outer.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeGoElastic), outer.serviceType)
	assert.Equal(t, "ElasticSearch", outer.destination)
	assert.Equal(t, "es:9200", outer.endPoint)
	assert.Equal(t, `{"query":{"match_all":{}}}`, outer.annotations[pinpoint.AnnotationEsDsl])

	inner := tracer.events[1]
	assert.Equal(t, "transport.RoundTrip()", inner.operation)
	assert.Equal(t, int32(ServiceTypeHttpClient4), inner.serviceType)
	assert.Equal(t, "es:9200", inner.destination)
	assert.NoError(t, inner.err)

	for i, e := range tracer.events {
		assert.True(t, e.ended, "span event %d (%s) was left open", i, e.operation)
	}
}

// A transport failure is what tracing is for, so it has to reach the caller and
// the span event that made the call.
func TestRoundTrip_RecordsTheTransportError(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://es:9200/test/_search?q=name:foo", nil)
	require.NoError(t, err)
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	want := errors.New("connection refused")
	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, want }))

	_, err = rt.RoundTrip(req)
	assert.ErrorIs(t, err, want, "the transport error must come back unchanged")

	require.Len(t, tracer.events, 2)
	assert.ErrorIs(t, tracer.events[1].err, want, "the HTTP event is the one that made the call")
	for i, e := range tracer.events {
		assert.True(t, e.ended, "span event %d (%s) was left open on failure", i, e.operation)
	}
}

// Only the first MaxDslLength characters are recorded, so a long query has to
// be cut rather than blow up the annotation.
func TestRoundTrip_TruncatesTheDsl(t *testing.T) {
	body := `{"query":"` + strings.Repeat("x", 4*MaxDslLength) + `"}`
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", strings.NewReader(body))
	require.NoError(t, err)
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	_, err = rt.RoundTrip(req)
	require.NoError(t, err)

	assert.Equal(t, body[:MaxDslLength], tracer.events[0].annotations[pinpoint.AnnotationEsDsl],
		"the annotation must be the first %d bytes of the query", MaxDslLength)
}

// The transport is installed on the client, so it sees every request the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func TestRoundTrip_IgnoresUnsampledRequests(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://es:9200/test/_search?q=name:foo", nil)
			require.NoError(t, err)
			req = req.WithContext(tt.ctx)

			called := false
			rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
			}))
			_, err = rt.RoundTrip(req)
			require.NoError(t, err)
			assert.True(t, called, "the underlying transport was not called")
		})
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
				require.NoError(t, err)
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
				require.NoError(t, err)
				return req
			},
			want: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dslString(tt.req(t))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// go-elasticsearch can be configured to compress request bodies, and a gzip
// blob is not a query anyone can read off a span.
func Test_dslString_GzippedBody(t *testing.T) {
	query := `{"query":{"match_all":{}}}`

	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	_, err := zw.Write([]byte(query))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", bytes.NewReader(body.Bytes()))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")

	got, err := dslString(req)
	require.NoError(t, err)
	assert.Equal(t, query, got, "a gzipped body must be inflated for the annotation")
}

// A body that claims to be gzipped but is not must leave the annotation as the
// raw bytes rather than take the request down.
func Test_dslString_MalformedGzipBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", strings.NewReader("not gzip"))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")

	got, err := dslString(req)
	assert.Error(t, err, "dslString reported no error for a malformed gzip body")
	assert.Equal(t, "not gzip", got, "the raw body is the best the annotation can do")
}

// Called with no transport, the wrapper has to fall back to the one net/http
// would have used rather than leave a nil round tripper behind.
func TestNewTransport_DefaultsToHttpDefaultTransport(t *testing.T) {
	assert.Same(t, http.DefaultTransport, NewTransport(nil).(*transport).rt)
}

// A transport the caller provided must be kept, not replaced by the default.
func TestNewTransport_KeepsTheGivenTransport(t *testing.T) {
	given := roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, nil })

	assert.Implements(t, (*http.RoundTripper)(nil), NewTransport(given))
	assert.False(t, NewTransport(given).(*transport).rt == http.RoundTripper(http.DefaultTransport),
		"the caller's transport was replaced by the default")
}

// A DSL that fails to read must not lose the call: the request still goes out,
// with an empty annotation instead of a query.
func TestRoundTrip_RecordsTheCallWhenTheDslCannotBeRead(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://es:9200/test/_search", strings.NewReader("not gzip"))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	tracer := newCapturingTracer()
	req = req.WithContext(pinpoint.NewContext(req.Context(), tracer))

	called := false
	rt := NewTransport(roundTripperFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
	}))
	_, err = rt.RoundTrip(req)

	require.NoError(t, err, "an unreadable DSL must not fail the request")
	assert.True(t, called, "the underlying transport was not called")
	require.Len(t, tracer.events, 2)
	assert.Contains(t, tracer.events[0].annotations, int32(pinpoint.AnnotationEsDsl),
		"the call must still be annotated, even with an unreadable query")
	for i, e := range tracer.events {
		assert.True(t, e.ended, "span event %d (%s) was left open", i, e.operation)
	}
}
