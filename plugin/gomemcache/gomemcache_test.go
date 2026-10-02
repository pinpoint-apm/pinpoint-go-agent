package ppgomemcache

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/bradfitz/gomemcache/memcache"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
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
			tracer := pptest.NewRecordingTracer()
			addr := closedAddr(t)
			c := NewClient(addr).WithContext(pinpoint.NewContext(context.Background(), tracer))

			err := tt.call(c)

			require.Error(t, err, "the call unexpectedly succeeded against a closed port")

			require.Len(t, tracer.Events, 1, "one operation must produce exactly one span event")
			e := tracer.Events[0]
			assert.Equal(t, tt.operation, e.Operation)
			assert.Equal(t, int32(pinpoint.ServiceTypeMemcached), e.ServiceType)
			assert.Equal(t, "MEMCACHED", e.Destination)
			assert.Equal(t, addr, e.EndPoint)
			assert.Equal(t, tt.key, e.Strings[pinpoint.AnnotationArgs0], "key annotation")
			assert.Error(t, e.Err, "the failure was not recorded on the span event")
			assert.False(t, e.End.Before(e.Start), "duration = %v..%v, want a non-negative span", e.Start, e.End)
			assert.True(t, e.Ended, "the span event was left open")
		})
	}
}

// The endpoint identifies the memcached pool on the server map, so a client
// built from several servers has to record all of them.
func TestNewClient_EndpointJoinsEveryServer(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	c := NewClient("127.0.0.1:1", "127.0.0.2:1").WithContext(pinpoint.NewContext(context.Background(), tracer))

	_, _ = c.Get("foo")

	assert.Equal(t, "127.0.0.1:1,127.0.0.2:1", tracer.Last().EndPoint)
}

// WrapClient wraps a client created elsewhere exactly as NewClient wraps the
// one it creates: the endpoint given is recorded and, until WithContext, the
// wrapper records nothing.
func TestWrapClient(t *testing.T) {
	addr := closedAddr(t)
	raw := memcache.New(addr)
	c := WrapClient(raw, "cache-pool")
	assert.Same(t, raw, c.Client, "the wrapper must keep the client it was given")
	assert.False(t, c.currentTracer().IsSampled(), "a wrapped client starts without a tracer")

	tracer := pptest.NewRecordingTracer()
	_, _ = c.WithContext(pinpoint.NewContext(context.Background(), tracer)).Get("foo")

	require.Len(t, tracer.Events, 1)
	assert.Equal(t, "cache-pool", tracer.Last().EndPoint)
}

// A client built from no server at all still has to record an endpoint field
// rather than crash the first call.
func TestNewClient_WithoutAServer(t *testing.T) {
	tracer := pptest.NewRecordingTracer()
	c := NewClient().WithContext(pinpoint.NewContext(context.Background(), tracer))

	// Ping over an empty server list has nothing to reach and reports success.
	assert.NoError(t, c.Ping())
	require.Len(t, tracer.Events, 1, "the call must still be traced")
	assert.Equal(t, "", tracer.Last().EndPoint)
}

// WithContext also rebinds the shared receiver, so the tracer the next call
// records against is the one bound last.
func TestClient_WithContextRebindsTheReceiver(t *testing.T) {
	mc := NewClient("localhost:1")

	first := pptest.NewRecordingTracer()
	mc.WithContext(pinpoint.NewContext(context.Background(), first))
	_, _ = mc.Get("foo")

	second := pptest.NewRecordingTracer()
	mc.WithContext(pinpoint.NewContext(context.Background(), second))
	_, _ = mc.Get("bar")

	require.Len(t, first.Events, 1, "the first tracer must keep only its own call")
	require.Len(t, second.Events, 1, "the rebound tracer must record the next call")
	assert.Equal(t, "foo", first.Events[0].Strings[pinpoint.AnnotationArgs0])
	assert.Equal(t, "bar", second.Events[0].Strings[pinpoint.AnnotationArgs0])
}

// A copy handed to one request must keep recording on its own tracer even
// after the shared client is rebound for another.
func TestClient_CopyKeepsItsOwnTracer(t *testing.T) {
	mc := NewClient("localhost:1")

	mine := pptest.NewRecordingTracer()
	c := mc.WithContext(pinpoint.NewContext(context.Background(), mine))

	// Another request rebinds the shared client.
	mc.WithContext(pinpoint.NewContext(context.Background(), pptest.NewRecordingTracer()))

	_, _ = c.Get("foo")

	require.Len(t, mine.Events, 1, "the copy recorded on someone else's tracer")
	assert.Equal(t, "foo", mine.Events[0].Strings[pinpoint.AnnotationArgs0])
}

// closedAddr returns a loopback address with nothing listening on it: the port
// is bound and released, so a connection is refused right away. A hard-coded
// port like :1 only works while nothing serves it and while the sandbox
// allows the dial at all, which is not something a test should rest on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// Client and its embedded *memcache.Client are exported, so a literal
// construction skips the store WrapClient makes; the first operation then
// dereferenced a nil box.
func TestClient_BuiltAsALiteralHasANoopTracer(t *testing.T) {
	c := &Client{Client: memcache.New("localhost:11211")}
	assert.NotPanics(t, func() {
		assert.Equal(t, pinpoint.NoopTracer(), c.currentTracer())
	})
}
