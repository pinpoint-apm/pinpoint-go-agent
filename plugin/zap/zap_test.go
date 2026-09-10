package ppzap

import (
	"context"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func startAgent(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentName("testAgent"),
	}, opts...)

	config, err := pinpoint.NewConfig(opts...)
	require.NoError(t, err)

	agent, err := pinpoint.NewTestAgent(config, t)
	require.NoError(t, err)
	t.Cleanup(agent.Shutdown)

	return agent
}

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
	startAgent(t)
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
	startAgent(t)

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
	startAgent(t)

	for _, tt := range []struct {
		name   string
		tracer pinpoint.Tracer
	}{
		{"nil tracer", nil},
		{"noop tracer", pinpoint.NoopTracer()},
		{"tracer from a context without a span", pinpoint.FromContext(context.Background())},
		{"tracer from a nil context", pinpoint.FromContext(nil)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, NewField(tt.tracer), "an unsampled tracer must contribute no fields")
		})
	}
}

// The fields have to reach the log line itself, on every level, alongside
// whatever the application logs.
func TestNewField_Logged(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	for name, log := range map[string]func(*zap.Logger, ...zap.Field){
		"Debug": func(l *zap.Logger, f ...zap.Field) { l.Debug("message", f...) },
		"Info":  func(l *zap.Logger, f ...zap.Field) { l.Info("message", f...) },
		"Warn":  func(l *zap.Logger, f ...zap.Field) { l.Warn("message", f...) },
		"Error": func(l *zap.Logger, f ...zap.Field) { l.Error("message", f...) },
	} {
		t.Run(name, func(t *testing.T) {
			logger, logs := observedLogger(t)
			log(logger, append(NewField(tracer), zap.String("foo", "bar"))...)

			fields := loggedFields(t, logs)
			assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
			assert.Equal(t, tracer.SpanId(), fields[pinpoint.LogSpanIdKey])
			assert.Equal(t, "bar", fields["foo"], "the application's own fields must survive")
		})
	}
}

// NewLogger is what most applications use, and it has to add the same fields
// NewField produces without dropping the ones the logger already carries.
func TestNewLogger(t *testing.T) {
	startAgent(t)
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

// A logger derived for an unsampled tracer must carry only the application's
// own fields, and must not be a different logger than the one it was given.
func TestNewLogger_WithoutASampledTracer(t *testing.T) {
	startAgent(t)

	logger, logs := observedLogger(t)
	NewLogger(logger, pinpoint.NoopTracer()).With(zap.String("foo", "bar")).Error("message")

	fields := loggedFields(t, logs)
	assert.Equal(t, "bar", fields["foo"])
	assert.NotContains(t, fields, pinpoint.LogTransactionIdKey)
	assert.NotContains(t, fields, pinpoint.LogSpanIdKey)

	assert.Same(t, logger, NewLogger(logger, nil), "an unsampled tracer must not clone the logger")
}

// The sugared logger has no fields of its own to instrument, so it is derived
// from an instrumented *zap.Logger. That path has to carry the ids too.
func TestNewLogger_Sugar(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger, logs := observedLogger(t)
	NewLogger(logger, tracer).Sugar().With("foo", "bar").Errorw("sugared log message")

	fields := loggedFields(t, logs)
	assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	assert.Equal(t, "bar", fields["foo"])
}

// One base logger serves every request in a process, so deriving from it
// concurrently must stay race-free. Run under -race.
func TestNewLogger_ConcurrentLogging(t *testing.T) {
	startAgent(t)

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
