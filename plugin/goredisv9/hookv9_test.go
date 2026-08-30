package ppgoredisv9

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/go-redis/v9"
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
		{"no cluster options", NewClusterHook(nil), "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			ctx := pinpoint.NewContext(context.Background(), tracer)

			if err := tt.hook.ProcessHook(func(context.Context, redis.Cmder) error { return nil })(ctx, cmd("get")); err != nil {
				t.Fatal(err)
			}
			if got := tracer.last().endPoint; got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
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

	if !inner {
		t.Fatal("the next hook did not run")
	}
	if !errors.Is(err, cmdErr) {
		t.Errorf("ProcessHook() = %v, want %v", err, cmdErr)
	}
	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis/v9.Process()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis/v9.Process()")
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
// in it.
func TestProcessPipelineHook(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)
	h := NewHook(&redis.Options{Addr: "redis1:6379"})

	cmds := []redis.Cmder{cmd("set"), cmd("get"), cmd("del")}
	if err := h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error { return nil })(ctx, cmds); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis/v9.ProcessPipeline()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis/v9.ProcessPipeline()")
	}
	if got, want := e.annotations[pinpoint.AnnotationArgs0], "set, get, del"; got != want {
		t.Errorf("command annotation = %q, want %q", got, want)
	}
	if e.err != nil {
		t.Errorf("recorded error = %v, want nil", e.err)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

func Test_cmdName(t *testing.T) {
	if got := cmdName(nil); got != "" {
		t.Errorf("cmdName(nil) = %q, want empty", got)
	}
	if got, want := cmdName([]redis.Cmder{cmd("get")}), "get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
	if got, want := cmdName([]redis.Cmder{cmd("set"), cmd("get")}), "set, get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
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

			if err := h.ProcessHook(func(context.Context, redis.Cmder) error {
				inner++
				return cmdErr
			})(tt.ctx, cmd("get")); !errors.Is(err, cmdErr) {
				t.Errorf("ProcessHook() = %v, want %v", err, cmdErr)
			}
			if err := h.ProcessPipelineHook(func(context.Context, []redis.Cmder) error {
				inner++
				return cmdErr
			})(tt.ctx, []redis.Cmder{cmd("get")}); !errors.Is(err, cmdErr) {
				t.Errorf("ProcessPipelineHook() = %v, want %v", err, cmdErr)
			}
			if inner != 2 {
				t.Errorf("the next hook ran %d times, want 2", inner)
			}
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

	if conn != nil {
		t.Errorf("DialHook returned a connection along with an error: %v", conn)
	}
	if !errors.Is(err, want) {
		t.Errorf("DialHook() = %v, want %v", err, want)
	}
	if gotNetwork != "tcp" || gotAddr != "redis1:6379" {
		t.Errorf("dialed %s/%s, want tcp/redis1:6379", gotNetwork, gotAddr)
	}
}
