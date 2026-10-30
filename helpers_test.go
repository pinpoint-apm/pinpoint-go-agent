package pinpoint

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// IsInjected is what a client plugin asks before starting its event, so it has
// to say "injected" for exactly the headers Inject writes - by presence, since
// the agent's own injected header may carry an empty value.
func TestIsInjected(t *testing.T) {
	for _, tt := range []struct {
		name string
		hdr  http.Header
		want bool
	}{
		{"nothing", http.Header{}, false},
		{"trace id", http.Header{"Pinpoint-Traceid": {"app^1^2"}}, true},
		{"empty trace id is still a context", http.Header{"Pinpoint-Traceid": {""}}, true},
		{"unsampled marker", http.Header{"Pinpoint-Sampled": {"s0"}}, true},
		{"other pinpoint header alone", http.Header{"Pinpoint-Host": {"db"}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsInjected(HttpHeaderReader(tt.hdr)))
		})
	}
}

// IsInjectedBy tells a context the tracer's own span wrote, which a client
// plugin leaves to travel alone, from another span's - a proxy forwarding the
// headers its own caller sent - which it clears and replaces with its own.
func TestIsInjectedBy(t *testing.T) {
	a := newTestAgent(defaultConfig())
	s := newSampledSpan(a, "op", "/rpc")
	s.NewSpanEvent("call")
	defer s.EndSpanEvent()

	own := http.Header{}
	s.Inject(HttpHeaderWriter(own))
	assert.True(t, IsInjectedBy(s, HttpHeaderReader(own)), "the span's own injection")
	assert.True(t, IsInjectedBy(NestedTracer(s), HttpHeaderReader(own)), "the same span through a nested view")

	// What this span's caller sent it: the span's own id as the callee's, the
	// caller's as the parent.
	forwarded := http.Header{}
	forwarded.Set(HeaderTraceId, s.txId.String())
	forwarded.Set(HeaderSpanId, strconv.FormatInt(s.spanId, 10))
	forwarded.Set(HeaderParentSpanId, "12345")
	assert.False(t, IsInjectedBy(s, HttpHeaderReader(forwarded)), "a context forwarded from the caller")

	unsampled := newUnSampledSpan(a, "/rpc")
	assert.True(t, IsInjectedBy(unsampled, HttpHeaderReader(http.Header{"Pinpoint-Sampled": {"s0"}})),
		"an unsampled span writes the marker alone")
	assert.False(t, IsInjectedBy(unsampled, HttpHeaderReader(forwarded)), "a sampled context under an unsampled span")
	assert.True(t, IsInjectedBy(NoopTracer(), HttpHeaderReader(forwarded)), "a no-op tracer injects nothing to replace it with")
}

// IsInjectedHeader names exactly what Inject may write, in any case: a carrier
// holding another span's context is cleared of these before the injection.
func TestIsInjectedHeader(t *testing.T) {
	for _, key := range []string{HeaderTraceId, "pinpoint-traceid", "Pinpoint-Pspanid", HeaderSampled, HeaderHost, HeaderParentServiceName} {
		assert.True(t, IsInjectedHeader(key), key)
	}
	for _, key := range []string{"Pinpoint-Custom", "X-Request-Id", ""} {
		assert.False(t, IsInjectedHeader(key), key)
	}
}

// OnTracerGoroutine tells the goroutine a span's call stack runs on from any
// other, and says yes whenever it cannot tell.
func TestOnTracerGoroutine(t *testing.T) {
	assert.True(t, OnTracerGoroutine(NoopTracer()), "not a tracer the agent tracks")

	s := newSampledSpan(newTestAgent(defaultConfig()), "op", "/rpc")
	assert.True(t, OnTracerGoroutine(s), "no event opened yet")
	s.NewSpanEvent("call")
	defer s.EndSpanEvent()
	assert.True(t, OnTracerGoroutine(s))
	assert.True(t, OnTracerGoroutine(NestedTracer(s)))

	other := make(chan bool)
	go func() { other <- OnTracerGoroutine(s) }()
	assert.Equal(t, goIdOffset == 0, <-other, "another goroutine, where goroutine ids are readable")
}

// A request's goroutine is known from its start, before any event: a library
// callback that runs elsewhere first - a gocql speculative attempt, a go-redis
// AutoPipeliner command - must not be taken for it and claim the span.
func TestOnTracerGoroutine_PinnedAtRequestStart(t *testing.T) {
	if goIdOffset <= 0 {
		t.Skip("goroutine ids are not readable")
	}
	s := newSampledSpan(newTestAgent(defaultConfig()), "op", "/rpc")
	s.extract(&noopDistributedTracingContextReader{})

	other := make(chan bool)
	go func() {
		on := OnTracerGoroutine(s)
		if on {
			s.NewSpanEvent("callback").EndSpanEvent()
		}
		other <- on
	}()
	assert.False(t, <-other, "a callback off the request's goroutine before any event")
	assert.True(t, OnTracerGoroutine(s), "the request's own goroutine")
}

// RemoteHost strips a port only when there is one, keeps a bare IP, and falls
// back to loopback for a peer that is no network address.
func TestRemoteHost(t *testing.T) {
	for addr, want := range map[string]string{
		"10.0.0.1:12345": "10.0.0.1",
		"[::1]:8000":     "::1",
		"10.0.0.1":       "10.0.0.1",
		"::1":            "::1",
		"/tmp/grpc.sock": "127.0.0.1",
		"":               "127.0.0.1",
	} {
		assert.Equal(t, want, RemoteHost(addr), "RemoteHost(%q)", addr)
	}
}

// AnnotationList names every item up to the cap and counts the rest, so an
// annotation cannot grow with the caller's batch.
func TestAnnotationList(t *testing.T) {
	item := func(i int) string { return "c" + strconv.Itoa(i) }

	assert.Equal(t, "", AnnotationList(0, item))
	assert.Equal(t, "c0", AnnotationList(1, item))
	assert.Equal(t, "c0, c1, c2", AnnotationList(3, item))

	atCap := AnnotationList(maxAnnotationListItems, item)
	assert.Equal(t, maxAnnotationListItems, strings.Count(atCap, "c"), "a batch at the cap is listed whole")
	assert.NotContains(t, atCap, "more)")

	over := AnnotationList(maxAnnotationListItems+3, item)
	assert.True(t, strings.HasSuffix(over, ", ...(3 more)"), "got %q", over)
	assert.Equal(t, len(atCap)+len(", ...(3 more)"), len(over), "the cap names the same items and counts the rest")
}
