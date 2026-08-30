package pplogrus

import (
	"bytes"
	"context"
	"testing"

	"github.com/pinpoint-apm/pinpoint-go-agent"
	"github.com/sirupsen/logrus"
)

func startAgent(t *testing.T) {
	t.Helper()
	config, err := pinpoint.NewConfig(pinpoint.WithAppName("testApp"), pinpoint.WithAgentId("testAgent"))
	if err != nil {
		t.Fatal(err)
	}
	agent, err := pinpoint.NewTestAgent(config, t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Shutdown)
}

func newTracer(t *testing.T) pinpoint.Tracer {
	t.Helper()
	tracer := pinpoint.GetAgent().NewSpanTracer("test", "/hello")
	t.Cleanup(tracer.EndSpan)
	if !tracer.IsSampled() {
		t.Fatal("the test agent produced an unsampled tracer")
	}
	return tracer
}

// The two fields are what lets the Pinpoint web UI jump from a log line to the
// span that produced it, so both have to carry the tracer's own ids.
func TestNewField(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	fields := NewField(tracer)

	if got, want := fields[pinpoint.LogTransactionIdKey], tracer.TransactionId().String(); got != want {
		t.Errorf("%s = %v, want %v", pinpoint.LogTransactionIdKey, got, want)
	}
	if got, want := fields[pinpoint.LogSpanIdKey], tracer.SpanId(); got != want {
		t.Errorf("%s = %v, want %v", pinpoint.LogSpanIdKey, got, want)
	}
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			if fields := NewField(tt.tracer); len(fields) != 0 {
				t.Errorf("NewField() = %v, want no fields", fields)
			}
		})
	}
}

// WithField is the deprecated spelling and has to stay equivalent.
func TestWithField(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	deprecated, current := WithField(tracer), NewField(tracer)

	if len(deprecated) != len(current) {
		t.Fatalf("WithField() = %v, NewField() = %v", deprecated, current)
	}
	for k, v := range current {
		if deprecated[k] != v {
			t.Errorf("WithField()[%s] = %v, want %v", k, deprecated[k], v)
		}
	}
}

// The entry constructors are what most applications use, and both have to end
// up with the same fields NewField produces - on the standard logger and on a
// provided one.
func TestNewEntryAndNewLoggerEntry(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger := logrus.New()
	logger.SetOutput(&bytes.Buffer{})

	for name, entry := range map[string]*logrus.Entry{
		"NewEntry":       NewEntry(tracer),
		"NewLoggerEntry": NewLoggerEntry(logger, tracer),
	} {
		if got, want := entry.Data[pinpoint.LogTransactionIdKey], tracer.TransactionId().String(); got != want {
			t.Errorf("%s: %s = %v, want %v", name, pinpoint.LogTransactionIdKey, got, want)
		}
		if got, want := entry.Data[pinpoint.LogSpanIdKey], tracer.SpanId(); got != want {
			t.Errorf("%s: %s = %v, want %v", name, pinpoint.LogSpanIdKey, got, want)
		}
	}

	if NewLoggerEntry(logger, tracer).Logger != logger {
		t.Error("NewLoggerEntry did not use the provided logger")
	}
}

// The hook takes the tracer from the entry's context instead of the call site,
// so it has to add the same two fields to whatever the application logs.
func TestHook_Fire(t *testing.T) {
	startAgent(t)
	tracer := newTracer(t)

	logger := logrus.New()
	var out bytes.Buffer
	logger.SetOutput(&out)
	logger.AddHook(NewHook())

	logger.WithContext(pinpoint.NewContext(context.Background(), tracer)).
		WithField("foo", "bar").
		Error("hook log message")

	logged := out.String()
	for _, want := range []string{pinpoint.LogTransactionIdKey, pinpoint.LogSpanIdKey, "foo=bar"} {
		if !bytes.Contains([]byte(logged), []byte(want)) {
			t.Errorf("log line %q is missing %q", logged, want)
		}
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
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewHook().Fire(tt.entry); err != nil {
				t.Fatalf("Fire() = %v", err)
			}
			if len(tt.entry.Data) != 0 {
				t.Errorf("Fire() added %v, want nothing", tt.entry.Data)
			}
		})
	}
}

// A hook registered for fewer levels would silently skip the ids on the log
// lines that matter most.
func TestHook_Levels(t *testing.T) {
	if got := NewHook().Levels(); len(got) != len(logrus.AllLevels) {
		t.Errorf("Levels() = %v, want all %d levels", got, len(logrus.AllLevels))
	}
}
