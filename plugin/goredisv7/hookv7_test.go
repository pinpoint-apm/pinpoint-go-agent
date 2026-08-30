package ppgoredisv7

import (
	"context"
	"errors"
	"testing"

	"github.com/go-redis/redis/v7"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// recordingTracer captures what the hook records on a span event. A real
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
		{"no cluster options", NewClusterHook(nil), "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.hook.BeforeProcess(ctx, cmd("get", nil)); err != nil {
				t.Fatal(err)
			}
			if err := tt.hook.AfterProcess(ctx, cmd("get", nil)); err != nil {
				t.Fatal(err)
			}
			if got := tracer.last().endPoint; got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
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
	if err != nil {
		t.Fatal(err)
	}
	if got != ctx {
		t.Error("BeforeProcess replaced the context")
	}
	if err := h.AfterProcess(ctx, cmd("get", cmdErr)); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis/v7.Process()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis/v7.Process()")
	}
	if e.serviceType != pinpoint.ServiceTypeRedis {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeRedis)
	}
	if e.destination != "REDIS" {
		t.Errorf("destination = %q, want REDIS", e.destination)
	}
	if got := e.annotations[pinpoint.AnnotationArgs0]; got != "get" {
		t.Errorf("command annotation = %q, want %q", got, "get")
	}
	if !errors.Is(e.err, cmdErr) {
		t.Errorf("recorded error = %v, want %v", e.err, cmdErr)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

// A pipeline is one round trip, so it is one span event listing every command
// in it, failed by the first command that failed.
func TestHook_ProcessPipeline(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmdErr := errors.New("WRONGTYPE")
	cmds := []redis.Cmder{cmd("set", nil), cmd("get", cmdErr), cmd("del", errors.New("later"))}

	if _, err := h.BeforeProcessPipeline(ctx, cmds); err != nil {
		t.Fatal(err)
	}
	if err := h.AfterProcessPipeline(ctx, cmds); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis/v7.ProcessPipeline()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis/v7.ProcessPipeline()")
	}
	if got, want := e.annotations[pinpoint.AnnotationArgs0], "set, get, del"; got != want {
		t.Errorf("command annotation = %q, want %q", got, want)
	}
	if !errors.Is(e.err, cmdErr) {
		t.Errorf("recorded error = %v, want the first failure %v", e.err, cmdErr)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

func Test_cmdName(t *testing.T) {
	if got := cmdName(nil); got != "" {
		t.Errorf("cmdName(nil) = %q, want empty", got)
	}
	if got, want := cmdName([]redis.Cmder{cmd("get", nil)}), "get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
	if got, want := cmdName([]redis.Cmder{cmd("set", nil), cmd("get", nil)}), "set, get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
}

func Test_pipeError(t *testing.T) {
	if err := pipeError([]redis.Cmder{cmd("set", nil), cmd("get", nil)}); err != nil {
		t.Errorf("pipeError() = %v, want nil", err)
	}

	first := errors.New("first")
	err := pipeError([]redis.Cmder{cmd("set", nil), cmd("get", first), cmd("del", errors.New("second"))})
	if !errors.Is(err, first) {
		t.Errorf("pipeError() = %v, want %v", err, first)
	}
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
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.ctx {
				t.Error("BeforeProcess replaced the context")
			}
			if err := h.AfterProcess(tt.ctx, cmd("get", nil)); err != nil {
				t.Fatal(err)
			}

			if _, err := h.BeforeProcessPipeline(tt.ctx, []redis.Cmder{cmd("get", nil)}); err != nil {
				t.Fatal(err)
			}
			if err := h.AfterProcessPipeline(tt.ctx, []redis.Cmder{cmd("get", nil)}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
