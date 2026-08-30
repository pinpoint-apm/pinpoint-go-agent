package ppgoredis

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-redis/redis"
	"github.com/pinpoint-apm/pinpoint-go-agent"
)

// WithContext must hand each request its own copy: the wrapped client is
// shared, and rebinding it in place raced the field write and recorded one
// request's commands on another request's tracer. Run under -race.
func TestClient_WithContextReturnsCopy(t *testing.T) {
	rc := NewClient(&redis.Options{Addr: "localhost:1"})
	orig := rc.Client

	c := rc.WithContext(context.Background())
	if c == rc {
		t.Error("WithContext returned the shared wrapper, want a copy")
	}
	if rc.Client != orig {
		t.Error("WithContext mutated the shared wrapper")
	}

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

	if !inner {
		t.Fatal("go-redis's own process function did not run")
	}
	if !errors.Is(err, cmdErr) {
		t.Errorf("process() = %v, want %v", err, cmdErr)
	}
	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis.Process()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis.Process()")
	}
	if e.serviceType != pinpoint.ServiceTypeRedis {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeRedis)
	}
	if e.destination != "REDIS" {
		t.Errorf("destination = %q, want REDIS", e.destination)
	}
	if e.endPoint != "redis1:6379" {
		t.Errorf("endpoint = %q, want %q", e.endPoint, "redis1:6379")
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
func Test_processPipeline(t *testing.T) {
	tracer := newRecordingTracer()
	ctx := pinpoint.NewContext(context.Background(), tracer)

	cmds := []redis.Cmder{redis.NewCmd("set", "key"), redis.NewCmd("get", "key")}
	if err := processPipeline(ctx, "redis1:6379")(func([]redis.Cmder) error { return nil })(cmds); err != nil {
		t.Fatal(err)
	}

	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "go-redis.ProcessPipeline()" {
		t.Errorf("operation = %q, want %q", e.operation, "go-redis.ProcessPipeline()")
	}
	if got, want := e.annotations[pinpoint.AnnotationArgs0], "set, get"; got != want {
		t.Errorf("command annotation = %q, want %q", got, want)
	}
	if !e.ended {
		t.Error("the span event was left open")
	}
}

func Test_cmdName(t *testing.T) {
	if got := cmdName(nil); got != "" {
		t.Errorf("cmdName(nil) = %q, want empty", got)
	}
	if got, want := cmdName([]redis.Cmder{redis.NewCmd("get", "key")}), "get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
	if got, want := cmdName([]redis.Cmder{redis.NewCmd("set", "key"), redis.NewCmd("get", "key")}), "set, get"; got != want {
		t.Errorf("cmdName() = %q, want %q", got, want)
	}
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
			cmdErr := errors.New("WRONGTYPE")
			inner := 0

			if err := process(tt.ctx, "redis1:6379")(func(redis.Cmder) error {
				inner++
				return cmdErr
			})(redis.NewCmd("get", "key")); !errors.Is(err, cmdErr) {
				t.Errorf("process() = %v, want %v", err, cmdErr)
			}
			if err := processPipeline(tt.ctx, "redis1:6379")(func([]redis.Cmder) error {
				inner++
				return cmdErr
			})([]redis.Cmder{redis.NewCmd("get", "key")}); !errors.Is(err, cmdErr) {
				t.Errorf("processPipeline() = %v, want %v", err, cmdErr)
			}
			if inner != 2 {
				t.Errorf("go-redis's own process function ran %d times, want 2", inner)
			}
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

	if cc.endpoint != "127.0.0.1:1,127.0.0.2:1" {
		t.Errorf("endpoint = %q, want %q", cc.endpoint, "127.0.0.1:1,127.0.0.2:1")
	}

	c := cc.WithContext(context.Background())
	if c == cc {
		t.Error("WithContext returned the shared wrapper, want a copy")
	}
	if cc.ClusterClient != orig {
		t.Error("WithContext mutated the shared wrapper")
	}
	if c.endpoint != cc.endpoint {
		t.Errorf("the copy's endpoint = %q, want %q", c.endpoint, cc.endpoint)
	}
}
