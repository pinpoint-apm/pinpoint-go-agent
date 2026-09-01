package pphttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingTransport captures the request the wrapped client actually put on
// the wire, and answers with a canned response or error.
type recordingTransport struct {
	sent   *http.Request
	status int
	header http.Header
	err    error
}

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.sent = req
	if rt.err != nil {
		return nil, rt.err
	}
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	header := rt.header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// pinpointHeaders are the distributed tracing headers Inject writes; a callee
// continues the transaction from them.
func pinpointHeaders(t *testing.T, h http.Header) map[string]string {
	t.Helper()
	got := map[string]string{}
	for name, values := range h {
		if strings.HasPrefix(strings.ToLower(name), "pinpoint-") {
			got[name] = values[0]
		}
	}
	return got
}

func serverTracer(t *testing.T) pinpoint.Tracer {
	t.Helper()
	tracer := pinpoint.GetAgent().NewSpanTracer("caller", "/caller")
	t.Cleanup(tracer.EndSpan)
	require.True(t, tracer.IsSampled())
	return tracer
}

// The whole point of the client wrapper: the callee has to receive the headers
// that let it join this transaction.
func TestWrapClient_InjectsTracingHeaders(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	rt := &recordingTransport{}
	client := WrapClient(&http.Client{Transport: rt})

	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer),
		http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotNil(t, rt.sent, "the wrapped transport was never called")
	assert.NotEmpty(t, pinpointHeaders(t, rt.sent.Header), "no pinpoint header reached the callee")

	// The callee's tracer must land in the caller's transaction.
	callee := NewHttpServerTracerWithReader(http.MethodGet, "/callee", "HTTP Server", rt.sent.Header)
	defer callee.EndSpan()
	assert.Equal(t, tracer.TransactionId().String(), callee.TransactionId().String())
}

// http.RoundTripper requires that the request it is handed is not modified, so
// the injected headers have to go on a copy.
func TestWrapClient_DoesNotModifyTheCallersRequest(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	rt := &recordingTransport{}
	client := WrapClient(&http.Client{Transport: rt})

	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer),
		http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)
	req.Header.Set("X-Caller", "v")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Empty(t, pinpointHeaders(t, req.Header), "the caller's own request was written to")
	assert.Equal(t, "v", rt.sent.Header.Get("X-Caller"), "the caller's headers must survive the copy")
	assert.NotSame(t, req.Header, rt.sent.Header)
}

// WrapClient copies the client, so the original stays untouched and any other
// fields the caller set are kept.
func TestWrapClient_CopiesTheClient(t *testing.T) {
	startAgent(t)

	rt := &recordingTransport{}
	original := &http.Client{Transport: rt}
	wrapped := WrapClient(original)

	assert.NotSame(t, original, wrapped)
	assert.Same(t, rt, original.Transport, "the caller's client must keep its own transport")
	assert.IsType(t, &roundTripper{}, wrapped.Transport)
}

// A nil client is the documented shorthand for http.DefaultClient; a nil
// transport is the shorthand for http.DefaultTransport.
func TestWrapClient_Defaults(t *testing.T) {
	startAgent(t)

	wrapped := WrapClient(nil)
	require.NotNil(t, wrapped)
	assert.NotSame(t, http.DefaultClient, wrapped, "the default client must not be instrumented in place")

	rt, ok := wrapped.Transport.(*roundTripper)
	require.True(t, ok)
	assert.Same(t, http.DefaultTransport, rt.original)
	assert.Nil(t, http.DefaultClient.Transport, "http.DefaultClient was modified")
}

// WrapClientWithContext takes the tracer from the context it was built with, so
// requests that carry no tracer of their own are still traced.
func TestWrapClientWithContext(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	rt := &recordingTransport{}
	client := WrapClientWithContext(pinpoint.NewContext(context.Background(), tracer), &http.Client{Transport: rt})

	resp, err := client.Get("http://example.com/callee")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotNil(t, rt.sent)
	assert.NotEmpty(t, pinpointHeaders(t, rt.sent.Header),
		"the tracer from the client's context should have been used")
}

// Without a tracer anywhere the request must still go through, carrying the
// "not sampled" marker and nothing else: the callee has to know not to start a
// transaction of its own rather than treating the call as an untraced entry
// point.
func TestWrapClient_WithoutATracer(t *testing.T) {
	startAgent(t)

	rt := &recordingTransport{}
	client := WrapClient(&http.Client{Transport: rt})

	resp, err := client.Get("http://example.com/callee")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NotNil(t, rt.sent, "the request must still be sent")
	assert.Equal(t, map[string]string{"Pinpoint-Sampled": "s0"}, pinpointHeaders(t, rt.sent.Header))

	callee := NewHttpServerTracerWithReader(http.MethodGet, "/callee", "HTTP Server", rt.sent.Header)
	defer callee.EndSpan()
	assert.False(t, callee.IsSampled(), "the callee must honour the not-sampled marker")
}

// A transport error is the caller's to handle; the wrapper records it and
// returns it unchanged.
func TestWrapClient_TransportError(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	wantErr := errors.New("dial failed")
	client := WrapClient(&http.Client{Transport: &recordingTransport{err: wantErr}})

	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer),
		http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	_, err = client.Do(req)
	assert.ErrorIs(t, err, wantErr, "the transport error must reach the caller unchanged")
}

// DoClient is the wrapper for callers that keep their own do function; it reads
// the tracer off the request and must return the response untouched.
func TestDoClient(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	rt := &recordingTransport{status: http.StatusTeapot}
	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer),
		http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	resp, err := DoClient((&http.Client{Transport: rt}).Do, req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusTeapot, resp.StatusCode)
	assert.NotEmpty(t, pinpointHeaders(t, req.Header), "DoClient injects into the request it is given")
}

func TestDoClient_WithoutATracer(t *testing.T) {
	startAgent(t)

	rt := &recordingTransport{}
	req, err := http.NewRequest(http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	resp, err := DoClient((&http.Client{Transport: rt}).Do, req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, map[string]string{"Pinpoint-Sampled": "s0"}, pinpointHeaders(t, req.Header),
		"an untraced call carries only the not-sampled marker")
}

func TestDoClient_Error(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	wantErr := errors.New("dial failed")
	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), tracer),
		http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	_, err = DoClient((&http.Client{Transport: &recordingTransport{err: wantErr}}).Do, req)
	assert.ErrorIs(t, err, wantErr)
}

// The client-side recorders are configured separately from the server ones, so
// a client header must only be recorded when the client option asks for it.
func TestClientHeaderRecordersUseTheClientOptions(t *testing.T) {
	usePluginConfig(t,
		WithHttpClientRecordRequestHeader([]string{"X-Req"}),
		WithHttpClientRecordRespondHeader([]string{"X-Res"}),
		WithHttpClientRecordRequestCookie([]string{"session"}),
		WithHttpServerRecordRequestHeader([]string{"X-Server-Only"}),
	)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)
	req.Header.Set("X-Req", "req-value")
	req.Header.Set("X-Server-Only", "nope")
	req.AddCookie(&http.Cookie{Name: "session", Value: "s1"})
	req.AddCookie(&http.Cookie{Name: "ignored", Value: "nope"})

	res := http.Header{}
	res.Set("X-Res", "res-value")
	res.Set("X-Server-Only", "nope")

	a := newRecordingAnnotation()
	RecordClientHttpRequestHeader(a, header{req.Header})
	RecordClientHttpResponseHeader(a, header{res})
	RecordClientHttpCookie(a, cookie{req})

	assert.Equal(t, map[string]string{"X-Req": "req-value"}, a.values(pinpoint.AnnotationHttpRequestHeader))
	assert.Equal(t, map[string]string{"X-Res": "res-value"}, a.values(pinpoint.AnnotationHttpResponseHeader))
	assert.Equal(t, map[string]string{"session": "s1"}, a.cookies())
	assert.NotContains(t, a.values(pinpoint.AnnotationHttpRequestHeader), "X-Server-Only",
		"a server-side option must not record a client header")
}

// The deprecated pair is the same before/after code path; it must keep working
// for callers that have not migrated.
func TestNewAndEndHttpClientTracer(t *testing.T) {
	startAgent(t)
	tracer := serverTracer(t)

	req, err := http.NewRequest(http.MethodGet, "http://example.com/callee", nil)
	require.NoError(t, err)

	clientTracer := NewHttpClientTracer(tracer, "http/Client.Do()", req)
	require.NotNil(t, clientTracer)
	assert.NotEmpty(t, pinpointHeaders(t, req.Header))

	EndHttpClientTracer(clientTracer, &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}, nil)

	// A nil tracer is what callers hand these when tracing is off.
	assert.NotPanics(t, func() {
		assert.Nil(t, NewHttpClientTracer(nil, "http/Client.Do()", req))
		EndHttpClientTracer(nil, nil, nil)
	})
}

// An end-to-end round trip over a real listener: the server side has to see the
// headers the client side wrote and continue the transaction.
func TestClientAndServerShareOneTransaction(t *testing.T) {
	startAgent(t)

	var calleeTxID string
	server := httptest.NewServer(WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calleeTxID = pinpoint.TracerFromRequestContext(r).TransactionId().String()
		w.WriteHeader(http.StatusOK)
	})))
	defer server.Close()

	caller := serverTracer(t)
	req, err := http.NewRequestWithContext(pinpoint.NewContext(context.Background(), caller),
		http.MethodGet, server.URL+"/callee", nil)
	require.NoError(t, err)

	resp, err := WrapClient(server.Client()).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, caller.TransactionId().String(), calleeTxID,
		"the callee's span must join the caller's transaction")
}

// A hand-built request - not from http.NewRequest - may carry a nil URL and a
// nil header map. net/http rejects such a request with an error, and the
// instrumentation must not turn that error into a panic.
func TestDoClient_HandBuiltRequest(t *testing.T) {
	agent := startAgent(t)
	tracer := agent.NewSpanTracer("test", "/caller")
	defer tracer.EndSpan()

	req := pinpoint.RequestWithTracerContext(&http.Request{Method: "GET"}, tracer)
	want := errors.New("http: nil Request.URL")

	assert.NotPanics(t, func() {
		_, err := DoClient(func(*http.Request) (*http.Response, error) { return nil, want }, req)
		assert.ErrorIs(t, err, want, "the doFunc's own verdict must come back unchanged")
	})
}
