package ppzap

import (
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent/v2"
	"github.com/pinpoint-apm/pinpoint-go-agent/v2/test/pptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func newTracer(t *testing.T) pinpoint.Tracer {
	t.Helper()
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/hello")
	t.Cleanup(tracer.EndSpan)
	require.True(t, tracer.IsSampled(), "the test agent produced an unsampled tracer")
	return tracer
}

// observedLogger returns a logger recording its entries, so a log line can be
// read back field by field instead of matched as a substring.
func observedLogger(t *testing.T) (*zap.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

func loggedFields(t *testing.T, logs *observer.ObservedLogs) map[string]interface{} {
	t.Helper()
	entries := logs.All()
	require.Len(t, entries, 1, "expected exactly one log line")
	return entries[0].ContextMap()
}

// The two fields are what lets the Pinpoint web UI jump from a log line to the
// span that produced it, so both have to carry the tracer's own ids.
func TestNewField(t *testing.T) {
	pptest.StartAgent(t)
	tracer := newTracer(t)

	fields := NewField(tracer)

	require.Len(t, fields, 2, "only the two correlation fields belong in the log entry")
	assert.Equal(t, pinpoint.LogTransactionIdKey, fields[0].Key)
	assert.Equal(t, tracer.TransactionId().String(), fields[0].String)
	assert.Equal(t, pinpoint.LogSpanIdKey, fields[1].Key)
	assert.Equal(t, tracer.SpanId(), fields[1].Integer)
}

// spanSpy records the logging mark the adapter sets; the agent keeps it
// unexported and only sends it with the span.
type spanSpy struct {
	pinpoint.SpanRecorder
	logging int32
}

func (s *spanSpy) SetLogging(logInfo int32) {
	s.logging = logInfo
	s.SpanRecorder.SetLogging(logInfo)
}

type tracerSpy struct {
	pinpoint.Tracer
	span *spanSpy
}

func (t *tracerSpy) Span() pinpoint.SpanRecorder { return t.span }

// Marking the span as logged is the signal the Pinpoint web UI reads to know a
// log line exists for the trace, so it has to happen whenever ids are handed
// out - and not for a span that contributes none.
func TestNewField_SetsLogging(t *testing.T) {
	pptest.StartAgent(t)

	sampled := &tracerSpy{Tracer: newTracer(t)}
	sampled.span = &spanSpy{SpanRecorder: sampled.Tracer.Span()}
	NewField(sampled)
	assert.Equal(t, int32(pinpoint.Logged), sampled.span.logging)

	unsampled := &tracerSpy{Tracer: pinpoint.NoopTracer()}
	unsampled.span = &spanSpy{SpanRecorder: unsampled.Tracer.Span()}
	NewField(unsampled)
	assert.Equal(t, int32(pinpoint.NotLogged), unsampled.span.logging,
		"an unsampled span must not be marked as logged")
}

// Application code reaches for the tracer before it knows whether one exists,
// so a nil or unsampled tracer has to yield no fields rather than nil-panic or
// log ids that point at nothing.
func TestNewField_WithoutASampledTracer(t *testing.T) {
	pptest.StartAgent(t)

	assert.Empty(t, NewField(nil), "a nil tracer must contribute no fields")
	assert.Empty(t, NewField(pinpoint.NoopTracer()), "an unsampled tracer must contribute no fields")
}

// NewLogger is what most applications use, and it has to add the same fields
// NewField produces without dropping the ones the logger already carries.
func TestNewLogger(t *testing.T) {
	pptest.StartAgent(t)
	tracer := newTracer(t)

	logger, logs := observedLogger(t)
	NewLogger(logger.With(zap.String("before", "1")), tracer).
		With(zap.String("after", "2")).
		Error("logger log message")

	fields := loggedFields(t, logs)
	assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	assert.Equal(t, tracer.SpanId(), fields[pinpoint.LogSpanIdKey])
	assert.Equal(t, "1", fields["before"], "the provided logger's own fields must be kept")
	assert.Equal(t, "2", fields["after"])
	assert.Equal(t, "logger log message", logs.All()[0].Message)
}

// One base logger serves every request in a process, so deriving from it
// concurrently must stay race-free. Run under -race.
func TestNewLogger_ConcurrentLogging(t *testing.T) {
	pptest.StartAgent(t)

	logger, _ := observedLogger(t)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracer := pinpoint.GetAgent().NewSpanTracer("test", "/hello")
			defer tracer.EndSpan()
			for j := 0; j < 25; j++ {
				NewLogger(logger, tracer).Info("message", zap.String("foo", "bar"))
			}
		}()
	}
	wg.Wait()
}
