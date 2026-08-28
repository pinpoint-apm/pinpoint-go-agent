package pprueidis

import (
	"context"
	"testing"
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
