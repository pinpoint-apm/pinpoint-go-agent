package pinpoint

import (
	"context"
	"github.com/stretchr/testify/assert"
	"testing"
)

func Test_defaultSpan(t *testing.T) {
	span := defaultSpan()

	assert.Equal(t, span.parentSpanId, int64(-1), "parentSpanId")
	assert.Equal(t, span.parentAppType, -1, "parentAppType")
	assert.Equal(t, span.eventDepth, int32(1), "eventDepth")
	assert.Equal(t, span.serviceType, int32(ServiceTypeGoApp), "serviceType")
	assert.NotNil(t, span.stack, "stack")
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
	span.agent = newMockAgent()
	return span
}

func Test_span_Extract(t *testing.T) {
	type args struct {
		reader DistributedTracingContextReader
	}

	m := map[string]string{
		HttpTraceId:      "t123456^12345^1",
		HttpSpanId:       "67890",
		HttpParentSpanId: "123",
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
			assert.Equal(t, m[HttpTraceId], span.txId.String(), "HttpTraceId")
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
			assert.Equal(t, span.stack.Len(), int(1), "stack.len")

			se := span.spanEvents[0]
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
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
			assert.Equal(t, span.stack.Len(), int(2), "stack.len")
			span.EndSpanEvent()
			assert.Equal(t, span.stack.Len(), int(1), "stack.len")
			span.EndSpanEvent()
			assert.Equal(t, span.stack.Len(), int(0), "stack.len")
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

			se := s.stack.Front().Value.(*spanEvent)
			assert.Equal(t, se.asyncId, int32(1), "asyncId")
			assert.Equal(t, se.asyncSeqGen, int32(1), "asyncSeqGen")

			as := a.(*span)
			assert.Equal(t, as.agent, s.agent, "agent")
			assert.Equal(t, as.txId, s.txId, "txId")
			assert.Equal(t, as.spanId, s.spanId, "spanId")

			ase := as.stack.Front().Value.(*spanEvent)
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

				ase := as.stack.Front().Value.(*spanEvent)
				assert.Equal(t, ase.serviceType, int32(ServiceTypeGoFunction), "serviceType")
				assert.Equal(t, as.stack.Len(), 2, "stack.Len()")
			}, context.Background())

			se := s.stack.Front().Value.(*spanEvent)
			assert.Equal(t, se.asyncId, int32(2), "asyncId")
			assert.Equal(t, se.asyncSeqGen, int32(1), "asyncSeqGen")

			f()
		})
	}
}
