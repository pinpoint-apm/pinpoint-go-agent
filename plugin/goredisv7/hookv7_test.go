package ppgoredisv7

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/go-redis/redis/v7"
	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	processOperation  = "go-redis/v7.Process()"
	pipelineOperation = "go-redis/v7.ProcessPipeline()"
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

func cmd(name string, err error) redis.Cmder {
	c := redis.NewCmd(name, "key")
	if err != nil {
		c.SetErr(err)
	}
	return c
}

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

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
			_, err := tt.hook.BeforeProcess(ctx, cmd("get", nil))
			require.NoError(t, err)
			require.NoError(t, tt.hook.AfterProcess(ctx, cmd("get", nil)))

			assert.Equal(t, tt.want, tracer.last().endPoint)
		})
	}
}

// One command is one span event: opened before the call, closed after it with
// the command name and whatever the server said.
func TestHook_Process(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmdErr := errors.New("WRONGTYPE")
	got, err := h.BeforeProcess(ctx, cmd("get", nil))
	require.NoError(t, err)
	assert.Equal(t, ctx, got, "BeforeProcess replaced the context")
	require.NoError(t, h.AfterProcess(ctx, cmd("get", cmdErr)))

	require.Len(t, tracer.events, 1, "one command must produce exactly one span event")
	e := tracer.events[0]
	assert.Equal(t, processOperation, e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "REDIS", e.destination)
	assert.Equal(t, "redis1:6379", e.endPoint)
	assert.Equal(t, "get", e.annotations[pinpoint.AnnotationArgs0])
	assert.ErrorIs(t, e.err, cmdErr, "the command's own error must reach the span event")
	assert.True(t, e.ended, "the span event was left open")
}

// A command that succeeded records no error, so a later failed one is not
// mistaken for it.
func TestHook_ProcessSuccessfulCommand(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	_, err := h.BeforeProcess(ctx, cmd("get", nil))
	require.NoError(t, err)
	require.NoError(t, h.AfterProcess(ctx, cmd("get", nil)))

	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
	assert.True(t, tracer.events[0].ended)
}

// A pipeline is one round trip, so it is one span event listing every command
// in it, failed by the first command that failed.
func TestHook_ProcessPipeline(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmdErr := errors.New("WRONGTYPE")
	cmds := []redis.Cmder{cmd("set", nil), cmd("get", cmdErr), cmd("del", errors.New("second"))}

	got, err := h.BeforeProcessPipeline(ctx, cmds)
	require.NoError(t, err)
	assert.Equal(t, ctx, got, "BeforeProcessPipeline replaced the context")
	require.NoError(t, h.AfterProcessPipeline(ctx, cmds))

	require.Len(t, tracer.events, 1, "a pipeline is one round trip, so one span event")
	e := tracer.events[0]
	assert.Equal(t, pipelineOperation, e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "set, get, del", e.annotations[pinpoint.AnnotationArgs0])
	assert.ErrorIs(t, e.err, cmdErr, "the pipeline must be failed by its first failure")
	assert.True(t, e.ended, "the span event was left open")
}

func Test_cmdName(t *testing.T) {
	assert.Equal(t, "", cmdName(nil))
	assert.Equal(t, "", cmdName([]redis.Cmder{}))
	assert.Equal(t, "get", cmdName([]redis.Cmder{cmd("get", nil)}))
	assert.Equal(t, "set, get", cmdName([]redis.Cmder{cmd("set", nil), cmd("get", nil)}))
	assert.Equal(t, "set, get, del",
		cmdName([]redis.Cmder{cmd("set", nil), cmd("get", nil), cmd("del", nil)}))
}

// A pipeline fails as a whole on its first failed command; reporting a later
// one would point at the wrong command in the trace.
func Test_pipeError(t *testing.T) {
	assert.NoError(t, pipeError(nil))
	assert.NoError(t, pipeError([]redis.Cmder{cmd("set", nil), cmd("get", nil)}))

	first := errors.New("first")
	assert.ErrorIs(t,
		pipeError([]redis.Cmder{cmd("set", nil), cmd("get", first), cmd("del", errors.New("second"))}),
		first)
	assert.ErrorIs(t, pipeError([]redis.Cmder{cmd("set", first)}), first)
}

// The hook is registered on the client, so it runs for every command the
// application makes - including those from code that never started a span.
// Recording those would unbalance the span-event stack of whatever ran next on
// that goroutine.
func TestHook_IgnoresUnsampledCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := h.BeforeProcess(tt.ctx, cmd("get", nil))
			require.NoError(t, err)
			assert.Equal(t, tt.ctx, got, "BeforeProcess replaced the context")
			require.NoError(t, h.AfterProcess(tt.ctx, cmd("get", nil)))

			gotPipe, err := h.BeforeProcessPipeline(tt.ctx, []redis.Cmder{cmd("get", nil)})
			require.NoError(t, err)
			assert.Equal(t, tt.ctx, gotPipe, "BeforeProcessPipeline replaced the context")
			require.NoError(t, h.AfterProcessPipeline(tt.ctx, []redis.Cmder{cmd("get", nil)}))
		})
	}
}

// An After without its Before is what a command that started untraced and
// finished traced would produce; it must not close an event it never opened.
func TestHook_AfterWithoutBefore(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})
	ctx := pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())

	assert.NotPanics(t, func() {
		_ = h.AfterProcess(ctx, cmd("get", nil))
		_ = h.AfterProcessPipeline(ctx, []redis.Cmder{cmd("get", nil)})
	})
}

// One hook serves every connection of a shared client, so concurrent commands
// through it must stay race-free. Run under -race.
func TestHook_ConcurrentCommands(t *testing.T) {
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine carries its own tracer, as each request would.
			tracer := newRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				_, err := h.BeforeProcess(ctx, cmd("get", nil))
				assert.NoError(t, err)
				assert.NoError(t, h.AfterProcess(ctx, cmd("get", nil)))
			}
			assert.Len(t, tracer.events, 25)
		}()
	}
	wg.Wait()
}
