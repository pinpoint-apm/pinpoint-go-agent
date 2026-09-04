package pinpoint

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_defaultSpan(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	assert.Equal(t, span.parentSpanId, int64(-1), "parentSpanId")
	assert.Equal(t, span.parentAppType, 1, "parentAppType")
	assert.Equal(t, span.eventDepth.Load(), int32(1), "eventDepth")
	assert.Equal(t, span.serviceType, int32(ServiceTypeGoApp), "serviceType")
	assert.NotNil(t, span.eventStack, "stack")
}

type DistributedTracingContextMap struct {
	m map[string]string
}

func (r *DistributedTracingContextMap) Get(key string) string {
	return r.m[key]
}

func (r *DistributedTracingContextMap) Set(key string, val string) {
	r.m[key] = val
}

func defaultTestSpan() *span {
	return testSpanWithConfig(defaultConfig())
}

// testSpanWithConfig pins the span to config, so callers that need non-default
// limits must set them before the span is created - a live span keeps the
// snapshot it was born with.
func testSpanWithConfig(config *Config) *span {
	return defaultSpan(newTestAgent(config))
}

func Test_span_Extract(t *testing.T) {
	type args struct {
		reader DistributedTracingContextReader
	}

	m := map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "67890",
		HeaderParentSpanId: "123",
	}

	tests := []struct {
		name string
		args args
	}{
		{"1", args{&DistributedTracingContextMap{m}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.Extract(tt.args.reader)

			assert.Equal(t, span.txId.AgentId, "t123456", "AgentId")
			assert.Equal(t, span.txId.StartTime, int64(12345), "StartTime")
			assert.Equal(t, span.txId.Sequence, int64(1), "Sequence")
			assert.Equal(t, span.spanId, int64(67890), "spanId")
			assert.Equal(t, span.parentSpanId, int64(123), "parentSpanId")
		})
	}
}

func Test_span_Extract_malformedTraceId(t *testing.T) {
	// A malformed or hostile Pinpoint-TraceID must not panic. The span starts
	// a brand new transaction and ignores every other Pinpoint header, so no
	// orphan "root" span pointing at a foreign parent reaches the collector.
	cases := []string{
		"no-separator",    // missing both separators (would index s[1] before)
		"agent^123",       // missing sequence separator (would index s[2] before)
		"agent^bad^worse", // non-numeric time/sequence
		"a^b^c^d",         // too many fields
	}
	for _, tid := range cases {
		t.Run(tid, func(t *testing.T) {
			span := defaultTestSpan()
			reader := &DistributedTracingContextMap{m: map[string]string{
				HeaderTraceId:               tid,
				HeaderSpanId:                "67890",
				HeaderParentSpanId:          "123",
				HeaderParentApplicationName: "upstream",
				HeaderHost:                  "upstream:8080",
			}}

			assert.NotPanics(t, func() { span.Extract(reader) }, "Extract must not panic")
			assert.Equal(t, span.agent.agentID, span.txId.AgentId, "a new local transaction id is assigned")
			assert.Equal(t, int64(-1), span.parentSpanId, "root span")
			assert.NotEqual(t, int64(67890), span.spanId, "span id header must be ignored")
			assert.NotEqual(t, int64(0), span.spanId, "span id is generated")
			assert.Empty(t, span.parentAppName, "parent app header must be ignored")
			assert.Empty(t, span.acceptorHost, "host header must be ignored")
		})
	}
}

func Test_span_Extract_malformedSpanIds(t *testing.T) {
	span := defaultTestSpan()
	span.Extract(&DistributedTracingContextMap{m: map[string]string{
		HeaderTraceId:      "t123456^12345^1",
		HeaderSpanId:       "abc",
		HeaderParentSpanId: "0x10",
	}})

	assert.Equal(t, "t123456", span.txId.AgentId, "valid trace id is kept")
	assert.NotEqual(t, int64(0), span.spanId, "span id is generated on parse failure")
	assert.Equal(t, int64(-1), span.parentSpanId, "parent span id falls back to root")
}

func Test_splitTransactionId(t *testing.T) {
	tests := []struct {
		tid       string
		ok        bool
		agentId   string
		startTime int64
		sequence  int64
	}{
		{"t123456^12345^1", true, "t123456", 12345, 1},
		{"abcdefghijklmnopqrstuvwx^1^2", true, "abcdefghijklmnopqrstuvwx", 1, 2}, // 24-char agentId
		{"a^9223372036854775807^0", true, "a", math.MaxInt64, 0},
		{"", false, "", 0, 0},
		{"abc", false, "", 0, 0},
		{"abc^1", false, "", 0, 0},
		{"a^b^c^d", false, "", 0, 0},
		{"agent^abc^1", false, "", 0, 0},
		{"agent^1^abc", false, "", 0, 0},
		{"agent^-1^1", false, "", 0, 0},
		{"agent^^1", false, "", 0, 0},
		{"agent^1^", false, "", 0, 0},
		{"^1^2", false, "", 0, 0},
		{"abcdefghijklmnopqrstuvwxy^1^2", false, "", 0, 0}, // 25-char agentId
		{"a^9223372036854775808^0", false, "", 0, 0},       // overflows int64
		{"a^123456789012345678901^0", false, "", 0, 0},     // 21 digits
	}
	for _, tt := range tests {
		t.Run(tt.tid, func(t *testing.T) {
			agentId, startTime, sequence, ok := splitTransactionId(tt.tid)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.agentId, agentId)
			assert.Equal(t, tt.startTime, startTime)
			assert.Equal(t, tt.sequence, sequence)
		})
	}
}

// Test_TransactionId_String pins the hand-rolled formatter to the "%s^%d^%d" it
// replaced, including the widest int64s - which is what sizes its stack buffer.
func Test_TransactionId_String(t *testing.T) {
	for _, tid := range []TransactionId{
		{"t123456", 12345, 1},
		{"", 0, 0},
		{"agent-id", math.MinInt64, math.MaxInt64},
		{"agent-id", math.MaxInt64, math.MinInt64},
		{"agent-id", -1, -1},
	} {
		want := fmt.Sprintf("%s^%d^%d", tid.AgentId, tid.StartTime, tid.Sequence)
		assert.Equal(t, want, tid.String())
	}
}

func Test_span_Inject(t *testing.T) {
	type args struct {
		writer DistributedTracingContextWriter
	}

	m := make(map[string]string)

	tests := []struct {
		name string
		args args
	}{
		{"1", args{&DistributedTracingContextMap{m}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.txId.AgentId = "t123456"
			span.txId.StartTime = int64(12345)
			span.txId.Sequence = int64(1)
			span.NewSpanEvent("t")

			span.Inject(tt.args.writer)
			assert.Equal(t, m[HeaderTraceId], span.txId.String(), "headerTraceId")
		})
	}
}

func Test_span_Inject_EventOverflow(t *testing.T) {
	// Overflow limits profiling detail; it is not a sampling decision. The
	// trace context must still be written or the downstream starts a new
	// trace and the call chain is cut here.
	// The limits are the smallest publishable ones: applyDynamicConfig clamps
	// MaxCallStackDepth to minEventDepth and MaxCallStackSequence to
	// minEventSequence, so the event counts below are what it takes to overflow.
	tests := []struct {
		name     string
		limitOpt string
		limit    int
		overflow func(s *span)
	}{
		{"depth overflow - ancestor event left on the stack", CfgSpanMaxCallStackDepth, minEventDepth, func(s *span) {
			s.NewSpanEvent("t1")
			s.NewSpanEvent("t2")
		}},
		{"sequence overflow - empty stack", CfgSpanMaxCallStackSequence, minEventSequence, func(s *span) {
			for i := 0; i < minEventSequence; i++ {
				s.NewSpanEvent("t1").EndSpanEvent()
			}
			s.NewSpanEvent("t2")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := defaultConfig()
			config.Set(tt.limitOpt, tt.limit)
			s := testSpanWithConfig(config)
			s.spanId = int64(12345)
			s.txId = TransactionId{AgentId: "t123456", StartTime: int64(12345), Sequence: int64(1)}
			tt.overflow(s)
			assert.Equal(t, s.eventOverflow.Load(), int32(1), "eventOverflow")

			m := make(map[string]string)
			s.Inject(&DistributedTracingContextMap{m})

			assert.Equal(t, m[HeaderTraceId], s.txId.String(), "HeaderTraceId")
			assert.Equal(t, m[HeaderParentSpanId], "12345", "HeaderParentSpanId")
			assert.NotEmpty(t, m[HeaderSpanId], "HeaderSpanId")
			assert.NotEqual(t, m[HeaderSpanId], m[HeaderParentSpanId], "nextSpanId != spanId")

			// the dropped event carries no link back, and an ancestor event
			// must not be credited with a call it did not make
			if se, ok := s.eventStack.peek(); ok {
				assert.Equal(t, se.nextSpanId, int64(0), "ancestor event nextSpanId")
			}
		})
	}
}

func Test_span_NewSpanEvent(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence.Load(), int32(1), "eventSequence")
			assert.Equal(t, span.eventDepth.Load(), int32(2), "eventDepth")
			assert.Equal(t, span.eventStack.len(), int(1), "stack.len")

			se, exist := span.eventStack.peek()
			assert.Equal(t, exist, true, "eventStack.peek")
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
		})
	}
}

func Test_span_NewSpanEventDepthOverflow(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			config := defaultConfig()
			config.Set(CfgSpanMaxCallStackDepth, 3)
			s := testSpanWithConfig(config)

			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence.Load(), int32(2), "eventSequence")
			assert.Equal(t, s.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, s.eventOverflow.Load(), int32(2), "eventOverflow")
			assert.Equal(t, s.eventOverflowLog.Load(), true, "eventOverflowLog")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 1, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 0, "stack.len()")

			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence.Load(), int32(4), "eventSequence")
			assert.Equal(t, s.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, s.eventOverflow.Load(), int32(2), "eventOverflow")
			assert.Equal(t, s.eventOverflowLog.Load(), true, "eventOverflowLog")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			_, ok := s.SpanEvent().(*noopSpanEvent)
			assert.Equal(t, ok, true, "noopSpanEvent")

			tracer := s.NewGoroutineTracer()
			noop, ok := tracer.(*noopSpan)
			assert.Equal(t, ok, true, "noopSpan")
			assert.Equal(t, noop.IsSampled(), false, "IsSampled")
			assert.Equal(t, noop.SpanId(), int64(0), "SpanId")
			assert.Equal(t, noop.withStats, false, "SpanId")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			_, ok = s.SpanEvent().(*noopSpanEvent)
			assert.Equal(t, ok, false, "noopSpanEvent")

			se, ok := s.SpanEvent().(*spanEvent)
			assert.Equal(t, ok, true, "spanEvent")
			assert.Equal(t, se.depth, int32(2), "depth")
			assert.Equal(t, se.sequence, int32(3), "sequence")

			tracer = s.NewGoroutineTracer()
			ss, ok := tracer.(*span)
			assert.Equal(t, ok, true, "span")
			assert.Equal(t, tracer.IsSampled(), true, "IsSampled")
			assert.Equal(t, ss.isAsyncSpan(), true, "isAsyncSpan")
			tracer.EndSpan()

			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 1, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 0, "stack.len()")
		})
	}
}

func Test_span_NewSpanEventSequenceOverflow(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			config := defaultConfig()
			config.Set(CfgSpanMaxCallStackSequence, 5)
			span := testSpanWithConfig(config)

			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName)
			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence.Load(), int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence.Load(), int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, span.eventOverflowLog.Load(), true, "eventOverflowLog")
			assert.Equal(t, span.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence.Load(), int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow.Load(), int32(2), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(2), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 1, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, span.eventDepth.Load(), int32(1), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 0, "stack.len()")
		})
	}
}

func Test_span_EndSpan(t *testing.T) {
	type args struct {
		spanEvents []string
	}
	tests := []struct {
		name string
		args args
	}{
		{"check end span without span events", args{[]string{}}},
		{"check end span clears all the span events", args{[]string{"t1", "t2", "t3"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			for _, event := range tt.args.spanEvents {
				span.NewSpanEvent(event)
			}
			span.EndSpan()
			assert.Equal(t, span.eventStack.len(), 0, "stack.len()")
		})
	}
}

// A Tracer instruments a single call stack, but plugins cannot always keep one
// on a single goroutine - a gRPC client stream, gocql's speculative execution
// and pgxpool's background dial all pair events from goroutines the library
// spawns. That misuse may corrupt the trace, but it must never be a data race
// on the span's counters. Run under -race.
func Test_span_ConcurrentEventPairingIsRaceFree(t *testing.T) {
	span := defaultTestSpan()

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				span.NewSpanEvent("concurrent")
				span.EndSpanEvent()
			}
		}()
	}
	wg.Wait()
	span.EndSpan()
}

func Test_span_EndSpanEvent(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.NewSpanEvent(tt.args.operationName)
			span.NewSpanEvent("t2")
			assert.Equal(t, span.eventStack.len(), int(2), "stack.len()")
			span.EndSpanEvent()
			assert.Equal(t, span.eventStack.len(), int(1), "stack.len()")
			span.EndSpanEvent()
			assert.Equal(t, span.eventStack.len(), int(0), "stack.len()")
			span.EndSpanEvent()
			assert.Equal(t, span.eventStack.len(), int(0), "stack.len()")
		})
	}
}

func Test_span_NewGoroutineTracer(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := defaultTestSpan()
			s.NewSpanEvent(tt.args.operationName)
			a := s.NewGoroutineTracer()

			se, _ := s.eventStack.peek()
			assert.Equal(t, se.asyncId, int32(1), "asyncId")
			assert.Equal(t, se.asyncSeqGen, int32(1), "asyncSeqGen")

			as := a.(*span)
			assert.Equal(t, as.agent, s.agent, "agent")
			assert.Equal(t, as.txId, s.txId, "txId")
			assert.Equal(t, as.spanId, s.spanId, "spanId")

			ase, _ := as.eventStack.peek()
			assert.Equal(t, ase.serviceType, int32(100), "serviceType")
		})
	}
}

func Test_span_WrapGoroutine(t *testing.T) {
	type args struct {
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{"t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := defaultTestSpan()
			s.NewSpanEvent(tt.args.operationName)
			f := s.WrapGoroutine("t1", func(ctx context.Context) {
				tracer := FromContext(ctx)
				as := tracer.(*span)
				assert.Equal(t, as.agent, s.agent, "agent")
				assert.Equal(t, as.txId, s.txId, "txId")
				assert.Equal(t, as.spanId, s.spanId, "spanId")

				ase, _ := as.eventStack.peek()
				assert.Equal(t, ase.serviceType, int32(ServiceTypeGoFunction), "serviceType")
				assert.Equal(t, as.eventStack.len(), 2, "stack.len()")
			}, context.Background())

			se, _ := s.eventStack.peek()
			assert.Equal(t, se.asyncId, int32(1), "asyncId")
			assert.Equal(t, se.asyncSeqGen, int32(1), "asyncSeqGen")

			f()
		})
	}
}

func TestSpan_AddMetric_IgnoresWrongValueType(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))

	assert.NotPanics(t, func() {
		span.AddMetric(MetricURLStat, UrlStatEntry{Url: "/", Status: 200})
		NoopTracer().AddMetric(MetricURLStat, UrlStatEntry{Url: "/", Status: 200})
	})
}

func TestSpan_AddMetric_IgnoresTypedNilURLStat(t *testing.T) {
	config := defaultConfig()
	config.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(config)
	sampled := defaultSpan(agent)
	unsampled := &noopSpan{agent: agent, cfg: config.load(), withStats: true}
	var entry *UrlStatEntry

	assert.NotPanics(t, func() {
		sampled.AddMetric(MetricURLStat, entry)
		unsampled.AddMetric(MetricURLStat, entry)
	})
	assert.Nil(t, sampled.urlStat)
	assert.Nil(t, unsampled.urlStat)
}

func TestSpan_EndSpanTwiceCountsOnce(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	span := defaultSpan(agent)

	span.EndSpan()
	span.EndSpan()

	var requests int64
	for i := range agent.stats.shards {
		requests += atomic.LoadInt64(&agent.stats.shards[i].requestCount)
	}
	assert.Equal(t, int64(1), requests, "response time collected once")
}

func TestNoopSpan_EndSpanTwiceCountsOnce(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	span := newUnSampledSpan(agent, "/")

	span.SetFailure()
	assert.Equal(t, 1, span.statusErr, "unsampled span still records failure")

	span.EndSpan()
	span.EndSpan()

	var requests int64
	for i := range agent.stats.shards {
		requests += atomic.LoadInt64(&agent.stats.shards[i].requestCount)
	}
	assert.Equal(t, int64(1), requests, "response time collected once")
}

// The shared noop singleton must stay immutable: concurrent tracer-less
// requests call SetFailure on it. Run under -race.
func TestNoopSpan_SharedSingletonSetFailureIsRaceFree(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				NoopTracer().Span().SetFailure()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 0, defaultNoopSpan.statusErr, "singleton untouched")
}

// Span ids are int64: bitSize 0 (platform int) dropped an upstream node's id
// on a 32-bit build, silently breaking the trace chain.
func TestSpan_ExtractParsesFullRangeSpanId(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	reader := &DistributedTracingContextMap{m: map[string]string{
		HeaderTraceId:      "agent^1^1",
		HeaderSpanId:       "9007199254740993",
		HeaderParentSpanId: "-9007199254740993",
	}}

	span.Extract(reader)
	assert.Equal(t, int64(9007199254740993), span.spanId, "spanId")
	assert.Equal(t, int64(-9007199254740993), span.parentSpanId, "parentSpanId")
}

// EndSpanEvent records a panic and re-panics; the value an upstream recover
// sees must be the original one, or sentinel comparisons stop matching.
func TestSpan_EndSpanEventRepanicsOriginalValue(t *testing.T) {
	span := defaultTestSpan()
	span.NewSpanEvent("event")

	var got interface{}
	func() {
		defer func() { got = recover() }()
		defer span.EndSpanEvent()
		panic("sentinel")
	}()

	assert.Equal(t, "sentinel", got, "original panic value preserved")
}

// The event that records a panic must still be shipped: EndSpanEvent used to
// re-panic before appending the popped event, so the crash site's event never
// reached the collector.
// A call stack overflow blocks span events only; span.SetError must still
// record the transaction failure and its exception info on the PSpan.
func TestSpan_SetErrorDuringEventOverflow(t *testing.T) {
	config := defaultConfig()
	config.Set(CfgSpanMaxCallStackDepth, 1) // clamped to minEventDepth
	agent := newTestAgent(config)
	agent.spanGrpc = newMockSpanGrpc(agent)

	span := defaultSpan(agent)
	for span.eventOverflow.Load() == 0 {
		span.NewSpanEvent("t")
	}
	span.SetError(fmt.Errorf("boom"))

	agent.spanGrpc.sendSpanBatchAsync([]*spanChunk{span.newEventChunk(true)})
	agent.spanGrpc.awaitInFlightSpanBatch()

	client := agent.spanGrpc.spanClient.(*mockSpanGrpcClient)
	batch := client.lastRequest().GetSpan()
	if !assert.Len(t, batch, 1) {
		return
	}
	pspan := batch[0].GetSpan()
	assert.Equal(t, int32(1), pspan.GetErr(), "Err")
	if assert.NotNil(t, pspan.GetExceptionInfo(), "ExceptionInfo") {
		assert.Equal(t, "boom", pspan.GetExceptionInfo().GetStringValue().GetValue())
	}
}

func TestSpan_SetErrorAfterEndSpanIsNoop(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	span.EndSpan()
	span.SetError(fmt.Errorf("late"))

	assert.Equal(t, 0, span.err, "err")
	assert.Equal(t, "", span.errorString, "errorString")
}

func TestSpan_EndSpanEventRecordsPanickedEvent(t *testing.T) {
	span := defaultTestSpan()
	span.NewSpanEvent("event")

	func() {
		defer func() { recover() }()
		defer span.EndSpanEvent()
		panic("sentinel")
	}()

	if assert.Len(t, span.spanEvents, 1, "panicked event recorded") {
		assert.Equal(t, "sentinel", span.spanEvents[0].errorString)
		assert.True(t, span.spanEvents[0].finished.Load(), "recorded before end() marked it finished")
	}
}

// Setters on a pointer kept past EndSpanEvent are dropped: the event may
// already be in a chunk the sender goroutine is serializing.
func TestSpanEvent_SettersAfterEndAreNoops(t *testing.T) {
	span := defaultTestSpan()
	span.NewSpanEvent("event")
	se := span.SpanEvent().(*spanEvent)
	span.EndSpanEvent()

	se.SetError(errors.New("late"))
	se.SetServiceType(ServiceTypeMysql)
	se.SetDestination("db")
	se.SetEndPoint("host:1")
	se.SetSQL("select 1", "")
	se.Annotations().AppendString(AnnotationApi, "late")

	assert.True(t, se.finished.Load())
	assert.Equal(t, "", se.errorString)
	assert.Equal(t, int32(ServiceTypeGoFunction), se.serviceType)
	assert.Equal(t, "", se.destinationId)
	assert.Equal(t, "", se.endPoint)
	assert.Empty(t, se.annotations.values)
	_, noop := se.Annotations().(*noopAnnotation)
	assert.True(t, noop, "Annotations after end is a no-op collector")
}

// Run under -race: late setters race with the sender serializing the chunk.
func TestSpanEvent_LateSetterConcurrentWithSenderIsRaceFree(t *testing.T) {
	span := defaultTestSpan()
	span.operationName = "op"
	span.apiId = 0 // exercise the builder-local AnnotationApi fallback
	span.NewSpanEvent("event")
	se := span.SpanEvent().(*spanEvent)
	se.apiId = 0
	span.EndSpanEvent()
	span.EndSpan()

	chunk, ok := span.agent.spanQueue.tryDequeue()
	if !assert.True(t, ok) {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			se.SetError(errors.New("late"))
			se.SetEndPoint("host")
			se.SetDestination("db")
			se.SetServiceType(ServiceTypeMysql)
			se.Annotations().AppendString(AnnotationApi, "late")
		}
	}()
	go func() {
		defer wg.Done()
		b := &spanMessageBuilder{}
		for i := 0; i < 100; i++ {
			b.makePSpanMessage(chunk)
		}
	}()
	wg.Wait()

	assert.Empty(t, se.annotations.values, "fallback not written back to the event")
	assert.Empty(t, span.annotations.values, "fallback not written back to the span")
}

// Concurrent EndSpan calls enqueue exactly one final chunk.
func TestSpan_ConcurrentEndSpanEnqueuesOneChunk(t *testing.T) {
	agent := newTestAgent(defaultConfig())
	span := defaultSpan(agent)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			span.EndSpan()
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, agent.spanQueue.length())
}
