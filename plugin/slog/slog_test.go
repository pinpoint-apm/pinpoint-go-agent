package ppslog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// jsonLogger returns a logger writing JSON through the instrumented handler, so
// a log line can be read back field by field instead of matched as a substring.
func jsonLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	return slog.New(NewHandler(slog.NewJSONHandler(&out, opts))), &out
}

func loggedFields(t *testing.T, out *bytes.Buffer) map[string]interface{} {
	t.Helper()
	line := strings.TrimSpace(out.String())
	require.NotEmpty(t, line, "nothing was logged")
	var fields map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(line), &fields))
	return fields
}

// The two attributes are what lets the Pinpoint web UI jump from a log line to
// the span that produced it, so both have to carry the tracer's own ids.
func TestNewAttrs(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	attrs := NewAttrs(tracer)

	require.Len(t, attrs, 2, "only the two correlation attributes belong in the record")
	assert.Equal(t, pinpoint.LogTransactionIdKey, attrs[0].Key)
	assert.Equal(t, tracer.TransactionId().String(), attrs[0].Value.String())
	assert.Equal(t, pinpoint.LogSpanIdKey, attrs[1].Key)
	assert.Equal(t, tracer.SpanId(), attrs[1].Value.Int64())
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
func TestNewAttrs_SetsLogging(t *testing.T) {
	startAgent(t)

	sampled := &tracerSpy{Tracer: newTracer(t)}
	sampled.span = &spanSpy{SpanRecorder: sampled.Tracer.Span()}
	NewAttrs(sampled)
	assert.Equal(t, int32(pinpoint.Logged), sampled.span.logging)

	unsampled := &tracerSpy{Tracer: pinpoint.NoopTracer()}
	unsampled.span = &spanSpy{SpanRecorder: unsampled.Tracer.Span()}
	NewAttrs(unsampled)
	assert.Equal(t, int32(pinpoint.NotLogged), unsampled.span.logging,
		"an unsampled span must not be marked as logged")
}

// The handler marks the span through the same path.
func TestHandler_HandleSetsLogging(t *testing.T) {
	startAgent(t)

	tracer := &tracerSpy{Tracer: newTracer(t)}
	tracer.span = &spanSpy{SpanRecorder: tracer.Tracer.Span()}

	logger, _ := jsonLogger(t)
	logger.InfoContext(pinpoint.NewContext(context.Background(), tracer), "message")

	assert.Equal(t, int32(pinpoint.Logged), tracer.span.logging)
}

// Application code reaches for the tracer before it knows whether one exists,
// so a nil or unsampled tracer has to yield no attributes rather than nil-panic
// or log ids that point at nothing.
func TestNewAttrs_WithoutASampledTracer(t *testing.T) {
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
			assert.Empty(t, NewAttrs(tt.tracer), "an unsampled tracer must contribute no attributes")
		})
	}
}

// The handler takes the tracer from the record's context instead of the call
// site, so it has to add the same two attributes to whatever the application
// logs.
func TestHandler_Handle(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger, out := jsonLogger(t)
	logger.With("foo", "bar").ErrorContext(pinpoint.NewContext(context.Background(), tracer), "handler log message")

	fields := loggedFields(t, out)
	assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	assert.Equal(t, float64(tracer.SpanId()), fields[pinpoint.LogSpanIdKey])
	assert.Equal(t, "bar", fields["foo"], "the application's own attributes must survive the handler")
	assert.Equal(t, "handler log message", fields["msg"])
}

// The handler is installed once for every level the logger emits, so the ids
// have to reach a debug line as readily as an error one.
func TestHandler_HandleOnEveryLevel(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)
	ctx := pinpoint.NewContext(context.Background(), tracer)

	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		t.Run(level.String(), func(t *testing.T) {
			logger, out := jsonLogger(t)
			logger.Log(ctx, level, "message")

			assert.Equal(t, tracer.TransactionId().String(), loggedFields(t, out)[pinpoint.LogTransactionIdKey])
		})
	}
}

// Most log lines are written without a context carrying a span. The handler
// must leave those records alone instead of failing the log call.
func TestHandler_HandleWithoutATracer(t *testing.T) {
	startAgent(t)

	for _, tt := range []struct {
		name string
		ctx  context.Context
	}{
		{"context without a span", context.Background()},
		{"context with a noop tracer", pinpoint.NewContext(context.Background(), pinpoint.NoopTracer())},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logger, out := jsonLogger(t)
			logger.InfoContext(tt.ctx, "message", "foo", "bar")

			fields := loggedFields(t, out)
			assert.Equal(t, "bar", fields["foo"])
			assert.NotContains(t, fields, pinpoint.LogTransactionIdKey)
			assert.NotContains(t, fields, pinpoint.LogSpanIdKey)
		})
	}
}

// slog.Logger.Info and friends pass context.Background(), which never carries a
// span. Such a line must still be written, without the ids.
func TestHandler_HandleWithoutAContext(t *testing.T) {
	startAgent(t)
	newTracer(t)

	logger, out := jsonLogger(t)
	logger.Info("message")

	fields := loggedFields(t, out)
	assert.Equal(t, "message", fields["msg"])
	assert.NotContains(t, fields, pinpoint.LogTransactionIdKey)
}

// WithAttrs and WithGroup have to be delegated, or the application's own
// attributes are dropped. The ids must survive both, and stay at the top level
// under a group - a qualified key is not the one the UI looks for.
func TestHandler_WithAttrsAndWithGroup(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)
	ctx := pinpoint.NewContext(context.Background(), tracer)

	t.Run("WithAttrs", func(t *testing.T) {
		logger, out := jsonLogger(t)
		logger.With("foo", "bar").InfoContext(ctx, "message")

		fields := loggedFields(t, out)
		assert.Equal(t, "bar", fields["foo"])
		assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	})

	t.Run("WithGroup", func(t *testing.T) {
		logger, out := jsonLogger(t)
		logger.WithGroup("req").With("foo", "bar").InfoContext(ctx, "message", "baz", "qux")

		fields := loggedFields(t, out)
		group, ok := fields["req"].(map[string]interface{})
		require.True(t, ok, "the group is missing: %v", fields)
		assert.Equal(t, "bar", group["foo"])
		assert.Equal(t, "qux", group["baz"])
		assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey],
			"the ids must not be qualified by the application's group")
		assert.Equal(t, float64(tracer.SpanId()), fields[pinpoint.LogSpanIdKey])
	})

	t.Run("attributes before and after a group", func(t *testing.T) {
		logger, out := jsonLogger(t)
		logger.With("before", 1).WithGroup("g").With("after", 2).InfoContext(ctx, "message")

		fields := loggedFields(t, out)
		assert.Equal(t, float64(1), fields["before"])
		group, ok := fields["g"].(map[string]interface{})
		require.True(t, ok, "the group is missing: %v", fields)
		assert.Equal(t, float64(2), group["after"])
		assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	})

	t.Run("nested groups", func(t *testing.T) {
		logger, out := jsonLogger(t)
		logger.WithGroup("outer").WithGroup("inner").InfoContext(ctx, "message", "foo", "bar")

		fields := loggedFields(t, out)
		outer, ok := fields["outer"].(map[string]interface{})
		require.True(t, ok, "the outer group is missing: %v", fields)
		inner, ok := outer["inner"].(map[string]interface{})
		require.True(t, ok, "the inner group is missing: %v", fields)
		assert.Equal(t, "bar", inner["foo"])
		assert.Equal(t, tracer.TransactionId().String(), fields[pinpoint.LogTransactionIdKey])
	})

	t.Run("group without a sampled tracer", func(t *testing.T) {
		logger, out := jsonLogger(t)
		logger.WithGroup("req").Info("message", "foo", "bar")

		fields := loggedFields(t, out)
		group, ok := fields["req"].(map[string]interface{})
		require.True(t, ok, "the group is missing: %v", fields)
		assert.Equal(t, "bar", group["foo"])
		assert.NotContains(t, fields, pinpoint.LogTransactionIdKey)
	})
}

// Enabled is what a logger asks before building a record, so dropping the
// delegation would silently re-enable the levels the wrapped handler disabled.
func TestHandler_Enabled(t *testing.T) {
	var out bytes.Buffer
	h := NewHandler(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelWarn}))

	assert.False(t, h.Enabled(context.Background(), slog.LevelInfo))
	assert.True(t, h.Enabled(context.Background(), slog.LevelWarn))
	assert.False(t, h.WithGroup("g").Enabled(context.Background(), slog.LevelInfo))
}

// One handler instance serves every log call in a process, so concurrent
// logging through it must stay race-free. Run under -race.
func TestHandler_ConcurrentLogging(t *testing.T) {
	startAgent(t)

	logger, _ := jsonLogger(t)
	grouped := logger.WithGroup("req").With("foo", "bar")

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracer := pinpoint.GetAgent().NewSpanTracer("test", "/hello")
			defer tracer.EndSpan()
			ctx := pinpoint.NewContext(context.Background(), tracer)
			for j := 0; j < 25; j++ {
				logger.InfoContext(ctx, "message", "foo", "bar")
				grouped.InfoContext(ctx, "message")
			}
		}()
	}
	wg.Wait()
}
