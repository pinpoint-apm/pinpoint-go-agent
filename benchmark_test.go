package pinpoint

// Hot-path micro-benchmarks.
//
// These cover the code that runs inside the instrumented application on every
// request / every traced function call. They establish a baseline (time/op and
// allocs/op) so that optimizations to the hot path can be measured objectively.
//
// Run (excluding unit tests, with allocation reporting):
//
//	go test -run=^$ -bench=. -benchmem
//	go test -run=^$ -bench=Parallel -benchmem -cpu=1,4,8   // scalability under contention
//
// The parallel variants are the important ones for shared-state contention:
// the apiCache RWMutex (cacheSpanApi) and the activeSpan sync.Map are taken on
// every span event / span, so their cost only shows up across multiple cores.

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/sirupsen/logrus"
)

// benchAgent returns an agent suitable for hot-path benchmarking: gRPC disabled,
// a rate-1 sampler (sample everything), and call-stack limits raised so a single
// long-lived span never trips the overflow path mid-benchmark.
func benchAgent() *agent {
	cfg := defaultConfig()
	cfg.spanMaxEventSequence = math.MaxInt32
	cfg.spanMaxEventDepth = math.MaxInt32

	// Keep trace/debug guards short-circuited (the production-relevant condition
	// for the hot path) regardless of any level a previously run unit test left
	// on the global logger. Warn also suppresses the one-time cache-miss INFO
	// lines so they don't clutter benchmark output.
	logger.defaultLogger.SetLevel(logrus.WarnLevel)
	logger.extraLogger = nil

	return newTestAgent(cfg)
}

func benchSpan(a *agent) *span {
	s := defaultSpan()
	s.agent = a
	return s
}

// startDrain consumes spanChan/metaChan in the background so enqueue stays on
// its non-blocking fast path, mirroring production where the sender keeps up.
// Without this, the buffered channels fill after ~1024 chunks and the benchmark
// would instead measure the queue-full drop path.
func startDrain(a *agent) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-a.spanChan:
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			case <-a.metaChan:
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
	}
}

// BenchmarkNewSampledSpan measures the per-request fixed cost of allocating a
// sampled span: the span struct, eventStack, spanEvents/errorChains slices, plus
// the cacheSpanApi lookup for the entry-point operation.
func BenchmarkNewSampledSpan(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = newSampledSpan(a, "operation", "/bench/rpc")
	}
}

// BenchmarkSpanEvent measures one instrumented function call: NewSpanEvent +
// EndSpanEvent. This is the single most frequent hot-path operation and covers
// the API-id cache lookup (cacheSpanApi), the eventStack push/pop and the
// spanEventLock.
func BenchmarkSpanEvent(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	s := benchSpan(a)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.NewSpanEvent("operation").EndSpanEvent()
	}
}

// BenchmarkSpanEventNested measures a realistic call stack: depth nested events
// opened then closed per iteration.
func BenchmarkSpanEventNested(b *testing.B) {
	const depth = 10
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	s := benchSpan(a)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for d := 0; d < depth; d++ {
			s.NewSpanEvent("operation")
		}
		for d := 0; d < depth; d++ {
			s.EndSpanEvent()
		}
	}
}

// BenchmarkSpanEventParallel exercises the shared-state contention of the span
// event path across cores: every goroutine hits the same apiCache RWMutex in
// cacheSpanApi. Each goroutine owns its own span (spans are not goroutine-safe).
func BenchmarkSpanEventParallel(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		s := benchSpan(a)
		for pb.Next() {
			s.NewSpanEvent("operation").EndSpanEvent()
		}
	})
}

// BenchmarkSpanLifecycle measures a full transaction end to end: span creation,
// a few span events, and EndSpan. Beyond the event path this also covers the
// activeSpan sync.Map store/delete and the response-time atomics.
func BenchmarkSpanLifecycle(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	reader := &DistributedTracingContextMap{m: map[string]string{}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tracer := a.NewSpanTracerWithReader("operation", "/bench/rpc", reader)
		tracer.NewSpanEvent("event1").EndSpanEvent()
		tracer.NewSpanEvent("event2").EndSpanEvent()
		tracer.EndSpan()
	}
}

// BenchmarkSpanLifecycleParallel measures the full transaction path under
// contention: activeSpan sync.Map churn and apiCache lock across cores.
func BenchmarkSpanLifecycleParallel(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		reader := &DistributedTracingContextMap{m: map[string]string{}}
		for pb.Next() {
			tracer := a.NewSpanTracerWithReader("operation", "/bench/rpc", reader)
			tracer.NewSpanEvent("event1").EndSpanEvent()
			tracer.NewSpanEvent("event2").EndSpanEvent()
			tracer.EndSpan()
		}
	})
}

// BenchmarkExtractContinue isolates distributed-tracing header parsing on the
// continue-trace path (TraceID present): trace-id parsing + ParseInt + the
// activeSpan sync.Map store. The span is allocated once so the measurement
// reflects Extract itself, not span creation (covered by BenchmarkNewSampledSpan).
func BenchmarkExtractContinue(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	reader := &DistributedTracingContextMap{m: map[string]string{
		HeaderTraceId:               "test-agent^1610000000000^12345",
		HeaderSpanId:                "1234567890",
		HeaderParentSpanId:          "987654321",
		HeaderFlags:                 "0",
		HeaderParentApplicationName: "UpstreamApp",
		HeaderParentApplicationType: "1800",
		HeaderHost:                  "10.0.0.1:8080",
	}}
	s := benchSpan(a)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Extract(reader)
		dropSampledActiveSpan(s) // keep activeSpan balanced
	}
}

// BenchmarkExtractNewTrace isolates the new-trace path (empty carrier): txId/spanId
// generation rather than header parsing.
func BenchmarkExtractNewTrace(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	reader := &DistributedTracingContextMap{m: map[string]string{}}
	s := benchSpan(a)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Extract(reader)
		dropSampledActiveSpan(s)
	}
}

// BenchmarkSplitTransactionId isolates the allocation-free trace-id parser.
func BenchmarkSplitTransactionId(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _, _ = splitTransactionId("test-agent^1610000000000^12345")
	}
}

// BenchmarkCacheSpanApi isolates the per-event API-id cache lookup: the
// struct-keyed map read under the apiCache RWMutex RLock on the steady-state
// cache-hit path.
func BenchmarkCacheSpanApi(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	a.cacheSpanApi("operation", apiTypeDefault) // prime the cache

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = a.cacheSpanApi("operation", apiTypeDefault)
	}
}

// BenchmarkCacheSpanApiParallel measures apiCache RLock contention across cores.
func BenchmarkCacheSpanApiParallel(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	a.cacheSpanApi("operation", apiTypeDefault)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = a.cacheSpanApi("operation", apiTypeDefault)
		}
	})
}

// BenchmarkAnnotationAppendString measures recording a string annotation on the
// application hot path. Protobuf construction is deferred to getList(), so the
// append itself is just a slice append (the &annotation{} allocation here is
// benchmark setup, not part of Append).
func BenchmarkAnnotationAppendString(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := &annotation{}
		a.AppendString(AnnotationHttpUrl, "/bench/rpc?q=1")
	}
}

// BenchmarkAnnotationAppendIntStringString covers the SQL-id annotation shape on
// the hot path (the eager path previously allocated the PIntStringStringValue
// plus two StringValue wrappers per call).
func BenchmarkAnnotationAppendIntStringString(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := &annotation{}
		a.AppendIntStringString(AnnotationSqlId, 1, "params", "args")
	}
}

// BenchmarkAnnotationBuildList measures the deferred sender-side cost: record a
// few annotations and materialize the protobuf list via getList() (what the
// agent does once per span/event during serialization). Confirms that deferring
// construction does not increase the total work, only relocates it off the
// request hot path.
func BenchmarkAnnotationBuildList(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a := &annotation{}
		a.AppendString(AnnotationHttpUrl, "/bench/rpc?q=1")
		a.AppendInt(AnnotationHttpStatusCode, 200)
		a.AppendIntStringString(AnnotationSqlId, 1, "params", "args")
		_ = a.getList()
	}
}

// BenchmarkSetSQL measures SQL normalization (newSqlNormalizer.run) plus the SQL
// id cache lookup and annotation append — the per-query hot path for DB-heavy apps.
func BenchmarkSetSQL(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	s := benchSpan(a)
	const query = "SELECT id, name, email FROM users WHERE id = 1234 AND status = 'active' AND age > 21"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		se := newSpanEvent(s, "query")
		se.SetSQL(query, "")
	}
}

// BenchmarkSpanEventSetError measures the error-recording path (cacheError +
// errorString) without call-stack collection (errorTraceCallStack defaults off).
func BenchmarkSpanEventSetError(b *testing.B) {
	a := benchAgent()
	stop := startDrain(a)
	defer stop()
	s := benchSpan(a)
	err := errors.New("bench error")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		se := newSpanEvent(s, "operation")
		se.SetError(err)
	}
}
