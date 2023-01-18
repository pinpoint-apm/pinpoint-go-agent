package pinpoint

import (
	"context"
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_defaultSpan(t *testing.T) {
	span := defaultSpan()

	assert.Equal(t, span.parentSpanId, int64(-1), "parentSpanId")
	assert.Equal(t, span.parentAppType, 1, "parentAppType")
	assert.Equal(t, span.eventDepth, int32(1), "eventDepth")
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
	span := defaultSpan()
	span.agent = newTestAgent(defaultConfig())
	return span
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
			assert.Equal(t, span.eventSequence, int32(1), "eventSequence")
			assert.Equal(t, span.eventDepth, int32(2), "eventDepth")
			assert.Equal(t, span.eventStack.len(), int(1), "stack.len")

			se := span.spanEvents[0]
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
			s := defaultTestSpan()
			save := s.agent.config.spanMaxEventDepth
			s.agent.config.spanMaxEventDepth = 3

			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence, int32(2), "eventSequence")
			assert.Equal(t, s.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, s.eventOverflow, 2, "eventOverflow")
			assert.Equal(t, s.eventOverflowLog, true, "eventOverflowLog")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow, 1, "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow, 0, "eventOverflow")
			assert.Equal(t, s.eventStack.len(), 2, "stack.len()")

			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 1, "stack.len()")
			s.EndSpanEvent()
			assert.Equal(t, s.eventStack.len(), 0, "stack.len()")

			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)
			s.NewSpanEvent(tt.args.operationName)

			assert.Equal(t, s.eventSequence, int32(4), "eventSequence")
			assert.Equal(t, s.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, s.eventOverflow, 2, "eventOverflow")
			assert.Equal(t, s.eventOverflowLog, true, "eventOverflowLog")

			s.EndSpanEvent()
			assert.Equal(t, s.eventOverflow, 1, "eventOverflow")
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
			assert.Equal(t, s.eventOverflow, 0, "eventOverflow")
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

			s.agent.config.spanMaxEventDepth = save
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
			span := defaultTestSpan()
			save := span.agent.config.spanMaxEventSequence
			span.agent.config.spanMaxEventSequence = 5

			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName).EndSpanEvent()
			span.NewSpanEvent(tt.args.operationName)
			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence, int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow, 0, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence, int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow, 1, "eventOverflow")
			assert.Equal(t, span.eventOverflowLog, true, "eventOverflowLog")
			assert.Equal(t, span.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.NewSpanEvent(tt.args.operationName)
			assert.Equal(t, span.eventSequence, int32(5), "eventSequence")
			assert.Equal(t, span.eventOverflow, 2, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow, 1, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow, 0, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(3), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 2, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow, 0, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(2), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 1, "stack.len()")

			span.EndSpanEvent()
			assert.Equal(t, span.eventOverflow, 0, "eventOverflow")
			assert.Equal(t, span.eventDepth, int32(1), "eventDepth")
			assert.Equal(t, span.eventStack.len(), 0, "stack.len()")

			span.agent.config.spanMaxEventSequence = save
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
			assert.Equal(t, se.asyncId, int32(2), "asyncId")
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
			assert.Equal(t, se.asyncId, int32(3), "asyncId")
			assert.Equal(t, se.asyncSeqGen, int32(1), "asyncSeqGen")

			f()
		})
	}
}
