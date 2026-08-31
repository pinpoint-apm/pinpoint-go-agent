package pplogrus

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startAgent(t *testing.T, opts ...pinpoint.ConfigOption) pinpoint.Agent {
	t.Helper()

	opts = append([]pinpoint.ConfigOption{
		pinpoint.WithAppName("testApp"),
		pinpoint.WithAgentId("testAgent"),
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

// jsonLogger returns a logger writing JSON into buf, so a log line can be read
// back field by field instead of matched as a substring.
func jsonLogger(t *testing.T) (*logrus.Logger, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	logger := logrus.New()
	logger.SetOutput(&out)
	logger.SetFormatter(&logrus.JSONFormatter{})
	logger.SetLevel(logrus.TraceLevel)
	return logger, &out
}

func loggedFields(t *testing.T, out *bytes.Buffer) map[string]interface{} {
	t.Helper()
	line := strings.TrimSpace(out.String())
	require.NotEmpty(t, line, "nothing was logged")
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	return fields
}

// The two fields are what lets the Pinpoint web UI jump from a log line to the
// span that produced it, so both have to carry the tracer's own ids.
func TestNewField(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	fields := NewField(tracer)

	assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	assert.Equal(t, tracer.SpanId(), fields[pinpoint.LogSpanIdKey])
	assert.Len(t, fields, 2, "only the two correlation fields belong in the log entry")
}

// Application code reaches for the tracer before it knows whether one exists,
// so a nil or unsampled tracer has to yield empty fields rather than nil-panic
// or log ids that point at nothing.
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

// WithField is the deprecated spelling and has to stay equivalent.
func TestWithField(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	assert.Equal(t, NewField(tracer), WithField(tracer))
	assert.Empty(t, WithField(nil), "the deprecated spelling must tolerate a nil tracer too")
}

// The entry constructors are what most applications use, and both have to end
// up with the same fields NewField produces - on the standard logger and on a
// provided one.
func TestNewEntryAndNewLoggerEntry(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger, _ := jsonLogger(t)

	for name, entry := range map[string]*logrus.Entry{
		"NewEntry":       NewEntry(tracer),
		"NewLoggerEntry": NewLoggerEntry(logger, tracer),
	} {
		assert.Equal(t, tracer.TransactionId().String(), entry.Data[pinpoint.LogTransactionIdKey], name)
		assert.Equal(t, tracer.SpanId(), entry.Data[pinpoint.LogSpanIdKey], name)
	}

	assert.Same(t, logger, NewLoggerEntry(logger, tracer).Logger, "NewLoggerEntry did not use the provided logger")
	assert.Same(t, logrus.StandardLogger(), NewEntry(tracer).Logger, "NewEntry must use the standard logger")
}

// An entry built for an unsampled tracer must carry only the application's own
// fields.
func TestNewLoggerEntry_WithoutASampledTracer(t *testing.T) {
	startAgent(t)

	logger, out := jsonLogger(t)
	NewLoggerEntry(logger, pinpoint.NoopTracer()).WithField("foo", "bar").Error("message")

	fields := loggedFields(t, out)
	assert.Equal(t, "bar", fields["foo"])
	assert.NotContains(t, fields, pinpoint.LogTransactionIdKey)
	assert.NotContains(t, fields, pinpoint.LogSpanIdKey)
}

// The hook takes the tracer from the entry's context instead of the call site,
// so it has to add the same two fields to whatever the application logs.
func TestHook_Fire(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger, out := jsonLogger(t)
	logger.AddHook(NewHook())

	logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).
		WithField("foo", "bar").
		Error("hook log message")

	fields := loggedFields(t, out)
	assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	assert.Equal(t, float64(tracer.SpanId()), fields[pinpoint.LogSpanIdKey])
	assert.Equal(t, "bar", fields["foo"], "the application's own fields must survive the hook")
	assert.Equal(t, "hook log message", fields["msg"])
}

// The hook fires on every level, so the ids have to reach a debug line as
// readily as an error one.
func TestHook_FireOnEveryLevel(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	for _, level := range logrus.AllLevels {
		if level == logrus.PanicLevel || level == logrus.FatalLevel {
			continue // these end the process rather than returning
		}
		t.Run(level.String(), func(t *testing.T) {
			logger, out := jsonLogger(t)
			logger.AddHook(NewHook())

			logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).Log(level, "message")

			assert.Equal(t, tracer.TransactionId().String(), loggedFields(t, out)[pinpoint.LogTransactionIdKey])
		})
	}
}

// Most log lines are written without a context. The hook must leave those
// entries alone instead of failing the log call.
func TestHook_FireWithoutATracer(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name  string
		entry *logrus.Entry
	}{
		{"no context", &logrus.Entry{Data: logrus.Fields{}}},
		{"context without a span", &logrus.Entry{Context: context.Background(), Data: logrus.Fields{}}},
		{"context with a noop tracer", &logrus.Entry{
			Context: pinpoint.NewContext(context.Background(), pinpoint.NoopTracer()),
			Data:    logrus.Fields{},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, NewHook().Fire(tt.entry))
			assert.Empty(t, tt.entry.Data, "an entry without a sampled tracer must be left alone")
		})
	}
}

// A hook registered for fewer levels would silently skip the ids on the log
// lines that matter most.
func TestHook_Levels(t *testing.T) {
	assert.ElementsMatch(t, logrus.AllLevels, NewHook().Levels())
}

// One hook instance serves every log call in a process, so concurrent logging
// through it must stay race-free. Run under -race.
func TestHook_ConcurrentLogging(t *testing.T) {
	startAgent(t)

	logger, _ := jsonLogger(t)
	logger.AddHook(NewHook())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracer := pinpoint.GetAgent().NewSpanTracer("test", "/hello")
			defer tracer.EndSpan()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				logger.WithContext(ctx).WithField("foo", "bar").Info("message")
			}
		}()
	}
	wg.Wait()
}
