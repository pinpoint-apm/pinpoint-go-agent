package pinpoint

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sirupsen/logrus"
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
		HeaderTraceId:               "t123456^12345^1",
		HeaderSpanId:                "67890",
		HeaderParentSpanId:          "123",
		HeaderParentApplicationName: "upstream",
		HeaderHost:                  "upstream:8080",
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
			assert.Equal(t, "upstream", span.parentAppName, "parentAppName")
			assert.Equal(t, "upstream:8080", span.acceptorHost, "acceptorHost")
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

// countActiveSpans totals the registry across shards: addSampledActiveSpan is
// keyed by span id, so a span registered under a stale id would show up as a
// second entry, and a path that forgot to register shows up as none.
func countActiveSpans(agent *agent) int {
	n := 0
	for i := range agent.stats.activeSpan.shards {
		s := &agent.stats.activeSpan.shards[i]
		s.mu.Lock()
		n += len(s.m)
		s.mu.Unlock()
	}
	return n
}

func Test_span_Extract_noTraceId(t *testing.T) {
	// No Pinpoint-TraceID means this request starts a new transaction, so the
	// upstream span/parent headers belong to a trace this span is not part of
	// and must not be adopted - otherwise the collector gets a non-root span
	// whose parent does not exist in the transaction.
	span := defaultTestSpan()
	reader := &DistributedTracingContextMap{m: map[string]string{
		HeaderSpanId:                "67890",
		HeaderParentSpanId:          "123",
		HeaderParentApplicationName: "upstream",
		HeaderParentApplicationType: "1010",
		HeaderParentServiceName:     "upstream-service",
		HeaderHost:                  "upstream:8080",
	}}

	span.Extract(reader)

	assert.Equal(t, span.agent.agentID, span.txId.AgentId, "a new local transaction id is assigned")
	assert.Equal(t, int64(-1), span.parentSpanId, "root span")
	assert.NotEqual(t, int64(67890), span.spanId, "span id header must be ignored")
	assert.NotEqual(t, int64(0), span.spanId, "span id is generated")
	assert.Empty(t, span.parentAppName, "parent app header must be ignored")
	assert.Equal(t, 1, span.parentAppType, "parent app type header must be ignored")
	assert.Empty(t, span.parentServiceName, "parent service name header must be ignored")
	assert.Empty(t, span.acceptorHost, "host header must be ignored")
	assert.Equal(t, 1, countActiveSpans(span.agent), "registered as active exactly once")
}

func Test_span_Extract_registersActiveSpanOnce(t *testing.T) {
	for _, tid := range []string{"t123456^12345^1", "malformed", ""} {
		t.Run(tid, func(t *testing.T) {
			span := defaultTestSpan()
			span.Extract(&DistributedTracingContextMap{m: map[string]string{
				HeaderTraceId: tid,
				HeaderSpanId:  "67890",
			}})

			assert.Equal(t, 1, countActiveSpans(span.agent), "active span registered once")
			dropSampledActiveSpan(span)
			assert.Equal(t, 0, countActiveSpans(span.agent), "registered under the final span id")
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
		{"agent^abc^1", false, "", 0, 0},
		{"agent^1^abc", false, "", 0, 0},
		{"agent^^1", false, "", 0, 0},
		{"agent^1^", false, "", 0, 0},
		{"^1^2", false, "", 0, 0},
		// No length bound on the agent id here: Java checks it when an agent
		// registers, not when it parses this header.
		{"abcdefghijklmnopqrstuvwxy^1^2", true, "abcdefghijklmnopqrstuvwxy", 1, 2}, // 25 chars
		{"a^9223372036854775808^0", false, "", 0, 0},                               // overflows int64
		{"a^123456789012345678901^0", false, "", 0, 0},                             // 21 digits: overflows, as Long.parseLong does
		// Numeric fields follow Long.parseLong, so a sign, leading zeros and a
		// fourth field are all accepted the way the Java agent accepts them.
		{"agent^-1^1", true, "agent", -1, 1},           // negative start time
		{"a^+1^-2", true, "a", 1, -2},                  // '+' prefix, negative sequence
		{"a^000000000000000000001^2", true, "a", 1, 2}, // 21 chars, leading zeros, fits int64
		{"a^1^2^3", true, "a", 1, 2},                   // 4th field ignored, not rejected
		{"a^1^2^", true, "a", 1, 2},                    // trailing delimiter after sequence
		{"a^b^c^d", false, "", 0, 0},                   // still rejected: 'b' is not a number
		{"a^ 1^2", false, "", 0, 0},                    // no whitespace, as Long.parseLong
		{"a^1^0x10", false, "", 0, 0},                  // base 10 only
		{"a^1^1_0", false, "", 0, 0},                   // no underscore separators
		// The agent id is re-emitted in outbound Pinpoint-TraceID headers and
		// reported to the collector, so it is held to the id charset instead
		// of being echoed as received.
		{"AZm7kQ2vRtYpLxNc0dHgUw^1^2", true, "AZm7kQ2vRtYpLxNc0dHgUw", 1, 2}, // v4 base64url agent id
		{"agent.host-1_x^1^2", true, "agent.host-1_x", 1, 2},                 // '.', '-', '_' allowed
		{"bad agent^1^2", false, "", 0, 0},                                   // space
		{"bad\r\nagent^1^2", false, "", 0, 0},                                // CRLF: header injection on inject
		{"bad\x00agent^1^2", false, "", 0, 0},                                // NUL
		{"agent/../x^1^2", false, "", 0, 0},                                  // '/'
		{"에이전트^1^2", false, "", 0, 0},                                        // non-ASCII
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

// stubSpanIdGenerator hands out ids in order and restores the real generator
// when the test ends.
func stubSpanIdGenerator(t *testing.T, ids ...int64) {
	orig := generateSpanId
	generateSpanId = func() int64 {
		id := ids[0]
		ids = ids[1:]
		return id
	}
	t.Cleanup(func() {
		assert.Empty(t, ids, "unused stub ids")
		generateSpanId = orig
	})
}

func Test_nextSpanId(t *testing.T) {
	tests := []struct {
		name         string
		spanId       int64
		parentSpanId int64
		generated    []int64
		want         int64
	}{
		{"no collision", 10, 20, []int64{30}, 30},
		{"collides with spanId", 10, 20, []int64{10, 30}, 30},
		{"collides with parentSpanId", 10, 20, []int64{20, 30}, 30},
		{"collides with the null id", 10, 20, []int64{-1, 30}, 30},
		{"collides more than once", 10, 20, []int64{10, -1, 20, 30}, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubSpanIdGenerator(t, tt.generated...)
			assert.Equal(t, tt.want, nextSpanId(tt.spanId, tt.parentSpanId), "nextSpanId")
		})
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

			// The normal path still carries every header it always has.
			for _, h := range []string{HeaderTraceId, HeaderSpanId, HeaderParentSpanId,
				HeaderFlags, HeaderParentApplicationName, HeaderParentApplicationType} {
				assert.Contains(t, m, h, h)
			}
			// A namespace this agent does not have is not sent as "": a Java
			// receiver with profiler.cluster.namespace set rejects the empty
			// value and starts a new trace instead of continuing this one.
			assert.NotContains(t, m, HeaderParentApplicationNamespace, HeaderParentApplicationNamespace)
		})
	}
}

// Pinpoint-Host names the node being called; with nothing to name it is left
// out rather than sent empty, as Java's DefaultRequestTraceWriter does.
func Test_span_Inject_Host(t *testing.T) {
	tests := []struct {
		name          string
		destinationId string
		want          bool
	}{
		{"a recorded destination is sent", "my-cluster", true},
		{"an empty destination is omitted", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.NewSpanEvent("t").SpanEvent().SetDestination(tt.destinationId)

			m := make(map[string]string)
			span.Inject(&DistributedTracingContextMap{m})

			if tt.want {
				assert.Equal(t, tt.destinationId, m[HeaderHost], HeaderHost)
			} else {
				assert.NotContains(t, m, HeaderHost, HeaderHost)
			}
		})
	}
}

// endPoint (the address actually contacted) and destinationId (the logical node
// label) are independent, as in Java: Inject fills in only an endPoint the
// plugin left unset, and never overwrites one it recorded.
func Test_span_Inject_EndPoint(t *testing.T) {
	tests := []struct {
		name          string
		endPoint      string
		destinationId string
		want          string
	}{
		{"a recorded endPoint survives", "a", "my-cluster", "a"},
		{"an unset endPoint falls back to the destination", "", "my-cluster", "my-cluster"},
		{"nothing recorded stays empty", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			se := span.NewSpanEvent("t").SpanEvent()
			se.SetEndPoint(tt.endPoint)
			se.SetDestination(tt.destinationId)

			span.Inject(&DistributedTracingContextMap{make(map[string]string)})

			cur, ok := span.eventStack.peek()
			if assert.True(t, ok, "event stack") {
				assert.Equal(t, tt.want, cur.endPoint, "endPoint")
				assert.Equal(t, tt.destinationId, cur.destinationId, "destinationId")
			}
		})
	}
}

func Test_span_Inject_EventOverflow(t *testing.T) {
	// Overflow limits profiling detail; it is not a sampling decision. The
	// trace context must still be written or the downstream starts a new
	// trace and the call chain is cut here.
	// The limits are the smallest publishable ones: applyDynamicConfig clamps
	// MaxCallStackDepth to minEventDepth and MaxCallStackSequence to
	// minEventSequence, so the event counts below are what it takes to overflow
	// (depth records max+1 levels, as Java does).
	tests := []struct {
		name     string
		limitOpt string
		limit    int
		overflow func(s *span)
	}{
		{"depth overflow - ancestor event left on the stack", CfgSpanMaxCallStackDepth, minEventDepth, func(s *span) {
			for i := 0; i <= minEventDepth+1; i++ {
				s.NewSpanEvent("t1")
			}
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
				assert.Equal(t, se.nextSpanId, int64(noneSpanId), "ancestor event nextSpanId")
			}

			// The overflowed event is dropped, but the destination it recorded
			// is not: without Pinpoint-Host the downstream cannot fill in
			// acceptorHost, endPoint or remoteAddr.
			assert.NotContains(t, m, HeaderHost, "no destination recorded")

			s.SpanEvent().SetDestination("my-cluster")
			m2 := make(map[string]string)
			s.Inject(&DistributedTracingContextMap{m2})
			assert.Equal(t, "my-cluster", m2[HeaderHost], HeaderHost)

			// and it belongs to this overflow only
			s.EndSpanEvent()
			assert.Equal(t, int32(0), s.eventOverflow.Load(), "eventOverflow")
			assert.Empty(t, s.overflowSe.destination(), "destination cleared")
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

			// Java records max+1 levels (push checks the pre-push count), so
			// 4 levels fit and only the 5th overflows.
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence.Load(), int32(4), "eventSequence")
			assert.Equal(t, s.eventDepth.Load(), int32(5), "eventDepth")
			assert.Equal(t, s.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, s.eventOverflowLog.Load(), true, "eventOverflowLog")
			assert.Equal(t, s.eventStack.len(), 4, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 4, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 3, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 1, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 0, "stack.len()")

			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence.Load(), int32(8), "eventSequence")
			assert.Equal(t, s.eventDepth.Load(), int32(5), "eventDepth")
			assert.Equal(t, s.eventOverflow.Load(), int32(1), "eventOverflow")
			assert.Equal(t, s.eventOverflowLog.Load(), true, "eventOverflowLog")
			assert.Equal(t, s.eventStack.len(), 4, "stack.len()")

			// Overflowed events record nothing, except the destination the
			// span keeps for Inject's Pinpoint-Host.
			ose, ok := s.SpanEvent().(*overflowSpanEvent)
			assert.Equal(t, ok, true, "overflowSpanEvent")
			ose.SetDestination("my-cluster")
			assert.Equal(t, "my-cluster", s.overflowSe.destination(), "destination")

			tracer := s.NewGoroutineTracer()
			noop, ok := tracer.(*noopSpan)
			assert.Equal(t, ok, true, "noopSpan")
			assert.Equal(t, noop.IsSampled(), false, "IsSampled")
			assert.Equal(t, noop.SpanId(), int64(0), "SpanId")
			assert.False(t, noop.withStats.Load(), "SpanId")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow.Load(), int32(0), "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 4, "stack.len()")

			_, ok = s.SpanEvent().(*noopSpanEvent)
			assert.Equal(t, ok, false, "noopSpanEvent")

			se, ok := s.SpanEvent().(*spanEvent)
			assert.Equal(t, ok, true, "spanEvent")
			assert.Equal(t, se.depth, int32(4), "depth")
			assert.Equal(t, se.sequence, int32(7), "sequence")

			tracer = s.NewGoroutineTracer()
			ss, ok := tracer.(*span)
			assert.Equal(t, ok, true, "span")
			assert.Equal(t, tracer.IsSampled(), true, "IsSampled")
			assert.Equal(t, ss.isAsyncSpan(), true, "isAsyncSpan")
			tracer.EndSpan()

			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 3, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 1, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 0, "stack.len()")
		})
	}
}

// Span.MaxCallStackDepth allows max+1 nesting levels, as Java does: in
// DefaultCallStack.push, isDepthOverflow checks maxDepth < index where index is
// the pre-push element count, so with maxDepth=3 the 4th push (index=3) is
// still recorded at depth 4 (CallStackTest). Go used to stop at depth max.
func Test_span_NewSpanEventDepthBoundary(t *testing.T) {
	for _, max := range []int{minEventDepth, 3} {
		t.Run(fmt.Sprintf("max=%d", max), func(t *testing.T) {
			config := defaultConfig()
			config.Set(CfgSpanMaxCallStackDepth, max)
			s := testSpanWithConfig(config)

			for i := 0; i <= max; i++ {
				s.NewSpanEvent("t")
				assert.Equal(t, int32(0), s.eventOverflow.Load(), "level %d fits", i+1)
			}

			s.NewSpanEvent("t")
			assert.Equal(t, int32(1), s.eventOverflow.Load(), "level %d overflows", max+2)

			for i := 0; i < max+2; i++ {
				s.EndSpanEvent()
			}
			// popped deepest first, so the recorded depths run max+1 .. 1
			if assert.Len(t, s.spanEvents, max+1, "recorded events") {
				for i, se := range s.spanEvents {
					assert.Equal(t, int32(max+1-i), se.depth, "depth")
				}
			}
		})
	}

	t.Run("unlimited", func(t *testing.T) {
		config := defaultConfig()
		config.Set(CfgSpanMaxCallStackDepth, -1)
		s := testSpanWithConfig(config)
		for i := 0; i < 200; i++ {
			s.NewSpanEvent("t")
		}
		assert.Equal(t, int32(0), s.eventOverflow.Load(), "no depth overflow")
	})
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
	unsampled := newUnSampledSpan(agent, "/test")
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
	assert.Equal(t, int32(1), span.statusErr.Load(), "unsampled span still records failure")

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
	assert.Zero(t, defaultNoopSpan.statusErr.Load(), "singleton untouched")
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

// span.SetError takes the same optional error name as spanEvent.SetError, so
// both recorders group errors in the UI the same way.
func TestSpan_SetErrorName(t *testing.T) {
	tests := []struct {
		name      string
		errorName []string
		want      string
	}{
		{"defaults to the Go type name", nil, "errors.errorString"},
		{"given name wins", []string{"MyError"}, "MyError"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := defaultTestSpan()
			span.SetError(io.EOF, tt.errorName...)
			assert.Equal(t, span.agent.cacheError(tt.want), span.errorFuncId, "errorFuncId")
		})
	}
}

func TestSpan_SetErrorAfterEndSpanIsNoop(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	span.EndSpan()
	span.SetError(fmt.Errorf("late"))

	assert.Equal(t, int32(0), span.err.Load(), "err")
	assert.Equal(t, "", span.errorString, "errorString")
}

// doc/api_contracts.md 3: nothing recorded after EndSpan is sent, so the span
// level setters drop the write the way the span event ones do.
func TestSpan_SettersAfterEndSpanAreNoop(t *testing.T) {
	span := defaultSpan(newTestAgent(defaultConfig()))
	span.SetServiceType(ServiceTypeGoFunction)
	span.SetRpcName("/rpc")
	span.SetRemoteAddress("10.0.0.1")
	span.SetEndPoint("host:8080")
	span.SetAcceptorHost("acceptor")
	span.SetLogging(1)
	span.Annotations().AppendString(AnnotationHttpUrl, "/rpc")
	span.EndSpan()

	span.SetServiceType(ServiceTypeGoHttpClient)
	span.SetRpcName("late")
	span.SetRemoteAddress("late")
	span.SetEndPoint("late")
	span.SetAcceptorHost("late")
	span.SetLogging(2)
	span.SetFailure()
	span.Annotations().AppendString(AnnotationHttpUrl, "late")

	assert.Equal(t, int32(ServiceTypeGoFunction), span.serviceType, "serviceType")
	assert.Equal(t, "/rpc", span.rpcName, "rpcName")
	assert.Equal(t, "10.0.0.1", span.remoteAddr, "remoteAddr")
	assert.Equal(t, "host:8080", span.endPoint, "endPoint")
	assert.Equal(t, "acceptor", span.acceptorHost, "acceptorHost")
	assert.Equal(t, int32(1), span.loggingInfo, "loggingInfo")
	assert.Equal(t, int32(0), span.err.Load(), "err")
	assert.Equal(t, int32(0), span.statusErr.Load(), "statusErr")
	assert.Len(t, span.annotations.getList(), 1, "annotations")
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

// An Annotation handle taken before the end outlives the finished check in
// Annotations(), so the collector is sealed at EndSpan/EndSpanEvent: whether a
// late append rode along on the chunk otherwise depended on when the sender
// goroutine got to it.
func TestSpan_AnnotationHandleHeldPastEndIsSealed(t *testing.T) {
	span := defaultTestSpan()
	spanA := span.Annotations()
	span.NewSpanEvent("event")
	eventA := span.SpanEvent().Annotations()

	spanA.AppendString(AnnotationHttpUrl, "/rpc")
	eventA.AppendString(AnnotationArgs0, "arg")

	span.EndSpanEvent()
	eventA.AppendString(AnnotationArgs0, "late")
	assert.Len(t, span.spanEvents[0].annotations.values, 1, "event annotations")

	span.EndSpan()
	spanA.AppendString(AnnotationHttpUrl, "late")
	assert.Len(t, span.annotations.values, 1, "span annotations")
}

// EndSpan reads span.urlStat after enqueueing the final chunk, so a late
// AddMetric would race that read for a stat nothing enqueues.
func TestSpan_AddMetricAfterEndSpanIsNoop(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.urlStatChan = make(chan *urlStat, 1)
	span := newSampledSpan(agent, "op", "/rpc")

	span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/{id}", Method: "GET"})
	span.EndSpan()
	span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/late", Method: "POST"})

	assert.Equal(t, "/users/{id}", span.urlStat.Url, "urlStat")
}

// Run under -race: the span level counterpart of the span event test above -
// late setters race with the sender serializing the final chunk.
func TestSpan_LateSetterConcurrentWithSenderIsRaceFree(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.urlStatChan = make(chan *urlStat, 1)
	span := newSampledSpan(agent, "op", "/rpc")
	span.apiId = 0 // exercise the builder-local AnnotationApi fallback
	// Taken while the span is live: the handle survives EndSpan.
	a := span.Annotations()
	span.EndSpan()

	chunk, ok := agent.spanQueue.tryDequeue()
	if !assert.True(t, ok) {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			span.SetError(errors.New("late"))
			span.SetFailure()
			span.SetServiceType(ServiceTypeGoHttpClient)
			span.SetRpcName("late")
			span.SetRemoteAddress("late")
			span.SetEndPoint("late")
			span.SetAcceptorHost("late")
			span.SetLogging(2)
			span.Annotations().AppendString(AnnotationHttpUrl, "late")
			span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/late", Method: "POST"})
			a.AppendString(AnnotationHttpUrl, "late") // handle taken before the end
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

	assert.Equal(t, "/rpc", span.rpcName, "rpcName")
	assert.Equal(t, int32(0), span.err.Load(), "err")
	assert.Empty(t, span.annotations.values, "no late annotation, and no fallback written back")
	assert.Nil(t, span.urlStat, "urlStat")
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

// Unbalanced end: events left open are ended and still sent, so the converted
// PSpan keeps every sequence number that was handed out.
func TestSpan_UnclosedEventsAreEndedAndSent(t *testing.T) {
	span := defaultTestSpan()
	span.operationName = "op"
	span.NewSpanEvent("outer")
	span.NewSpanEvent("inner")
	span.EndSpan() // both events left open

	chunk, ok := span.agent.spanQueue.tryDequeue()
	if !assert.True(t, ok, "final chunk enqueued") {
		return
	}

	pspan := (&spanMessageBuilder{}).makePSpanMessage(chunk).GetSpan()
	if !assert.NotNil(t, pspan, "final chunk converts to a PSpan") {
		return
	}

	seqs := make([]int32, 0, len(pspan.SpanEvent))
	for _, pse := range pspan.SpanEvent {
		seqs = append(seqs, pse.Sequence)
	}
	assert.Equal(t, []int32{0, 1}, seqs, "no sequence hole")
	assert.Equal(t, 0, span.eventStack.len(), "stack.len()")
}

// Test_isIDChars pins the byte loop to the regexp it replaced: identical
// verdicts for every one- and two-byte string, so the character class did not
// drift when idPattern was dropped.
func Test_isIDChars(t *testing.T) {
	re := regexp.MustCompile("^[a-zA-Z0-9._\\-]+$")
	check := func(s string) {
		t.Helper()
		assert.Equal(t, re.MatchString(s), len(s) > 0 && isIDChars(s), "%q", s)
	}
	for a := 0; a < 256; a++ {
		check(string([]byte{byte(a)}))
		for b := 0; b < 256; b++ {
			check(string([]byte{byte(a), byte(b)}))
		}
	}
	for _, s := range []string{"", "한글", "é", "a\x00b", "a\r\nb", "ok-id_1.2", "a b", "a^b", "\xff\xfe"} {
		check(s)
	}
}

func BenchmarkValidateID(b *testing.B) {
	const id = "AZm7kQ2vRtYpLxNc0dHgUw" // v4 agent id shape: base64url of a UUID
	for i := 0; i < b.N; i++ {
		if !validateID(id, agentIDMaxLen) {
			b.Fatal("must validate")
		}
	}
}

// A parent application type that does not parse keeps the UNKNOWN default,
// as in the C++ agent; the discarded Atoi result used to leave 0.
func Test_span_Extract_malformedParentAppTypeKeepsDefault(t *testing.T) {
	span := defaultTestSpan()
	span.Extract(&DistributedTracingContextMap{m: map[string]string{
		HeaderTraceId:               "t123456^12345^1",
		HeaderSpanId:                "67890",
		HeaderParentApplicationName: "upstream",
		HeaderParentApplicationType: "not-a-number",
	}})

	assert.Equal(t, "upstream", span.parentAppName)
	assert.Equal(t, 1, span.parentAppType, "malformed type keeps the default")
}

// The warning for a peer-controlled malformed header is throttled per call
// site: a peer sending a thousand bad headers gets one line per interval, and
// the next line says how many were held back.
func Test_span_Extract_malformedHeaderWarningIsThrottled(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()
	malformedTraceIdLog = logThrottle{}

	for i := 0; i < 1000; i++ {
		span := defaultTestSpan()
		span.Extract(&DistributedTracingContextMap{m: map[string]string{HeaderTraceId: "not^a^traceid"}})
		dropSampledActiveSpan(span)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "malformed trace id header"), buf.String())

	malformedTraceIdLog.next.Store(0) // the interval elapses
	span := defaultTestSpan()
	span.Extract(&DistributedTracingContextMap{m: map[string]string{HeaderTraceId: "not^a^traceid"}})
	dropSampledActiveSpan(span)
	assert.Equal(t, 2, strings.Count(buf.String(), "malformed trace id header"))
	assert.Contains(t, buf.String(), "(999 similar warning(s) suppressed)")
}

// Goroutine-sharing detection must not change the call stack: an event
// started from another goroutine is still recorded, and the shape of the
// trace is the same whether or not debug logging is on.
func Test_span_NewSpanEvent_sharedGoroutineKeepsTheCallStack(t *testing.T) {
	for _, level := range []logrus.Level{logrus.WarnLevel, logrus.DebugLevel} {
		var buf bytes.Buffer
		restore := captureLogAt(&buf, level)
		sharedGoroutineLog = logThrottle{}

		span := defaultTestSpan()
		span.NewSpanEvent("parent")
		done := make(chan struct{})
		go func() {
			defer close(done)
			span.NewSpanEvent("from another goroutine")
			span.EndSpanEvent()
		}()
		<-done
		span.EndSpanEvent()

		assert.Equal(t, int32(2), span.eventSequence.Load(), "both events recorded at level %s", level)
		assert.Equal(t, int32(1), span.eventDepth.Load(), "stack balanced at level %s", level)
		if goIdOffset > 0 {
			assert.Contains(t, buf.String(), "shared by more than one goroutine", "detection runs at level %s", level)
		}
		restore()
	}
}

// After EndSpan the final PSpan is already queued. A late event pair must not
// cut a non-final chunk behind it (protocol violation once spanEventChunkSize
// of them accumulate), must not advance eventSequence past the range the
// PSpan declared, and must not register API metadata no span carries.
func TestSpan_LifecycleStopsAtEndSpan(t *testing.T) {
	span := defaultTestSpan()
	span.NewSpanEvent("t").EndSpanEvent()
	span.EndSpan()

	queued := span.agent.spanQueue.length()
	seq := span.eventSequence.Load()
	metas := len(span.agent.metaChan)

	for i := 0; i < 25; i++ {
		span.NewSpanEvent("late").EndSpanEvent()
	}
	assert.Equal(t, queued, span.agent.spanQueue.length(), "no chunk after the final one")
	assert.Equal(t, seq, span.eventSequence.Load(), "eventSequence frozen")
	assert.Equal(t, metas, len(span.agent.metaChan), "no api meta for dropped events")
	assert.Equal(t, 0, span.eventStack.len(), "nothing pushed")

	m := map[string]string{}
	span.Inject(&DistributedTracingContextMap{m})
	assert.Empty(t, m, "Inject writes no headers after EndSpan")

	assert.Equal(t, NoopTracer(), span.NewGoroutineTracer(), "async span after EndSpan is noop")
	assert.Equal(t, NoopTracer(), span.NewAsyncSpan(), "async span after EndSpan is noop")
}

// Regression: EndSpan sets finished and then ends the leftover unclosed events
// through the same path. They must still land in the final chunk.
func TestSpan_EndSpanKeepsLeftoverEventsInFinalChunk(t *testing.T) {
	span := defaultTestSpan()
	for _, n := range []string{"a", "b", "c"} {
		span.NewSpanEvent(n)
	}
	span.EndSpan()

	chunk, ok := span.agent.spanQueue.tryDequeue()
	assert.True(t, ok, "final chunk enqueued")
	assert.True(t, chunk.final, "final")
	assert.Len(t, chunk.eventChunk, 3, "leftover events recorded")
	_, more := span.agent.spanQueue.tryDequeue()
	assert.False(t, more, "exactly one chunk")
}

// The async span's own goroutine event is ended by EndSpan after finished is
// set; it must not be dropped by the EndSpanEvent guard.
func TestSpan_AsyncEndSpanKeepsItsEvent(t *testing.T) {
	parent := defaultTestSpan()
	parent.NewSpanEvent("t")
	async := parent.NewGoroutineTracer().(*span)
	async.NewSpanEvent("work").EndSpanEvent()
	async.EndSpan()

	chunk, ok := async.agent.spanQueue.tryDequeue()
	assert.True(t, ok, "final chunk enqueued")
	assert.True(t, chunk.final, "final")
	assert.Len(t, chunk.eventChunk, 2, "work event and the async goroutine event")
}

// overflowedSpan returns a span at max depth 3 holding four live events (max+1
// levels are recorded) and one overflow placeholder.
func overflowedSpan() *span {
	config := defaultConfig()
	config.Set(CfgSpanMaxCallStackDepth, 3)
	s := testSpanWithConfig(config)
	for i := 0; i < 5; i++ {
		s.NewSpanEvent("t")
	}
	s.overflowSe.SetDestination("my-cluster")
	return s
}

func Test_span_EndSpanEvent_ConcurrentOverflowFloorsAtZero(t *testing.T) {
	for i := 0; i < 200; i++ {
		s := overflowedSpan()
		var wg sync.WaitGroup
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.EndSpanEvent()
			}()
		}
		wg.Wait()
		assert.GreaterOrEqual(t, s.eventOverflow.Load(), int32(0), "eventOverflow")
		assert.Equal(t, "", s.overflowSe.destination(), "destination cleared")
	}
}

func Test_span_EndSpanEvent_OverflowNeverGoesNegative(t *testing.T) {
	s := overflowedSpan()
	assert.Equal(t, int32(1), s.eventOverflow.Load(), "eventOverflow")

	s.EndSpanEvent()
	assert.Equal(t, int32(0), s.eventOverflow.Load(), "eventOverflow")
	assert.Equal(t, "", s.overflowSe.destination(), "destination cleared")
	assert.Equal(t, 4, s.eventStack.len(), "stack.len()")

	// A new overflow must land on 1, not 0: its end below then consumes the
	// placeholder instead of popping the live ancestor.
	s.NewSpanEvent("t")
	assert.Equal(t, int32(1), s.eventOverflow.Load(), "eventOverflow")
	s.EndSpanEvent()
	assert.Equal(t, int32(0), s.eventOverflow.Load(), "eventOverflow")
	assert.Equal(t, 4, s.eventStack.len(), "stack.len()")
	assert.Equal(t, int32(5), s.eventDepth.Load(), "eventDepth")
}

func Test_spanEvent_end_Idempotent(t *testing.T) {
	s := testSpanWithConfig(defaultConfig())
	s.NewSpanEvent("t")
	se, _ := s.eventStack.pop()
	se.end()
	se.end()
	assert.Equal(t, int32(1), s.eventDepth.Load(), "eventDepth")
}

// An error recorded on a span event past the call stack limit still fails the
// transaction: Java's DefaultTrace.traceBlockBegin0 hands out a real recorder
// during overflow and its recordException marks the trace root; the C++
// DisabledSpanEvent::SetError does the same. Nothing is recorded on the event.
func TestOverflowSpanEvent_SetErrorMarksSpanFailed(t *testing.T) {
	tests := []struct {
		name    string
		rules   []string
		wantErr int32
	}{
		{"fails the span", nil, 1},
		{"Error.IgnoreErrors keeps the span ok", []string{"*errors.errorString:boom"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewConfig(WithAppName("overflowErrApp"), WithErrorIgnoreErrors(tt.rules...))
			assert.NoError(t, err)
			cfg.Set(CfgSpanMaxCallStackDepth, 1) // clamped to minEventDepth
			cfg.Set(CfgHttpUrlStatEnable, true)
			agent := newTestAgent(cfg)
			agent.urlStatChan = make(chan *urlStat, 1)
			span := newSampledSpan(agent, "op", "/rpc")
			span.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/{id}", Method: "GET"})
			for span.eventOverflow.Load() == 0 {
				span.NewSpanEvent("t")
			}

			se := span.SpanEvent()
			_, ok := se.(*overflowSpanEvent)
			assert.True(t, ok, "overflowSpanEvent")
			se.SetError(errors.New("boom"))
			span.EndSpan()

			assert.Equal(t, tt.wantErr, span.err.Load(), "span.err")
			stat := <-agent.urlStatChan
			assert.Equal(t, int(tt.wantErr), stat.statusErr, "urlStat.statusErr")
			// Nothing lands on the overflowed event or the span's own error fields.
			assert.Equal(t, int32(0), span.errorFuncId, "span.errorFuncId")
			assert.Equal(t, "", span.errorString, "span.errorString")
			assert.Empty(t, span.errorChains, "errorChains")
			for _, ev := range span.spanEvents {
				assert.Equal(t, int32(0), ev.errorFuncId, "event errorFuncId")
				assert.Empty(t, ev.annotations.values, "event annotations")
			}
		})
	}
}

// After EndSpan the overflow recorder drops the write like every other setter.
func TestOverflowSpanEvent_SetErrorAfterEndSpanIsNoop(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSpanMaxCallStackDepth, 1)
	span := testSpanWithConfig(cfg)
	for span.eventOverflow.Load() == 0 {
		span.NewSpanEvent("t")
	}
	se := span.SpanEvent()
	span.EndSpan()
	se.SetError(errors.New("late"))
	assert.Equal(t, int32(0), span.err.Load(), "err")
}

// An async span is serialized as a PSpanChunk, which has no err field, so a
// failure recorded on it must reach the root span's PSpan.err and URL stat -
// Java's ChildTrace shares the parent's TraceRoot and the wire err is the
// root's (SpanMessageMapper: span.traceRoot.shared.errorCode -> err).
func TestSpan_AsyncErrorFailsTheTraceRoot(t *testing.T) {
	tests := []struct {
		name   string
		record func(async Tracer)
	}{
		{"span SetError", func(a Tracer) { a.Span().SetError(errors.New("boom")) }},
		{"span SetFailure", func(a Tracer) { a.Span().SetFailure() }},
		{"span event SetError", func(a Tracer) { a.NewSpanEvent("work").SpanEvent().SetError(errors.New("boom")); a.EndSpanEvent() }},
		{"nested async span event SetError", func(a Tracer) {
			a.NewSpanEvent("work")
			nested := a.NewGoroutineTracer()
			nested.NewSpanEvent("deep").SpanEvent().SetError(errors.New("boom"))
			nested.EndSpanEvent()
			nested.EndSpan()
			a.EndSpanEvent()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := NewConfig(WithAppName("asyncErrApp"))
			assert.NoError(t, err)
			cfg.Set(CfgHttpUrlStatEnable, true)
			agent := newTestAgent(cfg)
			agent.urlStatChan = make(chan *urlStat, 1)
			root := newSampledSpan(agent, "op", "/rpc")
			root.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/{id}", Method: "GET"})
			root.NewSpanEvent("fork")

			async := root.NewGoroutineTracer()
			tt.record(async)
			async.EndSpan()
			for {
				// drain the async chunks; the root's final chunk is enqueued last
				if _, ok := agent.spanQueue.tryDequeue(); !ok {
					break
				}
			}

			root.EndSpanEvent()
			root.EndSpan()

			chunk, ok := agent.spanQueue.tryDequeue()
			if !assert.True(t, ok, "root final chunk") {
				return
			}
			pspan := (&spanMessageBuilder{}).makePSpanMessage(chunk).GetSpan()
			assert.NotNil(t, pspan, "root chunk converts to a PSpan")
			assert.Equal(t, int32(1), pspan.Err, "PSpan.err")
			assert.Equal(t, 1, (<-agent.urlStatChan).statusErr, "urlStat.statusErr")
			// Only the flag travels; the message stays on the recording span.
			assert.Equal(t, "", root.errorString, "root.errorString")
		})
	}
}

// Known limit: the root's final chunk is sent at its own EndSpan, so a child
// that fails after the root has ended is not reflected. Java defers the root
// store until the last child ends; this agent does not (yet).
func TestSpan_AsyncErrorAfterRootEndIsNotReported(t *testing.T) {
	cfg, err := NewConfig(WithAppName("asyncLateErrApp"))
	assert.NoError(t, err)
	cfg.Set(CfgHttpUrlStatEnable, true)
	agent := newTestAgent(cfg)
	agent.urlStatChan = make(chan *urlStat, 1)
	root := newSampledSpan(agent, "op", "/rpc")
	root.AddMetric(MetricURLStat, &UrlStatEntry{Url: "/users/{id}", Method: "GET"})
	root.NewSpanEvent("fork")
	async := root.NewGoroutineTracer()
	root.EndSpanEvent()
	root.EndSpan()

	chunk, ok := agent.spanQueue.tryDequeue()
	if !assert.True(t, ok, "root final chunk") {
		return
	}

	// The sender serializes the chunk; whatever it reads then is on the wire.
	pspan := (&spanMessageBuilder{}).makePSpanMessage(chunk).GetSpan()
	if !assert.NotNil(t, pspan) {
		return
	}

	async.Span().SetError(errors.New("late"))
	async.EndSpan()

	assert.Equal(t, int32(0), pspan.Err, "PSpan.err was read before the late failure")
	assert.Equal(t, 0, (<-agent.urlStatChan).statusErr, "urlStat.statusErr")
	assert.Equal(t, int32(1), root.err.Load(), "the flag lands on the root, only too late")
}

// SQL.ErrorCount adds up across the async spans of one trace and flags the root.
func TestSpan_AsyncSQLCountFailsTheTraceRoot(t *testing.T) {
	cfg := defaultConfig()
	cfg.Set(CfgSQLErrorCount, 3)
	root := testSpanWithConfig(cfg)
	root.NewSpanEvent("fork")
	async := root.NewGoroutineTracer().(*span)

	newSpanEvent(root, "query").SetSQL("SELECT 1", "")
	newSpanEvent(async, "query").SetSQL("SELECT 1", "")
	assert.Equal(t, int32(0), root.err.Load(), "below limit")
	newSpanEvent(async, "query").SetSQL("SELECT 1", "")

	assert.Equal(t, int32(3), root.sqlCount.Load(), "root sqlCount")
	assert.Equal(t, int32(0), async.sqlCount.Load(), "async sqlCount")
	assert.Equal(t, int32(1), root.err.Load(), "root err")
	assert.Equal(t, int32(0), async.err.Load(), "async err")
}

// Extract writes txId, spanId, endPoint and friends non-atomically and
// registers the span as active. After EndSpan the sender is serializing that
// span and the registry entry is already dropped, so a late Extract both
// races the fields and leaks the span back into the registry.
func TestSpan_ExtractAfterEndSpanIsNoop(t *testing.T) {
	var buf bytes.Buffer
	defer captureWarnLog(&buf)()
	afterEndSpanLog = logThrottle{}

	span := defaultTestSpan()
	span.Extract(&DistributedTracingContextMap{map[string]string{}})
	txId, spanId, endPoint := span.txId, span.spanId, span.endPoint
	span.EndSpan()
	active := countActiveSpans(span.agent)

	span.Extract(&DistributedTracingContextMap{map[string]string{
		HeaderTraceId:               "agent^1^2",
		HeaderSpanId:                "67890",
		HeaderParentSpanId:          "12345",
		HeaderParentApplicationName: "parent",
		HeaderHost:                  "host:8080",
	}})

	assert.Contains(t, buf.String(), "Extract called after EndSpan")
	assert.Equal(t, txId, span.txId, "txId unchanged")
	assert.Equal(t, spanId, span.spanId, "spanId unchanged")
	assert.Equal(t, endPoint, span.endPoint, "endPoint unchanged")
	assert.Equal(t, active, countActiveSpans(span.agent), "no re-registration after EndSpan")
}
