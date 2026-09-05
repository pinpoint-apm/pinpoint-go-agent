package pinpoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only a span standing for a real, unsampled transaction may tell the callee
// not to trace. A noop tracer has no transaction behind it, so it must leave
// the callee free to start one of its own.
func Test_noopSpan_Inject(t *testing.T) {
	unsampled := func() *noopSpan {
		a := newTestAgent(defaultConfig())
		return newUnSampledSpan(a, "/test")
	}

	tests := []struct {
		name string
		span func() *noopSpan
		want map[string]string
	}{
		{"a noop tracer writes no header", func() *noopSpan { return &defaultNoopSpan }, map[string]string{}},
		{"an unsampled span propagates the decision", unsampled, map[string]string{HeaderSampled: "s0"}},
		{"an unsampled span's goroutine tracer propagates it too",
			func() *noopSpan { return unsampled().NewGoroutineTracer().(*noopSpan) },
			map[string]string{HeaderSampled: "s0"}},
		{"a noop tracer's goroutine tracer stays silent",
			func() *noopSpan { return defaultNoopSpan.NewAsyncSpan().(*noopSpan) },
			map[string]string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := make(map[string]string)
			tt.span().Inject(&DistributedTracingContextMap{m})
			assert.Equal(t, tt.want, m, "injected headers")
		})
	}
}

// The singleton is shared by every tracer-less request, so Inject must only
// read it. Run with -race.
func Test_noopSpan_Inject_SingletonNotMutated(t *testing.T) {
	require.Equal(t, noopSpan{}, defaultNoopSpan, "the singleton starts at its zero value")

	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				tracer := NoopTracer()
				tracer.Span().SetFailure()
				tracer.Inject(&DistributedTracingContextMap{make(map[string]string)})
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}

	assert.Equal(t, noopSpan{}, defaultNoopSpan, "the singleton was written to")
}
