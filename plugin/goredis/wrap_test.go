package ppgoredis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WithContext must hand each request its own copy: the wrapped client is
// shared, and rebinding it in place raced the field write and recorded one
// request's commands on another request's tracer. Run under -race.
func TestClient_WithContextReturnsCopy(t *testing.T) {
	rc := NewClient(&redis.Options{Addr: "localhost:1"})
	orig := rc.Client

	c := rc.WithContext(context.Background())
	assert.NotSame(t, rc, c, "WithContext returned the shared wrapper, want a copy")
	assert.Same(t, orig, rc.Client, "WithContext mutated the shared wrapper")
	assert.Equal(t, rc.endpoint, c.endpoint, "the copy must keep the endpoint")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				rc.WithContext(context.Background())
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
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)        { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string)     { e.endPoint = endPoint }
func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// One command is one span event, wrapped around go-redis's own process
// function. The command's error has to reach both the caller and the span.
func Test_process(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	cmdErr := errors.New("WRONGTYPE")
	inner := false
	err := process(ctx, "redis1:6379")(func(redis.Cmder) error {
		inner = true
		return cmdErr
	})(redis.NewCmd("get", "key"))

	require.True(t, inner, "go-redis's own process function did not run")
	assert.ErrorIs(t, err, cmdErr, "the command's error must come back unchanged")

	require.Len(t, tracer.events, 1, "one command must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, "go-redis.Process()", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "REDIS", e.destination)
	assert.Equal(t, "redis1:6379", e.endPoint)
	assert.Equal(t, "get", e.annotations[pinpoint.AnnotationArgs0])
	assert.ErrorIs(t, e.err, cmdErr)
	assert.True(t, e.ended, "the span event was left open")
}

// A command that succeeded records no error, so a later failed one is not
// mistaken for it.
func Test_processSuccessfulCommand(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	err := process(ctx, "redis1:6379")(func(redis.Cmder) error { return nil })(redis.NewCmd("get", "key"))

	require.NoError(t, err)
	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
	assert.True(t, tracer.events[0].ended)
}

// go-redis's own process function panicking must not leave the span event
// open: the deferred close is what keeps the request's event stack balanced.
func Test_processPanicClosesTheSpanEvent(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	assert.PanicsWithValue(t, "boom", func() {
		_ = process(ctx, "redis1:6379")(func(redis.Cmder) error { panic("boom") })(redis.NewCmd("get", "key"))
	})

	require.Len(t, tracer.events, 1)
	assert.True(t, tracer.events[0].ended, "a panicking command left the span event open")
}

// A pipeline is one round trip, so it is one span event listing every command
// in it.
func Test_processPipeline(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	cmds := []redis.Cmder{redis.NewCmd("set", "key"), redis.NewCmd("get", "key")}
	require.NoError(t, processPipeline(ctx, "redis1:6379")(func([]redis.Cmder) error { return nil })(cmds))

	require.Len(t, tracer.events, 1, "a pipeline is one round trip, so one span event")
	e := tracer.events[0]
	assert.Equal(t, "go-redis.ProcessPipeline()", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "redis1:6379", e.endPoint)
	assert.Equal(t, "set, get", e.annotations[pinpoint.AnnotationArgs0])
	assert.NoError(t, e.err)
	assert.True(t, e.ended, "the span event was left open")
}

// A failed pipeline records the error on its single event.
func Test_processPipelineError(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	want := errors.New("connection reset")
	err := processPipeline(ctx, "redis1:6379")(func([]redis.Cmder) error { return want })(
		[]redis.Cmder{redis.NewCmd("get", "key")})

	assert.ErrorIs(t, err, want)
	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, want)
}

func Test_cmdName(t *testing.T) {
	assert.Equal(t, "", cmdName(nil))
	assert.Equal(t, "", cmdName([]redis.Cmder{}))
	assert.Equal(t, "get", cmdName([]redis.Cmder{redis.NewCmd("get", "key")}))
	assert.Equal(t, "set, get",
		cmdName([]redis.Cmder{redis.NewCmd("set", "key"), redis.NewCmd("get", "key")}))
	assert.Equal(t, "set, get, del",
		cmdName([]redis.Cmder{redis.NewCmd("set", "key"), redis.NewCmd("get", "key"), redis.NewCmd("del", "key")}))
}

// The wrappers are installed on the shared client, so they run for every
// command the application makes - including those from code that never started
// a span. Recording those would unbalance the span-event stack of whatever ran
// next on that goroutine.
func Test_processIgnoresUnsampledCommands(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			cmdErr := errors.New("WRONGTYPE")
			inner := 0

			assert.ErrorIs(t, process(tt.ctx, "redis1:6379")(func(redis.Cmder) error {
				inner++
				return cmdErr
			})(redis.NewCmd("get", "key")), cmdErr)

			assert.ErrorIs(t, processPipeline(tt.ctx, "redis1:6379")(func([]redis.Cmder) error {
				inner++
				return cmdErr
			})([]redis.Cmder{redis.NewCmd("get", "key")}), cmdErr)

			assert.Equal(t, 2, inner, "go-redis's own process function must still run for an untraced command")
			assert.Empty(t, tracer.events, "an untraced command must not record a span event")
		})
	}
}

// A cluster spans several nodes, so the endpoint has to name all of them, and
// WithContext has to copy for the same reason the single-node client does.
func TestClusterClient_WithContextReturnsCopy(t *testing.T) {
	// Closed ports with short timeouts: the client is never used, only wrapped.
	cc := NewClusterClient(&redis.ClusterOptions{
		Addrs:       []string{"127.0.0.1:1", "127.0.0.2:1"},
		DialTimeout: time.Millisecond,
		MaxRetries:  -1,
	})
	t.Cleanup(func() { _ = cc.Close() })
	orig := cc.ClusterClient

	assert.Equal(t, "127.0.0.1:1,127.0.0.2:1", cc.endpoint,
		"a cluster endpoint must name every node the client knows")

	c := cc.WithContext(context.Background())
	assert.NotSame(t, cc, c, "WithContext returned the shared wrapper, want a copy")
	assert.Same(t, orig, cc.ClusterClient, "WithContext mutated the shared wrapper")
	assert.Equal(t, cc.endpoint, c.endpoint, "the copy must keep the endpoint")
}
