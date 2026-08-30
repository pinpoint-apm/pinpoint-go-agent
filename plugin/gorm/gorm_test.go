package ppgorm

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"gorm.io/gorm"
	"gorm.io/gorm/utils/tests"
)

// recordingTracer captures what the callbacks record on a span event. A real
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
	err         error
	ended       bool
}

func (e *recordedEvent) SetServiceType(typ int32)        { e.serviceType = typ }
func (e *recordedEvent) SetError(err error, _ ...string) { e.err = err }

// The callback names are the contract with gorm's own registry: registering
// under a name gorm does not know, or against a hook that does not exist,
// silently drops the instrumentation for that statement kind.
var callbackPairs = []struct {
	kind      string
	operation string
	before    string
	after     string
}{
	{"create", "gorm.create", "pinpoint:before_create", "pinpoint:after_create"},
	{"update", "gorm.update", "pinpoint:before_update", "pinpoint:after_update"},
	{"delete", "gorm.delete", "pinpoint:before_delete", "pinpoint:after_delete"},
	{"query", "gorm.query", "pinpoint:before_query", "pinpoint:after_query"},
	{"row", "gorm.row", "pinpoint:before_row", "pinpoint:after_row"},
	{"raw", "gorm.raw", "pinpoint:before_raw", "pinpoint:after_raw"},
}

func processorFor(db *gorm.DB, kind string) interface {
	Get(string) func(*gorm.DB)
} {
	switch kind {
	case "create":
		return db.Callback().Create()
	case "update":
		return db.Callback().Update()
	case "delete":
		return db.Callback().Delete()
	case "query":
		return db.Callback().Query()
	case "row":
		return db.Callback().Row()
	case "raw":
		return db.Callback().Raw()
	}
	panic("unknown callback kind " + kind)
}

func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := Open(tests.DummyDialector{}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// Every statement kind gorm runs has to end up with both pinpoint callbacks
// registered, or that kind goes untraced.
func TestOpen_RegistersEveryCallback(t *testing.T) {
	db := openDB(t)

	for _, p := range callbackPairs {
		t.Run(p.kind, func(t *testing.T) {
			processor := processorFor(db, p.kind)
			if processor.Get(p.before) == nil {
				t.Errorf("%s is not registered", p.before)
			}
			if processor.Get(p.after) == nil {
				t.Errorf("%s is not registered", p.after)
			}
		})
	}
}

// Each pair has to open exactly one span event, name it after the statement
// kind, and close it - an unbalanced pair leaves the span-event stack of the
// request skewed for everything that follows.
func TestCallbacks_RecordOneSpanEventPerStatement(t *testing.T) {
	db := openDB(t)

	for _, p := range callbackPairs {
		t.Run(p.kind, func(t *testing.T) {
			tracer := newRecordingTracer()
			processor := processorFor(db, p.kind)
			stmt := &gorm.DB{Statement: &gorm.Statement{
				Context: pinpoint.NewContext(context.Background(), tracer),
			}}

			processor.Get(p.before)(stmt)
			processor.Get(p.after)(stmt)

			if len(tracer.events) != 1 {
				t.Fatalf("recorded %d span events, want 1", len(tracer.events))
			}
			e := tracer.events[0]
			if e.operation != p.operation {
				t.Errorf("operation = %q, want %q", e.operation, p.operation)
			}
			if e.serviceType != pinpoint.ServiceTypeGoFunction {
				t.Errorf("service type = %d, want %d", e.serviceType, pinpoint.ServiceTypeGoFunction)
			}
			if !e.ended {
				t.Error("the span event was left open")
			}
		})
	}
}

// A statement that failed is the one worth finding in the trace, so the error
// gorm left on the DB has to reach the span event.
func TestCallbacks_RecordTheStatementError(t *testing.T) {
	db := openDB(t)
	tracer := newRecordingTracer()

	want := errors.New("duplicate key")
	stmt := &gorm.DB{
		Statement: &gorm.Statement{Context: pinpoint.NewContext(context.Background(), tracer)},
		Error:     want,
	}

	create := db.Callback().Create()
	create.Get("pinpoint:before_create")(stmt)
	create.Get("pinpoint:after_create")(stmt)

	if !errors.Is(tracer.events[0].err, want) {
		t.Errorf("recorded error = %v, want %v", tracer.events[0].err, want)
	}
}

// The callbacks are registered on the shared *gorm.DB, so they run for every
// statement the application makes - including those from code that never
// started a span, and those whose statement carries no context at all.
func TestCallbacks_WithoutASampledTracer(t *testing.T) {
	db := openDB(t)
	create := db.Callback().Create()

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"nil context", nil},
		{"background context", context.Background()},
		{"noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stmt := &gorm.DB{Statement: &gorm.Statement{Context: tt.ctx}}
			create.Get("pinpoint:before_create")(stmt)
			create.Get("pinpoint:after_create")(stmt)
		})
	}
}

// A dialector that cannot initialize must surface its own error, and must not
// be reported as a working instrumented connection.
func TestOpen_ReturnsTheDialectorError(t *testing.T) {
	want := errors.New("cannot reach the database")

	db, err := Open(failingDialector{err: want}, &gorm.Config{})

	if !errors.Is(err, want) {
		t.Errorf("Open() = %v, want %v", err, want)
	}
	if db != nil && db.Error == nil && err == nil {
		t.Error("Open returned a usable *gorm.DB for a failed dialector")
	}
}

type failingDialector struct {
	tests.DummyDialector
	err error
}

func (d failingDialector) Initialize(*gorm.DB) error { return d.err }
