package pprueidis

import (
	"context"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/rueidis"
)

func TestNewSpanEventSkipsCommandForUnsampledTracer(t *testing.T) {
	called := false
	tracer := (&Hook{}).newSpanEvent(context.Background(), "test", func() string {
		called = true
		return "large command"
	})

	if tracer.IsSampled() {
		t.Fatal("background context unexpectedly returned a sampled tracer")
	}
	if called {
		t.Fatal("command was built for an unsampled tracer")
	}
}

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
	annotations map[int32]string
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)    { e.serviceType = typ }
func (e *recordedEvent) SetDestination(id string)    { e.destination = id }
func (e *recordedEvent) SetEndPoint(endPoint string) { e.endPoint = endPoint }

func (e *recordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.annotations}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

// The endpoint is what puts the call on the right node of the server map. The
// hook is constructed from the same options the client is, and a caller that
// passes none must not produce an empty endpoint.
func TestNewHook_Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name string
		opts rueidis.ClientOption
		want string
	}{
		{"single address", rueidis.ClientOption{InitAddress: []string{"redis1:6379"}}, "redis1:6379"},
		{"several addresses", rueidis.ClientOption{InitAddress: []string{"redis1:6379", "redis2:6379"}}, "redis1:6379,redis2:6379"},
		{"no address", rueidis.ClientOption{}, "unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			NewHook(tt.opts).newSpanEvent(pinpoint.NewContext(context.Background(), tracer), "rueidis.Do()", func() string { return "GET,key" })

			if got := tracer.last().endPoint; got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
		})
	}
}

// Every rueidis operation opens its span event through this one function, so
// the service type, destination and command annotation it sets are what the
// whole plugin records.
func TestNewSpanEventRecordsTheCommand(t *testing.T) {
	tracer := newRecordingTracer()
	h := NewHook(rueidis.ClientOption{InitAddress: []string{"redis1:6379"}})

	built := 0
	h.newSpanEvent(pinpoint.NewContext(context.Background(), tracer), "rueidis.DoMulti()", func() string {
		built++
		return "SET,key,value, GET,key"
	})

	if built != 1 {
		t.Errorf("the command was built %d times, want 1", built)
	}
	if len(tracer.events) != 1 {
		t.Fatalf("recorded %d span events, want 1", len(tracer.events))
	}
	e := tracer.events[0]
	if e.operation != "rueidis.DoMulti()" {
		t.Errorf("operation = %q, want %q", e.operation, "rueidis.DoMulti()")
	}
	if e.serviceType != pinpoint.ServiceTypeRedis {
		t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeRedis)
	}
	if e.destination != "REDIS" {
		t.Errorf("destination = %q, want REDIS", e.destination)
	}
	if got, want := e.annotations[pinpoint.AnnotationArgs0], "SET,key,value, GET,key"; got != want {
		t.Errorf("command annotation = %q, want %q", got, want)
	}
}

// An empty command name would annotate the span event with an empty string,
// which reads as a command that ran with no name rather than one that could
// not be described.
func TestNewSpanEventSkipsAnEmptyCommandAnnotation(t *testing.T) {
	tracer := newRecordingTracer()
	h := NewHook(rueidis.ClientOption{InitAddress: []string{"redis1:6379"}})

	h.newSpanEvent(pinpoint.NewContext(context.Background(), tracer), "rueidis.Do()", func() string { return "" })

	if _, ok := tracer.last().annotations[pinpoint.AnnotationArgs0]; ok {
		t.Error("an empty command was recorded as an annotation")
	}
}

// An empty batch has no failure to report.
func Test_multiResultError(t *testing.T) {
	if err := multiResultError(nil); err != nil {
		t.Errorf("multiResultError(nil) = %v, want nil", err)
	}
	if err := multiResultError([]rueidis.RedisResult{}); err != nil {
		t.Errorf("multiResultError(empty) = %v, want nil", err)
	}
}
