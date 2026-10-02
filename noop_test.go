package pinpoint

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

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
			func() *noopSpan { return defaultNoopSpan.NewGoroutineTracer().(*noopSpan) },
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

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 200; j++ {
				tracer := NoopTracer()
				tracer.Span().SetFailure()
				tracer.Inject(&DistributedTracingContextMap{make(map[string]string)})
			}
		})
	}
	wg.Wait()

	assert.Zero(t, defaultNoopSpan.statusErr.Load(), "the singleton was written to")
}

// An unsampled span is a real transaction, so a recorded error must fail its
// failure rate of low-sampled traffic is biased toward zero. Span.IgnoreErrors
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
			c, err := NewConfig(WithAppName("unsampledErrApp"), WithHttpUrlStatEnable(true), WithSpanIgnoreErrors(tt.rules...))
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

// The unsampled span follows span.collectUrlStat's policy: Url first-wins,
// Method and Status last-wins, the urlStatUnknown stand-in does not claim the
// slot, and the caller's entry is copied.
func Test_noopSpan_AddMetric_URLStatIsFirstWinsOnUrl(t *testing.T) {
	c, err := NewConfig(WithAppName("unsampledUrlStatApp"), WithHttpUrlStatEnable(true))
	require.NoError(t, err)
	span := newUnSampledSpan(newTestAgent(c), "/test")

	span.AddMetric(MetricURLStat, &UrlStatEntry{Method: "GET"})
	assert.Equal(t, urlStatUnknown, span.urlStat.Url)

	first := &UrlStatEntry{Url: "/users/{id}", Method: "GET"}
	span.AddMetric(MetricURLStat, first)
	span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/42", Method: "POST", Status: 500})
	assert.Equal(t, "/users/{id}", span.urlStat.Url, "first real Url is kept")
	assert.Equal(t, "POST", span.urlStat.Method, "Method is last-wins")
	assert.Equal(t, 500, span.urlStat.Status, "Status is last-wins")
	assert.NotSame(t, first, span.urlStat, "caller's entry is copied")
}

// The singleton stays untouched (withStats gate) and a disabled gate records
// nothing.
func Test_noopSpan_AddMetric_URLStatGates(t *testing.T) {
	NoopTracer().AddMetric(MetricURLStat, &UrlStatEntry{Url: "/singleton", Method: "GET"})
	assert.Nil(t, defaultNoopSpan.urlStat)

	span := newUnSampledSpan(newTestAgent(defaultConfig()), "/test")
	span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/{id}", Method: "GET"})
	assert.Nil(t, span.urlStat)
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
	wg.Go(func() { span.SetError(errors.New("boom")) })
	wg.Go(func() { span.SetFailure() })
	span.EndSpan()
	wg.Wait()

	require.Len(t, a.urlStatChan, 1)
	assert.Contains(t, []int{0, 1}, (<-a.urlStatChan).statusErr)
}

// doc/api_contracts.md promises that an error recorded on an async or
// unsampled path too: continueDisableAsyncContextTraceObject hands the child
// the parent's LocalTraceRoot, so DisableSpanRecorder writes the failure into
// the shared root. The child keeps no statistics of its own, so without the
// link the failure would vanish.
func Test_noopSpan_AsyncChild_FailsRootUrlStat(t *testing.T) {
	tests := []struct {
		name  string
		child func(*noopSpan) Tracer
		call  func(Tracer)
	}{
		{"goroutine child SetError", (*noopSpan).NewGoroutineTracer, func(tr Tracer) { tr.Span().SetError(errors.New("boom")) }},
		{"goroutine child SetFailure", (*noopSpan).NewGoroutineTracer, func(tr Tracer) { tr.Span().SetFailure() }},
		{"grandchild SetError", func(s *noopSpan) Tracer {
			return s.NewGoroutineTracer().NewGoroutineTracer()
		}, func(tr Tracer) { tr.Span().SetError(errors.New("boom")) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewConfig(WithAppName("unsampledAsyncApp"), WithHttpUrlStatEnable(true))
			require.NoError(t, err)
			a := newTestAgent(c)
			a.urlStatChan = make(chan *urlStat, 1)

			root := newUnSampledSpan(a, "/test")
			root.collectUrlStat(&UrlStatEntry{Url: "/test", Method: "GET"})

			child := tt.child(root)
			tt.call(child)
			// The child never ends the root's statistics (withStats singleton).
			child.EndSpan()
			require.Empty(t, a.urlStatChan, "the child ended the root's statistics")

			root.EndSpan()
			assert.Equal(t, 1, (<-a.urlStatChan).statusErr, "the child's failure did not reach the root")
		})
	}
}

// Span.IgnoreErrors must apply to a child exactly as it does to the root -
// the child carries no config of its own, so it reads the root's.
func Test_noopSpan_AsyncChild_IgnoreErrors(t *testing.T) {
	c, err := NewConfig(WithAppName("unsampledAsyncIgnoreApp"), WithHttpUrlStatEnable(true),
		WithSpanIgnoreErrors("*errors.errorString:boom"))
	require.NoError(t, err)
	a := newTestAgent(c)
	a.urlStatChan = make(chan *urlStat, 1)

	root := newUnSampledSpan(a, "/test")
	root.collectUrlStat(&UrlStatEntry{Url: "/test", Method: "GET"})
	root.NewGoroutineTracer().Span().SetError(errors.New("boom"))
	root.EndSpan()

	assert.Zero(t, (<-a.urlStatChan).statusErr, "an ignored error failed the root")
}

// An async child of the singleton resolves to the singleton, which every
// tracer-less request shares: it must stay read-only. Run with -race.
func Test_noopSpan_AsyncChild_SingletonUntouched(t *testing.T) {
	require.Zero(t, defaultNoopSpan.statusErr.Load(), "the singleton starts at its zero value")

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 200; j++ {
				child := NoopTracer().NewGoroutineTracer()
				child.Span().SetFailure()
				child.Span().SetError(errors.New("boom"))
				child.EndSpan()
			}
		})
	}
	wg.Wait()

	assert.Zero(t, defaultNoopSpan.statusErr.Load(), "the singleton was written to")
}

// Every recorder of the noop tracer is a no-op: plugins call them on the
// shared singleton from every tracer-less request at once, so none of them may
// write it. Run with -race.
func Test_noopSpan_RecordersLeaveSingletonUntouched(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			tracer := NoopTracer()
			span := tracer.Span()
			span.SetServiceType(ServiceTypeGoApp)
			span.SetRpcName("/rpc")
			span.SetRemoteAddress("127.0.0.1")
			span.SetEndPoint("host:80")
			span.SetAcceptorHost("host")
			span.SetLogging(1)
			recordNoopAnnotations(span.Annotations())

			tracer.NewSpanEvent("event")
			se := tracer.SpanEvent()
			se.SetError(errors.New("boom"), "name")
			se.SetServiceType(ServiceTypeMysqlExecuteQuery)
			se.SetDestination("db")
			se.SetEndPoint("db:3306")
			se.SetSQL("SELECT 1", "")
			se.FixDuration(time.Now(), time.Now())
			recordNoopAnnotations(se.Annotations())
			tracer.EndSpanEvent()
		})
	}
	wg.Wait()

	assert.Zero(t, defaultNoopSpan.spanId)
	assert.Empty(t, defaultNoopSpan.rpcName)
	assert.Zero(t, defaultNoopSpan.statusErr.Load())
	assert.False(t, defaultNoopSpan.withStats.Load())
	assert.Nil(t, defaultNoopSpan.urlStat)
}

func recordNoopAnnotations(a Annotation) {
	a.AppendInt(1, 1)
	a.AppendLong(1, 1)
	a.AppendString(1, "s")
	a.AppendStringString(1, "s1", "s2")
	a.AppendIntStringString(1, 1, "s1", "s2")
	a.AppendBytesStringString(1, []byte("b"), "s1", "s2")
	a.AppendLongIntIntByteByteString(1, 1, 1, 1, 1, 1, "s")
}

func Test_noopAgent(t *testing.T) {
	agent := NoopAgent()
	assert.False(t, agent.Enable())
	assert.Same(t, NoopTracer(), agent.NewSpanTracer("op", "/rpc"))
	assert.Same(t, NoopTracer(), agent.NewSpanTracerWithReader("op", "/rpc", &noopDistributedTracingContextReader{}))
	agent.Shutdown()
	assert.Same(t, NoopTracer(), agent.NewSpanTracer("op", "/rpc"), "Shutdown leaves the noop agent usable")
}

func Test_noopSpan_Identity(t *testing.T) {
	assert.Equal(t, "0^Noop", NoopTracer().AsyncSpanId())
	assert.JSONEq(t, "{}", string(NoopTracer().JsonString()))

	span := newUnSampledSpan(newTestAgent(defaultConfig()), "/test")
	defer span.EndSpan()
	assert.Equal(t, strconv.FormatInt(span.SpanId(), 10)+"^Noop", span.AsyncSpanId())
}

// The goroutine WrapGoroutine returns runs under a child of the tracer, which
// carries the parent's unsampled marker but never its statistics.
func Test_noopSpan_WrapGoroutine(t *testing.T) {
	type ctxKey struct{}
	parent := context.WithValue(context.Background(), ctxKey{}, "v")
	root := newUnSampledSpan(newTestAgent(defaultConfig()), "/test")
	defer root.EndSpan()

	var got Tracer
	var value interface{}
	root.WrapGoroutine("worker", func(ctx context.Context) {
		got = FromContext(ctx)
		value = ctx.Value(ctxKey{})
	}, parent)()

	child, ok := got.(*noopSpan)
	require.True(t, ok, "the goroutine runs under a noop child")
	assert.NotSame(t, root, child)
	assert.True(t, child.unsampled, "the unsampled marker is inherited")
	assert.False(t, child.withStats.Load(), "the statistics stay with the root")
	assert.Same(t, root, child.root())
	assert.Equal(t, "v", value, "the parent context's values are kept")
}
