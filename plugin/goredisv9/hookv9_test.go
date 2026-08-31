package ppgoredisv9

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	processOperation  = "go-redis/v9.Process()"
	pipelineOperation = "go-redis/v9.ProcessPipeline()"
)

// recordingTracer captures what the hook records on a span event. A real
// tracer's recorders are write-only, so this stands in for one.
type recordingTracer struct {
	pinpoint.Tracer
	mu     sync.Mutex
	events []*recordedEvent
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *recordingTracer) IsSampled() bool { return true }

func (t *recordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, &recordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		operation:         operation,
		annotations:       map[int32]string{},
	})
	return t
}

func (t *recordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.last() }

func (t *recordingTracer) EndSpanEvent() { t.last().ended = true }

func (t *recordingTracer) last() *recordedEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.events[len(t.events)-1]
}

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

func cmd(name string) redis.Cmder {
	return redis.NewCmd(context.Background(), name, "key")
}

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		hook redis.Hook
		want string
	}{
		{"client options", NewHook(&redis.Options{Addr: "redis1:6379"}), "redis1:6379"},
		{"no client options", NewHook(nil), "unknown"},
		{"cluster options", NewClusterHook(&redis.ClusterOptions{Addrs: []string{"redis1:6379", "redis2:6379"}}), "redis1:6379,redis2:6379"},
		{"one cluster address", NewClusterHook(&redis.ClusterOptions{Addrs: []string{"redis1:6379"}}), "redis1:6379"},
		{"no cluster addresses", NewClusterHook(&redis.ClusterOptions{}), ""},
		{"no cluster options", NewClusterHook(nil), "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)

			require.NoError(t, tt.hook.ProcessHook(func(context.Context, redis.Cmder) error { return nil })(ctx, cmd("get")))
			assert.Equal(t, tt.want, tracer.last().endPoint)
		})
	}
}

// One command is one span event, wrapped around the next hook in the chain.
// The command's error has to reach both the caller and the span.
func TestProcessHook(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmdErr := errors.New("WRONGTYPE")
	inner := false
	err := h.ProcessHook(func(context.Context, redis.Cmder) error {
		inner = true
		return cmdErr
	})(ctx, cmd("get"))

	require.True(t, inner, "the next hook did not run")
	assert.ErrorIs(t, err, cmdErr, "the command's error must come back unchanged")

	require.Len(t, tracer.events, 1, "one command must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, processOperation, e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "REDIS", e.destination)
	assert.Equal(t, "redis1:6379", e.endPoint)
	assert.Equal(t, "get", e.annotations[pinpoint.AnnotationArgs0])
	assert.ErrorIs(t, e.err, cmdErr)
	assert.True(t, e.ended, "the span event was left open")
}

// A command that succeeded records no error, so a later failed one is not
// mistaken for it.
func TestProcessHook_SuccessfulCommand(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	err := NewHook(&redis.Options{Addr: "redis1:6379"}).
		ProcessHook(func(context.Context, redis.Cmder) error { return nil })(ctx, cmd("get"))

	require.NoError(t, err)
	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
	assert.True(t, tracer.events[0].ended)
}

// The next hook panicking must not leave the span event open: the deferred
// close is what keeps the surrounding request's event stack balanced.
func TestProcessHook_PanicClosesTheSpanEvent(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	assert.PanicsWithValue(t, "boom", func() {
		_ = NewHook(&redis.Options{Addr: "redis1:6379"}).
			ProcessHook(func(context.Context, redis.Cmder) error { panic("boom") })(ctx, cmd("get"))
	})

	require.Len(t, tracer.events, 1)
	assert.True(t, tracer.events[0].ended, "a panicking command left the span event open")
}

// A pipeline is one round trip, so it is one span event listing every command
// in it.
func TestProcessPipelineHook(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmds := []redis.Cmder{cmd("set"), cmd("get"), cmd("del")}
	require.NoError(t, h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })(ctx, cmds))

	require.Len(t, tracer.events, 1, "a pipeline is one round trip, so one span event")
	e := tracer.events[0]
	assert.Equal(t, pipelineOperation, e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "set, get, del", e.annotations[pinpoint.AnnotationArgs0])
	assert.NoError(t, e.err)
	assert.True(t, e.ended, "the span event was left open")
}

// A failed pipeline records the error on its single event.
func TestProcessPipelineHook_Error(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	want := errors.New("connection reset")
	err := NewHook(&redis.Options{Addr: "redis1:6379"}).
		ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return want })(ctx, []redis.Cmder{cmd("get")})

	assert.ErrorIs(t, err, want)
	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, want)
}

func Test_cmdName(t *testing.T) {
	assert.Equal(t, "", cmdName(nil))
	assert.Equal(t, "", cmdName([]redis.Cmder{}))
	assert.Equal(t, "get", cmdName([]redis.Cmder{cmd("get")}))
	assert.Equal(t, "set, get", cmdName([]redis.Cmder{cmd("set"), cmd("get")}))
	assert.Equal(t, "set, get, del", cmdName([]redis.Cmder{cmd("set"), cmd("get"), cmd("del")}))
}

// The hook is registered on the client, so it runs for every command the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine, so the hook has to step aside and still run the chain.
func TestHooks_IgnoreUnsampledCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmdErr := errors.New("WRONGTYPE")
			inner := 0

			assert.ErrorIs(t, h.ProcessHook(func(context.Context, redis.Cmder) error {
				inner++
				return cmdErr
			})(tt.ctx, cmd("get")), cmdErr)

			assert.ErrorIs(t, h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error {
				inner++
				return cmdErr
			})(tt.ctx, []redis.Cmder{cmd("get")}), cmdErr)

			assert.Equal(t, 2, inner, "the next hook must still run for an untraced command")
		})
	}
}

// Dialing is not traced, but the hook still sits in the chain: it has to pass
// the connection and any dial error straight through.
func TestDialHook_PassesThrough(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	want := errors.New("connection refused")

	var gotNetwork, gotAddr string
	conn, err := h.DialHook(func(ctx context.Context, network, addr string) (net.Conn, error) {
		gotNetwork, gotAddr = network, addr
		return nil, want
	})(context.Background(), "tcp", "redis1:6379")

	assert.Nil(t, conn, "DialHook returned a connection along with an error")
	assert.ErrorIs(t, err, want)
	assert.Equal(t, "tcp", gotNetwork)
	assert.Equal(t, "redis1:6379", gotAddr)
}

// A successful dial has to come back to go-redis unchanged, or the client
// never gets its connection.
func TestDialHook_ReturnsTheConnection(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	got, err := NewHook(&redis.Options{Addr: "redis1:6379"}).
		DialHook(func(context.Context, string, string) (net.Conn, error) { return client, nil })(
		context.Background(), "tcp", "redis1:6379")

	require.NoError(t, err)
	assert.Equal(t, client, got)
}

// One hook serves every connection of a shared client, so concurrent commands
// through it must stay race-free. Run under -race.
func TestHooks_ConcurrentCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	process := h.ProcessHook(func(context.Context, redis.Cmder) error { return nil })
	pipeline := h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine carries its own tracer, as each request would.
			tracer := newRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				assert.NoError(t, process(ctx, cmd("get")))
				assert.NoError(t, pipeline(ctx, []redis.Cmder{cmd("set"), cmd("get")}))
			}
			assert.Len(t, tracer.events, 50)
		}()
	}
	wg.Wait()
}
