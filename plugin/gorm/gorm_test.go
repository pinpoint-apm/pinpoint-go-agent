package ppgorm

import (
	"context"
	"errors"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	return db
}

// Every statement kind gorm runs has to end up with both pinpoint callbacks
// registered, or that kind goes untraced.
func TestOpen_RegistersEveryCallback(t *testing.T) {
	db := openDB(t)

	for _, p := range callbackPairs {
		t.Run(p.kind, func(t *testing.T) {
			processor := processorFor(db, p.kind)
			assert.NotNil(t, processor.Get(p.before), "%s is not registered", p.before)
			assert.NotNil(t, processor.Get(p.after), "%s is not registered", p.after)
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

			require.Len(t, tracer.events, 1, "one statement must produce exactly one span event")
			e := tracer.events[0]
			assert.Equal(t, p.operation, e.operation)
			assert.Equal(t, int32(pinpoint.ServiceTypeGoFunction), e.serviceType)
			assert.True(t, e.ended, "the span event was left open")
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

	require.Len(t, tracer.events, 1)
	assert.ErrorIs(t, tracer.events[0].err, want)
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
			assert.NotPanics(t, func() {
				create.Get("pinpoint:before_create")(stmt)
				create.Get("pinpoint:after_create")(stmt)
			}, "an untraced statement must not take the application down")
		})
	}
}

// A dialector that cannot initialize must surface its own error, and must not
// be reported as a working instrumented connection.
func TestOpen_ReturnsTheDialectorError(t *testing.T) {
	want := errors.New("cannot reach the database")

	db, err := Open(failingDialector{err: want}, &gorm.Config{})

	assert.ErrorIs(t, err, want, "the dialector's own error must reach the caller")
	if db != nil {
		assert.Nil(t, db.Callback().Create().Get("pinpoint:before_create"),
			"the pinpoint callbacks must not be registered on a failed connection")
	}
}

// The pinpoint callbacks are registered relative to gorm's own hooks, so gorm's
// callbacks have to still be there and still run: registering against a hook
// name gorm does not know silently drops the instrumentation.
func TestOpen_KeepsGormsOwnCallbacks(t *testing.T) {
	db := openDB(t)

	for _, tt := range []struct {
		kind string
		name string
	}{
		{"create", "gorm:before_create"},
		{"create", "gorm:after_create"},
		{"update", "gorm:before_update"},
		{"update", "gorm:after_update"},
		{"delete", "gorm:before_delete"},
		{"delete", "gorm:after_delete"},
		{"query", "gorm:query"},
		{"row", "gorm:row"},
		{"raw", "gorm:raw"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotNil(t, processorFor(db, tt.kind).Get(tt.name),
				"gorm's own %s callback was lost", tt.name)
		})
	}
}

// Open returns gorm's own *gorm.DB, so everything an application does with it
// keeps working - WithContext included, which is how the tracer gets in.
func TestOpen_ReturnsAUsableDB(t *testing.T) {
	db := openDB(t)

	require.NotNil(t, db)
	require.NoError(t, db.Error)

	tracer := newRecordingTracer()
	scoped := db.WithContext(pinpoint.NewContext(context.Background(), tracer))
	assert.Equal(t, tracer, pinpoint.FromContext(scoped.Statement.Context),
		"WithContext must carry the tracer through to the statement")
}

// A statement that succeeded records no error, so a later failed one is not
// mistaken for it.
func TestCallbacks_SuccessfulStatement(t *testing.T) {
	db := openDB(t)
	tracer := newRecordingTracer()

	stmt := &gorm.DB{Statement: &gorm.Statement{
		Context: pinpoint.NewContext(context.Background(), tracer),
	}}

	create := db.Callback().Create()
	create.Get("pinpoint:before_create")(stmt)
	create.Get("pinpoint:after_create")(stmt)

	require.Len(t, tracer.events, 1)
	assert.NoError(t, tracer.events[0].err)
}

type failingDialector struct {
	tests.DummyDialector
	err error
}

func (d failingDialector) Initialize(*gorm.DB) error { return d.err }
