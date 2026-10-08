// Package pptest holds what the plugin tests share: an agent that lives for
// one test, the JSON of a span, and a tracer that records its span events.
// It is test support, not part of the agent's supported API, and changes with
// the tests it serves.
package pptest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/stretchr/testify/require"
)

// StartAgent starts an offline agent named testApp/testAgent, with opts
// applied on top, and shuts it down when the test ends.
func StartAgent(t testing.TB, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()
	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentName("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)
	agent, err := pinpoint.NewTestAgent(config)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)
	return agent
}

// SpanOf decodes the span the tracer records.
func SpanOf(t testing.TB, tracer pinpoint.Tracer) map[string]interface{} {
	t.Helper()
	require.NotNil(t, tracer, "the handler never ran")
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(tracer.JsonString(), &m))
	return m
}

// RecordingTracer is a sampled tracer that records what a plugin sets on its
// span events. A real tracer's recorders are write-only, so this stands in
// for one; everything it does not record goes to the noop tracer.
type RecordingTracer struct {
	pinpoint.Tracer
	Events []*RecordedEvent
}

func NewRecordingTracer() *RecordingTracer {
	return &RecordingTracer{Tracer: pinpoint.NoopTracer()}
}

func (t *RecordingTracer) IsSampled() bool { return true }

func (t *RecordingTracer) NewSpanEvent(operation string) pinpoint.Tracer {
	t.Events = append(t.Events, &RecordedEvent{
		SpanEventRecorder: t.Tracer.SpanEvent(),
		Operation:         operation,
		Strings:           map[int32]string{},
	})
	return t
}

// SpanEvent is the innermost event still open, as on a real tracer; an event
// that has ended is not written to again.
func (t *RecordingTracer) SpanEvent() pinpoint.SpanEventRecorder { return t.open() }

func (t *RecordingTracer) EndSpanEvent() { t.open().Ended = true }

// Last is the span event started last, ended or not.
func (t *RecordingTracer) Last() *RecordedEvent {
	if len(t.Events) == 0 {
		panic("pptest: SpanEvent or EndSpanEvent before any NewSpanEvent")
	}
	return t.Events[len(t.Events)-1]
}

// open is the innermost event not yet ended; a readable failure, not an index
// panic, when a plugin path ends or writes an event it never started.
func (t *RecordingTracer) open() *RecordedEvent {
	for i := len(t.Events) - 1; i >= 0; i-- {
		if !t.Events[i].Ended {
			return t.Events[i]
		}
	}
	panic("pptest: SpanEvent or EndSpanEvent with no open span event")
}

// RecordedEvent is one span event and what was set on it. Strings holds the
// annotations by key: each AppendString value, and the first value of each
// AppendStringString.
type RecordedEvent struct {
	pinpoint.SpanEventRecorder
	Operation   string
	ServiceType int32
	Destination string
	EndPoint    string
	SQL         string
	Err         error
	Strings     map[int32]string
	Start, End  time.Time
	Ended       bool
}

func (e *RecordedEvent) SetServiceType(typ int32)         { e.ServiceType = typ }
func (e *RecordedEvent) SetDestination(id string)         { e.Destination = id }
func (e *RecordedEvent) SetEndPoint(endPoint string)      { e.EndPoint = endPoint }
func (e *RecordedEvent) SetSQL(sql string, _ string)      { e.SQL = sql }
func (e *RecordedEvent) SetError(err error, _ ...string)  { e.Err = err }
func (e *RecordedEvent) FixDuration(start, end time.Time) { e.Start, e.End = start, end }

func (e *RecordedEvent) Annotations() pinpoint.Annotation {
	return recordedAnnotation{Annotation: e.SpanEventRecorder.Annotations(), into: e.Strings}
}

type recordedAnnotation struct {
	pinpoint.Annotation
	into map[int32]string
}

func (a recordedAnnotation) AppendString(key int32, s string) { a.into[key] = s }

func (a recordedAnnotation) AppendStringString(key int32, s1, _ string) { a.into[key] = s1 }
