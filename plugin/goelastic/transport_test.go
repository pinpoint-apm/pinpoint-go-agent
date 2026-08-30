package ppgoelastic

import (
	"io"
	"net/http"
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
