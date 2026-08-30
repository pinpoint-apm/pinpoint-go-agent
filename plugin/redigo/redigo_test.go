package ppredigo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gomodule/redigo/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
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
		if _, err := c.Receive(); err != nil {
			t.Errorf("Receive() = %v", err)
		}
	}()

	for i := 0; i < 100; i++ {
		WithContext(c, context.Background())
		if err := c.Send("PING"); err != nil {
			t.Fatalf("Send() = %v", err)
		}
	}
	close(fake.recvCh)
	<-done
}

// A base connection without the optional interfaces must yield redigo's own
// errors instead of a nil-interface panic.
func Test_wrappedConn_MissingOptionalInterfaces(t *testing.T) {
	c := wrapConn(&fakeRedisConn{}, "localhost").(*wrappedConn)

	if _, err := c.DoWithTimeout(0, "PING"); !errors.Is(err, errTimeoutNotSupported) {
		t.Errorf("DoWithTimeout() = %v, want errTimeoutNotSupported", err)
	}
	if _, err := c.ReceiveWithTimeout(0); !errors.Is(err, errTimeoutNotSupported) {
		t.Errorf("ReceiveWithTimeout() = %v, want errTimeoutNotSupported", err)
	}
	if _, err := c.DoContext(context.Background(), "PING"); !errors.Is(err, errContextNotSupported) {
		t.Errorf("DoContext() = %v, want errContextNotSupported", err)
	}
	if _, err := c.ReceiveContext(context.Background()); !errors.Is(err, errContextNotSupported) {
		t.Errorf("ReceiveContext() = %v, want errContextNotSupported", err)
	}
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

			if err := tt.call(c); !errors.Is(err, connErr) {
				t.Fatalf("%s = %v, want %v", tt.operation, err, connErr)
			}

			if len(tracer.events) != 1 {
				t.Fatalf("recorded %d span events, want 1", len(tracer.events))
			}
			e := tracer.events[0]
			if e.operation != tt.operation {
				t.Errorf("operation = %q, want %q", e.operation, tt.operation)
			}
			if e.serviceType != pinpoint.ServiceTypeRedis {
				t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeRedis)
			}
			if e.destination != "REDIS" {
				t.Errorf("destination = %q, want REDIS", e.destination)
			}
			if e.endPoint != "redis1" {
				t.Errorf("endpoint = %q, want %q", e.endPoint, "redis1")
			}
			if got, ok := e.annotations[pinpoint.AnnotationArgs0]; tt.cmd == "" {
				if ok {
					t.Errorf("command annotation = %q, want none", got)
				}
			} else if got != tt.cmd {
				t.Errorf("command annotation = %q, want %q", got, tt.cmd)
			}
			if !errors.Is(e.err, connErr) {
				t.Errorf("recorded error = %v, want %v", e.err, connErr)
			}
			if !e.ended {
				t.Error("the span event was left open")
			}
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

			if err := tt.call(c, pinpoint.NewContext(context.Background(), tracer)); err != nil {
				t.Fatal(err)
			}

			if len(tracer.events) != 1 {
				t.Fatalf("recorded %d span events, want 1", len(tracer.events))
			}
			if got := tracer.events[0].operation; got != tt.operation {
				t.Errorf("operation = %q, want %q", got, tt.operation)
			}
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
		if _, err := c.Receive(); err != nil {
			t.Errorf("Receive() = %v", err)
		}
	}()

	// The connection reaching Receive means its span event is already recorded
	// and the recording lock is held; the goroutine then parks until recvCh.
	<-fake.inReceive
	for i := 0; i < 10; i++ {
		if err := c.Send("PING"); err != nil {
			t.Fatalf("Send() = %v", err)
		}
	}
	if len(tracer.events) != 1 {
		t.Errorf("recorded %d span events while one was in flight, want 1", len(tracer.events))
	}

	close(fake.recvCh)
	<-done

	if !tracer.events[0].ended {
		t.Error("the receive span event was left open")
	}
	if tracer.events[0].operation != "redigo.Receive()" {
		t.Errorf("operation = %q, want %q", tracer.events[0].operation, "redigo.Receive()")
	}
}

// The endpoint is what puts the call on the right node of the server map, and
// the two Dial families derive it differently: a network address must carry a
// port, while a URL's authority may leave both parts implicit.
func Test_makeWrappedConn(t *testing.T) {
	if _, err := makeWrappedConn(&fakeRedisConn{}, "redis1:6379"); err != nil {
		t.Errorf("makeWrappedConn() = %v", err)
	}
	// redis.Dial requires host:port, so an address without one is a caller
	// error and must not produce a connection with a garbled endpoint.
	if _, err := makeWrappedConn(&fakeRedisConn{}, "redis1"); err == nil {
		t.Error("makeWrappedConn() accepted an address without a port")
	}
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
			if (err != nil) != tt.wantErr {
				t.Fatalf("makeWrappedConnURL() error = %v, want error = %v", err, tt.wantErr)
			}
			if got := c.(*wrappedConn).endpoint; got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

// WithContext is handed whatever redis.Conn the application has. A connection
// this package did not wrap has no context to bind, and must be ignored rather
// than crash the caller.
func TestWithContext_OnAnUnwrappedConn(t *testing.T) {
	WithContext(&fakeRedisConn{}, context.Background())
}
