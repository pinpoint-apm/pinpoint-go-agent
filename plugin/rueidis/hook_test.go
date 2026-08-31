package pprueidis

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/redis/rueidis"
	"github.com/redis/rueidis/rueidishook"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func testHook() *Hook {
	return NewHook(rueidis.ClientOption{InitAddress: []string{"redis1:6379"}})
}

// rueidishook.WithHook is the only documented way to use this plugin, so *Hook
// has to keep satisfying that interface as rueidis adds methods to it.
func TestHookSatisfiesTheRueidisInterface(t *testing.T) {
	assert.Implements(t, (*rueidishook.Hook)(nil), testHook())
}

// The command name is built for the annotation only; an untraced call must not
// pay for building it.
func TestNewSpanEventSkipsCommandForUnsampledTracer(t *testing.T) {
	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			tracer := testHook().newSpanEvent(tt.ctx, "test", func() string {
				called = true
				return "large command"
			})

			assert.False(t, tracer.IsSampled(), "an unsampled context produced a sampled tracer")
			assert.False(t, called, "the command was built for an unsampled tracer")
		})
	}
}

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
		{"an empty address list", rueidis.ClientOption{InitAddress: []string{}}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tracer := newRecordingTracer()
			NewHook(tt.opts).newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
				"rueidis.Do()", func() string { return "GET,key" })

			assert.Equal(t, tt.want, tracer.last().endPoint)
		})
	}
}

// Every rueidis operation opens its span event through this one function, so
// the service type, destination and command annotation it sets are what the
// whole plugin records.
func TestNewSpanEventRecordsTheCommand(t *testing.T) {
	tracer := newRecordingTracer()

	built := 0
	testHook().newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
		"rueidis.DoMulti()", func() string {
			built++
			return "SET,key,value, GET,key"
		})

	assert.Equal(t, 1, built, "the command name must be built exactly once")
	require.Len(t, tracer.events, 1)
	e := tracer.events[0]
	assert.Equal(t, "rueidis.DoMulti()", e.operation)
	assert.Equal(t, int32(pinpoint.ServiceTypeRedis), e.serviceType)
	assert.Equal(t, "REDIS", e.destination)
	assert.Equal(t, "redis1:6379", e.endPoint)
	assert.Equal(t, "SET,key,value, GET,key", e.annotations[pinpoint.AnnotationArgs0])
}

// An empty command name would annotate the span event with an empty string,
// which reads as a command that ran with no name rather than one that could
// not be described.
func TestNewSpanEventSkipsAnEmptyCommandAnnotation(t *testing.T) {
	tracer := newRecordingTracer()

	testHook().newSpanEvent(pinpoint.NewContext(context.Background(), tracer),
		"rueidis.Do()", func() string { return "" })

	assert.NotContains(t, tracer.last().annotations, pinpoint.AnnotationArgs0,
		"an empty command was recorded as an annotation")
}

// A rueidis command can only be built from a live client's own builder, so the
// two name helpers are exercised on the empty batch each of them can be handed:
// a batch with nothing in it must annotate nothing rather than a bare
// separator.
func Test_cmdNames_EmptyBatch(t *testing.T) {
	assert.Equal(t, "", cmdCompletedName(nil))
	assert.Equal(t, "", cmdCompletedName([]rueidis.Completed{}))
	assert.Equal(t, "", cmdCacheableName(nil))
	assert.Equal(t, "", cmdCacheableName([]rueidis.CacheableTTL{}))
}

// A batch fails as a whole on its first failed command; reporting a later one
// would point at the wrong command in the trace.
func Test_multiResultError(t *testing.T) {
	assert.NoError(t, multiResultError(nil), "an empty batch has no failure to report")
	assert.NoError(t, multiResultError([]rueidis.RedisResult{}))

	first := errors.New("WRONGTYPE")
	assert.ErrorIs(t, multiResultError([]rueidis.RedisResult{
		rueidis.NewErrorResult(first),
		rueidis.NewErrorResult(errors.New("second")),
	}), first)

	assert.ErrorIs(t, multiResultError([]rueidis.RedisResult{
		rueidis.NewErrorResult(first),
	}), first)
}
