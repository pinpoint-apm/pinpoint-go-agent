package ppredigo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRedisConn implements only the base redis.Conn interface - no
// ConnWithTimeout, no ConnWithContext.
type fakeRedisConn struct {
	recvCh    chan struct{}
	inReceive chan struct{}
}

var _ redis.Conn = (*fakeRedisConn)(nil)

func (f *fakeRedisConn) Close() error { return nil }
func (f *fakeRedisConn) Err() error   { return nil }
func (f *fakeRedisConn) Do(cmd string, args ...interface{}) (interface{}, error) {
	return nil, nil
}
func (f *fakeRedisConn) Send(cmd string, args ...interface{}) error { return nil }
func (f *fakeRedisConn) Flush() error                               { return nil }
func (f *fakeRedisConn) Receive() (interface{}, error) {
	if f.inReceive != nil {
		close(f.inReceive)
	}
	<-f.recvCh
	return nil, nil
}

// redigo supports one goroutine in Send/Flush concurrent with another blocked
// in Receive (pub/sub); the wrapper must not corrupt or race in that pattern.
// Run under -race.
func Test_wrappedConn_ConcurrentSendAndReceive(t *testing.T) {
	fake := &fakeRedisConn{recvCh: make(chan struct{})}
	c := wrapConn(fake, "localhost")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.Receive()
		assert.NoError(t, err)
	}()

	for i := 0; i < 100; i++ {
		WithContext(c, context.Background())
		require.NoError(t, c.Send("PING"))
	}
	close(fake.recvCh)
	<-done
}

// A base connection without the optional interfaces must yield redigo's own
// errors instead of a nil-interface panic.
func Test_wrappedConn_MissingOptionalInterfaces(t *testing.T) {
	c := wrapConn(&fakeRedisConn{}, "localhost").(*wrappedConn)

	_, err := c.DoWithTimeout(0, "PING")
	assert.ErrorIs(t, err, errTimeoutNotSupported)
	_, err = c.ReceiveWithTimeout(0)
	assert.ErrorIs(t, err, errTimeoutNotSupported)
	_, err = c.DoContext(context.Background(), "PING")
	assert.ErrorIs(t, err, errContextNotSupported)
	_, err = c.ReceiveContext(context.Background())
	assert.ErrorIs(t, err, errContextNotSupported)

	// The pass-through methods must reach the base connection either way.
	assert.NoError(t, c.Close())
	assert.NoError(t, c.Err())
	assert.NoError(t, c.Flush())
}

// capturingTracer records what the wrapper puts on a span event. A real
// tracer's recorders are write-only, so this stands in for one.
type capturingTracer struct {
	pinpoint.Tracer
	events []*capturedEvent
}

func newCapturingTracer() *capturingTracer {
	return &capturingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *capturingTracer) IsSampled() bool { return true }

func (t *capturingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.events = append(t.events, &capturedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	})
	return t
}

func (t *capturingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.last() }

func (t *capturingTracer) EndSpanEvent() { t.last().ended = true }

func (t *capturingTracer) last() *capturedEvent { return t.events[len(t.events)-1] }

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

// fullRedisConn implements the optional interfaces too, so the timeout and
// context operations reach the wrapper's recording path instead of returning
// "not supported".
type fullRedisConn struct {
	fakeRedisConn
	err error
}

var (
	_ redis.ConnWithTimeout = (*fullRedisConn)(nil)
	_ redis.ConnWithContext = (*fullRedisConn)(nil)
)

func (f *fullRedisConn) Do(string, ...interface{}) (interface{}, error) { return nil, f.err }
func (f *fullRedisConn) Send(string, ...interface{}) error              { return f.err }
func (f *fullRedisConn) Receive() (interface{}, error)                  { return nil, f.err }
func (f *fullRedisConn) DoWithTimeout(time.Duration, string, ...interface{}) (interface{}, error) {
	return nil, f.err
}
func (f *fullRedisConn) ReceiveWithTimeout(time.Duration) (interface{}, error) { return nil, f.err }
func (f *fullRedisConn) DoContext(context.Context, string, ...interface{}) (interface{}, error) {
	return nil, f.err
}
func (f *fullRedisConn) ReceiveContext(context.Context) (interface{}, error) { return nil, f.err }

// Every operation the wrapper intercepts has to produce one span event named
// after it. Commands carry the command name; the receive operations have none
// to report, and must not annotate an empty one - that reads as a command that
// ran without a name.
func Test_wrappedConn_RecordsEveryOperation(t *testing.T) {
	for _, tt := range []struct {
		operation string
		cmd       string
		call      func(redis.Conn) error
	}{
		{"redigo.Do()", "GET", func(c redis.Conn) error { _, err := c.Do("GET", "key"); return err }},
		{"redigo.Send()", "SET", func(c redis.Conn) error { return c.Send("SET", "key", "value") }},
		{"redigo.Receive()", "", func(c redis.Conn) error { _, err := c.Receive(); return err }},
		{"redigo.DoWithTimeout()", "GET", func(c redis.Conn) error {
			_, err := c.(redis.ConnWithTimeout).DoWithTimeout(time.Second, "GET", "key")
			return err
		}},
		{"redigo.ReceiveWithTimeout()", "", func(c redis.Conn) error {
			_, err := c.(redis.ConnWithTimeout).ReceiveWithTimeout(time.Second)
			return err
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			connErr := errors.New("connection reset")
			tracer := newCapturingTracer()
			c := wrapConn(&fullRedisConn{err: connErr}, "redis1")
			WithContext(c, pinpoint.NewContext(context.Background(), tracer))

			assert.ErrorIs(t, tt.call(c), connErr, "the connection's error must come back unchanged")

			require.Len(t, tracer.events, 1, "one operation must produce exactly one span event")
			e := tracer.events[0]
			assert.Equal(t, tt.operation, e.operation)
			assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
			assert.Equal(t, "REDIS", e.destination)
			assert.Equal(t, "redis1", e.endPoint)
			if tt.cmd == "" {
				assert.NotContains(t, e.annotations, pinpoint.AnnotationArgs0,
					"an operation with no command must not annotate an empty one")
			} else {
				assert.Equal(t, tt.cmd, e.annotations[pinpoint.AnnotationArgs0])
			}
			assert.ErrorIs(t, e.err, connErr)
			assert.True(t, e.ended, "the span event was left open")
		})
	}
}

// The context operations take their tracer from the call rather than from the
// connection, so a connection that was never given a context still records.
func Test_wrappedConn_ContextOperationsUseTheCallContext(t *testing.T) {
	for _, tt := range []struct {
		operation string
		call      func(redis.Conn, context.Context) error
	}{
		{"redigo.DoContext()", func(c redis.Conn, ctx context.Context) error {
			_, err := c.(redis.ConnWithContext).DoContext(ctx, "GET", "key")
			return err
		}},
		{"redigo.ReceiveContext()", func(c redis.Conn, ctx context.Context) error {
			_, err := c.(redis.ConnWithContext).ReceiveContext(ctx)
			return err
		}},
	} {
		t.Run(tt.operation, func(t *testing.T) {
			tracer := newCapturingTracer()
			c := wrapConn(&fullRedisConn{}, "redis1")

			require.NoError(t, tt.call(c, pinpoint.NewContext(context.Background(), tracer)))

			require.Len(t, tracer.events, 1)
			assert.Equal(t, tt.operation, tracer.events[0].operation)
			assert.True(t, tracer.events[0].ended, "the span event was left open")
		})
	}
}

// A pinpoint.Tracer is not goroutine-safe, and redigo lets one goroutine send
// while another is blocked in Receive. The operation that arrives second has
// to go untraced rather than interleave a second NewSpanEvent/EndSpanEvent
// pair into the first one's event stack. Run under -race.
func Test_wrappedConn_ConcurrentOperationGoesUntraced(t *testing.T) {
	fake := &fakeRedisConn{recvCh: make(chan struct{}), inReceive: make(chan struct{})}
	tracer := newCapturingTracer()
	c := wrapConn(fake, "redis1")
	WithContext(c, pinpoint.NewContext(context.Background(), tracer))

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.Receive()
		assert.NoError(t, err)
	}()

	// The connection reaching Receive means its span event is already recorded
	// and the recording lock is held; the goroutine then parks until recvCh.
	<-fake.inReceive
	for i := 0; i < 10; i++ {
		require.NoError(t, c.Send("PING"))
	}
	assert.Len(t, tracer.events, 1, "a second operation must go untraced while one is in flight")

	close(fake.recvCh)
	<-done

	assert.True(t, tracer.events[0].ended, "the receive span event was left open")
	assert.Equal(t, "redigo.Receive()", tracer.events[0].operation)
}

// The endpoint is what puts the call on the right node of the server map, and
// the two Dial families derive it differently: a network address must carry a
// port, while a URL's authority may leave both parts implicit.
func Test_makeWrappedConn(t *testing.T) {
	c := makeWrappedConn(&fakeRedisConn{}, "redis1:6379")
	assert.Equal(t, "redis1", c.(*wrappedConn).endpoint, "the port is not part of the endpoint")

	ipv6 := makeWrappedConn(&fakeRedisConn{}, "[::1]:6379")
	assert.Equal(t, "::1", ipv6.(*wrappedConn).endpoint)

	// A unix-socket address has no host:port shape, but by the time the
	// endpoint is derived redis.Dial has already connected: the split failing
	// must wrap the live connection instead of dropping it unclosed.
	sock := makeWrappedConn(&fakeRedisConn{}, "/var/run/redis.sock")
	assert.Equal(t, "/var/run/redis.sock", sock.(*wrappedConn).endpoint)
}

func Test_makeWrappedConnURL(t *testing.T) {
	for _, tt := range []struct {
		rawurl  string
		want    string
		wantErr bool
	}{
		{rawurl: "redis://redis1:6379", want: "redis1"},
		// redigo defaults the port, so a URL without one is a working dial and
		// must not be reported back as an error.
		{rawurl: "redis://redis1", want: "redis1"},
		{rawurl: "redis://:6379", want: "localhost"},
		{rawurl: "redis://", want: "localhost"},
		{rawurl: "redis://user:pw@redis1:6379/0", want: "redis1"},
		{rawurl: "redis://[::1]:6379", want: "::1"},
		{rawurl: "redis://redis1:6379/\x7f", want: "unknown", wantErr: true},
	} {
		t.Run(tt.rawurl, func(t *testing.T) {
			c, err := makeWrappedConnURL(&fakeRedisConn{}, tt.rawurl)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			require.NotNil(t, c, "a connection that is already open must be handed back either way")
			assert.Equal(t, tt.want, c.(*wrappedConn).endpoint)
		})
	}
}

// WithContext is handed whatever redis.Conn the application has. A connection
// this package did not wrap has no context to bind, and must be ignored rather
// than crash the caller.
func TestWithContext_OnAnUnwrappedConn(t *testing.T) {
	assert.NotPanics(t, func() { WithContext(&fakeRedisConn{}, context.Background()) })
	assert.NotPanics(t, func() { WithContext(nil, context.Background()) })
}

// The context a connection was given is what every non-context operation
// records against, so rebinding it has to take effect for the next command.
func Test_wrappedConn_WithContextRebinds(t *testing.T) {
	c := wrapConn(&fullRedisConn{}, "redis1")

	first := newCapturingTracer()
	WithContext(c, pinpoint.NewContext(context.Background(), first))
	_, err := c.Do("GET", "key")
	require.NoError(t, err)

	second := newCapturingTracer()
	WithContext(c, pinpoint.NewContext(context.Background(), second))
	_, err = c.Do("SET", "key", "value")
	require.NoError(t, err)

	require.Len(t, first.events, 1, "the first tracer must keep only its own command")
	require.Len(t, second.events, 1, "the rebound tracer must record the next command")
	assert.Equal(t, "GET", first.events[0].annotations[pinpoint.AnnotationArgs0])
	assert.Equal(t, "SET", second.events[0].annotations[pinpoint.AnnotationArgs0])
}

// A connection used without ever being given a context must still work: the
// wrapper starts out bound to a background context, which is untraced.
func Test_wrappedConn_WithoutAContext(t *testing.T) {
	tracer := newCapturingTracer()
	c := wrapConn(&fullRedisConn{}, "redis1")

	_, err := c.Do("GET", "key")
	require.NoError(t, err)
	require.NoError(t, c.Send("SET", "key", "value"))

	assert.Empty(t, tracer.events, "an untraced connection must not record a span event")
}
