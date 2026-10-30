package ppkratosv3

import (
	"context"
	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"io"
	"net/http"
	"strings"
	"testing"
)

type regressionTransportFunc func(*http.Request) (*http.Response, error)

func (f regressionTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func regressionResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}
}
func TestClientMiddleware_StackedNilHeaders(t *testing.T) {
	pptest.StartAgent(t)
	c, err := khttp.NewClient(context.Background(), khttp.WithEndpoint("127.0.0.1:8000"), khttp.WithMiddleware(ClientMiddleware(), ClientMiddleware()), khttp.WithTransport(regressionTransportFunc(func(r *http.Request) (*http.Response, error) { return regressionResponse(r), nil })))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tr := pinpoint.GetAgent().NewSpanTracer("review", "/caller")
	defer tr.EndSpan()
	var headers http.Header
	var reply map[string]any
	defer func() {
		if p := recover(); p != nil {
			t.Errorf("stacked middleware panicked: %v", p)
		}
	}()
	err = c.Invoke(pinpoint.NewContext(context.Background(), tr), "GET", "/test", nil, &reply, khttp.Header(&headers))
	if err != nil {
		t.Fatal(err)
	}
}
func TestClientMiddleware_ReplacesStaleSamplingHeader(t *testing.T) {
	agent := pptest.StartAgent(t)
	sampled := false
	c, err := khttp.NewClient(context.Background(), khttp.WithEndpoint("127.0.0.1:8000"), khttp.WithMiddleware(ClientMiddleware()), khttp.WithTransport(regressionTransportFunc(func(r *http.Request) (*http.Response, error) {
		callee := agent.NewSpanTracerWithReader("downstream", "/test", pinpoint.HttpHeaderReader(r.Header))
		sampled = callee.IsSampled()
		if r.Header.Get("X-App") != "keep" {
			t.Error("application header was removed")
		}
		if r.Header.Get(pinpoint.HeaderSampled) != "" || r.Header.Get(pinpoint.HeaderParentApplicationNamespace) != "" {
			t.Error("stale tracing header remains")
		}
		callee.EndSpan()
		t.Logf("outbound trace=%q sampled=%q", r.Header.Get(pinpoint.HeaderTraceId), r.Header.Get(pinpoint.HeaderSampled))
		return regressionResponse(r), nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tr := agent.NewSpanTracer("review", "/caller")
	defer tr.EndSpan()
	headers := http.Header{}
	headers.Set(pinpoint.HeaderSampled, "s0")
	headers.Set(pinpoint.HeaderParentApplicationNamespace, "old-cluster")
	headers.Set("X-App", "keep")
	var reply map[string]any
	if err = c.Invoke(pinpoint.NewContext(context.Background(), tr), "GET", "/test", nil, &reply, khttp.Header(&headers)); err != nil {
		t.Fatal(err)
	}
	if !sampled {
		t.Error("sampled caller lost downstream trace because stale s0 was retained")
	}
}

// eventCounter counts the span events opened on the tracer it wraps.
type eventCounter struct {
	pinpoint.Tracer
	events int
}

func (c *eventCounter) NewSpanEvent(operation string) pinpoint.Tracer {
	c.events++
	c.Tracer.NewSpanEvent(operation)
	return c
}

// The middleware installed twice records one call as one event: the inner
// layer finds the context the outer one injected and lets it travel alone,
// where clearing it and injecting again recorded the call twice, the outer
// event's span id reaching no callee.
func TestClientMiddleware_StackedRecordsOneEvent(t *testing.T) {
	agent := pptest.StartAgent(t)
	var spanIds []string
	c, err := khttp.NewClient(context.Background(), khttp.WithEndpoint("127.0.0.1:8000"),
		khttp.WithMiddleware(ClientMiddleware(), ClientMiddleware()),
		khttp.WithTransport(regressionTransportFunc(func(r *http.Request) (*http.Response, error) {
			spanIds = r.Header.Values(pinpoint.HeaderSpanId)
			return regressionResponse(r), nil
		})))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tr := &eventCounter{Tracer: agent.NewSpanTracer("review", "/caller")}
	defer tr.EndSpan()
	var reply map[string]any
	if err = c.Invoke(pinpoint.NewContext(context.Background(), tr), "GET", "/test", nil, &reply); err != nil {
		t.Fatal(err)
	}
	if tr.events != 1 {
		t.Errorf("one call recorded as %d events", tr.events)
	}
	if len(spanIds) != 1 {
		t.Errorf("the callee got span ids %q", spanIds)
	}
}
