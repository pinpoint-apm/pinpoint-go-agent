package pinpoint

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func Test_newSpanEvent(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.serviceType, int32(ServiceTypeGoFunction), "serviceType")
			assert.NotNil(t, se.startTime, "startTime")
		})
	}
}

func Test_spanEvent_end(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			tt.args.span.appendSpanEvent(se)
			assert.Equal(t, se.parentSpan.eventDepth, int32(2), "eventDepth")

			time.Sleep(100 * time.Millisecond)
			se.end()

			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.parentSpan.eventDepth, int32(1), "eventDepth")
			assert.Greater(t, se.endElapsed, int64(99), "endElapsed")
		})
	}
}

func Test_spanEvent_generateNextSpanId(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			id := se.generateNextSpanId()
			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.nextSpanId, id, "nextSpanId")
			assert.NotEqual(t, se.nextSpanId, int64(0), "nextSpanId")
		})
	}
}

func Test_spanEvent_SetError(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args.span.agent = newTestAgent(defaultConfig())
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetError(errors.New("TEST_ERROR"))
			assert.Equal(t, int32(1), se.errorFuncId, "errorFuncId")
			assert.Equal(t, "TEST_ERROR", se.errorString, "errorString")
		})
	}
}

func Test_spanEvent_SetSQL(t *testing.T) {
	type args struct {
		span          *span
		operationName string
	}
	tests := []struct {
		name string
		args args
	}{
		{"1", args{defaultTestSpan(), "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args.span.agent = newTestAgent(defaultConfig())
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetSQL("SELECT 1", "")
			assert.Equal(t, len(se.annotations.values), int(1), "annotations.len")
		})
	}
}

// A goroutine span event must carry an api id its own agent registered: ids
// come from the per-agent apiIdGen, so a process-global cache would make the
// second agent (Shutdown() + NewAgent()) reuse an id it never published.
func Test_newSpanEventGoroutine_apiIdIsPerAgent(t *testing.T) {
	publishedGoroutineApiId := func(a *agent) int32 {
		for {
			select {
			case md := <-a.metaChan:
				if api, ok := md.(apiMeta); ok && api.descriptor == "Goroutine Invocation" {
					assert.Equal(t, apiTypeInvocation, api.apiType, "apiType")
					return api.id
				}
			default:
				return 0
			}
		}
	}

	for _, name := range []string{"first agent", "second agent"} {
		t.Run(name, func(t *testing.T) {
			s := defaultTestSpan()
			se := newSpanEventGoroutine(s)

			assert.Equal(t, int32(ServiceTypeAsync), se.serviceType, "serviceType")
			assert.NotZero(t, se.apiId, "apiId")
			assert.Equal(t, se.apiId, publishedGoroutineApiId(s.agent), "registered apiId")

			// second event on the same agent reuses the id, publishing nothing
			assert.Equal(t, se.apiId, newSpanEventGoroutine(s).apiId, "cached apiId")
			assert.Zero(t, publishedGoroutineApiId(s.agent), "re-registered apiId")
		})
	}
}

// Exception chain ids are per-agent: a new agent (Shutdown() + NewAgent())
// numbers its chains from 1 instead of continuing the previous agent's count.
func Test_span_getExceptionChainId_isPerAgent(t *testing.T) {
	for _, name := range []string{"first agent", "second agent"} {
		t.Run(name, func(t *testing.T) {
			s := defaultTestSpan()

			id, isNew := s.getExceptionChainId(errors.New("boom"))
			assert.Equal(t, int64(1), id, "first chain id")
			assert.True(t, isNew, "isNew")

			next, _ := s.getExceptionChainId(errors.New("bang"))
			assert.Equal(t, int64(2), next, "second chain id")
		})
	}
}
