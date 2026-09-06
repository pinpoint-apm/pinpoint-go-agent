package pinpoint

import (
	"errors"
	"sync"
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
	require.Zero(t, defaultNoopSpan.statusErr.Load(), "the singleton starts at its zero value")

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

	assert.Zero(t, defaultNoopSpan.statusErr.Load(), "the singleton was written to")
}

// An unsampled span is a real transaction, so a recorded error must fail its
// URL stat as Java's DisableSpanRecorder.recordException does; otherwise the
// failure rate of low-sampled traffic is biased toward zero. Error.IgnoreErrors
// applies as on the sampled path.
func Test_noopSpan_SetError_FailsUrlStat(t *testing.T) {
	tests := []struct {
		name    string
		rules   []string
		err     error
		errName []string
		want    int
	}{
		{"an error fails the url stat", nil, errors.New("boom"), nil, 1},
		{"a nil error is ignored", nil, nil, nil, 0},
		{"an ignored error keeps the url stat successful", []string{"*errors.errorString:boom"}, errors.New("boom"), nil, 0},
		{"the errorName is matched against the rules", []string{"MyError:"}, errors.New("boom"), []string{"MyError"}, 0},
		{"a non-matching rule still fails", []string{"MyError:"}, errors.New("boom"), nil, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConfig(WithAppName("unsampledErrApp"), WithHttpUrlStatEnable(true), WithErrorIgnoreErrors(tt.rules...))
			require.NoError(t, err)
			a := newTestAgent(c)
			a.urlStatChan = make(chan *urlStat, 1)

			span := newUnSampledSpan(a, "/test")
			span.collectUrlStat(&UrlStatEntry{Url: "/test", Method: "GET"})
			span.SetError(tt.err, tt.errName...)
			span.EndSpan()

			stat := <-a.urlStatChan
			assert.Equal(t, tt.want, stat.statusErr)
		})
	}
}

// The singleton stands for "no trace at all": SetError must neither write it
// nor produce a URL stat.
func Test_noopSpan_SetError_SingletonUntouched(t *testing.T) {
	tracer := NoopTracer()
	tracer.Span().SetError(errors.New("boom"))
	tracer.EndSpan()

	assert.Zero(t, defaultNoopSpan.statusErr.Load())
	assert.Nil(t, defaultNoopSpan.urlStat)
}

// SetError and SetFailure may come from other goroutines while EndSpan reads
// statusErr. Run with -race.
func Test_noopSpan_SetError_ConcurrentWithEndSpan(t *testing.T) {
	c, err := NewConfig(WithAppName("unsampledRaceApp"), WithHttpUrlStatEnable(true))
	require.NoError(t, err)
	a := newTestAgent(c)
	a.urlStatChan = make(chan *urlStat, 1)

	span := newUnSampledSpan(a, "/test")
	span.collectUrlStat(&UrlStatEntry{Url: "/test", Method: "GET"})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); span.SetError(errors.New("boom")) }()
	go func() { defer wg.Done(); span.SetFailure() }()
	span.EndSpan()
	wg.Wait()

	require.Len(t, a.urlStatChan, 1)
	assert.Contains(t, []int{0, 1}, (<-a.urlStatChan).statusErr)
}
