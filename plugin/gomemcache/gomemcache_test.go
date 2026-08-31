package ppgomemcache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithContext must hand each request its own copy and keep the shared
// receiver's tracer rebind race-free. Run under -race.
func TestClient_WithContextIsConcurrencySafe(t *testing.T) {
	mc := NewClient("localhost:1")

	c := mc.WithContext(context.Background())
	assert.NotSame(t, mc, c, "WithContext returned the shared wrapper, want a copy")
	assert.Same(t, mc.Client, c.Client, "the copy must share the underlying memcache client")
	assert.Equal(t, mc.endpoint, c.endpoint, "the copy must keep the endpoint")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				c := mc.WithContext(context.Background())
				_, _ = c.Get("foo") // no server: errors fast, still records the span event
			}
		}()
	}
	wg.Wait()
}

// recordingTracer captures what the wrapper records on a span event. A real
// tracer's recorders are write-only, so this stands in for one.
type recordingTracer struct {
	pinpoint.Tracer
	events []*recordedEvent
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *recordingTracer) IsSampled() bool { return true }

func (t *recordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.events = append(t.events, &recordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	})
	return t
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.last() }

func (t *recordingTracer) EndSpanEvent() { t.last().ended = true }

func (t *recordingTracer) last() *recordedEvent { return t.events[len(t.events)-1] }

type recordedEvent struct {
	pinpoint.SpanEventRecorder
	operation   string
	serviceType int32
	destination string
	endPoint    string
	err         error
	annotations map[int32]string
	start, end  time.Time
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)         { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)         { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string)      { e.endPoint = endPoint }
func (e *recordedEvent) SetError(err error, _ ...string)  { e.err = err }
func (e *recordedEvent) FixDuration(start, end time.Time) { e.start, e.end = start, end }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// Every wrapped operation has to produce exactly one span event named after it
// and annotated with the key it touched - that key is what makes a memcached
// span actionable. No server is running, so every call fails: the recorded
// error proves the failure reaches the span instead of being swallowed.
func TestClient_RecordsEveryOperation(t *testing.T) {
	item := func() *memcache.Item { return &memcache.Item{Key: "foo", Value: []byte("bar")} }

	for _, tt := range []struct {
		operation string
		key       string
		call      func(*Client) error
	}{
		{"gomemcache.Add()", "foo", func(c *Client) error { return c.Add(item()) }},
		{"gomemcache.Set()", "foo", func(c *Client) error { return c.Set(item()) }},
		{"gomemcache.Replace()", "foo", func(c *Client) error { return c.Replace(item()) }},
		{"gomemcache.Get()", "foo", func(c *Client) error { _, err := c.Get("foo"); return err }},
		{"gomemcache.GetMulti()", "foo,bar", func(c *Client) error {
			_, err := c.GetMulti([]string{"foo", "bar"})
			return err
		}},
		{"gomemcache.Delete()", "foo", func(c *Client) error { return c.Delete("foo") }},
		{"gomemcache.Increment()", "foo", func(c *Client) error { _, err := c.Increment("foo", 1); return err }},
		{"gomemcache.Decrement()", "foo", func(c *Client) error { _, err := c.Decrement("foo", 1); return err }},
		{"gomemcache.CompareAndSwap()", "foo", func(c *Client) error { return c.CompareAndSwap(item()) }},
		{"gomemcache.Touch()", "foo", func(c *Client) error { return c.Touch("foo", 30) }},
		{"gomemcache.Ping()", "", func(c *Client) error { return c.Ping() }},
		{"gomemcache.DeleteAll()", "", func(c *Client) error { return c.DeleteAll() }},
		{"gomemcache.FlushAll()", "", func(c *Client) error { return c.FlushAll() }},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			tracer := newRecordingTracer()
			// Port 1 is closed, so every call fails immediately.
			c := NewClient("localhost:1").WithContext(pinpoint.NewContext(context.Background(), tracer))

			err := tt.call(c)

			require.Error(t, err, "the call unexpectedly succeeded against a closed port")

			require.Len(t, tracer.events, 1, "one operation must produce exactly one span event")
			e := tracer.events[0]
			assert.Equal(t, tt.operation, e.operation)
			assert.Equal(t, int32(pinpoint.ServiceTypeMemcached), e.serviceType)
			assert.Equal(t, "MEMCACHED", e.destination)
			assert.Equal(t, "localhost:1", e.endPoint)
			assert.Equal(t, tt.key, e.annotations[pinpoint.AnnotationArgs0], "key annotation")
			assert.Error(t, e.err, "the failure was not recorded on the span event")
			assert.False(t, e.end.Before(e.start), "duration = %v..%v, want a non-negative span", e.start, e.end)
			assert.True(t, e.ended, "the span event was left open")
		})
	}
}

// The endpoint identifies the memcached pool on the server map, so a client
// built from several servers has to record all of them.
func TestNewClient_EndpointJoinsEveryServer(t *testing.T) {
	tracer := newRecordingTracer()
	c := NewClient("127.0.0.1:1", "127.0.0.2:1").WithContext(pinpoint.NewContext(context.Background(), tracer))

	_, _ = c.Get("foo")

	assert.Equal(t, "127.0.0.1:1,127.0.0.2:1", tracer.last().endPoint)
}

// A client built from no server at all still has to record an endpoint field
// rather than crash the first call.
func TestNewClient_WithoutAServer(t *testing.T) {
	tracer := newRecordingTracer()
	c := NewClient().WithContext(pinpoint.NewContext(context.Background(), tracer))

	// Ping over an empty server list has nothing to reach and reports success.
	assert.NoError(t, c.Ping())
	require.Len(t, tracer.events, 1, "the call must still be traced")
	assert.Equal(t, "", tracer.last().endPoint)
}

// The wrapper replaces the application's client, so every operation must still
// run when there is no span to record it on - and must record nothing, or the
// span-event stack of whatever runs next on that goroutine unbalances.
func TestClient_RecordsNothingWithoutASampledTracer(t *testing.T) {
	// A client never given a context starts on the noop tracer.
	_, err := NewClient("localhost:1").Get("foo")
	assert.Error(t, err, "the call unexpectedly succeeded against a closed port")

	for _, ctx := range []context.Context{
		context.Background(),
		pinpoint.NewContext(context.Background(), pinpoint.NoopTracer()),
	} {
		c := NewClient("localhost:1").WithContext(ctx)
		require.False(t, c.currentTracer().IsSampled(), "an untraced context produced a sampled tracer")

		_, err := c.Get("foo")
		assert.Error(t, err, "the call unexpectedly succeeded against a closed port")
	}
}

// WithContext also rebinds the shared receiver, so the tracer the next call
// records against is the one bound last.
func TestClient_WithContextRebindsTheReceiver(t *testing.T) {
	mc := NewClient("localhost:1")

	first := newRecordingTracer()
	mc.WithContext(pinpoint.NewContext(context.Background(), first))
	_, _ = mc.Get("foo")

	second := newRecordingTracer()
	mc.WithContext(pinpoint.NewContext(context.Background(), second))
	_, _ = mc.Get("bar")

	require.Len(t, first.events, 1, "the first tracer must keep only its own call")
	require.Len(t, second.events, 1, "the rebound tracer must record the next call")
	assert.Equal(t, "foo", first.events[0].annotations[pinpoint.AnnotationArgs0])
	assert.Equal(t, "bar", second.events[0].annotations[pinpoint.AnnotationArgs0])
}

// A copy handed to one request must keep recording on its own tracer even
// after the shared client is rebound for another.
func TestClient_CopyKeepsItsOwnTracer(t *testing.T) {
	mc := NewClient("localhost:1")

	mine := newRecordingTracer()
	c := mc.WithContext(pinpoint.NewContext(context.Background(), mine))

	// Another request rebinds the shared client.
	mc.WithContext(pinpoint.NewContext(context.Background(), newRecordingTracer()))

	_, _ = c.Get("foo")

	require.Len(t, mine.events, 1, "the copy recorded on someone else's tracer")
	assert.Equal(t, "foo", mine.events[0].annotations[pinpoint.AnnotationArgs0])
}
