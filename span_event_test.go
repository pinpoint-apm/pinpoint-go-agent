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
			assert.Equal(t, se.parentSpan.eventDepth, int32(1), "serviceType")

			time.Sleep(100 * time.Millisecond)
			se.end()

			assert.Equal(t, se.operationName, tt.args.operationName, "operationName")
			assert.Equal(t, se.parentSpan.eventDepth, int32(0), "eventDepth")
			assert.Greater(t, se.endElapsed.Milliseconds(), int64(99), "endElapsed")
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
			tt.args.span.agent = newMockAgent()
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetError(errors.New("TEST_ERROR"))
			assert.Equal(t, se.errorFuncId, int32(0), "errorFuncId")
			assert.Equal(t, se.errorString, "TEST_ERROR", "errorString")
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
			tt.args.span.agent = newMockAgent()
			se := newSpanEvent(tt.args.span, tt.args.operationName)
			se.SetSQL("SELECT 1", "")
			assert.Equal(t, len(se.annotations.list), int(1), "annotations.len")
		})
	}
}
